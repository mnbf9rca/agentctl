package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/attach"
	"github.com/mnbf9rca/agentctl/internal/buildinfo"
	"github.com/mnbf9rca/agentctl/internal/cliflags"
	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/control"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/session"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/target"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const (
	exitOK                = 0
	exitUnclassified      = 1
	exitUsage             = 2
	exitSession           = 3
	exitRole              = 4
	exitUnsafe            = 5
	exitTmux              = 6
	exitMissingExecutable = 7
	exitLaunch            = 8
)

var globalUsage = `Usage: agentctl COMMAND [OPTIONS]

Commands:
  launch    create an agent fleet
  relaunch  recreate one absent role window in a managed fleet
  attach    attach an agent fleet in iTerm2
  status    report fleet status
  clear     deliver /clear to a role
  compact   deliver /compact to a role
  kill      terminate a managed fleet
  version   report this binary's build identity
`

var commandUsage = map[string]string{
	"launch": "Usage: agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]\n",
	"relaunch": "Usage: agentctl relaunch [--session SESSION] [--harness HARNESS] [--model MODEL] [--dir PATH] ROLE\n\n" +
		"Recreates a role window that is absent. It refuses whenever the role's window still exists,\n" +
		"including a dead one, and reports whether the configuration came from the session or from flags.\n",
	"attach": "Usage: agentctl attach [--session SESSION]\n",
	"status": "Usage: agentctl status [--session SESSION] [--json]\n\n" +
		"Without --session, status reports every session; ambient session sources never narrow the listing.\n" +
		"A leading * marks the caller's session when agentctl can determine it from tmux.\n" +
		"Exited agents normally report missing, not dead, because managed windows do not use remain-on-exit.\n",
	"clear":   "Usage: agentctl clear [--session SESSION] ROLE\n",
	"compact": "Usage: agentctl compact [--session SESSION] ROLE\n",
	"kill":    "Usage: agentctl kill [--session SESSION]\n",
	"version": "Usage: agentctl version\n",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	return runWithRunner(context.Background(), arguments, stdout, stderr, tmuxx.RealRunner{}, os.LookupEnv)
}

type launchDependencies struct {
	runner tmuxx.Runner
	fleet  fleet.Dependencies
}

type sessionResolver interface {
	Resolve(context.Context, *string) (tmuxx.Session, error)
}

type sessionKiller interface {
	Execute(context.Context, tmuxx.Session) error
}

type statusCollector interface {
	Collect(context.Context, string, tmuxx.SessionID) (statuspkg.Report, error)
	CollectAll(context.Context) (*statuspkg.SessionsReport, error)
}

type controlExecutor interface {
	Execute(context.Context, string, tmuxx.Session, string) error
}

type sessionAttacher interface {
	CheckEnvironment() error
	Execute(context.Context, tmuxx.Session, io.Writer) error
	StillRunning(context.Context, tmuxx.Session) (bool, error)
}

type roleRelauncher interface {
	Relaunch(context.Context, tmuxx.Session, fleet.RelaunchRequest) (fleet.RelaunchResult, error)
}

// dependencies collects the seams every subcommand is wired through, so tests
// can supply exactly the ones the command under test uses.
type dependencies struct {
	launch     launchDependencies
	resolver   sessionResolver
	collector  statusCollector
	killer     sessionKiller
	controller controlExecutor
	attacher   sessionAttacher
	relauncher roleRelauncher
}

func runWithRunner(
	ctx context.Context,
	arguments []string,
	stdout, stderr io.Writer,
	runner tmuxx.Runner,
	lookupEnv session.LookupEnv,
) int {
	if handled, code := runVersion(arguments, stdout, stderr); handled {
		return code
	}
	client := tmuxx.New(runner)
	resolver := session.New(client, lookupEnv)
	collector := statuspkg.NewCollector(client).WithLookupEnv(statuspkg.LookupEnv(lookupEnv))
	targetResolver := target.New(client, target.LookupEnv(lookupEnv))
	attacher := attach.New(client, attach.LookupEnv(lookupEnv))
	return runWithAllDependencies(ctx, arguments, stdout, stderr, dependencies{
		launch:     launchDependencies{runner: runner},
		resolver:   resolver,
		collector:  collector,
		killer:     kill.New(client),
		controller: control.New(targetResolver, client),
		attacher:   attacher,
		relauncher: fleet.New(runner, fleet.Dependencies{}),
	})
}

