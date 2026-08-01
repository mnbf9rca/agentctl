package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestClassifyErrorWrapsTmuxFailureAndCarriesExitStderr(t *testing.T) {
	t.Setenv("GO_WANT_TMUXX_ERROR_HELPER", "1")
	command := exec.Command(os.Args[0], "-test.run=^TestTmuxErrorHelper$")
	command.Env = os.Environ()
	_, exitErr := command.Output()
	if exitErr == nil {
		t.Fatal("helper error = nil, want nonzero exit")
	}
	wrapped := fmt.Errorf("tmux list sessions: %w", exitErr)

	got := ClassifyError(wrapped)

	var tmuxFailure *TmuxError
	if !errors.As(got, &tmuxFailure) {
		t.Fatalf("ClassifyError() = %T %v, want *TmuxError", got, got)
	}
	if !errors.Is(got, exitErr) {
		t.Fatalf("ClassifyError() = %v, want wrapped exit error", got)
	}
	if !strings.Contains(got.Error(), "exit status 23") || !strings.Contains(got.Error(), "no server running") {
		t.Fatalf("ClassifyError().Error() = %q, want exit status and captured stderr", got.Error())
	}
}

func TestClassifyErrorPreservesContextSentinels(t *testing.T) {
	t.Parallel()

	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		sentinel := sentinel
		t.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()
			if got := ClassifyError(fmt.Errorf("tmux operation: %w", sentinel)); got != sentinel {
				t.Fatalf("ClassifyError() = %T %v, want exact sentinel %v", got, got, sentinel)
			}
		})
	}
}

func TestTmuxErrorHelper(t *testing.T) {
	if os.Getenv("GO_WANT_TMUXX_ERROR_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stderr, "no server running")
	os.Exit(23)
}
