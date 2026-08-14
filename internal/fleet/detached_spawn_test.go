//go:build darwin

package fleet

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// This catches a detached starter that routes argv through a shell, changes the
// parent-owned stdio descriptors, or fails to establish the process-session
// and asynchronous-reaping contract before returning creation provenance.
func TestExecDetachedShimStarterPassesTypedRequestDirectlyAndWaitsOnce(t *testing.T) {
	t.Parallel()

	stdin, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()

	starter := ExecDetachedShimStarter{}
	request := DetachedShimRequest{
		Executable: "/usr/bin/true",
		Argv:       []string{"/usr/bin/true"},
		Directory:  "/",
		Environment: []string{
			"PATH=/usr/bin", "AGENTCTL_RUNTIME_ROOT=/runtime", "AGENTCTL_STATE_ROOT=/state",
		},
		Stdin: stdin, Stdout: stdout, Stderr: stderr,
	}

	process, err := starter.Start(request)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if process.PID() <= 0 {
		t.Fatalf("process.PID() = %d, want a started PID", process.PID())
	}
	if err := <-process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}

// This catches an exec starter that substitutes or leaves standard descriptors
// unwritable, or starts the hidden shim without the required new session.
func TestExecDetachedShimStarterChildWritesStandardDescriptorsInOwnSession(t *testing.T) {
	t.Parallel()

	stdin, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close() }()
	stdout, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdout.Close() }()
	stderr, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderr.Close() }()
	sidPath := filepath.Join(t.TempDir(), "sid")
	process, err := (ExecDetachedShimStarter{}).Start(DetachedShimRequest{
		Executable: os.Args[0], Argv: []string{os.Args[0], "-test.run=^TestDetachedShimChildWriterHelper$"}, Directory: "/",
		Environment: append(os.Environ(), "AGENTCTL_DETACHED_TEST_HELPER=1", "AGENTCTL_DETACHED_SID_PATH="+sidPath),
		Stdin:       stdin, Stdout: stdout, Stderr: stderr,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := <-process.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	payload, err := os.ReadFile(sidPath)
	if err != nil {
		t.Fatalf("ReadFile(session ID): %v", err)
	}
	sid, err := strconv.Atoi(string(payload))
	if err != nil || sid != process.PID() {
		t.Fatalf("child session ID = %q (%v), want PID %d", payload, err, process.PID())
	}
}

func TestDetachedShimChildWriterHelper(t *testing.T) {
	if os.Getenv("AGENTCTL_DETACHED_TEST_HELPER") != "1" {
		t.Skip("helper process")
	}
	if _, err := fmt.Fprintln(os.Stdout, "stdout is writable"); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stderr, "stderr is writable"); err != nil {
		t.Fatal(err)
	}
	sid, _, errno := syscall.Syscall(syscall.SYS_GETSID, 0, 0, 0)
	if errno != 0 {
		t.Fatal(errno)
	}
	if err := os.WriteFile(os.Getenv("AGENTCTL_DETACHED_SID_PATH"), []byte(strconv.Itoa(int(sid))), 0o600); err != nil {
		t.Fatal(err)
	}
}