func runVersion(arguments []string, stdout, stderr io.Writer) (bool, int) {
	if len(arguments) == 1 && arguments[0] == "--version" {
		fmt.Fprintf(stdout, "agentctl %s\n", buildinfo.Current())
		return true, exitOK
	}
	if len(arguments) == 0 || arguments[0] != "version" {
		return false, exitOK
	}
	if len(arguments) != 1 {
		return true, usageError(stderr, "version accepts no arguments", commandUsage["version"])
	}
	fmt.Fprintf(stdout, "agentctl %s\n", buildinfo.Current())
	return true, exitOK
}

func runWithResolver(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver) int {
	return runWithAllDependencies(ctx, arguments, stdout, stderr, dependencies{resolver: resolver})
}

func runWithDependencies(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver, killer sessionKiller) int {
	return runWithAllDependencies(ctx, arguments, stdout, stderr, dependencies{resolver: resolver, killer: killer})
}

func runWithControlDependencies(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver, controller controlExecutor) int {
	return runWithAllDependencies(ctx, arguments, stdout, stderr, dependencies{resolver: resolver, controller: controller})
}

func runWithRelaunchDependencies(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver, relauncher roleRelauncher) int {
	return runWithAllDependencies(ctx, arguments, stdout, stderr, dependencies{resolver: resolver, relauncher: relauncher})
}

func runWith(arguments []string, stdout, stderr io.Writer, launch launchDependencies) int {
	return runWithAllDependencies(context.Background(), arguments, stdout, stderr, dependencies{launch: launch})
}

