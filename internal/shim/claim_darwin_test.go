//go:build darwin

package shim

import (
	"bufio"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestClaimUsesKernelFlockAndPreservesContendedSocket(t *testing.T) {
	rolePath := newTestRolePath(t)
	command := startClaimHolder(t, rolePath, false)
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: rolePath.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()+1))
	var contended *ClaimContendedError
	if !errors.As(err, &contended) {
		t.Fatalf("second AcquireClaim error = %T %v, want *ClaimContendedError", err, err)
	}
	if _, err := os.Lstat(rolePath.Socket); err != nil {
		t.Fatalf("contender removed socket before ownership: %v", err)
	}
}

func TestClaimAcquisitionRemovesOnlyAStaleSocketAfterOwnership(t *testing.T) {
	rolePath := newTestRolePath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: rolePath.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}

	claim, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })
	if _, err := os.Lstat(rolePath.Socket); !os.IsNotExist(err) {
		t.Fatalf("stale socket remains after claim: %v", err)
	}
}

func TestClaimRefusesUnsafeSocketAndLockArtifactsWithoutRepair(t *testing.T) {
	t.Run("regular socket path", func(t *testing.T) {
		rolePath := newTestRolePath(t)
		if err := os.WriteFile(rolePath.Socket, []byte("not a socket"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
		if err == nil {
			t.Fatal("AcquireClaim accepted regular file at socket path")
		}
		if data, readErr := os.ReadFile(rolePath.Socket); readErr != nil || string(data) != "not a socket" {
			t.Fatalf("unsafe socket artifact was repaired: data=%q error=%v", data, readErr)
		}
	})

	t.Run("symlink lock path", func(t *testing.T) {
		rolePath := newTestRolePath(t)
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("sentinel"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, rolePath.Lock); err != nil {
			t.Fatal(err)
		}
		_, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
		if err == nil {
			t.Fatal("AcquireClaim followed lock symlink")
		}
		if data, readErr := os.ReadFile(target); readErr != nil || string(data) != "sentinel" {
			t.Fatalf("symlink target mutated: data=%q error=%v", data, readErr)
		}
	})
}

func TestClaimWritesAdvisoryIdentityAndDetectsStateRootDisagreement(t *testing.T) {
	rolePath := newTestRolePath(t)
	want := testAdvisory(rolePath, os.Getpid())
	claim, err := AcquireClaim(rolePath, want)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = claim.Close() })

	got, err := ReadAdvisory(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("advisory = %#v, want %#v", got, want)
	}
	if err := got.CompareStateRoot(rolePath.StateRoot); err != nil {
		t.Fatalf("matching root: %v", err)
	}
	err = got.CompareStateRoot(rolePath.StateRoot + "-different")
	var disagreement *StateRootDisagreementError
	if !errors.As(err, &disagreement) {
		t.Fatalf("different root error = %T %v, want *StateRootDisagreementError", err, err)
	}
	if disagreement.RecordedRoot != rolePath.StateRoot || disagreement.LocalRoot != rolePath.StateRoot+"-different" {
		t.Fatalf("disagreement = %#v", disagreement)
	}
	info, err := os.Stat(rolePath.Lock)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("lock mode = %04o, want %04o", got, want)
	}
}

