package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/mnbf9rca/agentctl/internal/attach"
	"github.com/mnbf9rca/agentctl/internal/buildinfo"
	"github.com/mnbf9rca/agentctl/internal/cliflags"
	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/session"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/skillinstall"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
	"github.com/mnbf9rca/agentctl/skills"
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
	exitLaunchUnproven    = 9
)

var globalUsage = `Usage: agentctl COMMAND [OPTIONS]

Commands:
  launch    create an agent fleet
  run       run one foreground role without tmux
  relaunch  recreate a runtime-observed absent role
  attach    attach an agent fleet in iTerm2
  status    report fleet status
  clear     deliver /clear to a role
  compact   deliver /compact to a role
  kill      terminate a managed fleet
  skill     install or inspect the embedded agent skill
  version   report this binary's build identity
`

var commandUsage = map[string]string{
	"launch": "Usage: agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]\n" +
		"   or: agentctl launch --session SESSION --from-template FILE [--roles ROLE:HARNESS,...] [--models ROLE:MODEL,...] [--efforts ROLE:LEVEL,...] [--dir PATH]\n",
	"run": "Usage: agentctl run --session SESSION --role ROLE --harness HARNESS [--model MODEL] [--effort LEVEL]\n",
	"relaunch": "Usage: agentctl relaunch [--session SESSION] [--harness HARNESS] [--model MODEL] [--effort LEVEL] [--dir PATH] ROLE\n\n" +
		"Recreates only a missing role or an ESRCH-backed stale durable child record.\n" +
		"It refuses every state that may still have a live child and persists successful overrides after readiness.\n",
	"attach": "Usage: agentctl attach [--session SESSION]\n",
	"status": "Usage: agentctl status [--session SESSION] [--json]\n\n" +
		"Without --session, status reports every durable fleet; ambient session sources never narrow the listing.\n" +
		"A leading * marks the caller's session when agentctl can determine it from tmux.\n" +
		"Runtime state and anchored/unanchored confidence remain separate from optional tmux presentation.\n",
	"clear":   "Usage: agentctl clear [--session SESSION] ROLE\n",
	"compact": "Usage: agentctl compact [--session SESSION] ROLE\n",
	"kill":    "Usage: agentctl kill [--session SESSION]\n",
	"skill": "Usage: agentctl skill install [--force] | agentctl skill status\n\n" +
		"install writes this binary's embedded agent skill to ~/.claude/skills/agentctl\n" +
		"and ~/.agents/skills/agentctl; it refuses to overwrite files it cannot prove\n" +
		"it wrote. --force replaces an unowned target and reports every removed file.\n" +
		"status reports current|stale|modified|absent|unmanaged\n" +
		"per target.\n",
	"version": "Usage: agentctl version\n",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	return runWithRunner(context.Background(), arguments, stdout, stderr, tmuxx.RealRunner{}, os.LookupEnv)
}

type launchDependencies struct {
	skillHome    func() (string, error)
	skillVersion func() string
}

type sessionResolver interface {
	Select(context.Context, *string) (string, error)
}

type sessionKiller interface {
	Execute(context.Context, string) (kill.ShimKillResult, error)
}

type statusCollector interface {
	Collect(context.Context, string) (statuspkg.ShimReport, error)
	CollectAll(context.Context) (statuspkg.ShimSessionsReport, error)
}

type controlExecutor interface {
	Execute(context.Context, string, string, string) (shim.Response, error)
}

type sessionAttacher interface {
	CheckEnvironment() error
	ExecutePresentation(context.Context, string, io.Writer) (tmuxx.Session, error)
	StillRunning(context.Context, tmuxx.Session) (bool, error)
}

type roleRelauncher interface {
	Relaunch(context.Context, string, fleet.RelaunchRequest) (fleet.ShimRelaunchResult, error)
}

type shimFleetLauncher interface {
	Launch(context.Context, string, config.FleetConfig, *string) (fleet.ShimLaunchResult, error)
}

