//go:build darwin

// shim-version is a committed, separately built protocol peer used only by
// release verification. It deliberately speaks protocol version 2 and can
// also emit absent/current controls; it is not shipped in release archives.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

const (
	fixtureVersion = "foreign-protocol-v2"
	foreignVersion = 2
	sessionName    = "skew-matrix"
	roleName       = "planner"
	childPIDsEnv   = "AGENTCTL_SHIM_VERSION_CHILD_PIDS"
	ownedRootEnv   = "AGENTCTL_SHIM_VERSION_OWNED_ROOT"
)

type matrixRow struct {
	actor, peer, versionCase, source, outcome, result string
}

func main() {
	switch filepath.Base(os.Args[0]) {
	case "amq":
		runAMQStub()
	case "claude", "codex":
		runHarnessStub()
	}
	if len(os.Args) < 2 {
		fatalf("usage: shim-version matrix|serve|record|sweep")
	}
	var err error
	switch os.Args[1] {
	case "matrix":
		err = runMatrix(os.Args[2:])
	case "serve":
		err = runForeignServer(os.Args[2:])
	case "version":
		fmt.Println(fixtureVersion)
		return
	case "record":
		err = recordOwnedIdentity(os.Args[2:])
	case "sweep":
		err = sweepOwnedProcesses(os.Args[2:])
	default:
		err = fmt.Errorf("unknown mode %q", os.Args[1])
	}
	if err != nil {
		fatalf("%v", err)
	}
}

func runMatrix(arguments []string) error {
	if len(arguments) != 4 || arguments[0] != "--current-binary" || arguments[2] != "--artifact-dir" {
		return errors.New("matrix requires --current-binary PATH --artifact-dir DIR")
	}
	currentBinary, err := filepath.Abs(arguments[1])
	if err != nil {
		return err
	}
	artifactDir, err := filepath.Abs(arguments[3])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		return err
	}
	currentVersion, err := commandLine(currentBinary, "version")
	if err != nil {
		return fmt.Errorf("read current version: %w", err)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	currentHash, err := fileSHA256(currentBinary)
	if err != nil {
		return err
	}
	fixtureHash, err := fileSHA256(self)
	if err != nil {
		return err
	}

	ownedRoot := os.Getenv(ownedRootEnv)
	if ownedRoot == "" {
		ownedRoot = "/tmp"
	} else if err := os.MkdirAll(ownedRoot, 0o700); err != nil {
		return err
	}
	base, err := os.MkdirTemp(ownedRoot, "agentctl-skew-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(base) }()

	rows := make([]matrixRow, 0, 6)
	for _, versionCase := range []string{"foreign", "absent", "matching"} {
		row, err := currentClientLeg(currentBinary, self, base, versionCase)
		if err != nil {
			return err
		}
		rows = append(rows, row)
	}
	clientRows, err := currentShimLeg(currentBinary, self, base)
	if err != nil {
		return err
	}
	rows = append(rows, clientRows...)
	if err := os.RemoveAll(base); err != nil {
		return err
	}
	if _, err := os.Lstat(base); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("owned matrix root cleanup observation: %v", err)
	}

	var results bytes.Buffer
	results.WriteString("actor\tpeer\tversion_case\tsource\toutcome\tresult\n")
	for _, row := range rows {
		fmt.Fprintf(&results, "%s\t%s\t%s\t%s\t%s\t%s\n", row.actor, row.peer, row.versionCase, row.source, row.outcome, row.result)
	}
	metadata := fmt.Sprintf(
		"current_version=%s\nfixture_version=%s\ncurrent_sha256=%s\nfixture_sha256=%s\nprotocol_current=%d\nprotocol_foreign=%d\nowned_root_cleanup=PASS\n",
		strings.TrimSpace(currentVersion), fixtureVersion, currentHash, fixtureHash, shim.ShimProtocolVersion, foreignVersion,
	)
	if err := os.WriteFile(filepath.Join(artifactDir, "results.tsv"), results.Bytes(), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(artifactDir, "metadata.txt"), []byte(metadata), 0o600); err != nil {
		return err
	}
	fmt.Printf("SHIM VERSION MATRIX PASS (%d observed legs)\n", len(rows))
	return nil
}

