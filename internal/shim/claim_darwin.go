//go:build darwin

package shim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

const advisoryMaxBytes = 4096

// Advisory is the lockfile's same-user-editable identity record. The held
// flock, not this body, owns the role.
type Advisory struct {
	Version   int    `json:"version"`
	ShimPID   int    `json:"shim_pid"`
	Nonce     string `json:"nonce"`
	StateRoot string `json:"state_root"`
}

// AdvisoryParseError identifies malformed lockfile identity at the advisory
// boundary without exposing it as a wire-protocol schema fact.
type AdvisoryParseError struct {
	Cause string
}

func (e *AdvisoryParseError) Error() string { return "could not parse advisory lockfile: " + e.Cause }

func advisoryParseError(err error) error {
	if err == nil {
		return nil
	}
	return &AdvisoryParseError{Cause: err.Error()}
}

// StateRootDisagreementError preserves both independently resolved roots.
type StateRootDisagreementError struct {
	LocalRoot    string
	RecordedRoot string
}

// AnswererDisagreementError compares advisory metadata with the kernel's
// LOCAL_PEERPID fact; it does not describe the advisory PID as kernel identity.
type AnswererDisagreementError struct {
	RecordedPID int
	AnswererPID int
}

func (e *AnswererDisagreementError) Error() string {
	return fmt.Sprintf("lockfile shim PID %d differs from connected LOCAL_PEERPID %d", e.RecordedPID, e.AnswererPID)
}

// CompareAnswerer detects advisory-record/kernel-answerer disagreement.
func (a Advisory) CompareAnswerer(answererPID int) error {
	if a.ShimPID == answererPID {
		return nil
	}
	return &AnswererDisagreementError{RecordedPID: a.ShimPID, AnswererPID: answererPID}
}

func (e *StateRootDisagreementError) Error() string {
	return fmt.Sprintf("resolved state root %q differs from lockfile-recorded state root %q", e.LocalRoot, e.RecordedRoot)
}

// CompareStateRoot compares advisory metadata with an independently resolved
// state root before any durable-tree enumeration.
func (a Advisory) CompareStateRoot(local string) error {
	if local == a.StateRoot {
		return nil
	}
	return &StateRootDisagreementError{LocalRoot: local, RecordedRoot: a.StateRoot}
}

// ClaimContendedError reports kernel-arbitrated flock contention.
type ClaimContendedError struct {
	Path string
}

func (e *ClaimContendedError) Error() string {
	return fmt.Sprintf("flock on %q returned EWOULDBLOCK", e.Path)
}

// Claim holds the exclusive role flock until Close.
type Claim struct {
	path *RolePath
	file *os.File
	mu   sync.Mutex
}

// AcquireClaim acquires the sole ownership primitive, writes the advisory
// body, then removes a stale socket. No socket mutation occurs before flock.
func AcquireClaim(path *RolePath, advisory Advisory) (*Claim, error) {
	payload, err := marshalAdvisory(advisory, path.StateRoot)
	if err != nil {
		return nil, err
	}
	path.mu.Lock()
	defer path.mu.Unlock()
	if path.runtimeSession == nil {
		return nil, errors.New("role path is closed")
	}
	if err := verifyRetainedRoot("runtime-session", filepath.Dir(path.Lock), path.runtimeSession); err != nil {
		return nil, err
	}
	if path.stateRoles == nil {
		return nil, errors.New("role path is closed")
	}
	if err := verifyRetainedRoot("state-roles", filepath.Dir(path.Record), path.stateRoles); err != nil {
		return nil, err
	}
	lockName := path.Role + ".lock"
	file, err := path.runtimeSession.OpenFile(lockName, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open role lock: %w", err)
	}
	if err := verifyPrivateArtifact(file, path.Lock, "lockfile"); err != nil {
		_ = file.Close()
		return nil, err
	}
	pathInfo, err := path.runtimeSession.Lstat(lockName)
	fileInfo, statErr := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, &FilesystemObservationError{Kind: "lockfile", Path: path.Lock, Operation: "lstat declared path", Err: err}
	}
	if statErr != nil {
		_ = file.Close()
		return nil, &FilesystemObservationError{Kind: "lockfile", Path: path.Lock, Operation: "stat retained file", Err: statErr}
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, fileInfo) {
		_ = file.Close()
		return nil, fmt.Errorf("lockfile %q was substituted", path.Lock)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, &ClaimContendedError{Path: path.Lock}
		}
		return nil, fmt.Errorf("flock role lock: %w", err)
	}
	claim := &Claim{path: path, file: file}
	if err := writeAdvisory(file, payload); err != nil {
		_ = claim.Close()
		return nil, err
	}
	if err := removeStaleSocket(path); err != nil {
		_ = claim.Close()
		return nil, err
	}
	return claim, nil
}

