//go:build darwin

package attach

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"golang.org/x/sys/unix"
)

func TestRelayTerminalCheckRequiresTwoCopiesOfTheSameTerminalIdentity(t *testing.T) {
	firstPTY, first := openAttachTestTerminal(t)
	defer func() { _ = firstPTY.Close() }()
	defer func() { _ = first.Close() }()
	secondPTY, second := openAttachTestTerminal(t)
	defer func() { _ = secondPTY.Close() }()
	defer func() { _ = second.Close() }()

	if _, err := newRelayTerminalFactory(first, first, os.Stderr).check(); err != nil {
		t.Fatalf("same terminal check error = %v", err)
	}
	if _, err := newRelayTerminalFactory(first, second, os.Stderr).check(); !errors.As(err, new(*TerminalMismatchError)) {
		t.Fatalf("different terminal check error = %T %v, want mismatch", err, err)
	}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = null.Close() }()
	if _, err := newRelayTerminalFactory(null, null, os.Stderr).check(); !errors.As(err, new(*NotTerminalError)) {
		t.Fatalf("non-terminal check error = %T %v, want not-terminal", err, err)
	}
}

func TestRelayTerminalUsesFreshOpenFileDescriptionAndRestoresOnlyItsState(t *testing.T) {
	pair, inherited := openAttachTestTerminal(t)
	defer func() { _ = pair.Close() }()
	defer func() { _ = inherited.Close() }()
	factory := newRelayTerminalFactory(inherited, inherited, os.Stderr)
	check, err := factory.check()
	if err != nil {
		t.Fatal(err)
	}
	before, err := unix.FcntlInt(inherited.Fd(), syscall.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := factory.open(check)
	if err != nil {
		t.Fatal(err)
	}
	during, err := unix.FcntlInt(inherited.Fd(), syscall.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if before != during || during&syscall.O_NONBLOCK != before&syscall.O_NONBLOCK {
		t.Fatalf("inherited flags changed from %#x to %#x", before, during)
	}
	if err := owner.makeRaw(); err != nil {
		t.Fatal(err)
	}
	raw, err := ptyx.NewTerminal().Observe(owner.file)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Canonical() || raw.Echo() {
		t.Fatalf("owned terminal remained cooked: canonical=%t echo=%t", raw.Canonical(), raw.Echo())
	}
	if err := owner.restore(); err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	after, err := unix.FcntlInt(inherited.Fd(), syscall.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("inherited flags after close = %#x, want %#x", after, before)
	}
}

func TestRelayTerminalPreservesAnInheritedNonblockingOpenFileDescription(t *testing.T) {
	pair, observer := openAttachTestTerminal(t)
	defer func() { _ = pair.Close() }()
	defer func() { _ = observer.Close() }()
	observerFD := observer.Fd()
	if err := unix.SetNonblock(int(observerFD), true); err != nil {
		t.Fatal(err)
	}
	clientFD, err := unix.FcntlInt(observerFD, unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		t.Fatal(err)
	}
	client := os.NewFile(uintptr(clientFD), "inherited-nonblocking-terminal")
	defer func() { _ = client.Close() }()
	assertNonblocking := func(stage string) {
		t.Helper()
		flags, err := unix.FcntlInt(observerFD, syscall.F_GETFL, 0)
		if err != nil {
			t.Fatal(err)
		}
		if flags&syscall.O_NONBLOCK == 0 {
			t.Fatalf("%s flags=%#x, want O_NONBLOCK preserved", stage, flags)
		}
	}

	factory := newRelayTerminalFactory(client, client, client)
	check, err := factory.check()
	if err != nil {
		t.Fatal(err)
	}
	assertNonblocking("after check")
	owner, err := factory.open(check)
	if err != nil {
		t.Fatal(err)
	}
	assertNonblocking("while relaying")
	if err := owner.makeRaw(); err != nil {
		t.Fatal(err)
	}
	if err := owner.restore(); err != nil {
		t.Fatal(err)
	}
	if err := owner.close(); err != nil {
		t.Fatal(err)
	}
	assertNonblocking("after close")
}

func TestDiagnosticDuplicateIsFlooredAboveStandardDescriptorsAndReturnsBrokenPipe(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = reader.Close()
	defer func() { _ = writer.Close() }()
	duplicate, err := duplicateDiagnostic(writer)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = duplicate.Close() }()
	if duplicate.Fd() < 3 {
		t.Fatalf("diagnostic fd = %d, want >= 3", duplicate.Fd())
	}
	_, err = (fileDiagnosticSink{file: duplicate}).Attempt(context.Background(), []byte("diagnostic\n"))
	if !errors.Is(err, syscall.EPIPE) {
		t.Fatalf("diagnostic write error = %v, want EPIPE", err)
	}
}

func TestRedirectedDiagnosticDuplicateFailureDoesNotMisclassifyTerminalOpen(t *testing.T) {
	pair, terminal := openAttachTestTerminal(t)
	defer func() { _ = pair.Close() }()
	defer func() { _ = terminal.Close() }()
	redirected, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = redirected.Close() }()
	factory := newRelayTerminalFactory(terminal, terminal, redirected)
	factory.system.dup = func(*os.File) (*os.File, error) { return nil, errors.New("dup failed") }
	check, err := factory.check()
	if err != nil {
		t.Fatal(err)
	}
	owner, err := factory.open(check)
	if err != nil {
		t.Fatalf("terminal open was displaced by diagnostic duplication: %v", err)
	}
	defer func() { _ = owner.close() }()
	if owner.streams().diagnostic != nil {
		t.Fatal("failed diagnostic duplication unexpectedly produced a sink")
	}
}

func openAttachTestTerminal(t *testing.T) (*ptyx.PTY, *os.File) {
	t.Helper()
	pair, err := ptyx.NewOpener().Open(ptyx.WindowSize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(pair.SlaveName(), os.O_RDWR|syscall.O_CLOEXEC, 0)
	if err != nil {
		_ = pair.Close()
		t.Fatal(err)
	}
	return pair, file
}
