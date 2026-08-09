package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/session"
	"github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

type relauncherFunc func(context.Context, tmuxx.Session, fleet.RelaunchRequest) (fleet.RelaunchResult, error)

func (f relauncherFunc) Relaunch(ctx context.Context, session tmuxx.Session, request fleet.RelaunchRequest) (fleet.RelaunchResult, error) {
	return f(ctx, session, request)
}

func relaunchTestSession() tmuxx.Session { return tmuxx.Session{ID: "$4", Name: "epic123"} }

func staticResolver() sessionResolver {
	return resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
		return relaunchTestSession(), nil
	})
}

func TestRunRelaunchReportsWhatItCreatedAndEveryFieldsProvenance(t *testing.T) {
	for _, tt := range []struct {
		name   string
		result fleet.RelaunchResult
		want   string
	}{
		{
			name: "stored configuration",
			result: fleet.RelaunchResult{
				Role: "planner", Session: "epic123", Harness: "claude", Model: "fable", Effort: "max",
				Directory: "/repo", WindowID: "@71", PaneID: "%88",
				HarnessFrom: fleet.ProvenanceStored, ModelFrom: fleet.ProvenanceStored, EffortFrom: fleet.ProvenanceStored, DirectoryFrom: fleet.ProvenanceStored,
			},
			want: "agentctl: relaunched planner in epic123: window @71, pane %88, harness claude (stored), model fable (stored), effort max (stored), dir /repo (stored)\n",
		},
		{
			name: "defaulted model renders as default",
			result: fleet.RelaunchResult{
				Role: "planner", Session: "epic123", Harness: "codex", Model: "",
				Directory: "/repo", WindowID: "@71", PaneID: "%88",
				HarnessFrom: fleet.ProvenanceStored, ModelFrom: fleet.ProvenanceStored, EffortFrom: fleet.ProvenanceStored, DirectoryFrom: fleet.ProvenanceStored,
			},
			want: "agentctl: relaunched planner in epic123: window @71, pane %88, harness codex (stored), model default (stored), effort default (stored), dir /repo (stored)\n",
		},
		{
			name: "flag overrides are reported as overrides",
			result: fleet.RelaunchResult{
				Role: "planner", Session: "epic123", Harness: "codex", Model: "gpt-5.6", Effort: "high",
				Directory: "/repo", WindowID: "@71", PaneID: "%88",
				HarnessFrom: fleet.ProvenanceOverride, ModelFrom: fleet.ProvenanceOverride, EffortFrom: fleet.ProvenanceOverride, DirectoryFrom: fleet.ProvenanceStored,
			},
			want: "agentctl: relaunched planner in epic123: window @71, pane %88, harness codex (flag override), model gpt-5.6 (flag override), effort high (flag override), dir /repo (stored)\n",
		},
		{
			name: "legacy session reports flags",
			result: fleet.RelaunchResult{
				Role: "planner", Session: "epic123", Harness: "claude", Model: "",
				Directory: "/repo", WindowID: "@71", PaneID: "%88",
				HarnessFrom: fleet.ProvenanceFlags, ModelFrom: fleet.ProvenanceFlags, EffortFrom: fleet.ProvenanceFlags, DirectoryFrom: fleet.ProvenanceFlags,
			},
			want: "agentctl: relaunched planner in epic123: window @71, pane %88, harness claude (flags), model default (flags), effort default (flags), dir /repo (flags)\n",
		},
		{
			name: "directory override states the divergence",
			result: fleet.RelaunchResult{
				Role: "planner", Session: "epic123", Harness: "claude", Model: "fable",
				Directory: "/elsewhere", WindowID: "@71", PaneID: "%88",
				HarnessFrom: fleet.ProvenanceStored, ModelFrom: fleet.ProvenanceStored, EffortFrom: fleet.ProvenanceStored, DirectoryFrom: fleet.ProvenanceOverride,
				StoredDirectory: "/repo",
			},
			want: "agentctl: relaunched planner in epic123: window @71, pane %88, harness claude (stored), model fable (stored), effort default (stored), dir /elsewhere (flag override)\n" +
				"agentctl: planner now runs in /elsewhere; the fleet's recorded directory /repo is unchanged\n",
		},
		{
			name: "recovery states the destroyed window",
			result: fleet.RelaunchResult{
				Role: "planner", Session: "epic123", Harness: "claude",
				Directory: "/repo", WindowID: "@71", PaneID: "%88", RecoveredWindowID: "@23",
				HarnessFrom: fleet.ProvenanceStored, ModelFrom: fleet.ProvenanceStored, EffortFrom: fleet.ProvenanceStored, DirectoryFrom: fleet.ProvenanceStored,
			},
			want: "agentctl: relaunched planner in epic123: window @71, pane %88, harness claude (stored), model default (stored), effort default (stored), dir /repo (stored)\n" +
				"recovered: removed window @23, which carried no @agentctl_process baseline, and recreated planner\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			relauncher := relauncherFunc(func(context.Context, tmuxx.Session, fleet.RelaunchRequest) (fleet.RelaunchResult, error) {
				return tt.result, nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "epic123", "planner"}, &stdout, &stderr, dependencies{resolver: staticResolver(), relauncher: relauncher})

			if code != exitOK {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			if stdout.String() != tt.want {
				t.Fatalf("stdout = %q, want %q", stdout.String(), tt.want)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunRelaunchPassesOnlySuppliedOptionsAsOverrides(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		want fleet.RelaunchRequest
	}{
		{
			name: "no overrides",
			args: []string{"relaunch", "--session", "epic123", "planner"},
			want: fleet.RelaunchRequest{Role: "planner"},
		},
		{
			name: "every override",
			args: []string{"relaunch", "--session", "epic123", "--harness", "codex", "--model", "gpt-5.6", "--effort", "high", "--dir", "/elsewhere", "planner"},
			want: fleet.RelaunchRequest{Role: "planner", Harness: strPtr("codex"), Model: strPtr("gpt-5.6"), Effort: strPtr("high"), Directory: strPtr("/elsewhere")},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var got fleet.RelaunchRequest
			relauncher := relauncherFunc(func(_ context.Context, _ tmuxx.Session, request fleet.RelaunchRequest) (fleet.RelaunchResult, error) {
				got = request
				return fleet.RelaunchResult{Role: request.Role, Session: "epic123", Harness: "claude", Directory: "/repo", WindowID: "@71", PaneID: "%88"}, nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), tt.args, &stdout, &stderr, dependencies{resolver: staticResolver(), relauncher: relauncher})

			if code != exitOK {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("RelaunchRequest = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRunRelaunchRejectsInvalidInvocationsBeforeAnyDependency(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "no role", args: []string{"relaunch", "--session", "epic123"}},
		{name: "two roles", args: []string{"relaunch", "--session", "epic123", "planner", "reviewer"}},
		{name: "malformed role", args: []string{"relaunch", "--session", "epic123", "Planner"}},
		{name: "unknown harness", args: []string{"relaunch", "--session", "epic123", "--harness", "bash", "planner"}},
		{name: "empty harness", args: []string{"relaunch", "--session", "epic123", "--harness=", "planner"}},
		{name: "smuggled model", args: []string{"relaunch", "--session", "epic123", "--model", "--dangerously-bypass-approvals-and-sandbox", "planner"}},
		{name: "empty model", args: []string{"relaunch", "--session", "epic123", "--model=", "planner"}},
		{name: "unknown effort", args: []string{"relaunch", "--session", "epic123", "--effort", "turbo", "planner"}},
		{name: "empty effort", args: []string{"relaunch", "--session", "epic123", "--effort=", "planner"}},
		{name: "duplicate harness", args: []string{"relaunch", "--session", "epic123", "--harness", "claude", "--harness", "codex", "planner"}},
		{name: "duplicate effort", args: []string{"relaunch", "--session", "epic123", "--effort", "high", "--effort", "low", "planner"}},
		{name: "duplicate dir", args: []string{"relaunch", "--session", "epic123", "--dir", "/a", "--dir", "/b", "planner"}},
		{name: "roles option", args: []string{"relaunch", "--session", "epic123", "--roles", "planner:claude", "planner"}},
		{name: "text option", args: []string{"relaunch", "--session", "epic123", "--text", "hello", "planner"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resolverCalled := false
			relauncherCalled := false
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				resolverCalled = true
				return relaunchTestSession(), nil
			})
			relauncher := relauncherFunc(func(context.Context, tmuxx.Session, fleet.RelaunchRequest) (fleet.RelaunchResult, error) {
				relauncherCalled = true
				return fleet.RelaunchResult{}, nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), tt.args, &stdout, &stderr, dependencies{resolver: resolver, relauncher: relauncher})

			if code != exitUsage {
				t.Fatalf("runWithDependencies(%q) = %d, want %d; stderr = %q", tt.args, code, exitUsage, stderr.String())
			}
			if resolverCalled || relauncherCalled {
				t.Fatalf("dependency calls = resolver:%v relauncher:%v, want neither", resolverCalled, relauncherCalled)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stdout = %q, stderr = %q, want usage error only", stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunRelaunchMapsEveryRefusalToItsExitCodeAndMessage(t *testing.T) {
	session := relaunchTestSession()
	for _, tt := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{
			name: "unmanaged session",
			err:  &fleet.SessionGateError{Session: session, Option: "@agentctl_managed"},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; session \"epic123\" is not managed by agentctl\n",
		},
		{
			name: "absent version marker",
			err:  &fleet.SessionGateError{Session: session, Option: "@agentctl_version"},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; managed session carries no @agentctl_version marker\n",
		},
		{
			name: "other version",
			err:  &fleet.SessionGateError{Session: session, Option: "@agentctl_version", Value: "2"},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; session \"epic123\" has @agentctl_version=\"2\"; expected \"1\"\n",
		},
		{
			name: "defective roster",
			err:  &fleet.RosterError{Session: session, Roster: "planner,,reviewer"},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; managed session has invalid @agentctl_roles roster \"planner,,reviewer\"\n",
		},
		{
			name: "role outside roster",
			err:  &fleet.UnknownRoleError{Session: session, Role: "worker", Roster: "planner,reviewer"},
			code: exitRole,
			want: "agentctl: refusing to relaunch planner; role \"worker\" is not in @agentctl_roles \"planner,reviewer\"\n",
		},
		{
			name: "metadata defect",
			err:  &fleet.MetadataError{Session: session, Reason: `has @agentctl_dir "/repo" but no @agentctl_fleet`},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; managed session \"epic123\" has @agentctl_dir \"/repo\" but no @agentctl_fleet\n",
		},
		{
			name: "legacy session",
			err:  &fleet.LegacySessionError{Session: session, Role: "planner"},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; session records no per-role configuration; it was launched before agentctl recorded @agentctl_fleet and @agentctl_dir; supply --harness [--model] [--effort] --dir\n",
		},
		{
			name: "window still exists",
			err: &fleet.WindowPresentError{Session: session, Role: "planner", Windows: []fleet.ObservedWindow{
				{ID: "@23", State: status.StateRunning},
			}},
			code: exitRole,
			want: "agentctl: refusing to relaunch planner; role planner already has 1 window in epic123 (@23 running); relaunch accepts only an absent role or a recoverable no-baseline window\n",
		},
		{
			name: "sole no-baseline window would destroy session",
			err: &fleet.SoleWindowRecoveryError{
				Role: "planner", Session: "alpha",
				LaunchCommand: "agentctl launch --session alpha --roles planner:claude --models planner:opus-4-1 --efforts planner:high --dir /srv/work",
			},
			code: exitRole,
			want: "agentctl: refusing to relaunch planner; it is the only window in session alpha, so removing it would destroy the session. Recreate the fleet instead:\n" +
				"  agentctl kill --session alpha\n" +
				"  agentctl launch --session alpha --roles planner:claude --models planner:opus-4-1 --efforts planner:high --dir /srv/work\n",
		},
		{
			name: "dead window is not a relaunch",
			err: &fleet.WindowPresentError{Session: session, Role: "planner", Windows: []fleet.ObservedWindow{
				{ID: "@23", State: status.StateDead},
			}},
			code: exitRole,
			want: "agentctl: refusing to relaunch planner; role planner already has 1 window in epic123 (@23 dead); relaunch accepts only an absent role or a recoverable no-baseline window\n",
		},
		{
			name: "ambiguous role",
			err: &fleet.WindowPresentError{Session: session, Role: "planner", Windows: []fleet.ObservedWindow{
				{ID: "@23", State: status.StateAmbiguous}, {ID: "@31", State: status.StateAmbiguous},
			}},
			code: exitRole,
			want: "agentctl: refusing to relaunch planner; role planner already has 2 windows in epic123 (@23 ambiguous, @31 ambiguous); relaunch accepts only an absent role or a recoverable no-baseline window\n",
		},
		{
			name: "stored directory unusable",
			err:  &fleet.StoredDirectoryError{Session: session, Role: "planner", Path: "/gone", Err: fs.ErrNotExist},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; managed session \"epic123\" records launch directory \"/gone\": file does not exist; supply --dir to relaunch planner elsewhere\n",
		},
		{
			name: "stored directory is relative",
			err:  &fleet.StoredDirectoryError{Session: session, Role: "planner", Path: "payload", Err: errors.New("path is not absolute")},
			code: exitSession,
			want: "agentctl: refusing to relaunch planner; managed session \"epic123\" records launch directory \"payload\": path is not absolute; supply --dir to relaunch planner elsewhere\n",
		},
		{
			name: "missing executable",
			err:  &preflight.MissingExecutableError{Name: "codex"},
			code: exitMissingExecutable,
			want: "agentctl: required executable \"codex\" not found\n",
		},
		{
			name: "pre-ownership creation failure",
			err:  &fleet.WindowCreationError{Role: "planner", Cause: errors.New("invalid tmux creation output")},
			code: exitTmux,
			want: "agentctl: invalid tmux creation output; a window named planner may exist; inspect with tmux list-windows\n",
		},
		{
			name: "recovery kill failure",
			err: &fleet.RecoveryKillError{
				Role: "planner", Session: "epic123", WindowID: "@23", Cause: &tmuxx.TmuxError{Err: errors.New("tmux kill window: target vanished")},
			},
			code: exitTmux,
			want: "agentctl: failed to relaunch planner; could not remove unproven window @23 in epic123: tmux kill window: target vanished; nothing was created\n",
		},
		{
			name: "failure after successful recovery preserves both facts",
			err: &fleet.RecoveredWindowError{
				Role: "planner", WindowID: "@23",
				Cause: &fleet.RelaunchError{Role: "planner", WindowID: "@71", Cause: errors.New("stamp failed")},
			},
			code: exitLaunch,
			want: "agentctl: failed to relaunch planner; removed window @71: stamp failed\n" +
				"recovered: removed window @23, which carried no @agentctl_process baseline; planner was not recreated\n",
		},
		{
			name: "post-ownership rollback",
			err:  &fleet.RelaunchError{Role: "planner", WindowID: "@71", Cause: errors.New("stamp failed")},
			code: exitLaunch,
			want: "agentctl: failed to relaunch planner; removed window @71: stamp failed\n",
		},
		{
			name: "concurrent loser rollback failure",
			err: &fleet.RelaunchError{
				Role:     "planner",
				WindowID: "@71",
				Cause: &fleet.PostCreateWindowConflictError{
					Session:         session,
					Role:            "planner",
					CreatedWindowID: "@71",
					WindowIDs:       []tmuxx.WindowID{"@70", "@71"},
				},
				CleanupErr: errors.New("kill failed"),
			},
			code: exitLaunch,
			want: "agentctl: refusing to relaunch planner; post-create verification observed role planner in 2 windows in epic123 (@70, @71); expected only created window @71; failed to remove window @71: kill failed\n",
		},
		{
			name: "post-ownership rollback failure",
			err:  &fleet.RelaunchError{Role: "planner", WindowID: "@71", Cause: errors.New("stamp failed"), CleanupErr: errors.New("kill failed")},
			code: exitLaunch,
			want: "agentctl: failed to relaunch planner; failed to remove window @71: kill failed (relaunch failure: stamp failed)\n",
		},
		{
			name: "supplied directory is unusable",
			err:  &fleet.DirectoryError{Path: "/elsewhere", Err: fs.ErrNotExist},
			code: exitUsage,
			want: "agentctl: invalid launch directory \"/elsewhere\": file does not exist\n",
		},
		{
			name: "value rejected inside the launcher",
			err:  &config.ValidationError{Option: "harness", Value: "bash", EntryIndex: -1, Reason: "must be claude or codex"},
			code: exitUsage,
			want: "agentctl: invalid --harness value \"bash\": must be claude or codex\n",
		},
		{
			name: "tmux failure",
			err:  &tmuxx.TmuxError{Err: errors.New("tmux list windows: no server running")},
			code: exitTmux,
			want: "agentctl: tmux list windows: no server running\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			relauncher := relauncherFunc(func(context.Context, tmuxx.Session, fleet.RelaunchRequest) (fleet.RelaunchResult, error) {
				return fleet.RelaunchResult{}, tt.err
			})
			var stdout, stderr bytes.Buffer

			code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "epic123", "planner"}, &stdout, &stderr, dependencies{resolver: staticResolver(), relauncher: relauncher})

			if code != tt.code {
				t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, tt.code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if got := stderr.String(); !strings.HasPrefix(got, tt.want) {
				t.Fatalf("stderr = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRunRelaunchTranscriptRecreatesTheRoleThroughTheRunner(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tepic123\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner,reviewer\n")},
		tmuxx.Response{Stdout: []byte("planner:claude:fable:max,reviewer:codex::\n")},
		tmuxx.Response{Stdout: []byte("/fleet workspace\n")},
		tmuxx.Response{Stdout: []byte("@65\treviewer\treviewer\tcodex\t\t\tcodex\n")},
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		tmuxx.Response{Stdout: []byte("@71\tplanner\t\t\t\t\t\n")},
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "epic123", "planner"}, &stdout, &stderr, dependencies{
		resolver:   session.New(tmuxx.New(runner), func(string) (string, bool) { return "", false }),
		relauncher: fleet.New(runner, launchTestDependencies(runner).fleet),
	})

	if code != exitOK {
		t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
	}
	want := "agentctl: relaunched planner in epic123: window @71, pane %88, harness claude (stored), model fable (stored), effort max (stored), dir /fleet workspace (stored)\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	assertLaunchCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_roles"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_fleet"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_dir"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-window", "-d", "-t", "$4", "-n", "planner", "-c", "/fleet workspace",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude' '--' '--model' 'fable' '--effort' 'max'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_model", "fable"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_effort", "max"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "5150"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "5150"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_process", "2.1.220"}},
	)
}