func currentClientLeg(currentBinary, fixtureBinary, base, versionCase string) (matrixRow, error) {
	legRoot := filepath.Join(base, "current-client-"+versionCase)
	runtimeRoot := filepath.Join(legRoot, "runtime")
	stateRoot := filepath.Join(legRoot, "state")
	home := filepath.Join(legRoot, "home")
	for _, directory := range []string{runtimeRoot, stateRoot, home} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return matrixRow{}, err
		}
	}
	ready := filepath.Join(legRoot, "ready")
	server := exec.Command(fixtureBinary, "serve", "--case", versionCase, "--ready", ready)
	server.Env = matrixEnvironment(os.Environ(), runtimeRoot, stateRoot, home, "")
	var serverOutput bytes.Buffer
	server.Stdout = &serverOutput
	server.Stderr = &serverOutput
	if err := server.Start(); err != nil {
		return matrixRow{}, err
	}
	if err := waitForPath(ready, 5*time.Second); err != nil {
		_ = server.Process.Kill()
		_ = server.Wait()
		return matrixRow{}, fmt.Errorf("foreign server %s did not become ready: %w: %s", versionCase, err, serverOutput.String())
	}
	client := exec.Command(currentBinary, "clear", "--session", sessionName, roleName)
	client.Env = matrixEnvironment(os.Environ(), runtimeRoot, stateRoot, home, "")
	output, clientErr := client.CombinedOutput()
	serverErr := waitCommand(server, 5*time.Second)
	if serverErr != nil {
		return matrixRow{}, fmt.Errorf("foreign server %s: %w: %s", versionCase, serverErr, serverOutput.String())
	}
	text := string(output)
	if clientErr == nil {
		return matrixRow{}, fmt.Errorf("current client unexpectedly accepted %s hello: %s", versionCase, text)
	}
	peer := "foreign-shim"
	outcome := "protocol-skew"
	if versionCase == "matching" {
		peer = "matching-shim"
		outcome = "next-typed-gate"
		if strings.Contains(text, "protocol-skew") || !strings.Contains(text, "(starting)") {
			return matrixRow{}, fmt.Errorf("matching hello/request did not reach the typed runtime outcome: %s", text)
		}
	} else {
		observed := "was 2"
		if versionCase == "absent" {
			observed = "was absent"
		}
		if !strings.Contains(text, "connected shim hello protocol version "+observed) || !strings.Contains(text, "protocol-skew") {
			return matrixRow{}, fmt.Errorf("%s hello did not fail at version prepass: %s", versionCase, text)
		}
	}
	return matrixRow{"current-client", peer, versionCase, "connected shim hello", outcome, "PASS"}, nil
}

