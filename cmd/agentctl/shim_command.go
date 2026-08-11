//go:build darwin

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/cliflags"
	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

type hiddenShimOptions struct {
	session        string
	role           string
	harness        string
	harnessOptions harness.Options
}

func parseHiddenShimCommand(arguments []string) (hiddenShimOptions, error) {
	flags := cliflags.New("__shim")
	session := flags.String("session", "", "internal session identity")
	role := flags.String("role", "", "internal role identity")
	harnessName := flags.String("harness", "", "internal harness identity")
	model := flags.String("model", "", "internal model configuration")
	effort := flags.String("effort", "", "internal effort configuration")
	if err := flags.Parse(arguments); err != nil {
		return hiddenShimOptions{}, err
	}
	if len(flags.Args()) != 0 {
		return hiddenShimOptions{}, errors.New("hidden shim accepts no positional arguments")
	}
	for name, supplied := range map[string]bool{
		"session": flags.WasSet("session"), "role": flags.WasSet("role"), "harness": flags.WasSet("harness"),
	} {
		if !supplied {
			return hiddenShimOptions{}, fmt.Errorf("missing required internal option --%s", name)
		}
	}
	if err := config.ValidateSessionName(*session); err != nil {
		return hiddenShimOptions{}, err
	}
	if err := config.ValidateRoleName(*role); err != nil {
		return hiddenShimOptions{}, err
	}
	if _, err := config.ParseHarness(*harnessName); err != nil {
		return hiddenShimOptions{}, err
	}
	if flags.WasSet("model") {
		if err := config.ValidateModelName(*model); err != nil {
			return hiddenShimOptions{}, err
		}
	}
	if flags.WasSet("effort") {
		if err := config.ValidateEffort(*effort); err != nil {
			return hiddenShimOptions{}, err
		}
	}
	return hiddenShimOptions{
		session: *session, role: *role, harness: *harnessName,
		harnessOptions: harness.Options{Model: *model, Effort: *effort},
	}, nil
}

type productionHiddenShimCommand struct {
	stdin       *os.File
	stdout      *os.File
	environment func() []string
}

func newProductionHiddenShimCommand() hiddenShimCommand {
	return productionHiddenShimCommand{stdin: os.Stdin, stdout: os.Stdout, environment: os.Environ}
}

func (c productionHiddenShimCommand) Run(ctx context.Context, arguments []string, _, stderr io.Writer) int {
	options, err := parseHiddenShimCommand(arguments)
	if err != nil {
		return hiddenShimInvalidRequest(stderr, options, err)
	}
	namespace, err := shim.OpenNamespace()
	if err != nil {
		return hiddenShimInvalidRequest(stderr, options, err)
	}
	defer func() { _ = namespace.Close() }()
	terminal := ptyx.NewTerminal()
	outerState, err := terminal.Observe(c.stdin)
	if err != nil {
		return hiddenShimFailure(stderr, options, err)
	}
	initialSize, err := outerState.WindowSize()
	if err != nil {
		return hiddenShimFailure(stderr, options, err)
	}
	input, err := ptyx.NewFileEndpoint(c.stdin)
	if err != nil {
		return hiddenShimFailure(stderr, options, err)
	}
	output, err := ptyx.NewFileEndpoint(c.stdout)
	if err != nil {
		return hiddenShimFailure(stderr, options, errors.Join(err, input.Restore()))
	}
	runErr := shim.NewServer(namespace).Run(ctx, shim.RunRequest{
		Session: options.session, Role: options.role, Harness: options.harness,
		HarnessOptions: options.harnessOptions, Environment: c.environment(), InitialSize: initialSize,
		OperatorInput: input, OperatorOutput: output,
		OuterTerminal: c.stdin, OuterState: outerState,
	})
	runErr = errors.Join(runErr, output.Restore(), input.Restore())
	if runErr != nil {
		return hiddenShimFailure(stderr, options, runErr)
	}
	return exitOK
}

func hiddenShimInvalidRequest(stderr io.Writer, options hiddenShimOptions, err error) int {
	fmt.Fprintf(stderr, "agentctl: invalid shim request for session %q role %q: %q; no role was mutated\n", options.session, options.role, err.Error())
	return exitUsage
}

