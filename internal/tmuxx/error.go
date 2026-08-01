package tmuxx

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// TmuxError reports a tmux runner or output-parse failure.
type TmuxError struct {
	Err error
}

func (e *TmuxError) Error() string {
	message := e.Err.Error()
	var exitError *exec.ExitError
	if errors.As(e.Err, &exitError) {
		stderr := strings.TrimRight(string(exitError.Stderr), "\r\n")
		if stderr != "" && !strings.Contains(message, stderr) {
			return message + ": " + stderr
		}
	}
	return message
}

func (e *TmuxError) Unwrap() error {
	return e.Err
}

// ClassifyError preserves context sentinels and classifies every other tmux
// runner or output-parse failure as a TmuxError.
func ClassifyError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return &TmuxError{Err: err}
}