func runForeignServer(arguments []string) error {
	if len(arguments) != 4 || arguments[0] != "--case" || arguments[2] != "--ready" {
		return errors.New("serve requires --case CASE --ready PATH")
	}
	versionCase, ready := arguments[1], arguments[3]
	namespace, err := shim.OpenNamespace()
	if err != nil {
		return err
	}
	defer func() { _ = namespace.Close() }()
	store, err := fleet.OpenShimFleetRecordStore(namespace.StateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	record, err := fleet.NewShimFleetRecord(sessionName, mustWorkingDirectory(), config.FleetConfig{Roles: []config.RoleConfig{{Name: roleName, Harness: config.HarnessClaude}}})
	if err != nil {
		return err
	}
	if err := store.Create(record); err != nil {
		return err
	}
	path, err := namespace.RolePath(sessionName, roleName)
	if err != nil {
		return err
	}
	defer func() { _ = path.Close() }()
	claim, err := shim.AcquireClaim(path, shim.Advisory{Version: shim.ShimProtocolVersion, ShimPID: os.Getpid(), Nonce: "foreign-fixture", StateRoot: namespace.StateRoot})
	if err != nil {
		return err
	}
	defer func() { _ = claim.Close() }()
	if err := shim.WriteRecord(path, shim.NewChildStartingRecord(sessionName, roleName, os.Getpid(), "foreign-fixture")); err != nil {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path.Socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	if err := os.Chmod(path.Socket, 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(ready, []byte("ready\n"), 0o600); err != nil {
		return err
	}
	connection, err := listener.AcceptUnix()
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	var hello []byte
	switch versionCase {
	case "foreign":
		hello = []byte(`{"version":2,"unknown":true}`)
	case "absent":
		hello = []byte(`{"unknown":true}`)
	case "matching":
		hello = []byte(`{"version":1}`)
	default:
		return fmt.Errorf("unknown hello case %q", versionCase)
	}
	if _, err := shim.WriteFrame(connection, hello); err != nil {
		return err
	}
	_ = connection.SetReadDeadline(time.Now().Add(2 * time.Second))
	requestPayload, readErr := shim.ReadFrame(connection)
	if readErr != nil && !isPeerClosure(readErr) {
		return readErr
	}
	if versionCase == "matching" {
		request, err := shim.DecodeRequest(requestPayload)
		if err != nil {
			return fmt.Errorf("matching request: %w", err)
		}
		if request.Session != sessionName || request.Role != roleName || request.Operation != "clear" {
			return fmt.Errorf("matching request identity/operation = %#v", request)
		}
		state := "child-starting"
		shimPID := os.Getpid()
		response, err := shim.EncodeResponse(shim.Response{
			Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStarting, State: &state, ShimPID: &shimPID,
		})
		if err != nil {
			return err
		}
		if _, err := shim.WriteFrame(connection, response); err != nil {
			return err
		}
	}
	return nil
}

func currentShimLeg(currentBinary, fixtureBinary, base string) (rows []matrixRow, returnErr error) {
	legRoot := filepath.Join(base, "current-shim")
	runtimeRoot := filepath.Join(legRoot, "runtime")
	stateRoot := filepath.Join(legRoot, "state")
	home := filepath.Join(legRoot, "home")
	binDir := filepath.Join(legRoot, "bin")
	for _, directory := range []string{runtimeRoot, stateRoot, home, binDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	for _, name := range []string{"amq", "claude", "codex"} {
		if err := os.Link(fixtureBinary, filepath.Join(binDir, name)); err != nil {
			return nil, err
		}
	}
	scriptPath, err := exec.LookPath("script")
	if err != nil {
		return nil, err
	}
	input, keepOpen, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = keepOpen.Close() }()
	command := exec.Command(scriptPath, "-q", "/dev/null", currentBinary, "__shim", "--session", sessionName, "--role", roleName, "--harness", "claude")
	command.Env = matrixEnvironment(os.Environ(), runtimeRoot, stateRoot, home, binDir)
	childPIDs := filepath.Join(legRoot, "child-pids.txt")
	command.Env = append(command.Env, childPIDsEnv+"="+childPIDs)
	command.Stdin = input
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		return nil, err
	}
	socket := filepath.Join(runtimeRoot, sessionName, roleName+".sock")
	lock := filepath.Join(runtimeRoot, sessionName, roleName+".lock")
	reaped := false
	ownedShimPID := 0
	var ownedShimToken *shim.StartToken
	defer func() {
		cleanupErr := cleanupCurrentShim(command, socket, lock, childPIDs, stateRoot, ownedShimPID, ownedShimToken, reaped)
		if cleanupErr != nil {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if err := waitForPath(socket, 8*time.Second); err != nil {
		return nil, fmt.Errorf("current shim did not become ready: %w: %s", err, output.String())
	}
	if err := waitForRunning(socket, 8*time.Second); err != nil {
		return nil, fmt.Errorf("current shim did not report running: %w: %s", err, output.String())
	}
	advisoryPayload, err := os.ReadFile(lock)
	if err != nil {
		return nil, fmt.Errorf("read current shim advisory: %w", err)
	}
	var advisory struct {
		ShimPID int `json:"shim_pid"`
	}
	if err := json.Unmarshal(advisoryPayload, &advisory); err != nil || advisory.ShimPID <= 0 {
		return nil, fmt.Errorf("decode current shim advisory PID: pid=%d error=%v", advisory.ShimPID, err)
	}
	ownedShimPID = advisory.ShimPID
	token, err := shim.ReadStartToken(ownedShimPID)
	if err != nil {
		return nil, fmt.Errorf("read current shim start token: %w", err)
	}
	ownedShimToken = &token

	cases := []struct {
		actor, versionCase, request, wantCause string
	}{
		{"foreign-client", "foreign", `{"version":2,"unknown":true}`, "2"},
		{"absent-client", "absent", `{"unknown":true}`, "absent"},
		{"matching-client", "matching", `{"version":1,"session":"skew-matrix","role":"planner","operation":"observe"}`, ""},
	}
	rows = make([]matrixRow, 0, len(cases))
	for _, testCase := range cases {
		response, err := rawRoundTrip(socket, []byte(testCase.request))
		if err != nil {
			return nil, fmt.Errorf("%s request: %w", testCase.versionCase, err)
		}
		var decoded struct {
			Version int    `json:"version"`
			Outcome string `json:"outcome"`
			Cause   string `json:"cause"`
		}
		if err := json.Unmarshal(response, &decoded); err != nil {
			return nil, err
		}
		outcome := "protocol-skew"
		if testCase.versionCase == "matching" {
			outcome = "next-typed-gate"
			if decoded.Version != shim.ShimProtocolVersion || decoded.Outcome == "protocol-skew" || decoded.Outcome == "protocol-schema-invalid" || decoded.Outcome == "invalid-request" {
				return nil, fmt.Errorf("matching request response = %s", response)
			}
		} else if decoded.Version != shim.ShimProtocolVersion || decoded.Outcome != "protocol-skew" || decoded.Cause != testCase.wantCause {
			return nil, fmt.Errorf("%s request response = %s", testCase.versionCase, response)
		}
		rows = append(rows, matrixRow{testCase.actor, "current-shim", testCase.versionCase, "client request", outcome, "PASS"})
	}
	stop := []byte(`{"version":1,"session":"skew-matrix","role":"planner","operation":"stop"}`)
	if _, err := rawRoundTrip(socket, stop); err != nil {
		return nil, fmt.Errorf("stop current shim: %w", err)
	}
	waitErr := waitCommand(command, 8*time.Second)
	reaped = true
	if waitErr != nil {
		return nil, fmt.Errorf("current shim exit: %w: %s", waitErr, output.String())
	}
	return rows, nil
}

func cleanupCurrentShim(command *exec.Cmd, socket, lock, childPIDs, stateRoot string, shimPID int, shimToken *shim.StartToken, reaped bool) error {
	var cleanupErrors []error
	if shimPID == 0 {
		contents, err := os.ReadFile(lock)
		if err == nil {
			var advisory struct {
				ShimPID int `json:"shim_pid"`
			}
			if err := json.Unmarshal(contents, &advisory); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("decode owned shim advisory: %w", err))
			} else {
				shimPID = advisory.ShimPID
				token, tokenErr := shim.ReadStartToken(shimPID)
				if tokenErr == nil {
					shimToken = &token
				} else if !errors.Is(tokenErr, syscall.ESRCH) {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("read owned shim start token: %w", tokenErr))
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("read owned shim advisory: %w", err))
		}
	}
	if !reaped {
		if _, err := os.Lstat(socket); err == nil {
			stop := []byte(`{"version":1,"session":"skew-matrix","role":"planner","operation":"stop"}`)
			_, _ = rawRoundTrip(socket, stop)
		}
		if err := waitCommand(command, 3*time.Second); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("reap current shim wrapper: %w", err))
		}
	}
	if shimPID == 0 {
		if contents, err := os.ReadFile(lock); err == nil {
			var advisory struct {
				ShimPID int `json:"shim_pid"`
			}
			if err := json.Unmarshal(contents, &advisory); err != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("decode post-reap shim advisory: %w", err))
			} else {
				shimPID = advisory.ShimPID
				if token, tokenErr := shim.ReadStartToken(shimPID); tokenErr == nil {
					shimToken = &token
				} else if !errors.Is(tokenErr, syscall.ESRCH) {
					cleanupErrors = append(cleanupErrors, fmt.Errorf("read post-reap shim start token: %w", tokenErr))
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("read post-reap shim advisory: %w", err))
		}
	}
	contents, err := os.ReadFile(childPIDs)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("read owned child PIDs: %w", err))
	}
	for _, line := range strings.Split(strings.TrimSpace(string(contents)), "\n") {
		if line == "" {
			continue
		}
		var pid int
		var token shim.StartToken
		if _, parseErr := fmt.Sscanf(line, "%d %d %d", &pid, &token.Sec, &token.Usec); parseErr != nil || pid <= 0 {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("invalid owned child identity %q", line))
			continue
		}
		if waitErr := ensureOwnedPIDAbsent("child", pid, token); waitErr != nil {
			cleanupErrors = append(cleanupErrors, waitErr)
		}
	}
	if shimPID > 0 {
		if shimToken == nil {
			if err := syscall.Kill(shimPID, 0); !errors.Is(err, syscall.ESRCH) {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("owned shim PID %d has no recorded start token; refusing signal; kill(pid, 0)=%v", shimPID, err))
			}
		} else if waitErr := ensureOwnedPIDAbsent("shim", shimPID, *shimToken); waitErr != nil {
			cleanupErrors = append(cleanupErrors, waitErr)
		}
	}
	for _, artifact := range []string{
		socket,
		lock,
		filepath.Join(stateRoot, "sessions", sessionName, "roles", roleName+".json"),
	} {
		if _, err := os.Lstat(artifact); !errors.Is(err, os.ErrNotExist) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("owned artifact %q remains: %v", artifact, err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func ensureOwnedPIDAbsent(kind string, pid int, recorded shim.StartToken) error {
	waitErr := waitPIDAbsent(pid, 3*time.Second)
	if waitErr == nil {
		return nil
	}
	observed, tokenErr := shim.ReadStartToken(pid)
	if tokenErr != nil {
		return fmt.Errorf("owned %s PID %d remained but start token could not be observed; refusing signal: initial=%v token=%v", kind, pid, waitErr, tokenErr)
	}
	if !recorded.Equal(observed) {
		return fmt.Errorf("owned %s PID %d was reused; refusing signal: recorded=%#v observed=%#v", kind, pid, recorded, observed)
	}
	signalErr := syscall.Kill(pid, syscall.SIGKILL)
	recheckErr := waitPIDAbsent(pid, time.Second)
	return fmt.Errorf("owned %s PID %d required forced cleanup: initial=%v signal=%v recheck=%v", kind, pid, waitErr, signalErr, recheckErr)
}

func waitPIDAbsent(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil {
			return err
		}
		if !time.Now().Before(deadline) {
			return errors.New("kill(pid, 0) did not return ESRCH")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func recordOwnedIdentity(arguments []string) error {
	if len(arguments) != 4 || arguments[0] != "--pid-file" || arguments[2] != "--journal" {
		return errors.New("record requires --pid-file PATH --journal PATH")
	}
	payload, err := os.ReadFile(arguments[1])
	if err != nil {
		return err
	}
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(string(payload)), "%d", &pid); err != nil || pid <= 0 {
		return fmt.Errorf("invalid owned PID file %q", strings.TrimSpace(string(payload)))
	}
	token, err := shim.ReadStartToken(pid)
	if err != nil {
		return fmt.Errorf("read owned PID %d start token: %w", pid, err)
	}
	journal, err := os.OpenFile(arguments[3], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(journal, "%d %d %d\n", pid, token.Sec, token.Usec); err != nil {
		_ = journal.Close()
		return err
	}
	return journal.Close()
}

func sweepOwnedProcesses(arguments []string) error {
	if len(arguments) != 4 || arguments[0] != "--root" || arguments[2] != "--result" {
		return errors.New("sweep requires --root PATH --result PATH")
	}
	root, resultPath := arguments[1], arguments[3]
	var locks, journals []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		switch {
		case strings.HasSuffix(entry.Name(), ".lock"):
			locks = append(locks, path)
		case entry.Name() == "child-pids.txt", entry.Name() == "owned-child-pids.txt", entry.Name() == "owned-identities.txt":
			journals = append(journals, path)
		}
		return nil
	})
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var cleanupErrors []error
	for _, lock := range locks {
		if err := stopOwnedShim(lock); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	seen := map[string]bool{}
	for _, journal := range journals {
		payload, err := os.ReadFile(journal)
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("read owned identity journal %q: %w", journal, err))
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(payload)), "\n") {
			if line == "" || seen[line] {
				continue
			}
			seen[line] = true
			var pid int
			var token shim.StartToken
			if _, err := fmt.Sscanf(line, "%d %d %d", &pid, &token.Sec, &token.Usec); err != nil || pid <= 0 {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("invalid owned identity %q in %q", line, journal))
				continue
			}
			if err := stopOwnedPID(pid, token); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	err = errors.Join(cleanupErrors...)
	status := "TASK8 OWNED PROCESS SWEEP PASS\n"
	if err != nil {
		status = fmt.Sprintf("TASK8 OWNED PROCESS SWEEP FAIL: %v\n", err)
	}
	if writeErr := os.WriteFile(resultPath, []byte(status), 0o600); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return err
}

func stopOwnedShim(lock string) error {
	payload, err := os.ReadFile(lock)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read owned shim advisory %q: %w", lock, err)
	}
	var advisory shim.Advisory
	if err := json.Unmarshal(payload, &advisory); err != nil || advisory.ShimPID <= 0 {
		return fmt.Errorf("decode owned shim advisory %q: pid=%d error=%v", lock, advisory.ShimPID, err)
	}
	if err := syscall.Kill(advisory.ShimPID, 0); errors.Is(err, syscall.ESRCH) {
		return nil
	} else if err != nil {
		return fmt.Errorf("observe owned shim PID %d: %w", advisory.ShimPID, err)
	}
	socket := strings.TrimSuffix(lock, ".lock") + ".sock"
	address, err := net.ResolveUnixAddr("unix", socket)
	if err != nil {
		return fmt.Errorf("resolve owned shim socket for PID %d: %w", advisory.ShimPID, err)
	}
	connection, err := net.DialUnix("unix", nil, address)
	if err != nil {
		return fmt.Errorf("connect owned shim PID %d without signaling: %w", advisory.ShimPID, err)
	}
	defer func() { _ = connection.Close() }()
	hello, err := shim.ReadFrame(connection)
	if err != nil {
		return fmt.Errorf("read owned shim PID %d hello: %w", advisory.ShimPID, err)
	}
	if err := shim.DecodeHello(hello); err != nil {
		return fmt.Errorf("validate owned shim PID %d hello: %w", advisory.ShimPID, err)
	}
	answerer, err := shim.LocalPeerPID(connection)
	if err != nil || answerer != advisory.ShimPID {
		return fmt.Errorf("owned shim answerer disagreement: advisory=%d answerer=%d error=%v", advisory.ShimPID, answerer, err)
	}
	request, err := shim.EncodeRequest(shim.Request{
		Version:   shim.ShimProtocolVersion,
		Session:   filepath.Base(filepath.Dir(lock)),
		Role:      strings.TrimSuffix(filepath.Base(lock), ".lock"),
		Operation: "stop",
	})
	if err != nil {
		return err
	}
	if _, err := shim.WriteFrame(connection, request); err != nil {
		return fmt.Errorf("request owned shim PID %d stop: %w", advisory.ShimPID, err)
	}
	if _, err := shim.ReadFrame(connection); err != nil {
		return fmt.Errorf("read owned shim PID %d stop response: %w", advisory.ShimPID, err)
	}
	if err := waitPIDAbsent(advisory.ShimPID, 5*time.Second); err != nil {
		return fmt.Errorf("owned shim PID %d did not exit after peer-verified stop: %w", advisory.ShimPID, err)
	}
	return nil
}