type hiddenShimCommand interface {
	Run(context.Context, []string, io.Writer, io.Writer) int
}

type hiddenShimCommandFunc func(context.Context, []string, io.Writer, io.Writer) int

func (f hiddenShimCommandFunc) Run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	return f(ctx, arguments, stdout, stderr)
}

type foregroundExecutor interface {
	Execute(context.Context, string, config.RoleConfig, string) error
}

// dependencies collects the seams every subcommand is wired through, so tests
// can supply exactly the ones the command under test uses.
type dependencies struct {
	launch     launchDependencies
	launcher   shimFleetLauncher
	resolver   sessionResolver
	collector  statusCollector
	killer     sessionKiller
	controller controlExecutor
	attacher   sessionAttacher
	relauncher roleRelauncher
	hiddenShim hiddenShimCommand
	foreground foregroundExecutor
	getwd      func() (string, error)
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
	if len(arguments) == 0 || arguments[0] == "-h" || arguments[0] == "--help" || arguments[0] == "skill" || arguments[0] == "__shim" {
		return runWithDependencies(ctx, arguments, stdout, stderr, dependencies{
			launch: launchDependenciesForCLI(), hiddenShim: newProductionHiddenShimCommand(), getwd: os.Getwd,
		})
	}
	if _, known := commandUsage[arguments[0]]; !known {
		return runWithDependencies(ctx, arguments, stdout, stderr, dependencies{})
	}
	if handled, code := validateBeforeRuntime(arguments, stdout, stderr); handled {
		return code
	}
	deps, closeRuntime, err := buildShimDependencies(runner, lookupEnv)
	if err != nil {
		return shimSetupError(stderr, arguments[0], err)
	}
	defer func() { _ = closeRuntime() }()
	return runWithDependencies(ctx, arguments, stdout, stderr, deps)
}

// validateBeforeRuntime keeps argument and value failures ahead of namespace
// construction. Opening the runtime creates private state directories, so a
// malformed command must be rejected before that boundary is crossed.
func validateBeforeRuntime(arguments []string, stdout, stderr io.Writer) (bool, int) {
	command := arguments[0]
	usage := commandUsage[command]
	var err error
	switch command {
	case "launch":
		_, _, err = parseLaunchInvocation(arguments[1:])
	case "run":
		_, _, err = parseRunInvocation(arguments[1:])
	default:
		var options commandOptions
		options, err = parseCommand(command, arguments[1:])
		if err == nil && (command == "clear" || command == "compact" || command == "relaunch") {
			err = config.ValidateRoleName(options.role)
		}
		if err == nil && command == "relaunch" {
			err = validateRelaunchOverrides(options)
		}
	}
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return true, exitOK
	}
	if err != nil {
		return true, usageError(stderr, err.Error(), usage)
	}
	return false, exitOK
}

func parseRunInvocation(arguments []string) (commandOptions, config.RoleConfig, error) {
	options, err := parseCommand("run", arguments)
	if err != nil {
		return commandOptions{}, config.RoleConfig{}, err
	}
	if !options.sessionSet {
		return commandOptions{}, config.RoleConfig{}, errors.New("run requires --session")
	}
	if options.role == "" {
		return commandOptions{}, config.RoleConfig{}, errors.New("run requires --role")
	}
	if options.harness == nil {
		return commandOptions{}, config.RoleConfig{}, errors.New("run requires --harness")
	}
	if err := config.ValidateSessionName(options.session); err != nil {
		return commandOptions{}, config.RoleConfig{}, err
	}
	if err := config.ValidateRoleName(options.role); err != nil {
		return commandOptions{}, config.RoleConfig{}, err
	}
	harnessName, err := config.ParseHarness(*options.harness)
	if err != nil {
		return commandOptions{}, config.RoleConfig{}, err
	}
	role := config.RoleConfig{Name: options.role, Harness: harnessName}
	if options.model != nil {
		if err := config.ValidateModelName(*options.model); err != nil {
			return commandOptions{}, config.RoleConfig{}, err
		}
		role.Model = *options.model
	}
	if options.effort != nil {
		if err := config.ValidateEffort(*options.effort); err != nil {
			return commandOptions{}, config.RoleConfig{}, err
		}
		role.Effort = *options.effort
	}
	return options, role, nil
}

