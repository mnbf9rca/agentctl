package tmuxx

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestPresentationCreationUsesTypedTargetsAndExactArgv(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(
		Response{Stdout: []byte("$4\t@7\t%9\t4321\n")},
		Response{Stdout: []byte("@8\t%10\t5432\n")},
	)
	client := New(runner)
	first, err := client.CreatePresentationSession(
		context.Background(), "fleet", "planner", "/repo path", "exec '/current agentctl' '__shim'",
	)
	if err != nil {
		t.Fatalf("CreatePresentationSession() error = %v", err)
	}
	if want := (CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321}); first != want {
		t.Fatalf("CreatePresentationSession() = %#v, want %#v", first, want)
	}
	second, err := client.CreatePresentationWindow(
		context.Background(), first.SessionID, "coder", "/repo path", "exec '/current agentctl' '__shim'",
	)
	if err != nil {
		t.Fatalf("CreatePresentationWindow() error = %v", err)
	}
	if want := (CreatedWindow{WindowID: "@8", PaneID: "%10", PanePID: 5432}); second != want {
		t.Fatalf("CreatePresentationWindow() = %#v, want %#v", second, want)
	}

	want := []Call{
		{Executable: "tmux", Args: []string{
			"new-session", "-d", "-s", "fleet", "-n", "planner", "-c", "/repo path",
			"-P", "-F", "#{session_id}\t#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec '/current agentctl' '__shim'",
		}},
		{Executable: "tmux", Args: []string{
			"new-window", "-d", "-t", "$4", "-n", "coder", "-c", "/repo path",
			"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec '/current agentctl' '__shim'",
		}},
	}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("presentation calls = %#v, want %#v", runner.Calls, want)
	}
}

func TestPresentationRemovalUsesOnlyTypedIDs(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{}, Response{})
	client := New(runner)
	if err := client.RemovePresentationWindow(context.Background(), "@8"); err != nil {
		t.Fatalf("RemovePresentationWindow() error = %v", err)
	}
	if err := client.RemovePresentationSession(context.Background(), "$4"); err != nil {
		t.Fatalf("RemovePresentationSession() error = %v", err)
	}
	want := []Call{
		{Executable: "tmux", Args: []string{"kill-window", "-t", "@8"}},
		{Executable: "tmux", Args: []string{"kill-session", "-t", "$4"}},
	}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("presentation calls = %#v, want %#v", runner.Calls, want)
	}
}

func TestFindPresentationSessionUsesExactNameAndTreatsAbsenceAsOptional(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(
		Response{Stdout: []byte("$1\tfleet-old\n$4\tfleet\n$7\tother\n")},
		Response{Stdout: []byte("$1\tother\n")},
	)
	client := New(runner)
	got, present, err := client.FindPresentationSession(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("FindPresentationSession() error = %v", err)
	}
	if !present || got != (Session{ID: "$4", Name: "fleet"}) {
		t.Fatalf("FindPresentationSession() = %#v, %t, want exact $4/fleet", got, present)
	}
	got, present, err = client.FindPresentationSession(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("FindPresentationSession(absent) error = %v", err)
	}
	if present || got != (Session{}) {
		t.Fatalf("FindPresentationSession(absent) = %#v, %t, want zero/false", got, present)
	}
}

func TestFindPresentationSessionTreatsTmuxNoServerAsOptionalPresentationGone(t *testing.T) {
	t.Parallel()

	for _, diagnostic := range []string{
		"no server running on /tmp/tmux-501/default",
		"error connecting to /tmp/tmux-501/default (No such file or directory)",
	} {
		runner := NewFakeRunner(Response{Err: errors.New(diagnostic)})
		got, present, err := New(runner).FindPresentationSession(context.Background(), "fleet")
		if err != nil || present || got != (Session{}) {
			t.Fatalf("FindPresentationSession() = %#v, %t, %v for %q; want optional absence", got, present, err, diagnostic)
		}
	}
}

func TestFindPresentationSessionPreservesOtherTmuxFailures(t *testing.T) {
	t.Parallel()

	for _, message := range []string{
		"permission denied",
		"wrapper: no server running on /tmp/tmux-501/default",
		"no server running on /tmp/tmux-501/default\nadditional failure",
		"error connecting to /tmp/tmux-501/default (No such file or directory): refused",
	} {
		runner := NewFakeRunner(Response{Err: errors.New(message)})
		_, _, err := New(runner).FindPresentationSession(context.Background(), "fleet")
		if err == nil {
			t.Fatalf("FindPresentationSession() error = nil for %q, want non-absence tmux failure", message)
		}
	}
}
