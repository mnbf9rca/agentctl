// Package tmuxx provides typed wrappers around agentctl's external tmux and
// process-inspection commands.
package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
)

// Runner executes one program and returns its stdout.
type Runner interface {
	Output(context.Context, string, ...string) ([]byte, error)
}

// RealRunner executes programs with os/exec.
type RealRunner struct{}

// Output executes executable with args without invoking a shell.
func (RealRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).Output()
}

// Call records one Runner invocation.
type Call struct {
	Executable string
	Args       []string
}

// Response scripts one FakeRunner result.
type Response struct {
	Stdout []byte
	Err    error
}

// ErrFakeRunnerExhausted reports a call with no scripted response.
var ErrFakeRunnerExhausted = errors.New("fake runner response script exhausted")

// FakeRunner records calls and consumes scripted responses in FIFO order.
type FakeRunner struct {
	mu        sync.Mutex
	Calls     []Call
	responses []Response
}

// NewFakeRunner returns a fake with an independent copy of responses.
func NewFakeRunner(responses ...Response) *FakeRunner {
	copied := make([]Response, len(responses))
	for index, response := range responses {
		copied[index] = Response{
			Stdout: append([]byte(nil), response.Stdout...),
			Err:    response.Err,
		}
	}
	return &FakeRunner{responses: copied}
}

// Output records a call and returns the next scripted response.
func (f *FakeRunner) Output(_ context.Context, executable string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.Calls = append(f.Calls, Call{
		Executable: executable,
		Args:       append([]string(nil), args...),
	})
	if len(f.responses) == 0 {
		return nil, fmt.Errorf("%w for %s", ErrFakeRunnerExhausted, executable)
	}

	response := f.responses[0]
	f.responses = f.responses[1:]
	return append([]byte(nil), response.Stdout...), response.Err
}
