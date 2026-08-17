package shim

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"

	"github.com/mnbf9rca/agentctl/internal/config"
)

const recordMaxBytes = 64 * 1024

const darwinPIDMax = int64(1<<31 - 1)

func validDarwinPID(pid int) bool { return pid > 0 && int64(pid) <= darwinPIDMax }

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
	RecordStateCleanupFailed RecordState = "cleanup-failed"
)

// CleanupObservation is the closed retained-child observation vocabulary that
// may accompany a durably recorded cleanup failure.
type CleanupObservation string

const (
	CleanupObservationPresentMatch             CleanupObservation = "present-match"
	CleanupObservationPresentTokenDisagreement CleanupObservation = "present-token-disagreement"
	CleanupObservationPresentNotOurs           CleanupObservation = "present-not-ours"
	CleanupObservationCouldNotObserve          CleanupObservation = "could-not-observe"
)

// CleanupFailure is the single factual record field added by R19. Remaining
// uses the fixed child/socket/attach/record/lock order and identifies only artifacts
// observed still present.
type CleanupFailure struct {
	Cause       string             `json:"cause"`
	Observation CleanupObservation `json:"observation"`
	Remaining   []string           `json:"remaining"`
}

// Record is durable identity evidence, never a liveness observation.
type Record struct {
	Version         int             `json:"version"`
	State           RecordState     `json:"state"`
	Session         string          `json:"session"`
	Role            string          `json:"role"`
	ShimPID         int             `json:"shim_pid"`
	Nonce           string          `json:"nonce"`
	ChildPID        int             `json:"child_pid,omitempty"`
	ChildStartToken *StartToken     `json:"child_start_token,omitempty"`
	Cleanup         *CleanupFailure `json:"cleanup,omitempty"`
}

// RecordParseError identifies malformed durable identity at the record
// boundary without exposing it as a wire-protocol schema fact.
type RecordParseError struct {
	Cause string
}

func (e *RecordParseError) Error() string { return "could not parse durable record: " + e.Cause }

func recordParseError(err error) error {
	if err == nil {
		return nil
	}
	return &RecordParseError{Cause: err.Error()}
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
	if !validDarwinPID(pid) {
		return Record{}, errors.New("child PID must be a positive signed Darwin pid_t")
	}
	if err := token.validate(); err != nil {
		return Record{}, err
	}
	r.State = RecordStateChildRecorded
	r.ChildPID = pid
	r.ChildStartToken = &token
	return r, nil
}

// WithCleanupFailure changes an existing reservation/child record into the
// durably observable cleanup-failed state without inventing child absence.
func (r Record) WithCleanupFailure(pid int, token *StartToken, failure CleanupFailure) (Record, error) {
	if err := validateRecord(r); err != nil {
		return Record{}, err
	}
	if r.State != RecordStateChildStarting && r.State != RecordStateChildRecorded {
		return Record{}, errors.New("only child-starting or child-recorded may become cleanup-failed")
	}
	if !validDarwinPID(pid) {
		return Record{}, errors.New("cleanup-failed record requires a positive child PID")
	}
	if token != nil {
		if err := token.validate(); err != nil {
			return Record{}, err
		}
		copied := *token
		token = &copied
	}
	failure.Remaining = append([]string(nil), failure.Remaining...)
	if err := validateCleanupFailure(failure); err != nil {
		return Record{}, err
	}
	r.State = RecordStateCleanupFailed
	r.ChildPID = pid
	r.ChildStartToken = token
	r.Cleanup = &failure
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
	if !validDarwinPID(record.ShimPID) {
		return errors.New("record shim PID must be a positive signed Darwin pid_t")
	}
	if record.Nonce == "" {
		return errors.New("record nonce must not be empty")
	}
	switch record.State {
	case RecordStateChildStarting:
		if record.ChildPID != 0 || record.ChildStartToken != nil || record.Cleanup != nil {
			return errors.New("child-starting record must not carry child identity")
		}
	case RecordStateChildRecorded:
		if !validDarwinPID(record.ChildPID) || record.ChildStartToken == nil || record.Cleanup != nil {
			return errors.New("child-recorded record requires child PID and start token")
		}
		if err := record.ChildStartToken.validate(); err != nil {
			return err
		}
	case RecordStateCleanupFailed:
		if !validDarwinPID(record.ChildPID) || record.Cleanup == nil {
			return errors.New("cleanup-failed record requires child PID and cleanup facts")
		}
		if record.ChildStartToken != nil {
			if err := record.ChildStartToken.validate(); err != nil {
				return err
			}
		}
		if err := validateCleanupFailure(*record.Cleanup); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown record state %q", record.State)
	}
	return nil
}