func runWithAllDependencies(
	ctx context.Context,
	arguments []string,
	stdout, stderr io.Writer,
	deps dependencies,
) int {
	if len(arguments) == 0 {
		return usageError(stderr, "command required", globalUsage)
	}
	if arguments[0] == "-h" || arguments[0] == "--help" {
		fmt.Fprint(stdout, globalUsage)
		return exitOK
	}

	command := arguments[0]
	usage, ok := commandUsage[command]
	if !ok {
		return usageError(stderr, fmt.Sprintf("unknown command %q", command), globalUsage)
	}

	if command == "launch" {
		options, err := parseLaunch(arguments[1:])
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		if err != nil {
			return usageError(stderr, err.Error(), usage)
		}
		if err := config.ValidateSessionName(options.session); err != nil {
			return usageError(stderr, err.Error(), usage)
		}
		fleetConfig, err := config.ParseFleet(options.roles, options.models, options.efforts)
		if err != nil {
			return usageError(stderr, err.Error(), usage)
		}
		err = fleet.New(deps.launch.runner, deps.launch.fleet).Launch(ctx, options.session, fleetConfig, options.directory)
		return launchResult(stderr, err, usage)
	}

	options, err := parseCommand(command, arguments[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	if err != nil {
		return usageError(stderr, err.Error(), usage)
	}
	if command == "clear" || command == "compact" || command == "relaunch" {
		if err := config.ValidateRoleName(options.role); err != nil {
			return usageError(stderr, err.Error(), usage)
		}
	}
	if command == "relaunch" {
		if err := validateRelaunchOverrides(options); err != nil {
			return usageError(stderr, err.Error(), usage)
		}
	}
	if command == "attach" {
		if err := deps.attacher.CheckEnvironment(); err != nil {
			return attachError(stderr, err)
		}
	}
	if command == "status" && !options.sessionSet {
		return statusAll(ctx, stdout, stderr, deps.collector, options.json)
	}
	var explicit *string
	if options.sessionSet {
		explicit = &options.session
	}
	resolved, err := deps.resolver.Resolve(ctx, explicit)
	if err != nil {
		return resolverError(stderr, usage, err)
	}
	if command == "status" {
		report, err := deps.collector.Collect(ctx, resolved.Name, resolved.ID)
		if err != nil {
			return statusError(stderr, err)
		}
		if options.json {
			err = statuspkg.WriteJSON(stdout, report)
		} else {
			err = statuspkg.WriteTable(stdout, report)
		}
		if err != nil {
			return statusError(stderr, err)
		}
		return exitOK
	}
	if command == "kill" {
		if err := deps.killer.Execute(ctx, resolved); err != nil {
			return killError(stderr, err)
		}
		return exitOK
	}
	if command == "attach" {
		if err := deps.attacher.Execute(ctx, resolved, stdout); err != nil {
			return attachError(stderr, err)
		}
		observation, state := attachSessionState(ctx, deps.attacher, resolved)
		fmt.Fprintf(stdout, "agentctl: control-mode attachment to session %q ended (tmux exit 0); %s\n", resolved.Name, state)
		writeAttachNextSteps(stdout, observation, resolved)
		return exitOK
	}
	if command == "clear" || command == "compact" {
		if err := deps.controller.Execute(ctx, command, resolved, options.role); err != nil {
			return controlError(stderr, command, usage, err)
		}
		registered, _ := control.Lookup(command)
		fmt.Fprintf(stdout, "agentctl: delivered %s to %s:%s\n", registered.Payload, resolved.Name, options.role)
		return exitOK
	}
	if command == "relaunch" {
		result, err := deps.relauncher.Relaunch(ctx, resolved, fleet.RelaunchRequest{
			Role:      options.role,
			Harness:   options.harness,
			Model:     options.model,
			Directory: options.directory,
		})
		if err != nil {
			return relaunchError(stderr, options.role, usage, err)
		}
		writeRelaunchResult(stdout, result)
		return exitOK
	}

	panic(fmt.Sprintf("unreachable command dispatch for %q", command))
}

// statusAll reports every session for a status command without --session.
func statusAll(ctx context.Context, stdout, stderr io.Writer, collector statusCollector, asJSON bool) int {
	report, collectErr := collector.CollectAll(ctx)
	if report == nil {
		return statusError(stderr, collectErr)
	}
	var err error
	if asJSON {
		err = statuspkg.WriteSessionsJSON(stdout, *report)
	} else {
		err = statuspkg.WriteSessionsTable(stdout, *report)
	}
	if err != nil {
		return statusError(stderr, err)
	}
	if collectErr != nil {
		return statusError(stderr, collectErr)
	}
	return exitOK
}

// attachSessionState renders what agentctl observed about the session after
// control mode ended. The probe is advisory and never changes the exit code:
// the attachment itself completed, and a failed probe is reported as an
// unverified state rather than as an absence.
type attachObservation uint8

const (
	attachUnverifiable attachObservation = iota
	attachStillRunning
	attachNoLongerPresent
)

func attachSessionState(ctx context.Context, attacher sessionAttacher, target tmuxx.Session) (attachObservation, string) {
	present, err := attacher.StillRunning(ctx, target)
	switch {
	case err != nil:
		return attachUnverifiable, fmt.Sprintf("could not verify whether session %s is still running: %v", target.ID, err)
	case present:
		return attachStillRunning, fmt.Sprintf("session %s is still running", target.ID)
	default:
		return attachNoLongerPresent, fmt.Sprintf("session %s is no longer present", target.ID)
	}
}

func writeAttachNextSteps(out io.Writer, observation attachObservation, target tmuxx.Session) {
	switch observation {
	case attachStillRunning:
		fmt.Fprintf(out, "agentctl: session %q is still running.\n", target.Name)
		fmt.Fprintf(out, "agentctl:   re-attach:     agentctl attach --session %s\n", target.Name)
		fmt.Fprintf(out, "agentctl:   check status:  agentctl status --session %s\n", target.Name)
		fmt.Fprintf(out, "agentctl:   stop it:       agentctl kill --session %s\n", target.Name)
	case attachUnverifiable:
		fmt.Fprintf(out, "agentctl:   check status:  agentctl status --session %s\n", target.Name)
	}
}

func attachError(stderr io.Writer, err error) int {
	var environment *attach.EnvironmentError
	if errors.As(err, &environment) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitUnclassified
	}
	var refusal *attach.RefusalError
	if errors.As(err, &refusal) {
		fmt.Fprintf(stderr, "agentctl: refusing to attach; %v; to attach anyway, run: tmux -CC attach-session -t '=%s'\n", refusal, refusal.Session.Name)
		return exitSession
	}
	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitTmux
	}
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	return exitUnclassified
}

