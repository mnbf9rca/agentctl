//go:build darwin

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/attach"
	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/control"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/session"
	"github.com/mnbf9rca/agentctl/internal/shim"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func buildShimDependencies(runner tmuxx.Runner, lookupEnv session.LookupEnv) (dependencies, func() error, error) {
	namespace, err := shim.OpenNamespace()
	if err != nil {
		return dependencies{}, nil, err
	}
	records, err := fleet.OpenShimFleetRecordStore(namespace.StateRoot)
	if err != nil {
		_ = namespace.Close()
		return dependencies{}, nil, err
	}
	closeAll := func() error { return errors.Join(records.Close(), namespace.Close()) }
	tmuxClient := tmuxx.New(runner)
	shimClient := shim.NewClient(namespace)
	inspector := fleet.NewRuntimeShimRoleInspector(namespace, shimClient)
	rootGuard := shim.NewStateRootGuard(namespace)
	launchDependencies := fleet.ShimLaunchDependencies{}
	server := shim.NewServer(namespace)
	foreground := productionForegroundExecutor{
		runner: fleet.NewShimForegroundRunner(server, shimClient, records, inspector, launchDependencies),
		stdin:  os.Stdin, stdout: os.Stdout, environment: os.Environ,
	}
	collector := statuspkg.NewShimCollector(
		fleet.NewShimStatusFleetReader(records),
		statuspkg.NewRuntimeShimRoleSource(namespace, shimClient),
		statuspkg.NewRuntimePresentationSource(tmuxClient),
	)
	statusRuntime := runtimeStatusCollector{ShimCollector: collector, client: tmuxClient, lookupEnv: lookupEnv}
	controller := runtimeShimController{
		records:    records,
		dispatcher: control.NewShimDispatcher[shim.Response](shimClient, control.NewAncestryObserver(runner), os.Getpid),
	}
	return dependencies{
		launch:     launchDependenciesForCLI(),
		launcher:   fleet.NewShimLauncher(tmuxClient, shimClient, records, launchDependencies),
		resolver:   session.New(tmuxClient, lookupEnv),
		collector:  statusRuntime,
		killer:     kill.NewShimExecutor(shimClient, records, inspector, tmuxClient),
		controller: controller,
		rootGuard:  rootGuard,
		attacher: runtimeSessionAttacher{
			records:  records,
			delegate: attach.New(tmuxClient, attach.LookupEnv(lookupEnv)),
		},
		relauncher: fleet.NewShimRelauncher(tmuxClient, shimClient, records, inspector, launchDependencies),
		hiddenShim: newProductionHiddenShimCommand(),
		foreground: foreground,
		getwd:      os.Getwd,
	}, closeAll, nil
}

type runtimeShimController struct {
	records    fleet.ShimFleetRecords
	dispatcher control.ShimDispatcher[shim.Response]
}

func (c runtimeShimController) Execute(ctx context.Context, operation, sessionName, role string) (shim.Response, error) {
	record, err := c.records.Read(sessionName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return shim.Response{}, &fleet.ShimFleetMissingError{Session: sessionName}
		}
		return shim.Response{}, err
	}
	if _, ok := record.Roles[role]; !ok {
		return shim.Response{}, &fleet.UnknownRoleError{Role: role, Roster: strings.Join(record.Roster, ",")}
	}
	return c.dispatcher.Execute(ctx, operation, sessionName, role)
}

type runtimeStatusCollector struct {
	statuspkg.ShimCollector
	client    tmuxx.Client
	lookupEnv session.LookupEnv
}

func (c runtimeStatusCollector) CollectAll(ctx context.Context) (statuspkg.ShimSessionsReport, error) {
	report, err := c.ShimCollector.CollectAll(ctx)
	if err != nil {
		return report, err
	}
	pane, set := c.lookupEnv("TMUX_PANE")
	if !set {
		return report, nil
	}
	name, err := c.client.DisplayMessage(ctx, tmuxx.PaneID(pane))
	if err != nil {
		return report, nil
	}
	for index := range report.Sessions {
		report.Sessions[index].Current = report.Sessions[index].Session == name
	}
	return report, nil
}

func launchDependenciesForCLI() launchDependencies {
	return launchDependencies{skillHome: os.UserHomeDir, skillVersion: skillBinaryVersion}
}

type productionForegroundExecutor struct {
	runner      fleet.ShimForegroundRunner
	stdin       *os.File
	stdout      *os.File
	environment func() []string
}

// foregroundTerminalRestoreError prevents a known child/lifecycle outcome from
// hiding failure to restore the caller's terminal state.
type foregroundTerminalRestoreError struct {
	RunErr     error
	RestoreErr error
}

func (e *foregroundTerminalRestoreError) Error() string {
	return errors.Join(e.RunErr, e.RestoreErr).Error()
}

func (e *foregroundTerminalRestoreError) Unwrap() []error {
	return []error{e.RunErr, e.RestoreErr}
}

func (e productionForegroundExecutor) Execute(ctx context.Context, sessionName string, role config.RoleConfig, directory string) error {
	terminal := ptyx.NewTerminal()
	outerState, err := terminal.Observe(e.stdin)
	if err != nil {
		return err
	}
	initialSize, err := outerState.WindowSize()
	if err != nil {
		return err
	}
	input, err := ptyx.NewFileEndpoint(e.stdin)
	if err != nil {
		return err
	}
	output, err := ptyx.NewFileEndpoint(e.stdout)
	if err != nil {
		return errors.Join(err, input.Restore())
	}
	runErr := e.runner.Run(ctx, fleet.ShimForegroundRequest{
		Session: sessionName, Role: role, Directory: directory,
		ServerRequest: shim.RunRequest{
			Session: sessionName, Role: role.Name, Harness: string(role.Harness),
			HarnessOptions: harnessOptions(role), Environment: e.environment(), InitialSize: initialSize,
			OperatorInput: input, OperatorOutput: output, OuterTerminal: e.stdin, OuterState: outerState,
			OperatorMode: shim.OperatorForeground,
		},
	})
	restoreErr := errors.Join(output.Restore(), input.Restore())
	if restoreErr != nil {
		return &foregroundTerminalRestoreError{RunErr: runErr, RestoreErr: restoreErr}
	}
	return runErr
}

func harnessOptions(role config.RoleConfig) (options harness.Options) {
	options.Model, options.Effort = role.Model, role.Effort
	return options
}

var _ foregroundExecutor = productionForegroundExecutor{}

type shimFleetRecordReader interface {
	Read(string) (fleet.ShimFleetRecord, error)
}

// runtimeSessionAttacher proves the durable fleet before consulting optional
// presentation. An exact tmux name by itself is not a runtime-fleet fact.
type runtimeSessionAttacher struct {
	records  shimFleetRecordReader
	delegate sessionAttacher
}

func (a runtimeSessionAttacher) CheckEnvironment() error {
	return a.delegate.CheckEnvironment()
}

func (a runtimeSessionAttacher) ExecutePresentation(ctx context.Context, sessionName string, out io.Writer) (tmuxx.Session, error) {
	if _, err := a.records.Read(sessionName); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tmuxx.Session{}, &fleet.ShimFleetMissingError{Session: sessionName}
		}
		return tmuxx.Session{}, err
	}
	return a.delegate.ExecutePresentation(ctx, sessionName, out)
}

func (a runtimeSessionAttacher) StillRunning(ctx context.Context, target tmuxx.Session) (bool, error) {
	return a.delegate.StillRunning(ctx, target)
}

var _ sessionAttacher = runtimeSessionAttacher{}
