//go:build darwin

package shim

import (
	"bufio"
	"encoding/json"
	"errors"
	"math"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func TestClaimCloseAndRemoveDeletesOnlyItsVerifiedLockAndSocket(t *testing.T) {
	path := newTestRolePath(t)
	claim, err := AcquireClaim(path, testAdvisory(path, os.Getpid()))
	if err != nil {
		t.Fatalf("AcquireClaim() error = %v", err)
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path.Socket, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix() error = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	if err := claim.CloseAndRemove(); err != nil {
		t.Fatalf("CloseAndRemove() error = %v", err)
	}
	for _, artifact := range []string{path.Lock, path.Socket} {
		if _, err := os.Lstat(artifact); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Lstat(%q) error = %v, want os.ErrNotExist", artifact, err)
		}
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

func TestClaimVerifiesDurableRoleRootBeforeClaimMutation(t *testing.T) {
	rolePath := newTestRolePath(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: rolePath.Socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	stateRoles := filepath.Dir(rolePath.Record)
	if err := os.Rename(stateRoles, stateRoles+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stateRoles, 0o700); err != nil {
		t.Fatal(err)
	}
	claim, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
	if claim != nil {
		_ = claim.Close()
	}
	var substituted *RootSubstitutedError
	if !errors.As(err, &substituted) {
		t.Fatalf("AcquireClaim error = %T %v, want durable *RootSubstitutedError", err, err)
	}
	if _, statErr := os.Lstat(rolePath.Lock); !os.IsNotExist(statErr) {
		t.Fatalf("claim lock mutated before durable-root refusal: %v", statErr)
	}
	if _, statErr := os.Lstat(rolePath.Socket); statErr != nil {
		t.Fatalf("stale socket mutated before durable-root refusal: %v", statErr)
	}
}

func TestClaimObservationFailureIsNotReportedAsSubstitution(t *testing.T) {
	rolePath := newTestRolePath(t)
	if err := rolePath.stateRoles.Close(); err != nil {
		t.Fatal(err)
	}
	claim, err := AcquireClaim(rolePath, testAdvisory(rolePath, os.Getpid()))
	if claim != nil {
		_ = claim.Close()
	}
	if err == nil {
		t.Fatal("AcquireClaim succeeded after durable descriptor observation failed")
	}
	var substituted *RootSubstitutedError
	if errors.As(err, &substituted) {
		t.Fatalf("descriptor observation error was reported as substitution: %v", err)
	}
	var observation *FilesystemObservationError
	if !errors.As(err, &observation) {
		t.Fatalf("error = %T %v, want *FilesystemObservationError", err, err)
	}
	if _, statErr := os.Lstat(rolePath.Lock); !os.IsNotExist(statErr) {
		t.Fatalf("claim mutation preceded observation refusal: %v", statErr)
	}
}

func TestClaimRejectsPIDOutsideDarwinPIDTBeforeMutation(t *testing.T) {
	rolePath := newTestRolePath(t)
	advisory := testAdvisory(rolePath, int(math.MaxInt32)+1)
	if claim, err := AcquireClaim(rolePath, advisory); err == nil {
		_ = claim.Close()
		t.Fatal("AcquireClaim accepted shim PID above signed Darwin pid_t")
	}
	if _, err := os.Lstat(rolePath.Lock); !os.IsNotExist(err) {
		t.Fatalf("oversized PID mutated lock path: %v", err)
	}
	if payload, err := json.Marshal(advisory); err != nil {
		t.Fatal(err)
	} else if err := os.WriteFile(rolePath.Lock, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadAdvisory(rolePath); err == nil {
		t.Fatal("ReadAdvisory accepted shim PID above signed Darwin pid_t")
	}
}

func TestClaimWriterEnforcesAdvisoryLimitBeforeClaimMutation(t *testing.T) {
	for _, size := range []int{advisoryMaxBytes, advisoryMaxBytes + 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			rolePath := newTestRolePath(t)
			advisory := advisoryWithEncodedSize(t, rolePath, size)
			claim, err := AcquireClaim(rolePath, advisory)
			if size == advisoryMaxBytes {
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = claim.Close() }()
				info, statErr := os.Stat(rolePath.Lock)
				if statErr != nil {
					t.Fatal(statErr)
				}
				if info.Size() != int64(size) {
					t.Fatalf("advisory size = %d, want %d", info.Size(), size)
				}
				return
			}
			if err == nil {
				_ = claim.Close()
				t.Fatal("AcquireClaim accepted oversized escaped advisory")
			}
			if _, statErr := os.Lstat(rolePath.Lock); !os.IsNotExist(statErr) {
				t.Fatalf("oversized advisory mutated lock path: %v", statErr)
			}
		})
	}
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
	} else {
		var parse *AdvisoryParseError
		if !errors.As(err, &parse) {
			t.Fatalf("error = %T %v, want *AdvisoryParseError", err, err)
		}
		var schema *ProtocolSchemaError
		if errors.As(err, &schema) {
			t.Fatalf("advisory parse leaked ProtocolSchemaError: %v", err)
		}
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

func advisoryWithEncodedSize(t *testing.T, rolePath *RolePath, size int) Advisory {
	t.Helper()
	advisory := testAdvisory(rolePath, os.Getpid())
	advisory.Nonce = `"\`
	payload, err := json.Marshal(advisory)
	if err != nil {
		t.Fatal(err)
	}
	filler := size - (len(payload) + 1)
	if filler < 0 {
		t.Fatalf("advisory base exceeds requested %d-byte size", size)
	}
	advisory.Nonce += strings.Repeat("n", filler)
	payload, err = json.Marshal(advisory)
	if err != nil || len(payload)+1 != size {
		t.Fatalf("constructed advisory size = %d, error = %v, want %d", len(payload)+1, err, size)
	}
	return advisory
}
