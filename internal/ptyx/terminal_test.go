//go:build darwin

package ptyx

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

type terminalIOCTLCall struct {
	fd      uintptr
	request uintptr
}

type fakeTerminalSystem struct {
	mu          sync.Mutex
	calls       []terminalIOCTLCall
	termiosByFD map[uintptr]syscall.Termios
	sizeByFD    map[uintptr]WindowSize
	failRequest uintptr
	failure     error
}

func (f *fakeTerminalSystem) ioctl(fd, request uintptr, argument unsafe.Pointer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, terminalIOCTLCall{fd: fd, request: request})
	if request == f.failRequest {
		return f.failure
	}
	switch request {
	case syscall.TIOCGETA:
		*(*syscall.Termios)(argument) = f.termiosByFD[fd]
	case syscall.TIOCSETA:
		f.termiosByFD[fd] = *(*syscall.Termios)(argument)
	case syscall.TIOCGWINSZ:
		*(*WindowSize)(argument) = f.sizeByFD[fd]
	case syscall.TIOCSWINSZ:
		f.sizeByFD[fd] = *(*WindowSize)(argument)
	}
	return nil
}

func TestTerminalObserverDistinguishesCookedEchoFromSettledWithoutReadingContents(t *testing.T) {
	file := newTestFile(t, "terminal")
	fd := file.Fd()
	system := &fakeTerminalSystem{
		termiosByFD: map[uintptr]syscall.Termios{
			fd: {Lflag: syscall.ICANON | syscall.ECHO},
		},
		sizeByFD: map[uintptr]WindowSize{fd: {Rows: 24, Cols: 80}},
	}
	observer := darwinTerminal{system: system}

	cooked, err := observer.Observe(file)
	if err != nil {
		t.Fatalf("Observe(cooked) error = %v", err)
	}
	if !cooked.Canonical() || !cooked.Echo() || cooked.Settled() {
		t.Fatalf("cooked state = canonical:%t echo:%t settled:%t", cooked.Canonical(), cooked.Echo(), cooked.Settled())
	}
	if got, want := cooked.WindowSize(), (WindowSize{Rows: 24, Cols: 80}); got != want {
		t.Fatalf("WindowSize() = %#v, want %#v", got, want)
	}

	system.termiosByFD[fd] = syscall.Termios{Lflag: syscall.ISIG}
	settled, err := observer.Observe(file)
	if err != nil {
		t.Fatalf("Observe(settled) error = %v", err)
	}
	if settled.Canonical() || settled.Echo() || !settled.Settled() {
		t.Fatalf("raw state = canonical:%t echo:%t settled:%t", settled.Canonical(), settled.Echo(), settled.Settled())
	}

	wantCalls := []terminalIOCTLCall{
		{fd: fd, request: syscall.TIOCGETA},
		{fd: fd, request: syscall.TIOCGWINSZ},
		{fd: fd, request: syscall.TIOCGETA},
		{fd: fd, request: syscall.TIOCGWINSZ},
	}
	if !reflect.DeepEqual(system.calls, wantCalls) {
		t.Fatalf("ioctl calls = %#v, want observation-only %#v", system.calls, wantCalls)
	}
}

func TestUnobservedZeroTerminalStateIsNotSettled(t *testing.T) {
	state := TerminalState{}
	if state.Settled() {
		t.Fatal("zero TerminalState reports settled, want unobserved state to refuse readiness")
	}
}

