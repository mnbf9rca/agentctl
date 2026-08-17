//go:build darwin

package main

import (
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/ptyx"
)

func TestPrepareForegroundTerminalClassifiesNonTerminalStandardOutput(t *testing.T) {
	t.Parallel()

	stdin := new(os.File)
	stdout := new(os.File)
	_, err := prepareForegroundTerminalWithObserver(stdin, stdout, func(file *os.File) (ptyx.TerminalState, error) {
		if file == stdout {
			return ptyx.TerminalState{}, syscall.ENOTSUP
		}
		return ptyx.TerminalState{}, nil
	})
	var notTerminal *foregroundNotTerminalError
	if !errors.As(err, &notTerminal) {
		t.Fatalf("error=%T %v, want foregroundNotTerminalError", err, err)
	}
}

func TestPrepareForegroundTerminalPreservesOtherStandardOutputObservationFailures(t *testing.T) {
	t.Parallel()

	stdin := new(os.File)
	stdout := new(os.File)
	cause := errors.New("TIOCGWINSZ: input/output error")
	_, err := prepareForegroundTerminalWithObserver(stdin, stdout, func(file *os.File) (ptyx.TerminalState, error) {
		if file == stdout {
			return ptyx.TerminalState{}, cause
		}
		return ptyx.TerminalState{}, nil
	})
	var observation *foregroundTerminalObservationError
	if !errors.As(err, &observation) || !errors.Is(err, cause) {
		t.Fatalf("error=%T %v, want wrapped foregroundTerminalObservationError", err, err)
	}
}