func parseLaunchInvocation(arguments []string) (launchOptions, launchConfiguration, error) {
	options, err := parseLaunch(arguments)
	if err != nil {
		return launchOptions{}, launchConfiguration{}, err
	}
	document, err := decodeLaunchTemplate(options.fromTemplate)
	if err != nil {
		return launchOptions{}, launchConfiguration{}, err
	}
	if err := config.ValidateSessionName(options.session); err != nil {
		return launchOptions{}, launchConfiguration{}, err
	}
	configuration := launchConfiguration{directory: options.directory}
	if document == nil {
		configuration.fleet, err = config.ParseFleet(options.roles, options.models, options.efforts)
	} else {
		configuration, err = mergeLaunchTemplate(*document, options)
	}
	if err != nil {
		return launchOptions{}, launchConfiguration{}, err
	}
	return options, configuration, nil
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

func runWithDependencies(
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
	if arguments[0] == "__shim" {
		if deps.hiddenShim == nil {
			fmt.Fprintln(stderr, "agentctl: hidden shim handler is unavailable")
			return exitUnclassified
		}
		return deps.hiddenShim.Run(ctx, arguments[1:], stdout, stderr)
	}

	command := arguments[0]
	usage, ok := commandUsage[command]
	if !ok {
		return usageError(stderr, fmt.Sprintf("unknown command %q", command), globalUsage)
	}
	if command == "skill" {
		return runSkill(arguments[1:], stdout, stderr, usage)
	}
	if command == "run" {
		options, role, err := parseRunInvocation(arguments[1:])
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		if err != nil {
			return usageError(stderr, err.Error(), usage)
		}
		if deps.foreground == nil {
			fmt.Fprintln(stderr, "agentctl: run failed for session \""+options.session+"\": \"foreground lifecycle is unavailable\" (unclassified)")
			return exitUnclassified
		}
		getwd := deps.getwd
		if getwd == nil {
			getwd = os.Getwd
		}
		directory, err := getwd()
		if err != nil {
			fmt.Fprintf(stderr, "agentctl: run failed for session %q: %q (unclassified)\n", options.session, err.Error())
			return exitUnclassified
		}
		if err := deps.foreground.Execute(ctx, options.session, role, directory); err != nil {
			return foregroundError(stderr, options.session, options.role, err)
		}
		fmt.Fprintf(stderr, "agentctl: foreground role %q in session %q exited with status 0\n", options.role, options.session)
		return exitOK
	}

	if command == "launch" {
		options, launchConfig, err := parseLaunchInvocation(arguments[1:])
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, usage)
			return exitOK
		}
		if err != nil {
			return usageError(stderr, err.Error(), usage)
		}
		if deps.launcher == nil {
			fmt.Fprintf(stderr, "agentctl: launch failed for session %q: %q (unclassified)\n", options.session, "shim launcher is unavailable")
			return exitUnclassified
		}
		launched, err := deps.launcher.Launch(ctx, options.session, launchConfig.fleet, launchConfig.directory)
		if err != nil {
			return shimLaunchError(stderr, options.session, launchConfig.fleet.Roles[0].Name, err)
		}
		writeLaunchTemplateProvenance(stdout, options.session, launchConfig, launched.Directory)
		fmt.Fprintf(stderr, "agentctl: launched session %q; %d roles are ready\n", options.session, launched.TotalRoles)
		writeSkillLaunchNotices(stderr, deps.launch.skillHome, deps.launch.skillVersion)
		return exitOK
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
		return shimStatusAll(ctx, stdout, stderr, deps.collector, options.json)
	}
	var explicit *string
	if options.sessionSet {
		explicit = &options.session
	}
	resolved, err := deps.resolver.Select(ctx, explicit)
	if err != nil {
		return resolverError(stderr, usage, err)
	}
	if command == "status" {
		if err := writeSelectedShimStatus(ctx, stdout, deps.collector, resolved, options.json); err != nil {
			return statusError(stderr, resolved, err)
		}
		return exitOK
	}
	if command == "kill" {
		result, err := deps.killer.Execute(ctx, resolved)
		if err != nil {
			return shimKillError(stderr, resolved, err)
		}
		fmt.Fprintf(stderr, "agentctl: killed session %q; every recorded child was observed absent\n", result.Session)
		return exitOK
	}
	if command == "attach" {
		attached, err := deps.attacher.ExecutePresentation(ctx, resolved, stdout)
		if err != nil {
			return attachError(stderr, err)
		}
		observation, state := attachSessionState(ctx, deps.attacher, attached)
		fmt.Fprintf(stdout, "Attachment to session %q ended (tmux exit 0). %s\n", attached.Name, state)
		writeAttachNextSteps(stdout, observation, attached)
		return exitOK
	}
	if command == "clear" || command == "compact" {
		response, err := deps.controller.Execute(ctx, command, resolved, options.role)
		if err != nil {
			return shimOperationError(stderr, command, resolved, options.role, err)
		}
		return shimResponseResult(stderr, command, resolved, options.role, response)
	}
	if command == "relaunch" {
		result, err := deps.relauncher.Relaunch(ctx, resolved, fleet.RelaunchRequest{
			Role:      options.role,
			Harness:   options.harness,
			Model:     options.model,
			Effort:    options.effort,
			Directory: options.directory,
		})
		if err != nil {
			return shimRelaunchError(stderr, resolved, options.role, err)
		}
		fmt.Fprintf(stderr, "agentctl: relaunched role %q in session %q; the shim is ready\n", result.Role, result.Session)
		return exitOK
	}

	panic(fmt.Sprintf("unreachable command dispatch for %q", command))
}