func validateCleanupFailure(failure CleanupFailure) error {
	if failure.Cause == "" {
		return errors.New("cleanup failure cause must not be empty")
	}
	switch failure.Observation {
	case CleanupObservationPresentMatch,
		CleanupObservationPresentTokenDisagreement,
		CleanupObservationPresentNotOurs,
		CleanupObservationCouldNotObserve:
	default:
		return fmt.Errorf("unknown cleanup observation %q", failure.Observation)
	}
	if len(failure.Remaining) == 0 {
		return errors.New("cleanup failure must identify at least one remaining artifact")
	}
	order := map[string]int{"child": 0, "socket": 1, "attach": 2, "record": 3, "lock": 4}
	previous := -1
	for _, artifact := range failure.Remaining {
		index, ok := order[artifact]
		if !ok {
			return fmt.Errorf("unknown remaining cleanup artifact %q", artifact)
		}
		if index <= previous {
			return errors.New("remaining cleanup artifacts must be unique and in child,socket,attach,record,lock order")
		}
		previous = index
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
	if len(payload) > recordMaxBytes {
		return fmt.Errorf("durable record exceeds %d bytes", recordMaxBytes)
	}

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

// RemoveRecord removes one descriptor-anchored durable role record and
// synchronizes the containing directory. A missing record remains an observed
// error so callers cannot claim cleanup they did not perform.
func RemoveRecord(path *RolePath) error {
	path.mu.Lock()
	defer path.mu.Unlock()
	if path.stateRoles == nil {
		return errors.New("role path is closed")
	}
	if err := verifyRetainedRoot("state-roles", filepath.Dir(path.Record), path.stateRoles); err != nil {
		return err
	}
	if err := path.stateRoles.Remove(path.Role + ".json"); err != nil {
		return err
	}
	return syncRecordDirectory(path.stateRoles)
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
	if err != nil {
		return Record{}, &FilesystemObservationError{Kind: "durable record", Path: path.Record, Operation: "lstat declared path", Err: err}
	}
	if statErr != nil {
		return Record{}, &FilesystemObservationError{Kind: "durable record", Path: path.Record, Operation: "stat retained file", Err: statErr}
	}
	if pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(pathInfo, fileInfo) {
		return Record{}, fmt.Errorf("durable record %q was substituted", path.Record)
	}
	payload, err := io.ReadAll(io.LimitReader(file, recordMaxBytes+1))
	if err != nil {
		return Record{}, err
	}
	if len(payload) > recordMaxBytes {
		return Record{}, recordParseError(fmt.Errorf("durable record exceeds %d bytes", recordMaxBytes))
	}
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return Record{}, recordParseError(err)
	}
	allowed := map[string]bool{
		"version": true, "state": true, "session": true, "role": true,
		"shim_pid": true, "nonce": true, "child_pid": true, "child_start_token": true, "cleanup": true,
	}
	if err := requireFields(fields, allowed, []string{"version", "state", "session", "role", "shim_pid", "nonce"}); err != nil {
		return Record{}, recordParseError(err)
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "state": "string", "session": "string", "role": "string",
		"shim_pid": "integer", "nonce": "string", "child_pid": "integer", "child_start_token": "object", "cleanup": "object",
	}); err != nil {
		return Record{}, recordParseError(err)
	}
	if err := requireJSONIntegerRange(fields, "version", big.NewInt(ShimProtocolVersion), big.NewInt(ShimProtocolVersion)); err != nil {
		return Record{}, recordParseError(err)
	}
	if err := requireJSONIntegerRange(fields, "shim_pid", big.NewInt(1), big.NewInt(darwinPIDMax)); err != nil {
		return Record{}, recordParseError(err)
	}
	if err := requireJSONIntegerRange(fields, "child_pid", big.NewInt(1), big.NewInt(darwinPIDMax)); err != nil {
		return Record{}, recordParseError(err)
	}
	if len(fields["child_start_token"]) == 1 {
		if err := validateStartTokenJSON(fields["child_start_token"][0]); err != nil {
			return Record{}, recordParseError(err)
		}
	}
	if len(fields["cleanup"]) == 1 {
		if err := validateCleanupFailureJSON(fields["cleanup"][0]); err != nil {
			return Record{}, recordParseError(err)
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, recordParseError(err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Record{}, recordParseError(errors.New("durable record has trailing JSON"))
	}
	if err := validateRecord(record); err != nil {
		return Record{}, recordParseError(err)
	}
	if record.Session != path.Session || record.Role != path.Role {
		return Record{}, recordParseError(errors.New("durable record session/role differs from role path"))
	}
	return record, nil
}

func validateCleanupFailureJSON(raw json.RawMessage) error {
	fields, err := decodeJSONObject(raw)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"cause": true, "observation": true, "remaining": true}
	if err := requireFields(fields, allowed, []string{"cause", "observation", "remaining"}); err != nil {
		return err
	}
	if err := requireJSONTypes(fields, map[string]string{
		"cause": "string", "observation": "string", "remaining": "array",
	}); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var failure CleanupFailure
	if err := decoder.Decode(&failure); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("cleanup facts have trailing JSON")
	}
	return validateCleanupFailure(failure)
}
