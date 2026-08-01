package tmuxx

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestDeliverPayloadUsesFixedThreeCallSequence(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{}, Response{}, Response{})
	if err := New(runner).DeliverPayload(context.Background(), "%9", "/clear"); err != nil {
		t.Fatalf("DeliverPayload() error = %v", err)
	}
	want := []Call{
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "-l", "--", "/clear"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "Enter"}},
	}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}

func TestDeliverPayloadStopsOnFirstError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		responses []Response
		wantCalls []Call
	}{
		{
			name:      "clear input",
			responses: []Response{{Err: errors.New("clear failed")}},
			wantCalls: []Call{{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}}},
		},
		{
			name:      "literal payload",
			responses: []Response{{}, {Err: errors.New("payload failed")}},
			wantCalls: []Call{
				{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}},
				{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "-l", "--", "/compact"}},
			},
		},
		{
			name:      "enter",
			responses: []Response{{}, {}, {Err: errors.New("enter failed")}},
			wantCalls: []Call{
				{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}},
				{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "-l", "--", "/compact"}},
				{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "Enter"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(tt.responses...)
			if err := New(runner).DeliverPayload(context.Background(), "%9", "/compact"); err == nil {
				t.Fatal("DeliverPayload() error = nil, want runner error")
			}
			if !reflect.DeepEqual(runner.Calls, tt.wantCalls) {
				t.Fatalf("Calls = %#v, want %#v", runner.Calls, tt.wantCalls)
			}
		})
	}
}

func TestDeliverPayloadRejectsNameTargetBeforeRunningTmux(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner()
	if err := New(runner).DeliverPayload(context.Background(), "planner", "/clear"); err == nil {
		t.Fatal("DeliverPayload() error = nil, want invalid pane ID error")
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("Calls = %#v, want no external command", runner.Calls)
	}
}

func TestDeliverPayloadCancellationPreventsEnter(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	runner := &cancelAfterSecondCallRunner{
		FakeRunner: NewFakeRunner(Response{}, Response{}),
		cancel:     cancel,
	}
	err := New(runner).DeliverPayload(ctx, "%9", "/clear")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DeliverPayload() error = %v, want context.Canceled", err)
	}
	want := []Call{
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "C-u"}},
		{Executable: "tmux", Args: []string{"send-keys", "-t", "%9", "-l", "--", "/clear"}},
	}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}

func TestProcessNameUsesCanonicalPsArgvAndTrimsOneNewline(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner(Response{Stdout: []byte("bash\n")}, Response{Stdout: []byte("bash\n\n")})
	client := New(runner)
	got, err := client.ProcessName(context.Background(), 1234)
	if err != nil {
		t.Fatalf("ProcessName() error = %v", err)
	}
	if want := "bash"; got != want {
		t.Fatalf("ProcessName() = %q, want %q", got, want)
	}
	got, err = client.ProcessName(context.Background(), 5678)
	if err != nil {
		t.Fatalf("ProcessName() second error = %v", err)
	}
	if want := "bash\n"; got != want {
		t.Fatalf("ProcessName() second = %q, want %q", got, want)
	}
	assertCalls(t, runner,
		Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "1234"}},
		Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "5678"}},
	)
}

func TestProcessNameCollapsesExitAndEmptyOutputIntoUnavailable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		response Response
	}{
		{name: "dead pid exit", response: Response{Err: scriptedExitError{code: 1, message: "exit status 1"}}},
		{name: "stderr exit", response: Response{Err: scriptedExitError{code: 1, message: "ps: process id too large"}}},
		{name: "nonzero with stdout", response: Response{Stdout: []byte("partial\n"), Err: scriptedExitError{code: 2, message: "exit status 2"}}},
		{name: "empty stdout", response: Response{}},
		{name: "newline only stdout", response: Response{Stdout: []byte("\n")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(tt.response)
			got, err := New(runner).ProcessName(context.Background(), 1234)
			if got != "" {
				t.Fatalf("ProcessName() = %q, want empty identity", got)
			}
			if !errors.Is(err, ErrProcessUnavailable) {
				t.Fatalf("ProcessName() error = %v, want ErrProcessUnavailable", err)
			}
		})
	}
}

func TestProcessNamePreservesContextAndStartupErrors(t *testing.T) {
	t.Parallel()

	startupErr := errors.New("ps executable missing")
	tests := []struct {
		name     string
		ctx      func() context.Context
		response Response
		wantErr  error
	}{
		{
			name: "canceled context",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			response: Response{Err: context.Canceled},
			wantErr:  context.Canceled,
		},
		{
			name:     "startup error",
			ctx:      context.Background,
			response: Response{Err: startupErr},
			wantErr:  startupErr,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := NewFakeRunner(tt.response)
			_, err := New(runner).ProcessName(tt.ctx(), 1234)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ProcessName() error = %v, want %v", err, tt.wantErr)
			}
			if errors.Is(err, ErrProcessUnavailable) {
				t.Fatalf("ProcessName() error = %v, must not be ErrProcessUnavailable", err)
			}
		})
	}
}

func TestProcessNameRejectsNonpositivePIDBeforeRunningPs(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1} {
		runner := NewFakeRunner()
		if _, err := New(runner).ProcessName(context.Background(), pid); err == nil {
			t.Fatalf("ProcessName(%d) error = nil, want invalid PID error", pid)
		}
		if len(runner.Calls) != 0 {
			t.Fatalf("ProcessName(%d) Calls = %#v, want no external command", pid, runner.Calls)
		}
	}
}

type scriptedExitError struct {
	code    int
	message string
}

func (e scriptedExitError) Error() string {
	return e.message
}

func (e scriptedExitError) ExitCode() int {
	return e.code
}

type cancelAfterSecondCallRunner struct {
	*FakeRunner
	cancel context.CancelFunc
}

func (r *cancelAfterSecondCallRunner) Output(ctx context.Context, executable string, args ...string) ([]byte, error) {
	output, err := r.FakeRunner.Output(ctx, executable, args...)
	if len(r.Calls) == 2 {
		r.cancel()
	}
	return output, err
}

func (r *cancelAfterSecondCallRunner) RunInteractive(ctx context.Context, executable string, args ...string) error {
	err := r.FakeRunner.RunInteractive(ctx, executable, args...)
	if len(r.Calls) == 2 {
		r.cancel()
	}
	return err
}
