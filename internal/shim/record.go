package shim

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/mnbf9rca/agentctl/internal/config"
)

const recordMaxBytes = 64 * 1024

// StartToken is the raw Darwin kinfo_proc p_starttime timeval. It is not a
// formatted timestamp and must be compared component-for-component.
type StartToken struct {
	Sec  int64 `json:"sec"`
	Usec int64 `json:"usec"`
}

// Equal compares the raw timeval identity without wall-clock conversion.
func (t StartToken) Equal(other StartToken) bool { return t == other }

func (t StartToken) validate() error {
	if t.Sec <= 0 || t.Usec < 0 || t.Usec >= 1_000_000 {
		return fmt.Errorf("raw start token is malformed: sec=%d usec=%d", t.Sec, t.Usec)
	}
	return nil
}

// RecordState is the durable reservation/child identity phase.
type RecordState string

const (
	RecordStateChildStarting RecordState = "child-starting"
	RecordStateChildRecorded RecordState = "child-recorded"
)

// Record is durable identity evidence, never a liveness observation.
type Record struct {
	Version         int         `json:"version"`
	State           RecordState `json:"state"`
	Session         string      `json:"session"`
	Role            string      `json:"role"`
	ShimPID         int         `json:"shim_pid"`
	Nonce           string      `json:"nonce"`
	ChildPID        int         `json:"child_pid,omitempty"`
	ChildStartToken *StartToken `json:"child_start_token,omitempty"`
}

// NewChildStartingRecord constructs the reservation persisted before child
// start. Validation remains mandatory at WriteRecord.
func NewChildStartingRecord(session, role string, shimPID int, nonce string) Record {
	return Record{
		Version: ShimProtocolVersion,
		State:   RecordStateChildStarting,
		Session: session,
		Role:    role,
		ShimPID: shimPID,
		Nonce:   nonce,
	}
}

// WithChild upgrades a reservation after a successful start and raw token
// observation.
func (r Record) WithChild(pid int, token StartToken) (Record, error) {
	if err := validateRecord(r); err != nil {
		return Record{}, err
	}
	if r.State != RecordStateChildStarting {
		return Record{}, errors.New("only child-starting may be upgraded")
	}
	if pid <= 0 {
		return Record{}, errors.New("child PID must be positive")
	}
	if err := token.validate(); err != nil {
		return Record{}, err
	}
	r.State = RecordStateChildRecorded
	r.ChildPID = pid
	r.ChildStartToken = &token
	return r, nil
}

func validateRecord(record Record) error {
	if record.Version != ShimProtocolVersion {
		return fmt.Errorf("record protocol version is %d; expected %d", record.Version, ShimProtocolVersion)
	}
	if err := config.ValidateSessionName(record.Session); err != nil {
		return err
	}
	if err := config.ValidateRoleName(record.Role); err != nil {
		return err
	}
	if record.ShimPID <= 0 {
		return errors.New("record shim PID must be positive")
	}
	if record.Nonce == "" {
		return errors.New("record nonce must not be empty")
	}
	switch record.State {
	case RecordStateChildStarting:
		if record.ChildPID != 0 || record.ChildStartToken != nil {
			return errors.New("child-starting record must not carry child identity")
		}
	case RecordStateChildRecorded:
		if record.ChildPID <= 0 || record.ChildStartToken == nil {
			return errors.New("child-recorded record requires child PID and start token")
		}
		if err := record.ChildStartToken.validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown record state %q", record.State)
	}
	return nil
}

// RecordCommitUncertainError means rename completed but synchronizing the
// containing directory failed. The replacement is visible, but its survival
// across a crash is not proven; callers must retain ownership and refuse.
type RecordCommitUncertainError struct {
	Err error
}

func (e *RecordCommitUncertainError) Error() string {
	return fmt.Sprintf("durable record replacement is visible but commit is uncertain: %v", e.Err)
}

func (e *RecordCommitUncertainError) Unwrap() error { return e.Err }