type launchOptions struct {
	session   string
	roles     string
	models    *string
	efforts   *string
	directory *string
}

func parseLaunch(arguments []string) (launchOptions, error) {
	flags := cliflags.New("launch")
	session := flags.String("session", "", "session name")
	roles := flags.String("roles", "", "role and harness assignments")
	models := flags.String("models", "", "role and model assignments")
	efforts := flags.String("efforts", "", "role and effort assignments")
	directory := flags.String("dir", "", "working directory")

	if err := flags.Parse(arguments); err != nil {
		return launchOptions{}, err
	}
	if len(flags.Args()) != 0 {
		return launchOptions{}, errors.New("launch accepts no positional arguments")
	}

	options := launchOptions{session: *session, roles: *roles}
	if flags.WasSet("models") {
		modelValue := *models
		options.models = &modelValue
	}
	if flags.WasSet("efforts") {
		effortValue := *efforts
		options.efforts = &effortValue
	}
	if flags.WasSet("dir") {
		directoryValue := *directory
		options.directory = &directoryValue
	}
	return options, nil
}

func launchResult(stderr io.Writer, err error, usage string) int {
	if err == nil {
		return exitOK
	}

	var directoryError *fleet.DirectoryError
	if errors.As(err, &directoryError) {
		return usageError(stderr, err.Error(), usage)
	}
	var missing *preflight.MissingExecutableError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitMissingExecutable
	}
	var exists *fleet.SessionExistsError
	if errors.As(err, &exists) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitSession
	}
	var launchError *fleet.LaunchError
	if errors.As(err, &launchError) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitLaunch
	}

	fmt.Fprintf(stderr, "agentctl: %v\n", tmuxx.ClassifyError(err))
	return exitTmux
}

func killError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	var refusal *kill.RefusalError
	if errors.As(err, &refusal) {
		return exitSession
	}
	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		return exitTmux
	}
	return exitUnclassified
}

type commandOptions struct {
	session    string
	sessionSet bool
	json       bool
	role       string
	harness    *string
	model      *string
	directory  *string
}

func parseCommand(command string, arguments []string) (commandOptions, error) {
	flags := cliflags.New(command)
	sessionValue := flags.String("session", "", "session name")

	var roles, models *string
	var harness, model, directory *string
	var jsonOutput *bool
	switch command {
	case "launch":
		roles = flags.String("roles", "", "role and harness assignments")
		models = flags.String("models", "", "role and model assignments")
		flags.String("dir", "", "working directory")
	case "status":
		jsonOutput = flags.Bool("json", false, "emit JSON")
	case "relaunch":
		harness = flags.String("harness", "", "harness override")
		model = flags.String("model", "", "model override")
		directory = flags.String("dir", "", "working directory override")
	}

	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	options := commandOptions{session: *sessionValue, sessionSet: flags.WasSet("session")}
	if jsonOutput != nil {
		options.json = *jsonOutput
	}
	if command == "relaunch" {
		options.harness = suppliedValue(flags, "harness", harness)
		options.model = suppliedValue(flags, "model", model)
		options.directory = suppliedValue(flags, "dir", directory)
	}

	positional := flags.Args()
	switch command {
	case "launch":
		if !options.sessionSet {
			return commandOptions{}, errors.New("--session is required")
		}
		if err := config.ValidateSessionName(options.session); err != nil {
			return commandOptions{}, err
		}
		if *roles == "" {
			return commandOptions{}, errors.New("--roles is required")
		}
		if flags.WasSet("models") && *models == "" {
			return commandOptions{}, errors.New("--models must not be empty")
		}
		if len(positional) != 0 {
			return commandOptions{}, errors.New("launch accepts no positional arguments")
		}
	case "clear", "compact", "relaunch":
		if len(positional) != 1 {
			return commandOptions{}, fmt.Errorf("%s requires exactly one ROLE", command)
		}
		options.role = positional[0]
	case "status":
		if len(positional) != 0 {
			return commandOptions{}, errors.New("status accepts no positional arguments")
		}
	default:
		if len(positional) != 0 {
			return commandOptions{}, fmt.Errorf("%s accepts no positional arguments", command)
		}
	}

	return options, nil
}

