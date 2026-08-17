package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/attach"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRuntimeSessionAttacherRequiresDurableFleetBeforePresentationLookup(t *testing.T) {
	t.Parallel()

	delegate := &attacherStub{target: tmuxx.Session{ID: "$7", Name: "fleet"}}
	attacher := runtimeSessionAttacher{records: attachFleetReaderStub{err: os.ErrNotExist}, delegate: delegate}
	_, err := attacher.ExecutePresentation(context.Background(), "fleet", io.Discard)
	var missing *fleet.ShimFleetMissingError
	if !errors.As(err, &missing) || missing.Session != "fleet" {
		t.Fatalf("ExecutePresentation() error = %T %v, want durable fleet missing", err, err)
	}
	if delegate.session != "" {
		t.Fatalf("presentation delegate received session %q before durable fleet proof", delegate.session)
	}
}

func TestRunAttachRefusesNoPresentationWithRuntimeAvailabilityFact(t *testing.T) {
	t.Parallel()

	attacher := &attacherStub{executeErr: &attach.NoPresentationError{Session: "fleet", Roster: []string{"planner", "coder"}}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, attacher: attacher,
	})
	want := "agentctl: refusing to attach session fleet; no tmux presentation was observed; attach a role directly:\n" +
		"  agentctl attach --session fleet planner\n" +
		"  agentctl attach --session fleet coder\n"
	if code != exitSession || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("code=%d stdout=%q stderr=%q, want %d empty/%q", code, stdout.String(), stderr.String(), exitSession, want)
	}
}

func TestRunAttachMapsMissingDurableFleetBeforePresentation(t *testing.T) {
	t.Parallel()

	attacher := &attacherStub{executeErr: &fleet.ShimFleetMissingError{Session: "fleet"}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, attacher: attacher,
	})
	want := "agentctl: session \"fleet\" has no durable fleet configuration\n"
	if code != exitSession || stdout.Len() != 0 || stderr.String() != want {
		t.Fatalf("code=%d stdout=%q stderr=%q, want %d empty/%q", code, stdout.String(), stderr.String(), exitSession, want)
	}
}

func TestRunAttachUsesOnlyObservedPresentationAndExactTypedID(t *testing.T) {
	t.Parallel()

	attacher := &attacherStub{target: tmuxx.Session{ID: "$7", Name: "fleet"}, stillRunning: true}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach"}, &stdout, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, attacher: attacher,
	})
	if code != exitOK || attacher.session != "fleet" || attacher.stillTarget.ID != "$7" {
		t.Fatalf("code=%d session=%q stillTarget=%#v stderr=%q", code, attacher.session, attacher.stillTarget, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Attachment to session \"fleet\" ended (tmux exit 0). Session $7 is still running.") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunAttachRoleRoutesToDirectClientWithoutFleetEnvironmentOrTmuxAttach(t *testing.T) {
	t.Parallel()

	fleetAttach := &attacherStub{environmentErr: errors.New("fleet environment must not be checked")}
	roleAttach := &roleAttacherStub{result: attach.RoleResult{Disposition: shim.AttachDispositionChildExited}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet", "planner"}, &stdout, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, attacher: fleetAttach, roleAttacher: roleAttach,
	})
	if code != exitOK || roleAttach.session != "fleet" || roleAttach.role != "planner" || fleetAttach.session != "" || stderr.String() != "agentctl: role planner in session fleet ended while attached; 0 bytes were relayed (attach-viewer-ended)\n" {
		t.Fatalf("code=%d role=%q/%q fleetSession=%q stderr=%q", code, roleAttach.session, roleAttach.role, fleetAttach.session, stderr.String())
	}
}

func TestPublicRoleAttachRefusesNonTerminalBeforeRuntimeConstruction(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	stateRoot := filepath.Join(root, "state")
	t.Setenv("AGENTCTL_RUNTIME_ROOT", runtimeRoot)
	t.Setenv("AGENTCTL_STATE_ROOT", stateRoot)

	var stdout, stderr bytes.Buffer
	code := runWithRunner(context.Background(), []string{"attach", "--session", "fleet", "planner"}, &stdout, &stderr, tmuxx.NewFakeRunner(), os.LookupEnv)
	if code != exitUsage {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, path := range []string{runtimeRoot, stateRoot} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("runtime path %q exists before terminal refusal: %v", path, err)
		}
	}
}