func runSkill(arguments []string, stdout, stderr io.Writer, usage string) int {
	subcommand, force, err := parseSkill(arguments)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	if err != nil {
		return usageError(stderr, err.Error(), usage)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "agentctl: cannot resolve home directory: %v\n", err)
		return exitUnclassified
	}
	version := skillBinaryVersion()
	targets := skillinstall.Targets(home)

	switch subcommand {
	case "install":
		outcomes, installErr := skillinstall.Install(skills.Tree, skills.Root, version, targets, force)
		for _, outcome := range outcomes {
			destination := stdout
			if outcome.Action == "failed" || outcome.Action == "refused" {
				destination = stderr
			}
			if outcome.Detail == "" {
				fmt.Fprintf(destination, "%s: %s\n", outcome.Target.Dir, outcome.Action)
			} else {
				fmt.Fprintf(destination, "%s: %s: %s\n", outcome.Target.Dir, outcome.Action, outcome.Detail)
			}
			for _, removed := range outcome.Removed {
				fmt.Fprintf(stdout, "%s: removed\n", removed)
			}
		}
		if installErr == nil {
			return exitOK
		}
		if errors.Is(installErr, skillinstall.ErrUnowned) {
			return exitUnsafe
		}
		return exitUnclassified
	case "status":
		reports, statusErr := skillinstall.Status(skills.Tree, skills.Root, version, targets)
		for _, report := range reports {
			installed := report.InstalledVersion
			if installed == "" {
				installed = "none"
			}
			fmt.Fprintf(stdout, "%s: %s (installed %s, binary %s)\n", report.Target.Dir, report.State, installed, version)
		}
		if statusErr != nil {
			fmt.Fprintf(stderr, "agentctl: skill status: %v\n", statusErr)
			return exitUnclassified
		}
		return exitOK
	default:
		panic(fmt.Sprintf("unreachable skill subcommand %q", subcommand))
	}
}

