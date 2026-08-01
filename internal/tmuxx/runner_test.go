package tmuxx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestRealRunnerPassesExecutableAndArguments(t *testing.T) {
	t.Setenv("GO_WANT_TMUXX_HELPER_PROCESS", "1")

	got, err := (RealRunner{}).Output(
		context.Background(),
		os.Args[0],
		"-test.run=^TestRealRunnerHelper$",
		"--",
		"first",
		"two words",
	)
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if want := "first\x00two words"; string(got) != want {
		t.Fatalf("Output() = %q, want %q", got, want)
	}
}

func TestRealRunnerHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := (RealRunner{}).Output(ctx, os.Args[0], "-test.run=^TestRealRunnerHelper$")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Output() error = %v, want context.Canceled", err)
	}
}

func TestRealRunnerInteractivePassesArgumentsAndConnectedStreams(t *testing.T) {
	t.Setenv("GO_WANT_TMUXX_HELPER_PROCESS", "1")

	var stdout, stderr bytes.Buffer
	runner := RealRunner{
		Stdin:  strings.NewReader("terminal input"),
		Stdout: &stdout,
		Stderr: &stderr,
	}
	err := runner.RunInteractive(
		context.Background(),
		os.Args[0],
		"-test.run=^TestRealRunnerHelper$",
		"--",
		"interactive",
		"two words",
	)

	if err != nil {
		t.Fatalf("RunInteractive() error = %v", err)
	}
	if want := "stdout:terminal input\x00two words"; stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
	if want := "stderr:terminal input"; stderr.String() != want {
		t.Fatalf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestRealRunnerHelperRequiresMarker(t *testing.T) {
	t.Setenv("GO_WANT_TMUXX_HELPER_PROCESS", "")

	got, err := (RealRunner{}).Output(
		context.Background(),
		os.Args[0],
		"-test.run=^TestRealRunnerHelper$",
		"--",
	)
	if err != nil {
		t.Fatalf("Output() error = %v", err)
	}
	if !bytes.Contains(got, []byte("PASS")) {
		t.Fatalf("Output() = %q, want ordinary child test completion", got)
	}
}

func TestRealRunnerHelper(t *testing.T) {
	if os.Getenv("GO_WANT_TMUXX_HELPER_PROCESS") != "1" {
		return
	}

	separator := -1
	for index, arg := range os.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		return
	}
	if len(os.Args) > separator+1 && os.Args[separator+1] == "interactive" {
		input, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(os.Stdout, "stdout:%s", input)
		if len(os.Args) > separator+2 {
			_, _ = fmt.Fprintf(os.Stdout, "\x00%s", os.Args[separator+2])
		}
		_, _ = fmt.Fprintf(os.Stderr, "stderr:%s", input)
		os.Exit(0)
	}

	for index, arg := range os.Args[separator+1:] {
		if index > 0 {
			_, _ = fmt.Fprint(os.Stdout, "\x00")
		}
		_, _ = fmt.Fprint(os.Stdout, arg)
	}
	os.Exit(0)
}

func TestFakeRunnerRecordsDeepCopiedCallsAndConsumesFIFOResponses(t *testing.T) {
	t.Parallel()

	firstBytes := []byte("first")
	secondErr := errors.New("second failed")
	runner := NewFakeRunner(
		Response{Stdout: firstBytes},
		Response{Stdout: []byte("second"), Err: secondErr},
	)
	firstBytes[0] = 'X'

	args := []string{"one", "two words"}
	got, err := runner.Output(context.Background(), "tmux", args...)
	if err != nil {
		t.Fatalf("first Output() error = %v", err)
	}
	if want := "first"; string(got) != want {
		t.Fatalf("first Output() = %q, want %q", got, want)
	}
	args[0] = "mutated"
	got[0] = 'Y'

	second, err := runner.Output(context.Background(), "ps", "-p", "42")
	if !errors.Is(err, secondErr) {
		t.Fatalf("second Output() error = %v, want %v", err, secondErr)
	}
	if want := "second"; string(second) != want {
		t.Fatalf("second Output() = %q, want %q", second, want)
	}

	wantCalls := []Call{
		{Executable: "tmux", Args: []string{"one", "two words"}},
		{Executable: "ps", Args: []string{"-p", "42"}},
	}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestFakeRunnerFailsWhenScriptIsExhausted(t *testing.T) {
	t.Parallel()

	runner := NewFakeRunner()
	_, err := runner.Output(context.Background(), "tmux", "list-sessions")
	if !errors.Is(err, ErrFakeRunnerExhausted) {
		t.Fatalf("Output() error = %v, want ErrFakeRunnerExhausted", err)
	}
	wantCalls := []Call{{Executable: "tmux", Args: []string{"list-sessions"}}}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestFakeRunnerInteractiveRecordsCallAndConsumesResponse(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("interactive failed")
	runner := NewFakeRunner(Response{Stdout: []byte("ignored control protocol"), Err: wantErr})

	err := runner.RunInteractive(context.Background(), "tmux", "-CC", "attach-session", "-t", "$4")

	if !errors.Is(err, wantErr) {
		t.Fatalf("RunInteractive() error = %v, want %v", err, wantErr)
	}
	wantCalls := []Call{{Executable: "tmux", Args: []string{"-CC", "attach-session", "-t", "$4"}}}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}
