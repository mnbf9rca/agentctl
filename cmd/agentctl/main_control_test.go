package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/target"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunControlDeliversRegisteredOperationToResolvedRole(t *testing.T) {
	tests := []struct {
		operation string
		role      string
		payload   string
	}{
		{operation: "clear", role: "planner", payload: "/clear"},
		{operation: "compact", role: "reviewer", payload: "/compact"},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			session := tmuxx.Session{ID: "$4", Name: "epic123"}
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				return session, nil
			})
			type invocation struct {
				operation string
				session   tmuxx.Session
				role      string
			}
			var got invocation
			controller := controlExecutorFunc(func(_ context.Context, operation string, target tmuxx.Session, role string) error {
				got = invocation{operation: operation, session: target, role: role}
				return nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{tt.operation, "--session", "epic123", tt.role}, &stdout, &stderr, dependencies{resolver: resolver, controller: controller})

			if code != exitOK {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			want := invocation{operation: tt.operation, session: session, role: tt.role}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Execute() invocation = %#v, want %#v", got, want)
			}
			wantOutput := "agentctl: delivered " + tt.payload + " to epic123:" + tt.role + "\n"
			if stdout.String() != wantOutput {
				t.Fatalf("stdout = %q, want %q", stdout.String(), wantOutput)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunControlRejectsCallerPayloadInputsBeforeDependencies(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "extra positional text", args: []string{"clear", "planner", "erase this"}},
		{name: "text option", args: []string{"clear", "--text", "erase this", "planner"}},
		{name: "command option", args: []string{"compact", "--command=/rename", "planner"}},
		{name: "raw option", args: []string{"clear", "--raw=/rename", "planner"}},
		{name: "keys option", args: []string{"compact", "--keys=C-u", "planner"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolverCalled := false
			controllerCalled := false
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				resolverCalled = true
				return tmuxx.Session{ID: "$4", Name: "epic123"}, nil
			})
			controller := controlExecutorFunc(func(context.Context, string, tmuxx.Session, string) error {
				controllerCalled = true
				return nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), tt.args, &stdout, &stderr, dependencies{resolver: resolver, controller: controller})

			if code != exitUsage {
				t.Fatalf("runWithDependencies(%q) = %d, want %d", tt.args, code, exitUsage)
			}
			if resolverCalled || controllerCalled {
				t.Fatalf("dependency calls = resolver:%v controller:%v, want neither", resolverCalled, controllerCalled)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stdout = %q, stderr = %q, want usage error only", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunControlRejectsMalformedRoleBeforeSessionResolution(t *testing.T) {
	tests := []string{"", "Planner", "bad.role", "-planner"}
	for _, role := range tests {
		t.Run(role, func(t *testing.T) {
			resolverCalled := false
			controllerCalled := false
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				resolverCalled = true
				return tmuxx.Session{ID: "$4", Name: "epic123"}, nil
			})
			controller := controlExecutorFunc(func(context.Context, string, tmuxx.Session, string) error {
				controllerCalled = true
				return nil
			})
			var stdout, stderr bytes.Buffer
			arguments := []string{"clear", "--session", "epic123", role}
			if strings.HasPrefix(role, "-") {
				arguments = []string{"clear", "--session", "epic123", "--", role}
			}

			code := runWithDependencies(context.Background(), arguments, &stdout, &stderr, dependencies{resolver: resolver, controller: controller})

			if code != exitUsage {
				t.Fatalf("runWithDependencies(role %q) = %d, want %d", role, code, exitUsage)
			}
			if resolverCalled || controllerCalled {
				t.Fatalf("dependency calls = resolver:%v controller:%v, want neither", resolverCalled, controllerCalled)
			}
			wantPrefix := "agentctl: invalid role " + strconv.Quote(role) + ": must match ^[a-z0-9][a-z0-9_-]*$\n"
			if !strings.HasPrefix(stderr.String(), wantPrefix) || !strings.Contains(stderr.String(), "Usage: agentctl clear") {
				t.Fatalf("stderr = %q, want prefix %q and clear usage", stderr.String(), wantPrefix)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunControlMapsTypedTargetErrorsFromFields(t *testing.T) {
	session := tmuxx.Session{ID: "$4", Name: "epic123"}
	plannerWindow := tmuxx.Window{
		ID: "@4", Name: "planner", Role: "planner",
		Harness: "codex", Model: "o3", Process: "codex",
	}
	reviewerWindow := tmuxx.Window{
		ID: "@7", Name: "reviewer", Role: "reviewer",
		Harness: "claude", Model: "sonnet", Process: "claude",
	}
	plannerPane := tmuxx.Pane{ID: "%9", PID: 123, Dead: false, WindowPanes: 1}
	reviewerPane := tmuxx.Pane{ID: "%11", PID: 456, Dead: false, WindowPanes: 1}
	tests := []struct {
		name      string
		operation string
		role      string
		err       error
		wantCode  int
		wantError string
	}{
		{
			name:      "session metadata",
			operation: "clear",
			role:      "planner",
			err:       &target.SessionMetadataError{Session: session, Option: "@agentctl_version", Value: "2"},
			wantCode:  exitSession,
			wantError: "agentctl: refusing to send clear; session epic123 has @agentctl_version=\"2\"; expected \"1\"\n",
		},
		{
			name:      "unmanaged session",
			operation: "compact",
			role:      "reviewer",
			err:       &target.SessionMetadataError{Session: session, Option: "@agentctl_managed", Value: ""},
			wantCode:  exitSession,
			wantError: "agentctl: refusing to send compact; session epic123 has @agentctl_managed=\"\"; expected \"1\"\n",
		},
		{
			name:      "missing role",
			operation: "clear",
			role:      "planner",
			err:       &target.RoleResolutionError{Session: session, Role: "planner", WindowIDs: nil},
			wantCode:  exitRole,
			wantError: "agentctl: refusing to send clear; role planner matches no windows in epic123\n",
		},
		{
			name:      "ambiguous role",
			operation: "compact",
			role:      "reviewer",
			err: fmt.Errorf("wrapped target refusal: %w", &target.RoleResolutionError{
				Session: session, Role: "reviewer", WindowIDs: []tmuxx.WindowID{"@4", "@7"},
			}),
			wantCode:  exitRole,
			wantError: "agentctl: refusing to send compact; role reviewer matches 2 windows in epic123 (@4, @7)\n",
		},
		{
			name:      "stored role mismatch",
			operation: "compact",
			role:      "planner",
			err: &target.WindowMetadataError{Session: session, Role: "planner", Window: tmuxx.Window{
				ID: "@4", Name: "planner", Role: "reviewer",
				Harness: "codex", Model: "o3", Process: "codex",
			}},
			wantCode:  exitRole,
			wantError: "agentctl: refusing to send compact; window @4 named planner has stored role \"reviewer\"; expected \"planner\"\n",
		},
		{
			name:      "missing pane",
			operation: "clear",
			role:      "planner",
			err:       &target.PaneStateError{Session: session, Role: "planner", Window: plannerWindow, Panes: nil},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to send clear; window @4 for epic123:planner contains 0 panes; expected 1\n",
		},
		{
			name:      "reported multiple panes",
			operation: "clear",
			role:      "planner",
			err: &target.PaneStateError{Session: session, Role: "planner", Window: plannerWindow, Panes: []tmuxx.Pane{
				{ID: "%9", PID: 123, Dead: false, WindowPanes: 2},
			}},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to send clear; pane %9 reports 2 panes in window @4; expected 1\n",
		},
		{
			name:      "dead pane",
			operation: "compact",
			role:      "reviewer",
			err: &target.PaneStateError{Session: session, Role: "reviewer", Window: reviewerWindow, Panes: []tmuxx.Pane{
				{ID: "%11", PID: 456, Dead: true, WindowPanes: 1},
			}},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to send compact; epic123:reviewer pane %11 is dead\n",
		},
		{
			name:      "empty process baseline",
			operation: "clear",
			role:      "planner",
			err: &target.ProcessIdentityError{
				Session: session, Role: "planner", Window: tmuxx.Window{
					ID: "@4", Name: "planner", Role: "planner",
					Harness: "codex", Model: "o3", Process: "",
				}, Pane: plannerPane,
			},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to clear planner; window @4 has no @agentctl_process baseline; recover the role with \"agentctl relaunch planner\"\n",
		},
		{
			name:      "process unavailable",
			operation: "compact",
			role:      "reviewer",
			err: &target.ProcessIdentityError{
				Session: session, Role: "reviewer", Window: reviewerWindow, Pane: reviewerPane, Err: tmuxx.ErrProcessUnavailable,
			},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to send compact; epic123:reviewer process identity is unavailable for pane %11: process identity unavailable\n",
		},
		{
			name:      "process mismatch",
			operation: "clear",
			role:      "planner",
			err: &target.ProcessIdentityError{
				Session: session, Role: "planner", Window: plannerWindow, Pane: plannerPane, ActualProcess: "zsh",
			},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to send clear; epic123:planner pane %9 is running \"zsh\"; recorded process is \"codex\"\n",
		},
		{
			name:      "self target",
			operation: "compact",
			role:      "reviewer",
			err: &target.SelfTargetError{
				Session: session, Role: "reviewer", Window: reviewerWindow, Pane: reviewerPane, CallerPane: "%11",
			},
			wantCode:  exitUnsafe,
			wantError: "agentctl: refusing to send compact; epic123:reviewer is the calling pane %11\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				return session, nil
			})
			controller := controlExecutorFunc(func(context.Context, string, tmuxx.Session, string) error {
				return tt.err
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{tt.operation, "--session", "epic123", tt.role}, &stdout, &stderr, dependencies{resolver: resolver, controller: controller})

			if code != tt.wantCode {
				t.Fatalf("runWithDependencies() = %d, want %d", code, tt.wantCode)
			}
			if stderr.String() != tt.wantError {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tt.wantError)
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
		})
	}
}

func TestRunWithRunnerControlValidatesTargetThenDeliversByPaneID(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tepic123\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("@4\tplanner\tplanner\tcodex\to3\thigh\tcodex\n")},
		tmuxx.Response{Stdout: []byte("%9\t123\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("codex\n")},
		tmuxx.Response{},
		tmuxx.Response{},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"clear", "--session", "epic123", "planner"}, &stdout, &stderr, runner, lookupValues(nil))

	if code != exitOK {
		t.Fatalf("runWithRunner() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@4", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "123"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "-l", "--", "/clear"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "Enter"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
	}
	if stdout.String() != "agentctl: delivered /clear to epic123:planner\n" {
		t.Fatalf("stdout = %q, want factual delivery message", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunWithRunnerControlDeliveryFailureIsTmuxExitWithoutSuccessClaim(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tepic123\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("@4\tplanner\tplanner\tcodex\to3\thigh\tcodex\n")},
		tmuxx.Response{Stdout: []byte("%9\t123\t0\t1\n")},
		tmuxx.Response{Stdout: []byte("codex\n")},
		tmuxx.Response{Err: errors.New("send keys failed")},
	)
	var stdout, stderr bytes.Buffer

	code := runWithRunner(context.Background(), []string{"clear", "--session", "epic123", "planner"}, &stdout, &stderr, runner, lookupValues(nil))

	if code != exitTmux {
		t.Fatalf("runWithRunner() = %d, want %d", code, exitTmux)
	}
	wantCalls := []tmuxx.Call{
		{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"}},
		{Executable: "tmux", Args: []string{"list-panes", "-t", "@4", "-F", "#{pane_id}\t#{pane_pid}\t#{pane_dead}\t#{window_panes}"}},
		{Executable: "ps", Args: []string{"-o", "comm=", "-p", "123"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
	}
	if stderr.String() != "agentctl: tmux clear pane input: send keys failed\n" {
		t.Fatalf("stderr = %q, want retained delivery failure", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no delivery claim", stdout.String())
	}
}

type controlExecutorFunc func(context.Context, string, tmuxx.Session, string) error

func (f controlExecutorFunc) Execute(ctx context.Context, operation string, session tmuxx.Session, role string) error {
	return f(ctx, operation, session, role)
}