func TestTerminalWaitReadyObservesFixedBoundariesUntilBothFlagsClear(t *testing.T) {
	clock := newFakeReadinessClock()
	system := &scriptedReadinessSystem{
		clock: clock,
		steps: []readinessStep{
			{termios: syscall.Termios{Lflag: syscall.ICANON | syscall.ECHO}},
			{termios: syscall.Termios{Lflag: syscall.ECHO}},
			{termios: syscall.Termios{Lflag: syscall.ISIG}},
		},
	}
	terminal := darwinTerminal{system: system, clock: clock}
	file := newTestFile(t, "terminal")

	state, err := terminal.WaitReady(context.Background(), file)
	if err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if !state.Settled() {
		t.Fatalf("WaitReady() state = canonical:%t echo:%t, want settled", state.Canonical(), state.Echo())
	}
	wantCalls := []time.Duration{0, 50 * time.Millisecond, 100 * time.Millisecond}
	if !reflect.DeepEqual(system.calls, wantCalls) {
		t.Fatalf("TIOCGETA boundaries = %v, want %v", system.calls, wantCalls)
	}
}

func TestTerminalWaitReadyIncludesFinalFiveSecondObservation(t *testing.T) {
	clock := newFakeReadinessClock()
	system := &scriptedReadinessSystem{
		clock:          clock,
		defaultTermios: syscall.Termios{Lflag: syscall.ICANON},
	}
	terminal := darwinTerminal{system: system, clock: clock}
	file := newTestFile(t, "terminal")

	state, err := terminal.WaitReady(context.Background(), file)
	var timeout *ReadinessTimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("WaitReady() error = %v, want ReadinessTimeoutError", err)
	}
	if !state.Canonical() || state.Echo() || timeout.State != state {
		t.Fatalf("timeout state = canonical:%t echo:%t payload:%#v, want final canonical-only snapshot", state.Canonical(), state.Echo(), timeout.State)
	}
	if got, want := len(system.calls), 101; got != want {
		t.Fatalf("TIOCGETA calls = %d, want %d inclusive observations", got, want)
	}
	if got, want := system.calls[0], time.Duration(0); got != want {
		t.Fatalf("first TIOCGETA boundary = %v, want %v", got, want)
	}
	if got, want := system.calls[len(system.calls)-1], 5*time.Second; got != want {
		t.Fatalf("final TIOCGETA boundary = %v, want %v", got, want)
	}
}

func TestTerminalWaitReadyRetriesEINTRWithinScheduledObservation(t *testing.T) {
	clock := newFakeReadinessClock()
	system := &scriptedReadinessSystem{
		clock: clock,
		steps: []readinessStep{
			{err: syscall.EINTR},
			{err: syscall.EINTR},
			{termios: syscall.Termios{Lflag: syscall.ISIG}},
		},
	}
	terminal := darwinTerminal{system: system, clock: clock}
	file := newTestFile(t, "terminal")

	state, err := terminal.WaitReady(context.Background(), file)
	if err != nil {
		t.Fatalf("WaitReady() error = %v", err)
	}
	if !state.Settled() {
		t.Fatalf("WaitReady() state = canonical:%t echo:%t, want settled", state.Canonical(), state.Echo())
	}
	wantCalls := []time.Duration{0, 0, 0}
	if !reflect.DeepEqual(system.calls, wantCalls) {
		t.Fatalf("TIOCGETA boundaries = %v, want immediate same-slot retries %v", system.calls, wantCalls)
	}
}

func TestTerminalWaitReadyClassifiesEINTRAtInclusiveDeadline(t *testing.T) {
	clock := newFakeReadinessClock()
	system := &scriptedReadinessSystem{
		clock:          clock,
		defaultTermios: syscall.Termios{Lflag: syscall.ECHO},
		finalError:     syscall.EINTR,
	}
	terminal := darwinTerminal{system: system, clock: clock}
	file := newTestFile(t, "terminal")

	_, err := terminal.WaitReady(context.Background(), file)
	var observation *ReadinessObservationError
	if !errors.As(err, &observation) {
		t.Fatalf("WaitReady() error = %v, want ReadinessObservationError", err)
	}
	if !errors.Is(err, ErrReadinessInterruptedDeadline) {
		t.Fatalf("WaitReady() error = %v, want ErrReadinessInterruptedDeadline", err)
	}
	if got, want := len(system.calls), 101; got != want {
		t.Fatalf("TIOCGETA calls = %d, want %d", got, want)
	}
	if got, want := system.calls[len(system.calls)-1], 5*time.Second; got != want {
		t.Fatalf("final TIOCGETA boundary = %v, want %v", got, want)
	}
}

