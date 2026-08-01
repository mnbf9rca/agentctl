package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mnbf9rca/agentctl/internal/cliflags"
	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/session"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const (
	exitOK                = 0
	exitNotImplemented    = 1
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
  launch   create an agent fleet
  attach   attach an agent fleet in iTerm2
  status   report fleet status
  clear    deliver /clear to a role
  compact  deliver /compact to a role
  kill     terminate a managed fleet
`

var commandUsage = map[string]string{
	"launch": "Usage: agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--dir PATH]\n",
	"attach": "Usage: agentctl attach [--session SESSION]\n",
	"status": "Usage: agentctl status [--session SESSION] [--json]\n\n" +
		"Exited agents normally report missing, not dead, because managed windows do not use remain-on-exit.\n",
	"clear":   "Usage: agentctl clear [--session SESSION] ROLE\n",
	"compact": "Usage: agentctl compact [--session SESSION] ROLE\n",
	"kill":    "Usage: agentctl kill [--session SESSION]\n",
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
}

func runWithRunner(
	ctx context.Context,
	arguments []string,
	stdout, stderr io.Writer,
	runner tmuxx.Runner,
	lookupEnv session.LookupEnv,
) int {
	client := tmuxx.New(runner)
	resolver := session.New(client, lookupEnv)
	collector := statuspkg.NewCollector(client)
	return runWithAllDependencies(ctx, arguments, stdout, stderr, launchDependencies{runner: runner}, resolver, collector, kill.New(client))
}

func runWithResolver(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver) int {
	return runWithAllDependencies(ctx, arguments, stdout, stderr, launchDependencies{}, resolver, nil, nil)
}

func runWithDependencies(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver, killer sessionKiller) int {
	return runWithAllDependencies(ctx, arguments, stdout, stderr, launchDependencies{}, resolver, nil, killer)
}

func runWith(arguments []string, stdout, stderr io.Writer, dependencies launchDependencies) int {
	return runWithAllDependencies(context.Background(), arguments, stdout, stderr, dependencies, nil, nil, nil)
}

func runWithAllDependencies(
	ctx context.Context,
	arguments []string,
	stdout, stderr io.Writer,
	launch launchDependencies,
	resolver sessionResolver,
	collector statusCollector,
	killer sessionKiller,
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
		fleetConfig, err := config.ParseFleet(options.roles, options.models)
		if err != nil {
			return usageError(stderr, err.Error(), usage)
		}
		err = fleet.New(launch.runner, launch.fleet).Launch(ctx, options.session, fleetConfig, options.directory)
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
	var explicit *string
	if options.sessionSet {
		explicit = &options.session
	}
	resolved, err := resolver.Resolve(ctx, explicit)
	if err != nil {
		return resolverError(stderr, usage, err)
	}
	if command == "status" && collector != nil {
		report, err := collector.Collect(ctx, resolved.Name, resolved.ID)
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
		if killer == nil {
			fmt.Fprintln(stderr, "agentctl: kill: not implemented")
			return exitNotImplemented
		}
		if err := killer.Execute(ctx, resolved); err != nil {
			return killError(stderr, err)
		}
		return exitOK
	}

	fmt.Fprintf(stderr, "agentctl: %s: not implemented\n", command)
	return exitNotImplemented
}

type launchOptions struct {
	session   string
	roles     string
	models    *string
	directory *string
}

func parseLaunch(arguments []string) (launchOptions, error) {
	flags := cliflags.New("launch")
	session := flags.String("session", "", "session name")
	roles := flags.String("roles", "", "role and harness assignments")
	models := flags.String("models", "", "role and model assignments")
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

	fmt.Fprintf(stderr, "agentctl: %v\n", err)
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
	return exitNotImplemented
}

type commandOptions struct {
	session    string
	sessionSet bool
	json       bool
}

func parseCommand(command string, arguments []string) (commandOptions, error) {
	flags := cliflags.New(command)
	sessionValue := flags.String("session", "", "session name")

	var roles, models *string
	var jsonOutput *bool
	switch command {
	case "launch":
		roles = flags.String("roles", "", "role and harness assignments")
		models = flags.String("models", "", "role and model assignments")
		flags.String("dir", "", "working directory")
	case "status":
		jsonOutput = flags.Bool("json", false, "emit JSON")
	}

	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	options := commandOptions{session: *sessionValue, sessionSet: flags.WasSet("session")}
	if jsonOutput != nil {
		options.json = *jsonOutput
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
	case "clear", "compact":
		if len(positional) != 1 {
			return commandOptions{}, fmt.Errorf("%s requires exactly one ROLE", command)
		}
	default:
		if len(positional) != 0 {
			return commandOptions{}, fmt.Errorf("%s accepts no positional arguments", command)
		}
	}

	return options, nil
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
	return exitNotImplemented
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
	var tmuxFailure *statuspkg.TmuxError
	if errors.As(err, &tmuxFailure) {
		return exitTmux
	}
	return exitNotImplemented
}

func usageError(stderr io.Writer, message, usage string) int {
	fmt.Fprintf(stderr, "agentctl: %s\n%s", message, usage)
	return exitUsage
}
