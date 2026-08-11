//go:build darwin

package main

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

func TestParseHiddenShimCommandAcceptsOnlyValidatedLifecycleFields(t *testing.T) {
	got, err := parseHiddenShimCommand([]string{
		"--session", "fleet", "--role", "planner", "--harness", "codex",
		"--model", "gpt-5.6-sol", "--effort", "high",
	})
	if err != nil {
		t.Fatalf("parseHiddenShimCommand() error = %v", err)
	}
	want := hiddenShimOptions{
		session: "fleet", role: "planner", harness: "codex",
		harnessOptions: harness.Options{Model: "gpt-5.6-sol", Effort: "high"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseHiddenShimCommand() = %#v, want %#v", got, want)
	}
}

func TestHiddenShimFailureUsesExactCommitUncertainAndOwnershipRetainedRows(t *testing.T) {
	options := hiddenShimOptions{session: "fleet", role: "planner"}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "commit uncertain",
			err:  &shim.LifecycleCommitUncertainError{Phase: "child-recorded", Err: &shim.RecordCommitUncertainError{Err: errors.New("directory sync failed")}},
			want: "agentctl: role \"planner\" in session \"fleet\" has an uncertain durable child-recorded record commit: \"directory sync failed\"; the record was retained and the role was not reported absent (record-commit-uncertain)\n",
		},
		{
			name: "ownership retained",
			err:  &shim.LifecycleOwnershipRetainedError{ChildPID: 456, Observation: shim.ProcessPresentMatch, Cause: errors.New("listener failed")},
			want: "agentctl: role \"planner\" in session \"fleet\" failed after child PID 456 started: \"listener failed\"; cleanup observation was present-match, so ownership and the durable record were retained (ownership-retained)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr strings.Builder
			if got := hiddenShimFailure(&stderr, options, tt.err); got != exitLaunchUnproven {
				t.Fatalf("hiddenShimFailure() exit = %d, want %d", got, exitLaunchUnproven)
			}
			if got := stderr.String(); got != tt.want {
				t.Fatalf("stderr = %s, want %s", fmt.Sprintf("%q", got), fmt.Sprintf("%q", tt.want))
			}
		})
	}
}

func TestHiddenShimFailureUsesExactReadinessRows(t *testing.T) {
	options := hiddenShimOptions{session: "fleet", role: "planner"}
	tests := []struct {
		name string
		err  error
		exit int
		want string
	}{
		{
			name: "timeout cleaned",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessTimeout, ChildPID: 456, Cause: errors.New("timeout"), CleanupObservation: shim.ProcessAbsent, FinalICANON: true},
			exit: exitLaunch,
			want: "agentctl: role \"planner\" in session \"fleet\" was not ready after 5s; final tty flags were ICANON=true ECHO=false; cleanup observed child absence and removed every artifact owned by this invocation (readiness-timeout)\n",
		},
		{
			name: "observation cleaned",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessObservationFailed, ChildPID: 456, Cause: errors.New("ioctl failed"), CleanupObservation: shim.ProcessAbsent},
			exit: exitLaunch,
			want: "agentctl: could not observe harness tty readiness for role \"planner\" in session \"fleet\": \"ioctl failed\"; cleanup observed child absence and removed every artifact owned by this invocation (readiness-observation-failed)\n",
		},
		{
			name: "child exited cleaned",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeChildExitedBeforeReady, ChildPID: 456, Cause: errors.New("child exited"), CleanupObservation: shim.ProcessAbsent},
			exit: exitLaunch,
			want: "agentctl: child PID 456 exited before harness tty readiness for role \"planner\" in session \"fleet\"; cleanup observed absence and removed every artifact owned by this invocation (child-exited-before-ready)\n",
		},
		{
			name: "timeout retained",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessTimeout, ChildPID: 456, Cause: errors.New("timeout"), CleanupObservation: shim.ProcessPresentMatch, FinalECHO: true},
			exit: exitLaunchUnproven,
			want: "agentctl: role \"planner\" in session \"fleet\" was not ready after 5s; final tty flags were ICANON=false ECHO=true; child PID 456 was not observed absent, so ownership and the durable record were retained (readiness-timeout)\n",
		},
		{
			name: "rollback incomplete",
			err:  &shim.LifecycleRunError{Outcome: shim.OutcomeReadinessObservationFailed, ChildPID: 456, Cause: errors.New("ioctl failed"), CleanupObservation: shim.ProcessAbsent, CleanupErr: errors.New("remove record: permission denied"), Remaining: []string{"record", "lock"}},
			exit: exitLaunch,
			want: "agentctl: launch failed for role \"planner\" in session \"fleet\": \"ioctl failed\"; cleanup left record, lock: \"remove record: permission denied\" (owned-rollback-incomplete)\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr strings.Builder
			if got := hiddenShimFailure(&stderr, options, tt.err); got != tt.exit {
				t.Fatalf("exit = %d, want %d", got, tt.exit)
			}
			if got := stderr.String(); got != tt.want {
				t.Fatalf("stderr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestHiddenShimFailureUsesExactConnectedClientFrameRows(t *testing.T) {
	options := hiddenShimOptions{session: "fleet", role: "planner"}
	for _, test := range []struct {
		name      string
		direction shim.ProtocolFrameDirection
		cause     string
		want      string
	}{
		{name: "read", direction: shim.ProtocolFrameRead, cause: "unexpected EOF", want: "agentctl: could not read protocol frame from connected client: \"unexpected EOF\" (protocol-frame-read-invalid)\n"},
		{name: "write", direction: shim.ProtocolFrameWrite, cause: "broken pipe", want: "agentctl: could not write protocol frame to connected client: \"broken pipe\" (protocol-frame-write-failed)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr strings.Builder
			err := &shim.ProtocolFrameError{Direction: test.direction, Peer: shim.ProtocolPeerClient, Err: errors.New(test.cause)}
			if got := hiddenShimFailure(&stderr, options, err); got != exitTmux || stderr.String() != test.want {
				t.Fatalf("hiddenShimFailure() = %d, %q; want %d, %q", got, stderr.String(), exitTmux, test.want)
			}
		})
	}
}

func TestParseHiddenShimCommandRejectsRawCommandsPayloadsAndMalformedIdentity(t *testing.T) {
	tests := [][]string{
		nil,
		{"--session", "fleet", "--role", "planner"},
		{"--session", "fleet", "--role", "planner", "--harness", "bash"},
		{"--session", "Fleet", "--role", "planner", "--harness", "codex"},
		{"--session", "fleet", "--role", "-planner", "--harness", "codex"},
		{"--session", "fleet", "--role", "planner", "--harness", "codex", "--model", "--unsafe"},
		{"--session", "fleet", "--role", "planner", "--harness", "codex", "--effort", "--invented"},
		{"--session", "fleet", "--role", "planner", "--harness", "codex", "--payload", "/clear"},
		{"--session", "fleet", "--role", "planner", "--harness", "codex", "--command", "sh"},
		{"--session", "fleet", "--session", "other", "--role", "planner", "--harness", "codex"},
		{"--session", "fleet", "--role", "planner", "--harness", "codex", "extra"},
	}
	for _, arguments := range tests {
		if _, err := parseHiddenShimCommand(arguments); err == nil {
			t.Errorf("parseHiddenShimCommand(%q) accepted malformed/internal-expanding arguments", strings.Join(arguments, " "))
		}
	}
}