func stopOwnedPID(pid int, recorded shim.StartToken) error {
	if err := waitPIDAbsent(pid, 100*time.Millisecond); err == nil {
		return nil
	}
	observed, err := shim.ReadStartToken(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("owned PID %d start token could not be observed; refusing signal: %w", pid, err)
	}
	if !recorded.Equal(observed) {
		return fmt.Errorf("owned PID %d was reused; refusing signal: recorded=%#v observed=%#v", pid, recorded, observed)
	}
	if err := syscall.Kill(pid, syscall.SIGHUP); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal owned PID %d: %w", pid, err)
	}
	if err := waitPIDAbsent(pid, 2*time.Second); err == nil {
		return nil
	}
	observed, err = shim.ReadStartToken(pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return fmt.Errorf("recheck owned PID %d start token; refusing forced signal: %w", pid, err)
	}
	if !recorded.Equal(observed) {
		return fmt.Errorf("owned PID %d changed before forced signal; refusing: recorded=%#v observed=%#v", pid, recorded, observed)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("force owned PID %d cleanup: %w", pid, err)
	}
	if err := waitPIDAbsent(pid, 2*time.Second); err != nil {
		return fmt.Errorf("owned PID %d survived forced cleanup: %w", pid, err)
	}
	return nil
}

func rawRoundTrip(socket string, request []byte) ([]byte, error) {
	connection, err := net.DialTimeout("unix", socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = connection.Close() }()
	hello, err := shim.ReadFrame(connection)
	if err != nil {
		return nil, err
	}
	if err := shim.DecodeHello(hello); err != nil {
		return nil, fmt.Errorf("current hello: %w", err)
	}
	if _, err := shim.WriteFrame(connection, request); err != nil {
		return nil, err
	}
	return shim.ReadFrame(connection)
}