func TestClaimReadAdvisoryRefusesRuntimeDescriptorSubstitution(t *testing.T) {
	rolePath := newTestRolePath(t)
	claim, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = claim.Close() }()

	runtimeSession := filepath.Dir(rolePath.Lock)
	original := runtimeSession + "-original"
	if err := os.Rename(runtimeSession, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(runtimeSession, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := `{"version":1,"shim_pid":999,"nonce":"replacement","state_root":"` + rolePath.StateRoot + `"}`
	if err := os.WriteFile(rolePath.Lock, []byte(fake), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = ReadAdvisory(rolePath)
	var substituted *RootSubstitutedError
	if !errors.As(err, &substituted) {
		t.Fatalf("ReadAdvisory error = %T %v, want *RootSubstitutedError", err, err)
	}
}

func TestClaimReadAdvisoryRejectsDuplicateIdentityFields(t *testing.T) {
	rolePath := newTestRolePath(t)
	payload := `{"version":1,"shim_pid":101,"shim_pid":102,"nonce":"first","nonce":"second","state_root":"` + rolePath.StateRoot + `","state_root":"` + rolePath.StateRoot + `"}`
	if err := os.WriteFile(rolePath.Lock, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAdvisory(rolePath); err == nil {
		t.Fatal("ReadAdvisory accepted duplicate identity fields")
	}
}

func TestClaimAdvisoryComparisonDetectsKernelAnswererDisagreement(t *testing.T) {
	advisory := Advisory{Version: 1, ShimPID: 501, Nonce: "nonce", StateRoot: "/tmp/state"}
	if err := advisory.CompareAnswerer(501); err != nil {
		t.Fatalf("matching answerer: %v", err)
	}
	err := advisory.CompareAnswerer(502)
	var disagreement *AnswererDisagreementError
	if !errors.As(err, &disagreement) {
		t.Fatalf("different answerer error = %T %v, want *AnswererDisagreementError", err, err)
	}
	if disagreement.RecordedPID != 501 || disagreement.AnswererPID != 502 {
		t.Fatalf("disagreement = %#v", disagreement)
	}
}

// TestClaimSIGKILLReleasesKernelFlock is a live Darwin kernel probe. The child
// cannot run cleanup after SIGKILL, so successful reacquisition proves the
// kernel released the held flock independently of lockfile/socket residue.
func TestClaimSIGKILLReleasesKernelFlock(t *testing.T) {
	rolePath := newTestRolePath(t)
	command := startClaimHolder(t, rolePath, true)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILL helper exited successfully")
	}

	claim, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
	if err != nil {
		t.Fatalf("reacquire after SIGKILL: %v", err)
	}
	_ = claim.Close()
}

func startClaimHolder(t *testing.T, rolePath *RolePath, bindSocket bool) *exec.Cmd {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestClaimHolderHelper$")
	command.Env = append(os.Environ(),
		"AGENTCTL_CLAIM_HELPER=1",
		"AGENTCTL_RUNTIME_ROOT="+rolePath.RuntimeRoot,
		"AGENTCTL_STATE_ROOT="+rolePath.StateRoot,
	)
	if bindSocket {
		command.Env = append(command.Env, "AGENTCTL_CLAIM_HELPER_SOCKET=1")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "claim-held" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("helper readiness = %q, error = %v", scanner.Text(), scanner.Err())
	}
	return command
}

func TestClaimHolderHelper(t *testing.T) {
	if os.Getenv("AGENTCTL_CLAIM_HELPER") != "1" {
		t.Skip("helper process")
	}
	namespace, err := OpenNamespace()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	rolePath, err := namespace.RolePath("session", "role")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rolePath.Close() }()
	claim, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = claim.Close() }()
	if os.Getenv("AGENTCTL_CLAIM_HELPER_SOCKET") == "1" {
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: rolePath.Socket, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		listener.SetUnlinkOnClose(false)
		defer func() { _ = listener.Close() }()
	}
	println("claim-held")
	select {}
}

// TestLocalPeerPIDObservesConnectedAnswerer is a live Darwin kernel probe for
// SOL_LOCAL/LOCAL_PEERPID on the client's connected Unix socket descriptor.
// The answerer runs in a separate process so reversing the endpoint fails.
func TestLocalPeerPIDObservesConnectedAnswerer(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "peer.sock")
	command := exec.Command(os.Args[0], "-test.run=^TestLocalPeerPIDAnswererHelper$")
	command.Env = append(os.Environ(), "AGENTCTL_PEERPID_HELPER="+path)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "answerer-ready" {
		t.Fatalf("answerer readiness = %q, error = %v", scanner.Text(), scanner.Err())
	}
	client, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	pid, err := LocalPeerPID(client)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := pid, command.Process.Pid; got != want {
		t.Fatalf("LOCAL_PEERPID answerer = %d, want child server PID %d", got, want)
	}
}

func TestLocalPeerPIDAnswererHelper(t *testing.T) {
	path := os.Getenv("AGENTCTL_PEERPID_HELPER")
	if path == "" {
		t.Skip("helper process")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	println("answerer-ready")
	connection, err := listener.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = connection.Close() }()
	select {}
}

func newTestRolePath(t *testing.T) *RolePath {
	t.Helper()
	parent := shortTempDir(t)
	namespace, err := openNamespaceRoots(namespaceRoots{
		Runtime: filepath.Join(parent, "runtime"),
		State:   filepath.Join(parent, "state"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = namespace.Close() })
	rolePath, err := namespace.RolePath("session", "role")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rolePath.Close() })
	return rolePath
}

func testAdvisory(rolePath *RolePath, pid int) Advisory {
	return Advisory{
		Version:   ShimProtocolVersion,
		ShimPID:   pid,
		Nonce:     "nonce-" + strconv.Itoa(pid),
		StateRoot: rolePath.StateRoot,
	}
}