// suppliedValue distinguishes an omitted option from one explicitly set, so a
// relaunch never treats absence as an override.
func suppliedValue(flags *cliflags.Set, name string, destination *string) *string {
	if destination == nil || !flags.WasSet(name) {
		return nil
	}
	value := *destination
	return &value
}

// validateRelaunchOverrides applies the same value rules as launch, before any
// tmux command runs.
func validateRelaunchOverrides(options commandOptions) error {
	if options.harness != nil {
		if _, err := config.ParseHarness(*options.harness); err != nil {
			return err
		}
	}
	if options.model != nil {
		if err := config.ValidateModelName(*options.model); err != nil {
			return err
		}
	}
	return nil
}

func writeRelaunchResult(stdout io.Writer, result fleet.RelaunchResult) {
	fmt.Fprintf(stdout, "agentctl: relaunched %s in %s: window %s, pane %s, harness %s (%s), model %s (%s), dir %s (%s)\n",
		result.Role, result.Session, result.WindowID, result.PaneID,
		result.Harness, result.HarnessFrom,
		renderModel(result.Model), result.ModelFrom,
		result.Directory, result.DirectoryFrom,
	)
	if result.StoredDirectory != "" {
		fmt.Fprintf(stdout, "agentctl: %s now runs in %s; the fleet's recorded directory %s is unchanged\n",
			result.Role, result.Directory, result.StoredDirectory)
	}
}

// renderModel applies the human-output rule for a defaulted model; metadata and
// machine-readable output keep the empty string.
func renderModel(model string) string {
	if model == "" {
		return "default"
	}
	return model
}

func relaunchError(stderr io.Writer, role, usage string, err error) int {
	var validation *config.ValidationError
	if errors.As(err, &validation) {
		return usageError(stderr, err.Error(), usage)
	}
	var directoryError *fleet.DirectoryError
	if errors.As(err, &directoryError) {
		return usageError(stderr, err.Error(), usage)
	}
	var missing *preflight.MissingExecutableError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitMissingExecutable
	}
	var creation *fleet.WindowCreationError
	if errors.As(err, &creation) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitTmux
	}
	var relaunchFailure *fleet.RelaunchError
	if errors.As(err, &relaunchFailure) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitLaunch
	}

	var gate *fleet.SessionGateError
	var roster *fleet.RosterError
	var metadata *fleet.MetadataError
	var legacy *fleet.LegacySessionError
	var storedDirectory *fleet.StoredDirectoryError
	if errors.As(err, &gate) || errors.As(err, &roster) || errors.As(err, &metadata) ||
		errors.As(err, &legacy) || errors.As(err, &storedDirectory) {
		relaunchRefusal(stderr, role, err)
		return exitSession
	}
	var unknownRole *fleet.UnknownRoleError
	var present *fleet.WindowPresentError
	if errors.As(err, &unknownRole) || errors.As(err, &present) {
		relaunchRefusal(stderr, role, err)
		return exitRole
	}

	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitTmux
	}
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	return exitUnclassified
}

func relaunchRefusal(stderr io.Writer, role string, err error) {
	fmt.Fprintf(stderr, "agentctl: refusing to relaunch %s; %v\n", role, err)
}

