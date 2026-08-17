//go:build darwin

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"syscall"

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

func buildShimDependencies(runner tmuxx.Runner, lookupEnv session.LookupEnv, preparedRole *attach.PreparedRoleTerminal, preparedForeground *preparedForegroundTerminal) (dependencies, func() error, error) {
	namespace, err := shim.OpenNamespace()
	if err != nil {
		return dependencies{}, nil, err
	}
	records, err := fleet.OpenShimFleetRecordStore(namespace.StateRoot)
	if err != nil {
		_ = namespace.Close()
		return dependencies{}, nil, err
	}
	tmuxClient := tmuxx.New(runner)
	shimClient := shim.NewClient(namespace)
	inspector := fleet.NewRuntimeShimRoleInspector(namespace, shimClient)
	rootGuard := shim.NewStateRootGuard(namespace)
	launchDependencies := fleet.ShimLaunchDependencies{ArtifactInspector: inspector}
	server := shim.NewServer(namespace)
	foreground := productionForegroundExecutor{
		runner: fleet.NewShimForegroundRunner(server, shimClient, records, inspector, launchDependencies),
		stdin:  os.Stdin, stdout: os.Stdout, environment: os.Environ, prepared: preparedForeground,
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
	roleClient := attach.NewRoleClient(namespace, records, inspector, tmuxClient, preparedRole, os.Stdin, os.Stdout, os.Stderr)
	closeAll := func() error { return errors.Join(roleClient.Close(), records.Close(), namespace.Close()) }
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
		roleAttacher: roleClient,
		relauncher:   fleet.NewShimRelauncher(tmuxClient, shimClient, records, inspector, launchDependencies),
		hiddenShim:   newProductionHiddenShimCommand(),
		foreground:   foreground,
		getwd:        os.Getwd,
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
	prepared    *preparedForegroundTerminal
}

type preparedForegroundTerminal struct {
	outerState ptyx.TerminalState
}

type foregroundNotTerminalError struct{}

func (*foregroundNotTerminalError) Error() string {
	return "standard input and output must both be terminals"
}

type foregroundTerminalObservationError struct{ Cause error }

func (e *foregroundTerminalObservationError) Error() string { return e.Cause.Error() }
func (e *foregroundTerminalObservationError) Unwrap() error { return e.Cause }

func prepareForegroundTerminal(stdin, stdout *os.File) (*preparedForegroundTerminal, error) {
	return prepareForegroundTerminalWithObserver(stdin, stdout, ptyx.NewTerminal().Observe)
}

func prepareForegroundTerminalWithObserver(stdin, stdout *os.File, observe func(*os.File) (ptyx.TerminalState, error)) (*preparedForegroundTerminal, error) {
	if stdin == nil || stdout == nil {
		return nil, &foregroundNotTerminalError{}
	}
	outerState, err := observe(stdin)
	if err != nil {
		return nil, classifyForegroundTerminalObservation(err)
	}
	if _, err := observe(stdout); err != nil {
		return nil, classifyForegroundTerminalObservation(err)
	}
	return &preparedForegroundTerminal{outerState: outerState}, nil
}

func classifyForegroundTerminalObservation(err error) error {
	if errors.Is(err, syscall.ENOTTY) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENODEV) {
		return &foregroundNotTerminalError{}
	}
	return &foregroundTerminalObservationError{Cause: err}
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
	prepared := e.prepared
	if prepared == nil {
		var err error
		prepared, err = prepareForegroundTerminal(e.stdin, e.stdout)
		if err != nil {
			return err
		}
	}
	outerState := prepared.outerState
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
	record, err := a.records.Read(sessionName)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return tmuxx.Session{}, &fleet.ShimFleetMissingError{Session: sessionName}
		}
		return tmuxx.Session{}, err
	}
	if record.Presentation == fleet.PresentationDetached {
		return tmuxx.Session{}, &attach.NoPresentationError{Session: sessionName, Roster: append([]string(nil), record.Roster...)}
	}
	target, err := a.delegate.ExecutePresentation(ctx, sessionName, out)
	var noPresentation *attach.NoPresentationError
	if errors.As(err, &noPresentation) {
		noPresentation.Roster = append([]string(nil), record.Roster...)
	}
	return target, err
}

func (a runtimeSessionAttacher) StillRunning(ctx context.Context, target tmuxx.Session) (bool, error) {
	return a.delegate.StillRunning(ctx, target)
}

var _ sessionAttacher = runtimeSessionAttacher{}
