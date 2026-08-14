//go:build darwin

package fleet

import (
	"os"
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