// WriteRecord atomically replaces one descriptor-anchored durable role
// record. Errors before rename leave the prior complete record in place. A
// post-rename directory-sync failure returns RecordCommitUncertainError so it
// cannot be mistaken for a retained child-starting reservation.
func WriteRecord(path *RolePath, record Record) error {
	if err := validateRecord(record); err != nil {
		return err
	}
	if record.Session != path.Session || record.Role != path.Role {
		return errors.New("record session/role differs from role path")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')

	path.mu.Lock()
	defer path.mu.Unlock()
	if path.stateRoles == nil {
		return errors.New("role path is closed")
	}
	if err := verifyRetainedRoot("state-roles", filepath.Dir(path.Record), path.stateRoles); err != nil {
		return err
	}
	return writeRecordAtomic(path.stateRoles, path.Role+".json", payload)
}

func writeRecordAtomic(directory *os.Root, name string, payload []byte) error {
	return writeRecordAtomicWithSync(directory, name, payload, writeAllRecordPayload, syncRecordDirectory)
}

func writeRecordAtomicWith(directory *os.Root, name string, payload []byte, writePayload func(*os.File, []byte) error) error {
	return writeRecordAtomicWithSync(directory, name, payload, writePayload, syncRecordDirectory)
}

func writeAllRecordPayload(file *os.File, payload []byte) error {
	written := 0
	for written < len(payload) {
		n, err := file.Write(payload[written:])
		written += n
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

func writeRecordAtomicWithSync(
	directory *os.Root,
	name string,
	payload []byte,
	writePayload func(*os.File, []byte) error,
	syncDirectory func(*os.Root) error,
) error {
	var random [8]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return fmt.Errorf("generate record temporary name: %w", err)
	}
	temporary := "." + name + ".tmp-" + hex.EncodeToString(random[:])
	file, err := directory.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create record temporary file: %w", err)
	}
	keep := false
	fileOpen := true
	defer func() {
		if fileOpen {
			_ = file.Close()
		}
		if !keep {
			_ = directory.Remove(temporary)
		}
	}()
	if err := verifyPrivateArtifact(file, temporary, "durable record"); err != nil {
		return err
	}
	if err := writePayload(file, payload); err != nil {
		return fmt.Errorf("write durable record: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync durable record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close durable record: %w", err)
	}
	fileOpen = false
	if err := directory.Rename(temporary, name); err != nil {
		return fmt.Errorf("replace durable record: %w", err)
	}
	keep = true
	if err := syncDirectory(directory); err != nil {
		return &RecordCommitUncertainError{Err: err}
	}
	return nil
}

func syncRecordDirectory(directory *os.Root) error {
	directoryFile, err := directory.Open(".")
	if err != nil {
		return fmt.Errorf("open durable record directory for sync: %w", err)
	}
	syncErr := directoryFile.Sync()
	closeErr := directoryFile.Close()
	if syncErr != nil {
		syncErr = fmt.Errorf("sync durable record directory: %w", syncErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close durable record directory: %w", closeErr)
	}
	return errors.Join(syncErr, closeErr)
}

// ReadRecord reads and validates one descriptor-anchored durable role record.
func ReadRecord(path *RolePath) (Record, error) {
	path.mu.Lock()
	defer path.mu.Unlock()
	if path.stateRoles == nil {
		return Record{}, errors.New("role path is closed")
	}
	if err := verifyRetainedRoot("state-roles", filepath.Dir(path.Record), path.stateRoles); err != nil {
		return Record{}, err
	}
	name := path.Role + ".json"
	file, err := path.stateRoles.Open(name)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = file.Close() }()
	if err := verifyPrivateArtifact(file, path.Record, "durable record"); err != nil {
		return Record{}, err
	}
	pathInfo, err := path.stateRoles.Lstat(name)
	fileInfo, statErr := file.Stat()
	if err != nil || statErr != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, fileInfo) {
		return Record{}, fmt.Errorf("durable record %q was substituted", path.Record)
	}
	payload, err := io.ReadAll(io.LimitReader(file, recordMaxBytes+1))
	if err != nil {
		return Record{}, err
	}
	if len(payload) > recordMaxBytes {
		return Record{}, fmt.Errorf("durable record exceeds %d bytes", recordMaxBytes)
	}
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return Record{}, err
	}
	allowed := map[string]bool{
		"version": true, "state": true, "session": true, "role": true,
		"shim_pid": true, "nonce": true, "child_pid": true, "child_start_token": true,
	}
	if err := requireFields(fields, allowed, []string{"version", "state", "session", "role", "shim_pid", "nonce"}); err != nil {
		return Record{}, err
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "state": "string", "session": "string", "role": "string",
		"shim_pid": "integer", "nonce": "string", "child_pid": "integer", "child_start_token": "object",
	}); err != nil {
		return Record{}, err
	}
	if len(fields["child_start_token"]) == 1 {
		if err := validateStartTokenJSON(fields["child_start_token"][0]); err != nil {
			return Record{}, err
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("durable record has trailing JSON")
	}
	if err := validateRecord(record); err != nil {
		return Record{}, err
	}
	if record.Session != path.Session || record.Role != path.Role {
		return Record{}, errors.New("durable record session/role differs from role path")
	}
	return record, nil
}