func TestTerminalWaitReadyReportsNonInterruptedIOCTLFailureImmediately(t *testing.T) {
	wantFailure := errors.New("injected readiness ioctl failure")
	clock := newFakeReadinessClock()
	system := &scriptedReadinessSystem{
		clock: clock,
		steps: []readinessStep{{err: wantFailure}},
	}
	terminal := darwinTerminal{system: system, clock: clock}
	file := newTestFile(t, "terminal")

	_, err := terminal.WaitReady(context.Background(), file)
	var observation *ReadinessObservationError
	if !errors.As(err, &observation) {
		t.Fatalf("WaitReady() error = %v, want ReadinessObservationError", err)
	}
	if !errors.Is(err, wantFailure) {
		t.Fatalf("WaitReady() error = %v, want injected ioctl failure", err)
	}
	if got, want := err.Error(), "TIOCGETA failed while observing harness tty readiness: injected readiness ioctl failure"; got != want {
		t.Fatalf("WaitReady() error = %q, want %q", got, want)
	}
	wantCalls := []time.Duration{0}
	if !reflect.DeepEqual(system.calls, wantCalls) {
		t.Fatalf("TIOCGETA boundaries = %v, want immediate failure at %v", system.calls, wantCalls)
	}
}

func TestTerminalWaitReadyPrefersCancellationOverInterruptedDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	clock := newFakeReadinessClock()
	system := &cancelingReadinessSystem{clock: clock, cancel: cancel}
	terminal := darwinTerminal{system: system, clock: clock}
	file := newTestFile(t, "terminal")

	_, err := terminal.WaitReady(ctx, file)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WaitReady() error = %v, want context.Canceled", err)
	}
	if got, want := system.calls, 1; got != want {
		t.Fatalf("TIOCGETA calls = %d, want %d before cancellation", got, want)
	}
}

type readinessStep struct {
	termios syscall.Termios
	err     error
}

type scriptedReadinessSystem struct {
	clock          *fakeReadinessClock
	steps          []readinessStep
	defaultTermios syscall.Termios
	finalError     error
	calls          []time.Duration
}

func (s *scriptedReadinessSystem) ioctl(_ uintptr, request uintptr, argument unsafe.Pointer) error {
	if request != syscall.TIOCGETA {
		return errors.New("readiness observer issued a non-TIOCGETA ioctl")
	}
	s.calls = append(s.calls, s.clock.sinceStart())
	if len(s.steps) > 0 {
		step := s.steps[0]
		s.steps = s.steps[1:]
		if step.err != nil {
			return step.err
		}
		*(*syscall.Termios)(argument) = step.termios
		return nil
	}
	if s.finalError != nil && s.clock.sinceStart() == 5*time.Second {
		return s.finalError
	}
	*(*syscall.Termios)(argument) = s.defaultTermios
	return nil
}

type fakeReadinessClock struct {
	start time.Time
	now   time.Time
}

func newFakeReadinessClock() *fakeReadinessClock {
	start := time.Unix(100, 0)
	return &fakeReadinessClock{start: start, now: start}
}

func (c *fakeReadinessClock) Now() time.Time { return c.now }

func (c *fakeReadinessClock) WaitUntil(ctx context.Context, target time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.now = target
	return nil
}

func (c *fakeReadinessClock) sinceStart() time.Duration { return c.now.Sub(c.start) }

type cancelingReadinessSystem struct {
	clock  *fakeReadinessClock
	cancel context.CancelFunc
	calls  int
}