func skillBinaryVersion() string {
	return strings.TrimPrefix(buildinfo.Current(), "v")
}

func writeSkillLaunchNotices(stderr io.Writer, userHomeDir func() (string, error), currentVersion func() string) {
	if userHomeDir == nil || currentVersion == nil {
		return
	}
	home, err := userHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "skill: home directory could not be resolved: %v\n", err)
		return
	}
	version := currentVersion()
	for _, target := range skillinstall.Targets(home) {
		manifest, ok, err := skillinstall.ReadManifest(target.Dir)
		display := skillTargetDisplay(target)
		if err != nil {
			fmt.Fprintf(stderr, "skill: %s manifest could not be read: %v\n", display, err)
			continue
		}
		if ok && manifest.Version != version {
			fmt.Fprintf(stderr, "skill: %s is %s; this binary is %s — run 'agentctl skill install'\n", display, manifest.Version, version)
		}
	}
}

func skillTargetDisplay(target skillinstall.Target) string {
	if target.Harness == "claude" {
		return "~/.claude/skills/agentctl"
	}
	return "~/.agents/skills/agentctl"
}

func parseSkill(arguments []string) (string, bool, error) {
	if len(arguments) == 0 {
		return "", false, errors.New("skill requires install or status")
	}
	if arguments[0] == "-h" || arguments[0] == "--help" {
		return "", false, flag.ErrHelp
	}
	switch arguments[0] {
	case "install":
		flags := cliflags.New("skill install")
		force := flags.Bool("force", false, "replace files whose ownership cannot be proven")
		if err := flags.Parse(arguments[1:]); err != nil {
			return "", false, err
		}
		if len(flags.Args()) != 0 {
			return "", false, errors.New("skill install accepts no positional arguments")
		}
		return "install", *force, nil
	case "status":
		if len(arguments) == 2 && (arguments[1] == "-h" || arguments[1] == "--help") {
			return "", false, flag.ErrHelp
		}
		if len(arguments) != 1 {
			return "", false, errors.New("skill status accepts no arguments")
		}
		return "status", false, nil
	default:
		return "", false, fmt.Errorf("unknown skill subcommand %q", arguments[0])
	}
}

func writeSelectedShimStatus(ctx context.Context, stdout io.Writer, collector statusCollector, sessionName string, asJSON bool) error {
	report, err := collector.Collect(ctx, sessionName)
	if err != nil {
		return err
	}
	if asJSON {
		return statuspkg.WriteShimJSON(stdout, report)
	}
	return statuspkg.WriteShimTable(stdout, report)
}

func shimStatusAll(ctx context.Context, stdout, stderr io.Writer, collector statusCollector, asJSON bool) int {
	report, err := collector.CollectAll(ctx)
	if err != nil {
		return statusError(stderr, "", err)
	}
	if asJSON {
		err = statuspkg.WriteShimSessionsJSON(stdout, report)
	} else {
		err = statuspkg.WriteShimSessionsTable(stdout, report)
	}
	if err != nil {
		return statusError(stderr, "", err)
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
		return attachUnverifiable, fmt.Sprintf("Could not verify whether session %s is still running: %v", target.ID, err)
	case present:
		return attachStillRunning, fmt.Sprintf("Session %s is still running.", target.ID)
	default:
		return attachNoLongerPresent, fmt.Sprintf("Session %s is no longer present.", target.ID)
	}
}

func writeAttachNextSteps(out io.Writer, observation attachObservation, target tmuxx.Session) {
	switch observation {
	case attachStillRunning:
		fmt.Fprintf(out, "\n  re-attach:     agentctl attach --session %s\n", target.Name)
		fmt.Fprintf(out, "  check status:  agentctl status --session %s\n", target.Name)
		fmt.Fprintf(out, "  stop it:       agentctl kill --session %s\n", target.Name)
	case attachUnverifiable:
		fmt.Fprintf(out, "\n  check status:  agentctl status --session %s\n", target.Name)
	}
}