func TestPublicRoleAttachWithoutSelectableSessionStillRefusesTerminalFirstWithoutMutation(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	stateRoot := filepath.Join(root, "state")
	t.Setenv("AGENTCTL_RUNTIME_ROOT", runtimeRoot)
	t.Setenv("AGENTCTL_STATE_ROOT", stateRoot)
	t.Setenv("AGENTCTL_SESSION", "INVALID")

	code := runWithRunner(context.Background(), []string{"attach", "planner"}, io.Discard, io.Discard, tmuxx.NewFakeRunner(), os.LookupEnv)
	if code != exitUsage {
		t.Fatalf("code=%d, want %d", code, exitUsage)
	}
	for _, path := range []string{runtimeRoot, stateRoot} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("runtime path %q exists before terminal refusal: %v", path, err)
		}
	}
}

func TestRoleAttachTerminalRefusalsWithoutSelectedSessionAreByteExact(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code int
		want string
	}{
		{name: "not terminal", err: &attach.NotTerminalError{}, code: exitUsage, want: "agentctl: refusing to attach role planner; standard input and output must both be terminals (attach-not-a-terminal)\n"},
		{name: "mismatch", err: &attach.TerminalMismatchError{}, code: exitUsage, want: "agentctl: refusing to attach role planner; standard input and standard output are different terminals (attach-terminal-mismatch)\n"},
		{name: "observation", err: &attach.TerminalObservationError{Cause: errors.New("fstat failed")}, code: exitTmux, want: "agentctl: could not observe the attaching terminal for role planner: \"fstat failed\"; no attachment was made (attach-terminal-observation-failed)\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := roleAttachError(&stderr, "", "planner", test.err); code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d/%q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

func TestRunAttachAcceptsOnlyBareOrOneValidatedRole(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"attach", "one", "two"},
		{"attach", "INVALID"},
	} {
		roleAttach := &roleAttacherStub{}
		var stderr bytes.Buffer
		code := runWithDependencies(context.Background(), arguments, &bytes.Buffer{}, &stderr, dependencies{roleAttacher: roleAttach})
		if code != exitUsage || roleAttach.called {
			t.Fatalf("arguments=%q code=%d called=%t stderr=%q", arguments, code, roleAttach.called, stderr.String())
		}
	}
}

func TestRoleAttachOutputAndRestoreCompositionIsByteExact(t *testing.T) {
	t.Parallel()

	base := attach.RoleResult{Disposition: shim.AttachDispositionTailUndelivered, Bytes: 8, Raw: 8, Written: 3, Undelivered: 2}
	local := &attach.TerminalOutputError{Prior: &base, Raw: 8, Written: 3, Cause: errors.New("broken terminal")}
	restore := &attach.TerminalRestoreError{Prior: local, Cause: errors.New("restore failed")}
	var stderr bytes.Buffer
	code := roleAttachError(&stderr, "fleet", "planner", restore)
	want := "agentctl: role planner in session fleet ended while attached; 8 bytes were relayed, but 2 bytes of its final output could not be delivered before the flush deadline and were dropped; the terminal above is incomplete (attach-tail-undelivered)\n" +
		"  writing its output to this terminal failed: \"broken terminal\"; 3 of 8 received bytes reached the terminal (attach-stdout-failed)\n" +
		"  restoring the attaching terminal failed: \"restore failed\" (attach-terminal-restore-failed)\n"
	if code != exitTmux || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitTmux, want)
	}
}

func TestRoleAttachLocalThenRestoreWithoutBaseUsesScalarPriorOutcome(t *testing.T) {
	for _, test := range []struct {
		name  string
		local *attach.TerminalOutputError
		prior string
	}{
		{name: "stdout failed", local: &attach.TerminalOutputError{Raw: 8, Written: 3, Cause: errors.New("broken terminal")}, prior: "stdout-failed"},
		{name: "terminal stalled", local: &attach.TerminalOutputError{Raw: 8, Written: 3, Stalled: true}, prior: "terminal-stalled"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := roleAttachError(&stderr, "fleet", "planner", &attach.TerminalRestoreError{Prior: test.local, Cause: errors.New("restore failed")})
			want := "agentctl: attachment to role planner in session fleet ended with " + test.prior + ", but restoring the attaching terminal failed: \"restore failed\" (attach-terminal-restore-failed)\n"
			if code != exitTmux || stderr.String() != want {
				t.Fatalf("code=%d stderr=%q, want %d/%q", code, stderr.String(), exitTmux, want)
			}
		})
	}
}