func validateAdvisory(advisory Advisory, stateRoot string) error {
	if advisory.Version != ShimProtocolVersion {
		return fmt.Errorf("advisory protocol version is %d; expected %d", advisory.Version, ShimProtocolVersion)
	}
	if !validDarwinPID(advisory.ShimPID) {
		return errors.New("advisory shim PID must be a positive signed Darwin pid_t")
	}
	if advisory.Nonce == "" {
		return errors.New("advisory nonce must not be empty")
	}
	if err := validateRootInput("state", advisory.StateRoot); err != nil {
		return err
	}
	if advisory.StateRoot != stateRoot {
		return &StateRootDisagreementError{LocalRoot: stateRoot, RecordedRoot: advisory.StateRoot}
	}
	return nil
}

func marshalAdvisory(advisory Advisory, stateRoot string) ([]byte, error) {
	if err := validateAdvisory(advisory, stateRoot); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(advisory)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if len(payload) > advisoryMaxBytes {
		return nil, fmt.Errorf("advisory lockfile exceeds %d bytes", advisoryMaxBytes)
	}
	return payload, nil
}

func writeAdvisory(file *os.File, payload []byte) error {
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("truncate advisory lockfile: %w", err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek advisory lockfile: %w", err)
	}
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write advisory lockfile: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync advisory lockfile: %w", err)
	}
	return nil
}

func removeStaleSocket(path *RolePath) error {
	name := path.Role + ".sock"
	info, err := path.runtimeSession.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect stale socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing non-socket artifact at %q", path.Socket)
	}
	if err := path.runtimeSession.Remove(name); err != nil {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	return nil
}

// ReadAdvisory reads one bounded, strict advisory lockfile body through the
// retained runtime-session descriptor. The result is metadata only and never
// establishes ownership.
func ReadAdvisory(path *RolePath) (Advisory, error) {
	path.mu.Lock()
	defer path.mu.Unlock()
	if path.runtimeSession == nil {
		return Advisory{}, errors.New("role path is closed")
	}
	if err := verifyRetainedRoot("runtime-session", filepath.Dir(path.Lock), path.runtimeSession); err != nil {
		return Advisory{}, err
	}
	name := path.Role + ".lock"
	file, err := path.runtimeSession.Open(name)
	if err != nil {
		return Advisory{}, err
	}
	defer func() { _ = file.Close() }()
	if err := verifyPrivateArtifact(file, path.Lock, "lockfile"); err != nil {
		return Advisory{}, err
	}
	pathInfo, err := path.runtimeSession.Lstat(name)
	fileInfo, statErr := file.Stat()
	if err != nil {
		return Advisory{}, &FilesystemObservationError{Kind: "lockfile", Path: path.Lock, Operation: "lstat declared path", Err: err}
	}
	if statErr != nil {
		return Advisory{}, &FilesystemObservationError{Kind: "lockfile", Path: path.Lock, Operation: "stat retained file", Err: statErr}
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, fileInfo) {
		return Advisory{}, fmt.Errorf("lockfile %q was substituted", path.Lock)
	}
	payload, err := io.ReadAll(io.LimitReader(file, advisoryMaxBytes+1))
	if err != nil {
		return Advisory{}, err
	}
	if len(payload) > advisoryMaxBytes {
		return Advisory{}, errors.New("advisory lockfile exceeds 4096 bytes")
	}
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return Advisory{}, advisoryParseError(err)
	}
	allowed := map[string]bool{"version": true, "shim_pid": true, "nonce": true, "state_root": true}
	if err := requireFields(fields, allowed, []string{"version", "shim_pid", "nonce", "state_root"}); err != nil {
		return Advisory{}, advisoryParseError(err)
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "shim_pid": "integer", "nonce": "string", "state_root": "string",
	}); err != nil {
		return Advisory{}, advisoryParseError(err)
	}
	if err := requireJSONIntegerRange(fields, "version", big.NewInt(ShimProtocolVersion), big.NewInt(ShimProtocolVersion)); err != nil {
		return Advisory{}, advisoryParseError(err)
	}
	if err := requireJSONIntegerRange(fields, "shim_pid", big.NewInt(1), big.NewInt(darwinPIDMax)); err != nil {
		return Advisory{}, advisoryParseError(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var advisory Advisory
	if err := decoder.Decode(&advisory); err != nil {
		return Advisory{}, advisoryParseError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Advisory{}, advisoryParseError(errors.New("advisory lockfile has trailing JSON"))
	}
	if err := validateAdvisory(advisory, advisory.StateRoot); err != nil {
		return Advisory{}, err
	}
	return advisory, nil
}

// Close releases the role claim. Closing an unrelated descriptor cannot
// release this flock; only this held open file description does.
func (c *Claim) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

// LocalPeerPID returns Darwin's kernel-observed answerer identity for a
// connected Unix socket.
func LocalPeerPID(connection *net.UnixConn) (int, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return 0, err
	}
	var pid int
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		pid, socketErr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil {
		return 0, err
	}
	if socketErr != nil {
		return 0, socketErr
	}
	if !validDarwinPID(pid) {
		return 0, ErrInvalidProcessPID
	}
	return pid, nil
}
