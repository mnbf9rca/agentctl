//go:build darwin

package ptyx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestExecChildStarterStartsExactArgvAndEnvironmentOnControllingPTY(t *testing.T) {
	starter := ExecChildStarter{Opener: NewOpener()}
	request := StartRequest{
		Argv: []string{os.Args[0], "-test.run=^TestPTYXChildHelper$", "--", "two words"},
		Env:  []string{"GO_WANT_PTYX_CHILD=roundtrip", "EXACT_ENV=present"},
		Size: WindowSize{Rows: 41, Cols: 113},
	}

	child, err := starter.Start(context.Background(), request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { cleanupChild(t, child) })
	if child.PID() <= 0 {
		t.Fatalf("PID() = %d, want positive", child.PID())
	}

	transcript := readUntilTest(t, child.Master(), "CHILD_READY", 5*time.Second)
	wantReady := fmt.Sprintf("CHILD_READY pid=%d pgrp=%d foreground=%d rows=41 cols=113 arg=\"two words\" env=\"present\"", child.PID(), child.PID(), child.PID())
	if !strings.Contains(transcript, wantReady) {
		t.Fatalf("ready transcript = %q, want substring %q", transcript, wantReady)
	}
	writeAllTest(t, child.Master(), []byte("binary:\x00\xff\n"), 5*time.Second)
	transcript += readUntilTest(t, child.Master(), "CHILD_ECHO=62696e6172793a00ff", 5*time.Second)
	if !strings.Contains(transcript, "CHILD_ECHO=62696e6172793a00ff") {
		t.Fatalf("round-trip transcript = %q", transcript)
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exit, err := child.Wait(waitCtx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if !exit.Observed || exit.PID != child.PID() || exit.ExitCode != 0 {
		t.Fatalf("Wait() = %#v, want observed pid %d exit 0", exit, child.PID())
	}
}

func TestExecChildStarterRefusesCanceledContextBeforeOpeningPTY(t *testing.T) {
	opener := &recordingOpener{}
	starter := ExecChildStarter{Opener: opener}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	child, err := starter.Start(ctx, StartRequest{Argv: []string{"ignored"}, Size: WindowSize{Rows: 24, Cols: 80}})
	if child != nil {
		t.Fatalf("Start() child = %#v, want nil", child)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Start() error = %v, want context.Canceled", err)
	}
	if opener.calls != 0 {
		t.Fatalf("opener calls = %d, want 0", opener.calls)
	}
}

func TestChildStarterCanBeFakedByShim(t *testing.T) {
	wantChild := &fakeChild{pid: 1234}
	var starter ChildStarter = childStarterFunc(func(context.Context, StartRequest) (Child, error) {
		return wantChild, nil
	})

	child, err := starter.Start(context.Background(), StartRequest{})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if child != wantChild {
		t.Fatalf("Start() child = %#v, want injectable child %#v", child, wantChild)
	}
}

func TestExecChildStarterReturnsTypedStartedChildErrorWhenParentSlaveCloseFails(t *testing.T) {
	wantCloseFailure := errors.New("injected parent slave close failure")
	pair, err := NewOpener().Open(WindowSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	slave := pair.slave
	pair.closeSlave = func() error {
		return errors.Join(slave.Close(), wantCloseFailure)
	}
	starter := ExecChildStarter{Opener: &recordingOpener{pair: pair}}

	child, err := starter.Start(context.Background(), StartRequest{
		Argv: []string{os.Args[0], "-test.run=^TestPTYXChildHelper$"},
		Env:  []string{"GO_WANT_PTYX_CHILD=survive"},
		Size: WindowSize{Rows: 24, Cols: 80},
	})
	if child != nil {
		t.Fatalf("Start() child = %#v, want nil when error carries ownership", child)
	}
	var started *StartedChildError
	if !errors.As(err, &started) {
		t.Fatalf("Start() error = %v, want StartedChildError", err)
	}
	if !errors.Is(err, wantCloseFailure) {
		t.Fatalf("Start() error = %v, want injected close failure", err)
	}
	if started.Child == nil || started.Child.PID() <= 0 {
		t.Fatalf("StartedChildError.Child = %#v, want started child ownership", started.Child)
	}
	t.Cleanup(func() { cleanupChild(t, started.Child) })
}

func TestExecChildStarterCleansBothPTYDescriptorsWhenExecFails(t *testing.T) {
	pair := testPair(t)
	master := pair.master
	slave := pair.slave
	starter := ExecChildStarter{Opener: &recordingOpener{pair: pair}}

	child, err := starter.Start(context.Background(), StartRequest{
		Argv: []string{"/definitely/not/an/executable"},
		Size: WindowSize{Rows: 24, Cols: 80},
	})
	if child != nil {
		t.Fatalf("Start() child = %#v, want nil", child)
	}
	if err == nil {
		t.Fatal("Start() error = nil, want exec failure")
	}
	assertClosed(t, master, true)
	assertClosed(t, slave, true)
}

func TestChildTerminateSeparatesSignalAttemptFromObservedExit(t *testing.T) {
	starter := ExecChildStarter{Opener: NewOpener()}
	child, err := starter.Start(context.Background(), StartRequest{
		Argv: []string{os.Args[0], "-test.run=^TestPTYXChildHelper$"},
		Env:  []string{"GO_WANT_PTYX_CHILD=survive"},
		Size: WindowSize{Rows: 24, Cols: 80},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { cleanupChild(t, child) })
	_ = readUntilTest(t, child.Master(), "CHILD_READY", 5*time.Second)

	shortCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	observation, err := child.Terminate(shortCtx, syscall.SIGTERM)
	var surviving *SurvivingChildError
	if !errors.As(err, &surviving) {
		t.Fatalf("Terminate() error = %v, want SurvivingChildError", err)
	}
	if surviving.PID != child.PID() {
		t.Fatalf("SurvivingChildError.PID = %d, want %d", surviving.PID, child.PID())
	}
	if !observation.Signal.Attempted || observation.Signal.Signal != syscall.SIGTERM || observation.Signal.Err != nil {
		t.Fatalf("Terminate() signal = %#v, want successful SIGTERM attempt", observation.Signal)
	}
	if observation.Exit.Observed {
		t.Fatalf("Terminate() exit = %#v, want unobserved survivor", observation.Exit)
	}

	killCtx, killCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer killCancel()
	killed, err := child.Terminate(killCtx, os.Kill)
	if err != nil {
		t.Fatalf("SIGKILL Terminate() error = %v", err)
	}
	if !killed.Signal.Attempted || killed.Signal.Err != nil || !killed.Exit.Observed || killed.Exit.PID != child.PID() {
		t.Fatalf("SIGKILL Terminate() = %#v, want successful attempt and observed exit", killed)
	}
}

func TestChildSignalProcessGroupTargetsTheOwnedChildGroup(t *testing.T) {
	wantFailure := errors.New("injected group signal failure")
	signaler := &recordingChildSignaler{err: wantFailure}
	child := &execChild{pid: 4321, signaler: signaler}

	observation := child.SignalProcessGroup(syscall.SIGHUP)
	if !observation.Attempted || observation.Signal != syscall.SIGHUP || observation.ProcessGroupID != 4321 {
		t.Fatalf("SignalProcessGroup() = %#v, want attempted SIGHUP for process group 4321", observation)
	}
	if !errors.Is(observation.Err, wantFailure) {
		t.Fatalf("SignalProcessGroup() error = %v, want injected failure", observation.Err)
	}
	wantCalls := []childSignalCall{{pid: -4321, signal: syscall.SIGHUP}}
	if !reflect.DeepEqual(signaler.calls, wantCalls) {
		t.Fatalf("signal calls = %#v, want %#v", signaler.calls, wantCalls)
	}
}

func TestChildTerminateKeepsGroupESRCHSeparateFromObservedExit(t *testing.T) {
	done := make(chan struct{})
	close(done)
	child := &execChild{
		pid:      4321,
		signaler: &recordingChildSignaler{err: syscall.ESRCH},
		done:     done,
		exit:     ExitObservation{Observed: true, PID: 4321, ExitCode: 0},
	}

	observation, err := child.Terminate(context.Background(), syscall.SIGHUP)
	if err != nil {
		t.Fatalf("Terminate() error = %v, want observed exit despite group ESRCH", err)
	}
	if !errors.Is(observation.Signal.Err, syscall.ESRCH) {
		t.Fatalf("Terminate() signal error = %v, want recorded ESRCH", observation.Signal.Err)
	}
	if !observation.Exit.Observed || observation.Exit.PID != 4321 {
		t.Fatalf("Terminate() exit = %#v, want observed child 4321", observation.Exit)
	}
}

type recordingOpener struct {
	calls int
	pair  *PTY
	err   error
}

type childSignalCall struct {
	pid    int
	signal syscall.Signal
}

type recordingChildSignaler struct {
	calls []childSignalCall
	err   error
}

func (s *recordingChildSignaler) Kill(pid int, signal syscall.Signal) error {
	s.calls = append(s.calls, childSignalCall{pid: pid, signal: signal})
	return s.err
}

type childStarterFunc func(context.Context, StartRequest) (Child, error)

func (f childStarterFunc) Start(ctx context.Context, request StartRequest) (Child, error) {
	return f(ctx, request)
}

type fakeChild struct {
	pid int
}

func (c *fakeChild) PID() int { return c.pid }

func (*fakeChild) Master() *os.File { return nil }

func (c *fakeChild) Wait(context.Context) (ExitObservation, error) {
	return ExitObservation{Observed: true, PID: c.pid}, nil
}

func (c *fakeChild) SignalProcessGroup(signal os.Signal) SignalObservation {
	return SignalObservation{Attempted: true, Signal: signal, ProcessGroupID: c.pid}
}

func (c *fakeChild) Terminate(context.Context, os.Signal) (TerminationObservation, error) {
	return TerminationObservation{Exit: ExitObservation{Observed: true, PID: c.pid}}, nil
}

func (*fakeChild) CloseMaster() error { return nil }

func (o *recordingOpener) Open(WindowSize) (*PTY, error) {
	o.calls++
	return o.pair, o.err
}

func testPair(t *testing.T) *PTY {
	t.Helper()
	master := newTestFile(t, "master")
	slave := newTestFile(t, "slave")
	return &PTY{master: master, slave: slave, slaveName: slave.Name()}
}

func TestPTYXChildHelper(t *testing.T) {
	mode := os.Getenv("GO_WANT_PTYX_CHILD")
	if mode == "" {
		return
	}
	if mode == "survive" {
		signal.Ignore(syscall.SIGTERM)
	}
	if mode == "raw" {
		var termios syscall.Termios
		if err := rawIOCTL(os.Stdin.Fd(), syscall.TIOCGETA, unsafe.Pointer(&termios)); err != nil {
			os.Exit(2)
		}
		termios.Lflag &^= syscall.ICANON | syscall.ECHO
		if err := rawIOCTL(os.Stdin.Fd(), syscall.TIOCSETA, unsafe.Pointer(&termios)); err != nil {
			os.Exit(2)
		}
	}

	var foreground int32
	if err := rawIOCTL(os.Stdin.Fd(), syscall.TIOCGPGRP, unsafe.Pointer(&foreground)); err != nil {
		fmt.Fprintf(os.Stderr, "HELPER_FAILURE=%q\n", err)
		os.Exit(2)
	}
	pgrp, err := syscall.Getpgid(0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HELPER_FAILURE=%q\n", err)
		os.Exit(2)
	}
	var size WindowSize
	if err := rawIOCTL(os.Stdin.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)); err != nil {
		fmt.Fprintf(os.Stderr, "HELPER_FAILURE=%q\n", err)
		os.Exit(2)
	}
	argument := ""
	for index, value := range os.Args {
		if value == "--" && index+1 < len(os.Args) {
			argument = os.Args[index+1]
			break
		}
	}
	fmt.Printf("CHILD_READY pid=%d pgrp=%d foreground=%d rows=%d cols=%d arg=%q env=%q\n", os.Getpid(), pgrp, foreground, size.Rows, size.Cols, argument, os.Getenv("EXACT_ENV"))

	if mode == "survive" || mode == "raw" {
		select {}
	}
	buffer := make([]byte, 256)
	count, err := os.Stdin.Read(buffer)
	if err != nil {
		fmt.Fprintf(os.Stderr, "HELPER_FAILURE=%q\n", err)
		os.Exit(2)
	}
	fmt.Printf("CHILD_ECHO=%x\n", bytes.TrimSuffix(buffer[:count], []byte{'\n'}))
}

func readUntilTest(t *testing.T, file *os.File, marker string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var transcript strings.Builder
	buffer := make([]byte, 512)
	for time.Now().Before(deadline) {
		count, err := file.Read(buffer)
		if count > 0 {
			transcript.Write(buffer[:count])
			if strings.Contains(transcript.String(), marker) {
				return transcript.String()
			}
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			t.Fatalf("read PTY: %v; transcript=%q", err, transcript.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q; transcript=%q", marker, transcript.String())
	return ""
}

func writeAllTest(t *testing.T, file *os.File, value []byte, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for len(value) > 0 && time.Now().Before(deadline) {
		count, err := file.Write(value)
		if count > 0 {
			value = value[count:]
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			t.Fatalf("write PTY: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(value) != 0 {
		t.Fatalf("timeout with %d bytes unwritten", len(value))
	}
}

func cleanupChild(t *testing.T, child Child) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = child.Terminate(ctx, os.Kill)
	if err := child.CloseMaster(); err != nil && !errors.Is(err, os.ErrClosed) {
		t.Errorf("CloseMaster(): %v", err)
	}
}