func TestRoleAttachRefusalThenRestoreUsesObservedRefusalAsPriorOutcome(t *testing.T) {
	for _, outcome := range []shim.AttachRefusal{
		shim.AttachRefusalViewerPresent,
		shim.AttachRefusalPeerUnverified,
		shim.AttachRefusalPeerUnobservable,
		shim.AttachRefusalInitialSizeFailed,
	} {
		t.Run(string(outcome), func(t *testing.T) {
			var stderr bytes.Buffer
			prior := &attach.RefusalErrorRole{Control: shim.AttachControl{Outcome: outcome}}
			code := roleAttachError(&stderr, "fleet", "planner", &attach.TerminalRestoreError{Prior: prior, Cause: errors.New("restore failed")})
			want := "agentctl: attachment to role planner in session fleet ended with " + string(outcome) + ", but restoring the attaching terminal failed: \"restore failed\" (attach-terminal-restore-failed)\n"
			if code != exitTmux || stderr.String() != want {
				t.Fatalf("code=%d stderr=%q, want %d/%q", code, stderr.String(), exitTmux, want)
			}
		})
	}
}

func TestRoleAttachAnswererDisagreementThenRestoreUsesObservedPriorOutcome(t *testing.T) {
	var stderr bytes.Buffer
	prior := &attach.RoleObservationError{Observation: fleet.ShimRoleObservation{Outcome: shim.OutcomeAnswererDisagreement, ShimPID: 41, AnswererPID: 42}}
	code := roleAttachError(&stderr, "fleet", "planner", &attach.TerminalRestoreError{Prior: prior, Cause: errors.New("restore failed")})
	want := "agentctl: attachment to role planner in session fleet ended with answerer-disagreement, but restoring the attaching terminal failed: \"restore failed\" (attach-terminal-restore-failed)\n"
	if code != exitTmux || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d/%q", code, stderr.String(), exitTmux, want)
	}
}

func TestRoleAttachResizeFinalQuotesObservedCauseExactly(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := writeRoleAttachResult(&stderr, "fleet", "planner", attach.RoleResult{
		Disposition: shim.AttachDispositionResizeFailed,
		Rows:        41,
		Cols:        132,
		Cause:       "ioctl denied",
	})
	want := "agentctl: could not apply window size 41x132 to role planner in session fleet: \"ioctl denied\" (attach-resize-failed)\n"
	if code != exitTmux || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitTmux, want)
	}
}

func TestRoleAttachFinalRowsAreByteExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		result attach.RoleResult
		code   int
		want   string
	}{
		{"child exited", attach.RoleResult{Disposition: shim.AttachDispositionChildExited, Bytes: 12}, exitOK, "agentctl: role planner in session fleet ended while attached; 12 bytes were relayed (attach-viewer-ended)\n"},
		{"viewer evicted", attach.RoleResult{Disposition: shim.AttachDispositionViewerEvicted}, exitTmux, "agentctl: attachment to role planner in session fleet was ended because keeping it would have required buffering more than 131072 bytes of role output; ending it stopped nothing in the role (attach-evicted-slow)\n"},
		{"cleanup retained", attach.RoleResult{Disposition: shim.AttachDispositionCleanupRetained}, exitTmux, "agentctl: attachment to role planner in session fleet ended while the shim retained ownership during cleanup; the role's disposition is not established by this command (attach-ended-cleanup-retained)\n"},
		{"server closing", attach.RoleResult{Disposition: shim.AttachDispositionServerClosing}, exitTmux, "agentctl: attachment to role planner in session fleet ended because the shim closed the stream; the role's disposition is not established by this command (attach-ended-server-closing)\n"},
		{"tail undelivered", attach.RoleResult{Disposition: shim.AttachDispositionTailUndelivered, Bytes: 8, Undelivered: 2}, exitTmux, "agentctl: role planner in session fleet ended while attached; 8 bytes were relayed, but 2 bytes of its final output could not be delivered before the flush deadline and were dropped; the terminal above is incomplete (attach-tail-undelivered)\n"},
		{"tail unconfirmed none", attach.RoleResult{Disposition: shim.AttachDispositionTailUnconfirmed, Bytes: 8}, exitTmux, "agentctl: role planner in session fleet ended while attached; 8 bytes were relayed and no further bytes are known to have been missed, but the output cutoff was never confirmed, so whether any more of its final output was missed is unknown (attach-tail-unconfirmed-none-known)\n"},
		{"tail unconfirmed known", attach.RoleResult{Disposition: shim.AttachDispositionTailUnconfirmed, Bytes: 8, KnownUndelivered: 2}, exitTmux, "agentctl: role planner in session fleet ended while attached; 8 bytes were relayed and 2 further bytes are known not to have been, but the output cutoff was never confirmed, so whether any more of its final output was missed is unknown (attach-tail-unconfirmed)\n"},
		{"counter exhausted", attach.RoleResult{Disposition: shim.AttachDispositionCounterExhausted, Bytes: 9}, exitTmux, "agentctl: attachment to role planner in session fleet was ended after 9 bytes because a byte counter reached the largest exactly representable value; ending it stopped nothing in the role (attach-counter-exhausted)\n"},
		{"resize failed", attach.RoleResult{Disposition: shim.AttachDispositionResizeFailed, Rows: 41, Cols: 132, Cause: "ioctl denied"}, exitTmux, "agentctl: could not apply window size 41x132 to role planner in session fleet: \"ioctl denied\" (attach-resize-failed)\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := writeRoleAttachResult(&stderr, "fleet", "planner", test.result); code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

func TestRoleAttachRefusalAndStartupRowsAreByteExact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		code int
		want string
	}{
		{"viewer present", &attach.RefusalErrorRole{Control: shim.AttachControl{Outcome: shim.AttachRefusalViewerPresent, ViewerPID: 44}}, exitUnsafe, "agentctl: refusing to attach role planner in session fleet; a viewer is already attached at PID 44 (attach-viewer-present)\n"},
		{"peer unverified", &attach.RefusalErrorRole{Control: shim.AttachControl{Outcome: shim.AttachRefusalPeerUnverified, PeerPID: 44, PeerUID: 501, ShimUID: 502}}, exitUnsafe, "agentctl: refusing the attach connection for role planner in session fleet; connected LOCAL_PEERPID 44 has uid 501; expected 502 (attach-peer-unverified)\n"},
		{"peer unobservable", &attach.RefusalErrorRole{Control: shim.AttachControl{Outcome: shim.AttachRefusalPeerUnobservable, Cause: "peer query"}}, exitTmux, "agentctl: could not observe the attach peer for role planner in session fleet: \"peer query\" (attach-peer-unobservable)\n"},
		{"initial size", &attach.RefusalErrorRole{Control: shim.AttachControl{Outcome: shim.AttachRefusalInitialSizeFailed, Rows: 41, Cols: 132, Cause: "ioctl denied"}}, exitTmux, "agentctl: could not apply window size 41x132 to role planner in session fleet: \"ioctl denied\" (attach-resize-failed)\n"},
		{"presented", &attach.PresentedByTmuxError{}, exitUnsafe, "agentctl: refusing to attach role planner in session fleet; the role is presented by tmux and its pane is its viewer; use agentctl attach --session fleet (attach-presented-by-tmux)\n"},
		{"presentation missing", &attach.PresentationMissingError{}, exitUnsafe, "agentctl: refusing to attach role planner in session fleet; it was launched in tmux mode but no presentation was observed, so it has no viewer to share; recreate it with: agentctl relaunch planner (attach-presentation-missing)\n"},
		{"listener absent", &attach.ListenerAbsentError{Path: "/runtime/fleet/planner.attach"}, exitUnsafe, "agentctl: refusing to attach role planner in session fleet; the role holds its claim but has no attach stream at \"/runtime/fleet/planner.attach\" (attach-listener-absent)\n"},
		{"listener unobservable", &attach.ListenerUnobservableError{Path: "/runtime/fleet/planner.attach", Cause: errors.New("denied")}, exitTmux, "agentctl: could not observe the attach stream for role planner in session fleet at \"/runtime/fleet/planner.attach\": \"denied\"; no attachment was made (attach-listener-unobservable)\n"},
		{"not terminal", &attach.NotTerminalError{}, exitUsage, "agentctl: refusing to attach role planner in session fleet; standard input and output must both be terminals (attach-not-a-terminal)\n"},
		{"terminal mismatch", &attach.TerminalMismatchError{}, exitUsage, "agentctl: refusing to attach role planner in session fleet; standard input and standard output are different terminals (attach-terminal-mismatch)\n"},
		{"terminal observation", &attach.TerminalObservationError{Cause: errors.New("fstat failed")}, exitTmux, "agentctl: could not observe the attaching terminal for role planner in session fleet: \"fstat failed\"; no attachment was made (attach-terminal-observation-failed)\n"},
		{"terminal open", &attach.TerminalOpenError{Cause: errors.New("open failed")}, exitTmux, "agentctl: could not open this command's own handle on the attaching terminal for role planner in session fleet: \"open failed\"; no attachment was made (attach-terminal-open-failed)\n"},
		{"terminal verify", &attach.TerminalVerifyError{Stage: "identity-stat", Cause: errors.New("fstat failed")}, exitTmux, "agentctl: opened a candidate terminal handle for role planner in session fleet but could not complete identity-stat: \"fstat failed\"; no attachment was made (attach-terminal-verify-failed)\n"},
		{"terminal reopen mismatch", &attach.TerminalReopenMismatchError{Path: "/dev/ttys001"}, exitTmux, "agentctl: opening observed terminal name \"/dev/ttys001\" for role planner in session fleet produced a candidate handle whose identity did not match the terminal this command is attached to; no attachment was made (attach-terminal-reopen-mismatch)\n"},
		{"signal observation", &attach.SignalObservationError{Signal: syscall.SIGTERM, Observation: "mask", Cause: errors.New("mask failed")}, exitTmux, "agentctl: could not observe the current handling of SIGTERM for role planner in session fleet: mask query failed: \"mask failed\"; no attachment was made and this terminal was not modified (attach-signal-observation-failed)\n"},
		{"raw", &attach.TerminalRawError{Cause: errors.New("ioctl failed")}, exitTmux, "agentctl: could not place the attaching terminal in raw mode for role planner in session fleet: \"ioctl failed\"; no attachment was made (attach-terminal-raw-failed)\n"},
		{"transport", &attach.TransportError{Phase: "relay", Cause: errors.New("short frame")}, exitTmux, "agentctl: attach transport for role planner in session fleet failed during relay: \"short frame\" (attach-transport-failed)\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := roleAttachError(&stderr, "fleet", "planner", test.err); code != test.code || stderr.String() != test.want {
				t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), test.code, test.want)
			}
		})
	}
}