func controlError(stderr io.Writer, operation, usage string, err error) int {
	var unknown *control.UnknownOperationError
	if errors.As(err, &unknown) {
		return usageError(stderr, err.Error(), usage)
	}
	var sessionMetadata *target.SessionMetadataError
	if errors.As(err, &sessionMetadata) {
		controlRefusal(stderr, operation, "session %s has %s=%q; expected %q", sessionMetadata.Session.Name, sessionMetadata.Option, sessionMetadata.Value, "1")
		return exitSession
	}
	var roleResolution *target.RoleResolutionError
	if errors.As(err, &roleResolution) {
		if len(roleResolution.WindowIDs) == 0 {
			controlRefusal(stderr, operation, "role %s matches no windows in %s", roleResolution.Role, roleResolution.Session.Name)
		} else {
			ids := make([]string, len(roleResolution.WindowIDs))
			for index, id := range roleResolution.WindowIDs {
				ids[index] = string(id)
			}
			controlRefusal(stderr, operation, "role %s matches %d windows in %s (%s)", roleResolution.Role, len(ids), roleResolution.Session.Name, strings.Join(ids, ", "))
		}
		return exitRole
	}
	var windowMetadata *target.WindowMetadataError
	if errors.As(err, &windowMetadata) {
		if windowMetadata.Window.Managed != "1" {
			controlRefusal(stderr, operation, "window %s for %s:%s has @agentctl_managed=%q; expected %q", windowMetadata.Window.ID, windowMetadata.Session.Name, windowMetadata.Role, windowMetadata.Window.Managed, "1")
		} else {
			controlRefusal(stderr, operation, "window %s named %s has stored role %q; expected %q", windowMetadata.Window.ID, windowMetadata.Window.Name, windowMetadata.Window.Role, windowMetadata.Role)
		}
		return exitRole
	}
	var paneState *target.PaneStateError
	if errors.As(err, &paneState) {
		switch {
		case len(paneState.Panes) != 1:
			controlRefusal(stderr, operation, "window %s for %s:%s contains %d panes; expected 1", paneState.Window.ID, paneState.Session.Name, paneState.Role, len(paneState.Panes))
		case paneState.Panes[0].WindowPanes != 1:
			controlRefusal(stderr, operation, "pane %s reports %d panes in window %s; expected 1", paneState.Panes[0].ID, paneState.Panes[0].WindowPanes, paneState.Window.ID)
		default:
			controlRefusal(stderr, operation, "%s:%s pane %s is dead", paneState.Session.Name, paneState.Role, paneState.Panes[0].ID)
		}
		return exitUnsafe
	}
	var processIdentity *target.ProcessIdentityError
	if errors.As(err, &processIdentity) {
		switch {
		case processIdentity.Window.Process == "":
			controlRefusal(stderr, operation, "%s:%s has empty @agentctl_process baseline", processIdentity.Session.Name, processIdentity.Role)
		case processIdentity.Err != nil:
			controlRefusal(stderr, operation, "%s:%s process identity is unavailable for pane %s: %v", processIdentity.Session.Name, processIdentity.Role, processIdentity.Pane.ID, processIdentity.Err)
		default:
			controlRefusal(stderr, operation, "%s:%s pane %s is running %q; recorded process is %q", processIdentity.Session.Name, processIdentity.Role, processIdentity.Pane.ID, processIdentity.ActualProcess, processIdentity.Window.Process)
		}
		return exitUnsafe
	}
	var selfTarget *target.SelfTargetError
	if errors.As(err, &selfTarget) {
		controlRefusal(stderr, operation, "%s:%s is the calling pane %s", selfTarget.Session.Name, selfTarget.Role, selfTarget.CallerPane)
		return exitUnsafe
	}
	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		fmt.Fprintf(stderr, "agentctl: %v\n", err)
		return exitTmux
	}
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	return exitUnclassified
}

func controlRefusal(stderr io.Writer, operation, format string, arguments ...any) {
	fmt.Fprintf(stderr, "agentctl: refusing to send %s; ", operation)
	fmt.Fprintf(stderr, format, arguments...)
	fmt.Fprintln(stderr)
}

func resolverError(stderr io.Writer, usage string, err error) int {
	var usageFailure *session.UsageError
	if errors.As(err, &usageFailure) {
		return usageError(stderr, err.Error(), usage)
	}
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	var resolutionFailure *session.ResolutionError
	if errors.As(err, &resolutionFailure) {
		return exitSession
	}
	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		return exitTmux
	}
	return exitUnclassified
}

func statusError(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
	var versionFailure *statuspkg.VersionError
	if errors.As(err, &versionFailure) {
		return exitSession
	}
	var rosterFailure *statuspkg.RosterError
	if errors.As(err, &rosterFailure) {
		return exitSession
	}
	var tmuxFailure *tmuxx.TmuxError
	if errors.As(err, &tmuxFailure) {
		return exitTmux
	}
	return exitUnclassified
}

func usageError(stderr io.Writer, message, usage string) int {
	fmt.Fprintf(stderr, "agentctl: %s\n%s", message, usage)
	return exitUsage
}