func attachError(stderr io.Writer, err error) int {
	var missingFleet *fleet.ShimFleetMissingError
	if errors.As(err, &missingFleet) {
		fmt.Fprintf(stderr, "agentctl: session %q has no durable fleet configuration\n", missingFleet.Session)
		return exitSession
	}
	var noPresentation *attach.NoPresentationError
	if errors.As(err, &noPresentation) {
		return attachNoPresentationError(stderr, noPresentation)
	}
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
	session      string
	roles        string
	rolesSet     bool
	fromTemplate *string
	models       *string
	efforts      *string
	directory    *string
}

func parseLaunch(arguments []string) (launchOptions, error) {
	flags := cliflags.New("launch")
	session := flags.String("session", "", "session name")
	roles := flags.String("roles", "", "role and harness assignments")
	fromTemplate := flags.String("from-template", "", "JSON launch template")
	models := flags.String("models", "", "role and model assignments")
	efforts := flags.String("efforts", "", "role and effort assignments")
	directory := flags.String("dir", "", "working directory")

	if err := flags.Parse(arguments); err != nil {
		return launchOptions{}, err
	}
	if len(flags.Args()) != 0 {
		return launchOptions{}, errors.New("launch accepts no positional arguments")
	}

	options := launchOptions{session: *session, roles: *roles, rolesSet: flags.WasSet("roles")}
	if flags.WasSet("from-template") {
		templateValue := *fromTemplate
		options.fromTemplate = &templateValue
	}
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

type commandOptions struct {
	session    string
	sessionSet bool
	json       bool
	role       string
	harness    *string
	model      *string
	effort     *string
	directory  *string
}

type parsedCommandSpec struct {
	agentFacing bool
	flags       []string
}

var parsedCommandRegistry = map[string]parsedCommandSpec{
	"attach": {
		flags: []string{"session"},
	},
	"status": {
		agentFacing: true,
		flags:       []string{"session", "json"},
	},
	"clear": {
		agentFacing: true,
		flags:       []string{"session"},
	},
	"compact": {
		agentFacing: true,
		flags:       []string{"session"},
	},
	"kill": {
		agentFacing: true,
		flags:       []string{"session"},
	},
	"relaunch": {
		flags: []string{"session", "harness", "model", "effort", "dir"},
	},
	"run": {
		flags: []string{"session", "role", "harness", "model", "effort"},
	},
	"version": {},
}

func parseCommand(command string, arguments []string) (commandOptions, error) {
	specification, ok := parsedCommandRegistry[command]
	if !ok {
		return commandOptions{}, fmt.Errorf("unknown parsed command %q", command)
	}
	flags := cliflags.New(command)
	var sessionName, roleName, harnessName, modelName, effortName, directory *string
	var jsonOutput *bool
	for _, registered := range specification.flags {
		switch registered {
		case "session":
			sessionName = flags.String(registered, "", "session name")
		case "json":
			jsonOutput = flags.Bool(registered, false, "emit JSON")
		case "harness":
			harnessName = flags.String(registered, "", "harness override")
		case "role":
			roleName = flags.String(registered, "", "role name")
		case "model":
			modelName = flags.String(registered, "", "model override")
		case "effort":
			effortName = flags.String(registered, "", "effort override")
		case "dir":
			directory = flags.String(registered, "", "working directory override")
		default:
			return commandOptions{}, fmt.Errorf("command %q has unsupported registered flag --%s", command, registered)
		}
	}

	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	options := commandOptions{
		harness:   suppliedValue(flags, "harness", harnessName),
		model:     suppliedValue(flags, "model", modelName),
		effort:    suppliedValue(flags, "effort", effortName),
		directory: suppliedValue(flags, "dir", directory),
	}
	if sessionName != nil {
		options.session = *sessionName
		options.sessionSet = flags.WasSet("session")
	}
	if roleName != nil {
		options.role = *roleName
	}
	if jsonOutput != nil {
		options.json = *jsonOutput
	}

	positional := flags.Args()
	switch command {
	case "clear", "compact", "relaunch":
		if len(positional) != 1 {
			return commandOptions{}, fmt.Errorf("%s requires exactly one ROLE", command)
		}
		options.role = positional[0]
	case "status", "run":
		if len(positional) != 0 {
			return commandOptions{}, fmt.Errorf("%s accepts no positional arguments", command)
		}
	default:
		if len(positional) != 0 {
			return commandOptions{}, fmt.Errorf("%s accepts no positional arguments", command)
		}
	}

	return options, nil
}

func foregroundError(stderr io.Writer, sessionName, role string, err error) int {
	var restore *foregroundTerminalRestoreError
	if errors.As(err, &restore) {
		fmt.Fprintf(stderr, "agentctl: run failed for session %q: %q (unclassified)\n", sessionName, restore.Error())
		return exitUnclassified
	}
	var mismatch *fleet.ShimForegroundDirectoryMismatchError
	if errors.As(err, &mismatch) {
		fmt.Fprintf(stderr, "agentctl: refusing to run role %q in session %q; durable fleet directory %q differs from current working directory %q; no role was started or durable record mutated (fleet-directory-disagreement)\n", role, sessionName, mismatch.Stored, mismatch.Current)
		return exitUnsafe
	}
	var rollback *fleet.ShimForegroundRollbackError
	if errors.As(err, &rollback) {
		fmt.Fprintf(stderr, "agentctl: run failed for session %q: %q (unclassified)\n", sessionName, rollback.Error())
		return exitUnclassified
	}
	var lifecycle *shim.LifecycleRunError
	if errors.As(err, &lifecycle) {
		if lifecycle.CleanupObservation == shim.ProcessAbsent && lifecycle.CleanupErr != nil {
			if len(lifecycle.Remaining) > 0 {
				fmt.Fprintf(stderr, "agentctl: run failed for role %q in session %q: %q; cleanup left %s: %q (owned-rollback-incomplete)\n", role, sessionName, errorText(lifecycle.Cause), strings.Join(lifecycle.Remaining, ", "), errorText(lifecycle.CleanupErr))
				return exitLaunch
			}
			fmt.Fprintf(stderr, "agentctl: run failed for session %q: %q (unclassified)\n", sessionName, lifecycle.Error())
			return exitUnclassified
		}
		cleaned := lifecycle.CleanupObservation == shim.ProcessAbsent && lifecycle.CleanupErr == nil
		if cleaned {
			switch lifecycle.Outcome {
			case shim.OutcomeReadinessTimeout:
				fmt.Fprintf(stderr, "agentctl: role %q in session %q was not ready after 5s; final tty flags were ICANON=%t ECHO=%t; cleanup observed child absence and removed every artifact owned by this invocation (readiness-timeout)\n", role, sessionName, lifecycle.FinalICANON, lifecycle.FinalECHO)
			case shim.OutcomeReadinessObservationFailed:
				fmt.Fprintf(stderr, "agentctl: could not observe harness tty readiness for role %q in session %q: %q; cleanup observed child absence and removed every artifact owned by this invocation (readiness-observation-failed)\n", role, sessionName, errorText(lifecycle.Cause))
			case shim.OutcomeChildExitedBeforeReady:
				fmt.Fprintf(stderr, "agentctl: child PID %d exited before harness tty readiness for role %q in session %q; cleanup observed absence and removed every artifact owned by this invocation (child-exited-before-ready)\n", lifecycle.ChildPID, role, sessionName)
			default:
				fmt.Fprintf(stderr, "agentctl: run failed for role %q in session %q: %q; cleanup observed child absence and removed every artifact owned by this invocation (owned-rollback-complete)\n", role, sessionName, errorText(lifecycle.Cause))
			}
			return exitLaunch
		}
		if lifecycle.Outcome == shim.OutcomeReadinessTimeout {
			fmt.Fprintf(stderr, "agentctl: role %q in session %q was not ready after 5s; final tty flags were ICANON=%t ECHO=%t; child PID %d was not observed absent, so ownership and the durable record were retained (readiness-timeout)\n", role, sessionName, lifecycle.FinalICANON, lifecycle.FinalECHO, lifecycle.ChildPID)
		} else {
			fmt.Fprintf(stderr, "agentctl: role %q in session %q failed after child PID %d started: %q; cleanup observation was %s, so ownership and the durable record were retained (ownership-retained)\n", role, sessionName, lifecycle.ChildPID, errorText(lifecycle.Cause), lifecycle.CleanupObservation)
		}
		return exitLaunchUnproven
	}
	var lifecycleCommit *shim.LifecycleCommitUncertainError
	if errors.As(err, &lifecycleCommit) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has an uncertain durable %s record commit: %q; the record was retained and the role was not reported absent (record-commit-uncertain)\n", role, sessionName, lifecycleCommit.Phase, lifecycleCommit.Error())
		return exitLaunchUnproven
	}
	var recordCommit *shim.RecordCommitUncertainError
	if errors.As(err, &recordCommit) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has an uncertain durable fleet-config record commit: %q; the record was retained and the role was not reported absent (record-commit-uncertain)\n", role, sessionName, recordCommit.Error())
		return exitLaunchUnproven
	}
	var retained *shim.LifecycleOwnershipRetainedError
	if errors.As(err, &retained) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q failed after child PID %d started: %q; cleanup observation was %s, so ownership and the durable record were retained (ownership-retained)\n", role, sessionName, retained.ChildPID, errorText(retained.Cause), retained.Observation)
		return exitLaunchUnproven
	}
	var refusal *fleet.ShimRelaunchRefusalError
	if errors.As(err, &refusal) {
		return shimObservationResult(stderr, "run", sessionName, role, refusal.Observation)
	}
	var missing *preflight.MissingExecutableError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "agentctl: required executable %q was not found; no role was mutated\n", missing.Name)
		return exitMissingExecutable
	}
	var child *shim.ForegroundChildExitError
	if errors.As(err, &child) {
		if child.Signal != 0 {
			fmt.Fprintf(stderr, "agentctl: foreground role %q in session %q terminated by signal %s (child-signal)\n", role, sessionName, canonicalSignal(child.Signal))
		} else {
			fmt.Fprintf(stderr, "agentctl: foreground role %q in session %q exited with status %d (child-exit)\n", role, sessionName, child.Status)
		}
		return exitUnclassified
	}
	fmt.Fprintf(stderr, "agentctl: run failed for session %q: %q (unclassified)\n", sessionName, err.Error())
	return exitUnclassified
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func canonicalSignal(signal syscall.Signal) string {
	switch signal {
	case syscall.SIGHUP:
		return "SIGHUP"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	default:
		return fmt.Sprintf("SIG%d", signal)
	}
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
	if options.effort != nil {
		if err := config.ValidateEffort(*options.effort); err != nil {
			return err
		}
	}
	return nil
}

func renderEffort(effort string) string {
	if effort == "" {
		return "default"
	}
	return effort
}

// renderModel applies the human-output rule for a defaulted model; metadata and
// machine-readable output keep the empty string.
func renderModel(model string) string {
	if model == "" {
		return "default"
	}
	return model
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

func statusError(stderr io.Writer, sessionName string, err error) int {
	var missing *fleet.ShimFleetMissingError
	if errors.As(err, &missing) {
		fmt.Fprintf(stderr, "agentctl: session %q has no durable fleet configuration\n", missing.Session)
		return exitSession
	}
	fmt.Fprintf(stderr, "agentctl: %v\n", err)
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