func TestRunAttachBrokenDiagnosticSinkDoesNotEraseSelectedExit(t *testing.T) {
	t.Parallel()

	sink := &failingDiagnosticSink{err: syscall.EPIPE}
	roleAttach := &roleAttacherStub{result: attach.RoleResult{Disposition: shim.AttachDispositionChildExited, Bytes: 9}, diagnostic: sink}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet", "planner"}, &bytes.Buffer{}, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, roleAttacher: roleAttach,
	})
	if code != exitOK || sink.attempts != 1 || stderr.Len() != 0 {
		t.Fatalf("code=%d attempts=%d stderr=%q", code, sink.attempts, stderr.String())
	}
}

func TestRunAttachSkipsDiagnosticOnlyWhenTheStalledRelayTerminalIsItsDestination(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		same       bool
		wantWrites int
	}{
		{name: "same stalled terminal", same: true},
		{name: "redirected diagnostic", same: false, wantWrites: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &failingDiagnosticSink{}
			roleAttach := &roleAttacherStub{
				err:                      &attach.TerminalOutputError{Raw: 9, Written: 3, Stalled: true},
				diagnostic:               sink,
				diagnosticSharesTerminal: test.same,
			}
			code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet", "planner"}, &bytes.Buffer{}, &bytes.Buffer{}, dependencies{
				resolver: &resolverStub{selected: "fleet"}, roleAttacher: roleAttach,
			})
			if code != exitTmux || sink.attempts != test.wantWrites {
				t.Fatalf("code=%d attempts=%d, want %d/%d", code, sink.attempts, exitTmux, test.wantWrites)
			}
		})
	}
}

