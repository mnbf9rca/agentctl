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
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/session"
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
	"launch":  "Usage: agentctl launch --session SESSION --roles ROLE:HARNESS,... [--models ROLE:MODEL,...] [--dir PATH]\n",
	"attach":  "Usage: agentctl attach [--session SESSION]\n",
	"status":  "Usage: agentctl status [--session SESSION] [--json]\n",
	"clear":   "Usage: agentctl clear [--session SESSION] ROLE\n",
	"compact": "Usage: agentctl compact [--session SESSION] ROLE\n",
	"kill":    "Usage: agentctl kill [--session SESSION]\n",
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(arguments []string, stdout, stderr io.Writer) int {
	client := tmuxx.New(tmuxx.RealRunner{})
	resolver := session.New(client, os.LookupEnv)
	return runWithDependencies(context.Background(), arguments, stdout, stderr, resolver, kill.New(client))
}

type sessionResolver interface {
	Resolve(context.Context, *string) (tmuxx.Session, error)
}

type sessionKiller interface {
	Execute(context.Context, tmuxx.Session) error
}

func runWithResolver(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver) int {
	return runWithDependencies(ctx, arguments, stdout, stderr, resolver, nil)
}

func runWithDependencies(ctx context.Context, arguments []string, stdout, stderr io.Writer, resolver sessionResolver, killer sessionKiller) int {
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

	options, err := parseCommand(command, arguments[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	if err != nil {
		return usageError(stderr, err.Error(), usage)
	}
	var resolved tmuxx.Session
	if command != "launch" {
		var explicit *string
		if options.sessionSet {
			explicit = &options.session
		}
		resolved, err = resolver.Resolve(ctx, explicit)
		if err != nil {
			return resolverError(stderr, usage, err)
		}
	}
	if command == "kill" {
		if err := killer.Execute(ctx, resolved); err != nil {
			return killError(stderr, err)
		}
		return exitOK
	}

	fmt.Fprintf(stderr, "agentctl: %s: not implemented\n", command)
	return exitNotImplemented
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
}

func parseCommand(command string, arguments []string) (commandOptions, error) {
	flags := cliflags.New(command)
	sessionValue := flags.String("session", "", "session name")

	var roles, models *string
	switch command {
	case "launch":
		roles = flags.String("roles", "", "role and harness assignments")
		models = flags.String("models", "", "role and model assignments")
		flags.String("dir", "", "working directory")
	case "status":
		flags.Bool("json", false, "emit JSON")
	}

	if err := flags.Parse(arguments); err != nil {
		return commandOptions{}, err
	}
	options := commandOptions{session: *sessionValue, sessionSet: flags.WasSet("session")}

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

func usageError(stderr io.Writer, message, usage string) int {
	fmt.Fprintf(stderr, "agentctl: %s\n%s", message, usage)
	return exitUsage
}
