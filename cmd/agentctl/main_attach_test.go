package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/attach"
	"github.com/mnbf9rca/agentctl/internal/fleet"
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

	attacher := &attacherStub{executeErr: &attach.NoPresentationError{Session: "fleet"}}
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"attach", "--session", "fleet"}, &stdout, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, attacher: attacher,
	})
	want := "agentctl: refusing to attach session \"fleet\"; no tmux presentation was observed; status and control remain available without tmux\n"
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