func hiddenShimFailure(stderr io.Writer, options hiddenShimOptions, err error) int {
	var frame *shim.ProtocolFrameError
	if errors.As(err, &frame) && frame.Peer == shim.ProtocolPeerClient {
		switch frame.Direction {
		case shim.ProtocolFrameRead:
			fmt.Fprintf(stderr, "agentctl: could not read protocol frame from connected client: %q (protocol-frame-read-invalid)\n", errorText(frame.Err))
			return exitTmux
		case shim.ProtocolFrameWrite:
			fmt.Fprintf(stderr, "agentctl: could not write protocol frame to connected client: %q (protocol-frame-write-failed)\n", errorText(frame.Err))
			return exitTmux
		}
	}
	var run *shim.LifecycleRunError
	if errors.As(err, &run) {
		if run.CleanupObservation == shim.ProcessAbsent && run.CleanupErr != nil && len(run.Remaining) > 0 {
			fmt.Fprintf(stderr, "agentctl: launch failed for role %q in session %q: %q; cleanup left %s: %q (owned-rollback-incomplete)\n", options.role, options.session, run.Cause.Error(), strings.Join(run.Remaining, ", "), run.CleanupErr.Error())
			return exitLaunch
		}
		if run.CleanupObservation == shim.ProcessAbsent && run.CleanupErr == nil {
			switch run.Outcome {
			case shim.OutcomeReadinessTimeout:
				fmt.Fprintf(stderr, "agentctl: role %q in session %q was not ready after 5s; final tty flags were ICANON=%t ECHO=%t; cleanup observed child absence and removed every artifact owned by this invocation (readiness-timeout)\n", options.role, options.session, run.FinalICANON, run.FinalECHO)
				return exitLaunch
			case shim.OutcomeReadinessObservationFailed:
				fmt.Fprintf(stderr, "agentctl: could not observe harness tty readiness for role %q in session %q: %q; cleanup observed child absence and removed every artifact owned by this invocation (readiness-observation-failed)\n", options.role, options.session, run.Cause.Error())
				return exitLaunch
			case shim.OutcomeChildExitedBeforeReady:
				fmt.Fprintf(stderr, "agentctl: child PID %d exited before harness tty readiness for role %q in session %q; cleanup observed absence and removed every artifact owned by this invocation (child-exited-before-ready)\n", run.ChildPID, options.role, options.session)
				return exitLaunch
			}
		}
		if run.CleanupObservation != shim.ProcessAbsent {
			if run.Outcome == shim.OutcomeReadinessTimeout {
				fmt.Fprintf(stderr, "agentctl: role %q in session %q was not ready after 5s; final tty flags were ICANON=%t ECHO=%t; child PID %d was not observed absent, so ownership and the durable record were retained (readiness-timeout)\n", options.role, options.session, run.FinalICANON, run.FinalECHO, run.ChildPID)
				return exitLaunchUnproven
			}
			fmt.Fprintf(stderr, "agentctl: role %q in session %q failed after child PID %d started: %q; cleanup observation was %s, so ownership and the durable record were retained (ownership-retained)\n", options.role, options.session, run.ChildPID, run.Cause.Error(), run.CleanupObservation)
			return exitLaunchUnproven
		}
	}
	var uncertain *shim.LifecycleCommitUncertainError
	if errors.As(err, &uncertain) {
		cause := uncertain.Err.Error()
		var recordUncertain *shim.RecordCommitUncertainError
		if errors.As(uncertain.Err, &recordUncertain) && recordUncertain.Err != nil {
			cause = recordUncertain.Err.Error()
		}
		fmt.Fprintf(stderr, "agentctl: role %q in session %q has an uncertain durable %s record commit: %q; the record was retained and the role was not reported absent (record-commit-uncertain)\n", options.role, options.session, uncertain.Phase, cause)
		return exitLaunchUnproven
	}
	var retained *shim.LifecycleOwnershipRetainedError
	if errors.As(err, &retained) {
		fmt.Fprintf(stderr, "agentctl: role %q in session %q failed after child PID %d started: %q; cleanup observation was %s, so ownership and the durable record were retained (ownership-retained)\n", options.role, options.session, retained.ChildPID, retained.Cause.Error(), retained.Observation)
		return exitLaunchUnproven
	}
	fmt.Fprintf(stderr, "agentctl: hidden shim for role %q in session %q failed: %q (unclassified)\n", options.role, options.session, err.Error())
	return exitUnclassified
}