func TestAttachDiagnosticBoundFollowsItsDestination(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		bounded      bool
		wantDeadline bool
	}{
		{name: "relay terminal", bounded: true, wantDeadline: true},
		{name: "redirected sink"},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &deadlineDiagnosticSink{}
			writer := attachDiagnosticWriter{sink: sink, bounded: test.bounded}
			if _, err := writer.Write([]byte("row\n")); err != nil {
				t.Fatal(err)
			}
			if sink.deadline != test.wantDeadline {
				t.Fatalf("deadline=%t, want %t", sink.deadline, test.wantDeadline)
			}
		})
	}
}

func TestRuntimeSessionAttacherRoutesStoredPresentationWithoutInference(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		presentation fleet.Presentation
		wantDelegate bool
	}{
		{name: "detached", presentation: fleet.PresentationDetached},
		{name: "tmux", presentation: fleet.PresentationTmux, wantDelegate: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			delegate := &attacherStub{target: tmuxx.Session{ID: "$7", Name: "fleet"}}
			attacher := runtimeSessionAttacher{
				records:  attachFleetReaderStub{record: fleet.ShimFleetRecord{Presentation: test.presentation, Roster: []string{"planner", "coder"}}},
				delegate: delegate,
			}
			_, err := attacher.ExecutePresentation(context.Background(), "fleet", io.Discard)
			if test.wantDelegate {
				if err != nil || delegate.session != "fleet" {
					t.Fatalf("error=%v delegate=%q", err, delegate.session)
				}
				return
			}
			var missing *attach.NoPresentationError
			if !errors.As(err, &missing) || delegate.session != "" || !reflect.DeepEqual(missing.Roster, []string{"planner", "coder"}) {
				t.Fatalf("error=%T %v delegate=%q", err, err, delegate.session)
			}
		})
	}
}

func TestRunAttachChecksEnvironmentBeforeSessionResolution(t *testing.T) {
	t.Parallel()

	resolver := &resolverStub{err: errors.New("must not resolve")}
	attacher := &attacherStub{environmentErr: &attach.EnvironmentError{TermProgramSet: false}}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet"}, &bytes.Buffer{}, &stderr, dependencies{resolver: resolver, attacher: attacher})
	if code != exitUnclassified || resolver.explicit != nil || !strings.Contains(stderr.String(), "TERM_PROGRAM is unset") {
		t.Fatalf("code=%d resolved=%v stderr=%q", code, resolver.explicit, stderr.String())
	}
}

type attacherStub struct {
	environmentErr error
	executeErr     error
	target         tmuxx.Session
	stillRunning   bool
	stillErr       error
	session        string
	stillTarget    tmuxx.Session
}

type roleAttacherStub struct {
	result                   attach.RoleResult
	err                      error
	called                   bool
	session                  string
	role                     string
	diagnostic               attach.DiagnosticSink
	diagnosticSharesTerminal bool
}

func (a *roleAttacherStub) Execute(_ context.Context, sessionName, role string) (attach.RoleResult, error) {
	a.called = true
	a.session, a.role = sessionName, role
	return a.result, a.err
}

func (a *roleAttacherStub) Diagnostic() attach.DiagnosticSink { return a.diagnostic }
func (a *roleAttacherStub) DiagnosticSharesTerminal() bool    { return a.diagnosticSharesTerminal }
func (a *roleAttacherStub) Report(render func(attach.DiagnosticSink, bool) int) int {
	return render(a.diagnostic, a.diagnosticSharesTerminal)
}

type failingDiagnosticSink struct {
	attempts int
	err      error
}

type deadlineDiagnosticSink struct{ deadline bool }

func (s *deadlineDiagnosticSink) Attempt(ctx context.Context, value []byte) (int, error) {
	_, s.deadline = ctx.Deadline()
	return len(value), nil
}

func (s *failingDiagnosticSink) Attempt(context.Context, []byte) (int, error) {
	s.attempts++
	return 0, s.err
}

type attachFleetReaderStub struct {
	record fleet.ShimFleetRecord
	err    error
}

func (r attachFleetReaderStub) Read(string) (fleet.ShimFleetRecord, error) {
	return r.record, r.err
}

func (a *attacherStub) CheckEnvironment() error { return a.environmentErr }
func (a *attacherStub) ExecutePresentation(_ context.Context, sessionName string, _ io.Writer) (tmuxx.Session, error) {
	a.session = sessionName
	return a.target, a.executeErr
}
func (a *attacherStub) StillRunning(_ context.Context, target tmuxx.Session) (bool, error) {
	a.stillTarget = target
	return a.stillRunning, a.stillErr
}