func waitForRunning(socket string, timeout time.Duration) error {
	request := []byte(`{"version":1,"session":"skew-matrix","role":"planner","operation":"observe"}`)
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		response, err := rawRoundTrip(socket, request)
		if err == nil {
			var decoded struct {
				Outcome string `json:"outcome"`
			}
			if decodeErr := json.Unmarshal(response, &decoded); decodeErr != nil {
				return decodeErr
			}
			last = decoded.Outcome
			if decoded.Outcome == "running" {
				return nil
			}
		} else {
			last = err.Error()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out; last observation %q", last)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func runAMQStub() {
	harness := ""
	for _, argument := range os.Args[1:] {
		if argument == "claude" || argument == "codex" {
			harness = argument
			break
		}
	}
	if harness == "" {
		fatalf("amq stub did not receive a harness")
	}
	self, err := os.Executable()
	if err != nil {
		fatalf("%v", err)
	}
	if err := syscall.Exec(self, []string{harness}, os.Environ()); err != nil {
		fatalf("%v", err)
	}
}

func runHarnessStub() {
	if os.Getenv("AGENTCTL_SHIM_VERSION_SURVIVE_STOP") == "1" {
		signal.Ignore(syscall.SIGHUP)
	}
	if path := os.Getenv(childPIDsEnv); path != "" {
		token, err := shim.ReadStartToken(os.Getpid())
		if err != nil {
			fatalf("record child start token: %v", err)
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			fatalf("record child PID: %v", err)
		}
		if _, err := fmt.Fprintf(file, "%d %d %d\n", os.Getpid(), token.Sec, token.Usec); err != nil {
			_ = file.Close()
			fatalf("record child PID: %v", err)
		}
		if err := file.Close(); err != nil {
			fatalf("record child PID: %v", err)
		}
	}
	command := exec.Command("/bin/stty", "-icanon", "-echo")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		fatalf("stty: %v", err)
	}
	if os.Getenv("AGENTCTL_SHIM_VERSION_SURVIVE_STOP") == "1" {
		for {
			time.Sleep(time.Second)
		}
	}
	_, _ = io.Copy(io.Discard, bufio.NewReader(os.Stdin))
	os.Exit(0)
}

