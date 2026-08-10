package ptyx

import (
	"errors"
	"os"
	"reflect"
	"syscall"
	"testing"
	"unsafe"
)

type openCall struct {
	path string
	flag int
	perm os.FileMode
}

type ioctlCall struct {
	fd      uintptr
	request uintptr
}

type fakePTYSystem struct {
	files      []*os.File
	openCalls  []openCall
	ioctlCalls []ioctlCall
	failOpen   int
	failIOCTL  uintptr
	failure    error
}

func (f *fakePTYSystem) openFile(path string, flag int, perm os.FileMode) (*os.File, error) {
	f.openCalls = append(f.openCalls, openCall{path: path, flag: flag, perm: perm})
	if f.failOpen == len(f.openCalls) {
		return nil, f.failure
	}
	file := f.files[0]
	f.files = f.files[1:]
	return file, nil
}

func (f *fakePTYSystem) ioctl(fd, request uintptr, argument unsafe.Pointer) error {
	f.ioctlCalls = append(f.ioctlCalls, ioctlCall{fd: fd, request: request})
	if f.failIOCTL == request {
		return f.failure
	}
	if request == syscall.TIOCPTYGNAME {
		name := (*[128]byte)(argument)
		copy(name[:], "/dev/ttys999")
	}
	return nil
}

func TestDarwinOpenerUsesTheProvenSetupOrder(t *testing.T) {
	master := newTestFile(t, "master")
	slave := newTestFile(t, "slave")
	system := &fakePTYSystem{files: []*os.File{master, slave}}

	pair, err := (darwinOpener{system: system}).Open(WindowSize{Rows: 41, Cols: 113})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = pair.Close() })

	wantOpenCalls := []openCall{
		{path: "/dev/ptmx", flag: os.O_RDWR | syscall.O_NOCTTY | syscall.O_NONBLOCK},
		{path: "/dev/ttys999", flag: os.O_RDWR | syscall.O_NOCTTY},
	}
	if !reflect.DeepEqual(system.openCalls, wantOpenCalls) {
		t.Fatalf("open calls = %#v, want %#v", system.openCalls, wantOpenCalls)
	}
	wantIOCTLCalls := []ioctlCall{
		{fd: master.Fd(), request: syscall.TIOCPTYGRANT},
		{fd: master.Fd(), request: syscall.TIOCPTYUNLK},
		{fd: master.Fd(), request: syscall.TIOCPTYGNAME},
		{fd: master.Fd(), request: syscall.TIOCSWINSZ},
	}
	if !reflect.DeepEqual(system.ioctlCalls, wantIOCTLCalls) {
		t.Fatalf("ioctl calls = %#v, want %#v", system.ioctlCalls, wantIOCTLCalls)
	}
	if pair.Master() != master {
		t.Fatalf("Master() = %p, want %p", pair.Master(), master)
	}
	if got, want := pair.SlaveName(), "/dev/ttys999"; got != want {
		t.Fatalf("SlaveName() = %q, want %q", got, want)
	}
}

func TestDarwinOpenerClosesOnlyResourcesItOpenedAtEachFailure(t *testing.T) {
	wantFailure := errors.New("injected setup failure")
	tests := []struct {
		name             string
		failOpen         int
		failIOCTL        uintptr
		wantMasterClosed bool
		wantSlaveClosed  bool
	}{
		{name: "master open", failOpen: 1},
		{name: "grant", failIOCTL: syscall.TIOCPTYGRANT, wantMasterClosed: true},
		{name: "unlock", failIOCTL: syscall.TIOCPTYUNLK, wantMasterClosed: true},
		{name: "name", failIOCTL: syscall.TIOCPTYGNAME, wantMasterClosed: true},
		{name: "slave open", failOpen: 2, wantMasterClosed: true},
		{name: "window size", failIOCTL: syscall.TIOCSWINSZ, wantMasterClosed: true, wantSlaveClosed: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			master := newTestFile(t, "master")
			slave := newTestFile(t, "slave")
			system := &fakePTYSystem{
				files:     []*os.File{master, slave},
				failOpen:  test.failOpen,
				failIOCTL: test.failIOCTL,
				failure:   wantFailure,
			}

			pair, err := (darwinOpener{system: system}).Open(WindowSize{Rows: 24, Cols: 80})
			if pair != nil {
				t.Fatalf("Open() pair = %#v, want nil", pair)
			}
			if !errors.Is(err, wantFailure) {
				t.Fatalf("Open() error = %v, want injected failure", err)
			}
			assertClosed(t, master, test.wantMasterClosed)
			assertClosed(t, slave, test.wantSlaveClosed)
		})
	}
}

func newTestFile(t *testing.T, name string) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), name)
	if err != nil {
		t.Fatalf("CreateTemp(%q): %v", name, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func assertClosed(t *testing.T, file *os.File, want bool) {
	t.Helper()
	_, err := file.Stat()
	got := errors.Is(err, os.ErrClosed)
	if got != want {
		t.Fatalf("file %q closed = %t (Stat error %v), want %t", file.Name(), got, err, want)
	}
}