func (s *cancelingReadinessSystem) ioctl(_ uintptr, request uintptr, _ unsafe.Pointer) error {
	if request != syscall.TIOCGETA {
		return errors.New("readiness observer issued a non-TIOCGETA ioctl")
	}
	s.calls++
	s.cancel()
	s.clock.now = s.clock.start.Add(ReadinessTimeout)
	return syscall.EINTR
}

func TestTerminalObserverStopsAtEachFailedObservation(t *testing.T) {
	wantFailure := errors.New("terminal observation failed")
	tests := []struct {
		name        string
		failRequest uintptr
		wantCalls   []uintptr
	}{
		{name: "termios", failRequest: syscall.TIOCGETA, wantCalls: []uintptr{syscall.TIOCGETA}},
		{name: "window size", failRequest: syscall.TIOCGWINSZ, wantCalls: []uintptr{syscall.TIOCGETA, syscall.TIOCGWINSZ}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			file := newTestFile(t, "terminal")
			system := &fakeTerminalSystem{
				termiosByFD: map[uintptr]syscall.Termios{},
				sizeByFD:    map[uintptr]WindowSize{},
				failRequest: test.failRequest,
				failure:     wantFailure,
			}
			_, err := (darwinTerminal{system: system}).Observe(file)
			if !errors.Is(err, wantFailure) {
				t.Fatalf("Observe() error = %v, want injected failure", err)
			}
			var got []uintptr
			for _, call := range system.calls {
				got = append(got, call.request)
			}
			if !reflect.DeepEqual(got, test.wantCalls) {
				t.Fatalf("ioctl requests = %#v, want %#v", got, test.wantCalls)
			}
		})
	}
}

func TestTerminalForwardsExactTermiosAndWindowSizeInTheirRequiredDirections(t *testing.T) {
	outer := newTestFile(t, "outer")
	nested := newTestFile(t, "nested")
	outerSize := WindowSize{Rows: 51, Cols: 132, XPixel: 4, YPixel: 8}
	childMode := syscall.Termios{
		Iflag:  1,
		Oflag:  2,
		Cflag:  3,
		Lflag:  syscall.ISIG,
		Cc:     [20]uint8{1, 2, 3, 4},
		Ispeed: 9600,
		Ospeed: 19200,
	}
	system := &fakeTerminalSystem{
		termiosByFD: map[uintptr]syscall.Termios{nested.Fd(): childMode},
		sizeByFD:    map[uintptr]WindowSize{outer.Fd(): outerSize},
	}
	terminal := darwinTerminal{system: system}

	if err := terminal.ForwardWindowSize(outer, nested); err != nil {
		t.Fatalf("ForwardWindowSize() error = %v", err)
	}
	if err := terminal.ForwardTermios(nested, outer); err != nil {
		t.Fatalf("ForwardTermios() error = %v", err)
	}
	if got := system.sizeByFD[nested.Fd()]; got != outerSize {
		t.Fatalf("nested size = %#v, want %#v", got, outerSize)
	}
	if got := system.termiosByFD[outer.Fd()]; !reflect.DeepEqual(got, childMode) {
		t.Fatalf("outer termios = %#v, want %#v", got, childMode)
	}
	wantCalls := []terminalIOCTLCall{
		{fd: outer.Fd(), request: syscall.TIOCGWINSZ},
		{fd: nested.Fd(), request: syscall.TIOCSWINSZ},
		{fd: nested.Fd(), request: syscall.TIOCGETA},
		{fd: outer.Fd(), request: syscall.TIOCSETA},
	}
	if !reflect.DeepEqual(system.calls, wantCalls) {
		t.Fatalf("ioctl calls = %#v, want %#v", system.calls, wantCalls)
	}
}