func matrixEnvironment(environment []string, runtimeRoot, stateRoot, home, binDir string) []string {
	values := map[string]string{
		"AGENTCTL_RUNTIME_ROOT": runtimeRoot,
		"AGENTCTL_STATE_ROOT":   stateRoot,
		"HOME":                  home,
		"TMUX":                  "",
		"TMUX_PANE":             "",
	}
	if binDir != "" {
		values["PATH"] = binDir + string(os.PathListSeparator) + os.Getenv("PATH")
	}
	result := append([]string(nil), environment...)
	for name, value := range values {
		prefix := name + "="
		filtered := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, prefix+value)
	}
	return result
}

func waitForPath(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Lstat(path); err == nil {
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitCommand(command *exec.Cmd, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = command.Process.Kill()
		<-done
		return errors.New("process timed out")
	}
}

func commandLine(name string, arguments ...string) (string, error) {
	output, err := exec.Command(name, arguments...).CombinedOutput()
	return string(output), err
}

func fileSHA256(path string) (string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(contents)
	return fmt.Sprintf("%x", sum), nil
}

func isPeerClosure(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || strings.Contains(err.Error(), "connection reset")
}

func mustWorkingDirectory() string {
	directory, err := os.Getwd()
	if err != nil {
		fatalf("%v", err)
	}
	return directory
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, "shim-version: "+format+"\n", arguments...)
	os.Exit(1)
}