func TestRunRelaunchConcurrentLoserRefusesAfterRemovingItsCreatedWindow(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("$4\tepic123\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte("planner:claude::\n")},
		tmuxx.Response{Stdout: []byte("/repo\n")},
		tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		tmuxx.Response{Stdout: []byte(
			"@70\tplanner\tplanner\tclaude\t\t\tclaude\n" +
				"@71\tplanner\t\t\t\t\t\n",
		)},
		tmuxx.Response{},
	)
	var stdout, stderr bytes.Buffer

	code := runWithDependencies(context.Background(), []string{"relaunch", "--session", "epic123", "planner"}, &stdout, &stderr, dependencies{
		resolver:   session.New(tmuxx.New(runner), func(string) (string, bool) { return "", false }),
		relauncher: fleet.New(runner, launchTestDependencies(runner).fleet),
	})

	if code != exitLaunch {
		t.Fatalf("runWithDependencies() = %d, want %d; stderr = %q", code, exitLaunch, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no success output", stdout.String())
	}
	want := "agentctl: refusing to relaunch planner; post-create verification observed role planner in 2 windows in epic123 (@70, @71); expected only created window @71; removed window @71\n"
	if got := stderr.String(); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	last := runner.Calls[len(runner.Calls)-1]
	wantRollback := tmuxx.Call{Executable: "tmux", Args: []string{"kill-window", "-t", "@71"}}
	if !reflect.DeepEqual(last, wantRollback) {
		t.Fatalf("rollback call = %#v, want %#v", last, wantRollback)
	}
}

func strPtr(value string) *string { return &value }