func TestTerminalRefusesToApplyUnobservedTermiosState(t *testing.T) {
	file := newTestFile(t, "terminal")
	system := &fakeTerminalSystem{
		termiosByFD: map[uintptr]syscall.Termios{},
		sizeByFD:    map[uintptr]WindowSize{},
	}
	err := (darwinTerminal{system: system}).SetTermios(file, TerminalState{})
	if !errors.Is(err, ErrTerminalStateUnobserved) {
		t.Fatalf("SetTermios() error = %v, want ErrTerminalStateUnobserved", err)
	}
	if len(system.calls) != 0 {
		t.Fatalf("SetTermios() ioctl calls = %#v, want none", system.calls)
	}
}

func TestTerminalConcurrentResizeUsesIndependentExactValues(t *testing.T) {
	file := newTestFile(t, "nested")
	system := &fakeTerminalSystem{
		termiosByFD: map[uintptr]syscall.Termios{},
		sizeByFD:    map[uintptr]WindowSize{},
	}
	terminal := darwinTerminal{system: system}
	sizes := []WindowSize{
		{Rows: 24, Cols: 80},
		{Rows: 41, Cols: 113},
		{Rows: 51, Cols: 132},
		{Rows: 60, Cols: 200},
	}

	var wait sync.WaitGroup
	errorsSeen := make(chan error, len(sizes))
	for _, size := range sizes {
		size := size
		wait.Add(1)
		go func() {
			defer wait.Done()
			errorsSeen <- terminal.SetWindowSize(file, size)
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("SetWindowSize() error = %v", err)
		}
	}

	system.mu.Lock()
	defer system.mu.Unlock()
	if got, want := len(system.calls), len(sizes); got != want {
		t.Fatalf("TIOCSWINSZ calls = %d, want %d", got, want)
	}
	for _, call := range system.calls {
		if call.fd != file.Fd() || call.request != syscall.TIOCSWINSZ {
			t.Fatalf("resize call = %#v, want fd %d TIOCSWINSZ", call, file.Fd())
		}
	}
}

func TestTerminalObservationPreservesNonblockingDescriptorMode(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	fd := reader.Fd()
	endpoint, err := NewFileEndpoint(reader)
	if err != nil {
		t.Fatalf("NewFileEndpoint() error = %v", err)
	}
	t.Cleanup(func() {
		if err := endpoint.Restore(); err != nil {
			t.Errorf("Restore(): %v", err)
		}
	})
	system := &fakeTerminalSystem{
		termiosByFD: map[uintptr]syscall.Termios{fd: {Lflag: syscall.ISIG}},
		sizeByFD:    map[uintptr]WindowSize{fd: {Rows: 24, Cols: 80}},
	}

	if _, err := (darwinTerminal{system: system}).Observe(reader); err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	flags, err := fcntl(endpoint.fd, syscall.F_GETFL, 0)
	if err != nil {
		t.Fatalf("read descriptor flags: %v", err)
	}
	if flags&syscall.O_NONBLOCK == 0 {
		t.Fatalf("flags after Observe() = %#x, want O_NONBLOCK preserved", flags)
	}
}

func TestTerminalObserverSeesRealChildRawModeTransition(t *testing.T) {
	child, err := (ExecChildStarter{Opener: NewOpener()}).Start(context.Background(), StartRequest{
		Argv: []string{os.Args[0], "-test.run=^TestPTYXChildHelper$"},
		Env:  []string{"GO_WANT_PTYX_CHILD=raw"},
		Size: WindowSize{Rows: 33, Cols: 101},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { cleanupChild(t, child) })
	_ = readUntilTest(t, child.Master(), "CHILD_READY", 5*time.Second)

	state, err := NewTerminal().Observe(child.Master())
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if !state.Settled() || state.Canonical() || state.Echo() {
		t.Fatalf("child state = canonical:%t echo:%t settled:%t", state.Canonical(), state.Echo(), state.Settled())
	}
	if got, want := state.WindowSize(), (WindowSize{Rows: 33, Cols: 101}); got != want {
		t.Fatalf("WindowSize() = %#v, want %#v", got, want)
	}
}
