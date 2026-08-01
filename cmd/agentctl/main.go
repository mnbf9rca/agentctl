package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/mnbf9rca/agentctl/internal/cliflags"
)

const (
	exitOK                = 0
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

	err := parseCommand(command, arguments[1:])
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(stdout, usage)
		return exitOK
	}
	if err != nil {
		return usageError(stderr, err.Error(), usage)
	}

	fmt.Fprintf(stderr, "agentctl: %s: not implemented\n", command)
	return stubExitCode(command)
}

func parseCommand(command string, arguments []string) error {
	flags := cliflags.New(command)
	session := flags.String("session", "", "session name")

	var roles *string
	switch command {
	case "launch":
		roles = flags.String("roles", "", "role and harness assignments")
		flags.String("models", "", "role and model assignments")
		flags.String("dir", "", "working directory")
	case "status":
		flags.Bool("json", false, "emit JSON")
	}

	if err := flags.Parse(arguments); err != nil {
		return err
	}

	positional := flags.Args()
	switch command {
	case "launch":
		if *session == "" {
			return errors.New("--session is required")
		}
		if *roles == "" {
			return errors.New("--roles is required")
		}
		if len(positional) != 0 {
			return errors.New("launch accepts no positional arguments")
		}
	case "clear", "compact":
		if len(positional) != 1 {
			return fmt.Errorf("%s requires exactly one ROLE", command)
		}
	default:
		if len(positional) != 0 {
			return fmt.Errorf("%s accepts no positional arguments", command)
		}
	}

	return nil
}

func usageError(stderr io.Writer, message, usage string) int {
	fmt.Fprintf(stderr, "agentctl: %s\n%s", message, usage)
	return exitUsage
}

func stubExitCode(command string) int {
	switch command {
	case "launch":
		return exitLaunch
	case "clear", "compact":
		return exitRole
	default:
		return exitSession
	}
}
