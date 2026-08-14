//go:build darwin

package fleet

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
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"golang.org/x/sys/unix"
)

const (
	// ShimFleetRecordVersion is the strict session-record schema version.
	ShimFleetRecordVersion  = 1
	shimFleetRecordName     = "fleet.json"
	shimFleetRecordMaxBytes = 64 * 1024
)

// ShimFleetRoleRecord is one role's durable launch configuration. The map key
// in ShimFleetRecord.Roles is the validated role identity.
type ShimFleetRoleRecord struct {
	Harness string `json:"harness"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

// Presentation is the closed, durable PTY presentation choice. It is selected
// by trusted fleet wiring and never supplied through the hidden shim command.
type Presentation string

const (
	PresentationDetached Presentation = "detached"
	PresentationTmux     Presentation = "tmux"
)

// ShimFleetRecord is the complete session roster/configuration persisted
// before any role shim may be started.
type ShimFleetRecord struct {
	Version      int                            `json:"version"`
	Session      string                         `json:"session"`
	Directory    string                         `json:"directory"`
	Presentation Presentation                   `json:"presentation"`
	Roster       []string                       `json:"roster"`
	Roles        map[string]ShimFleetRoleRecord `json:"roles"`
}

// ShimFleetExistsError reports the kernel-observed session-directory
// contention that makes one concurrent launch the sole record owner.
type ShimFleetExistsError struct {
	Session string
	Path    string
}

func (e *ShimFleetExistsError) Error() string {
	return fmt.Sprintf("durable fleet record for session %q already exists at %q", e.Session, e.Path)
}

// ShimFleetRecordParseError reports strict schema or value rejection.
type ShimFleetRecordParseError struct{ Cause string }

func (e *ShimFleetRecordParseError) Error() string {
	return "could not parse durable fleet record: " + e.Cause
}

// ShimFleetMutationConflictError reports a session-scoped mutation flock
// contender or a version-checked read/modify/write conflict. Neither case
// permits overwriting the observed peer mutation.
type ShimFleetMutationConflictError struct {
	Session string
	Cause   string
}

func (e *ShimFleetMutationConflictError) Error() string {
	return fmt.Sprintf("refusing concurrent durable fleet mutation for session %q: %s", e.Session, e.Cause)
}

// NewShimFleetRecord validates and copies the complete fleet configuration.
func NewShimFleetRecord(session, directory string, presentation Presentation, fleet config.FleetConfig) (ShimFleetRecord, error) {
	record := ShimFleetRecord{
		Version: ShimFleetRecordVersion, Session: session, Directory: directory, Presentation: presentation,
		Roster: make([]string, 0, len(fleet.Roles)),
		Roles:  make(map[string]ShimFleetRoleRecord, len(fleet.Roles)),
	}
	for _, role := range fleet.Roles {
		record.Roster = append(record.Roster, role.Name)
		record.Roles[role.Name] = ShimFleetRoleRecord{
			Harness: string(role.Harness), Model: role.Model, Effort: role.Effort,
		}
	}
	if err := validateShimFleetRecord(record); err != nil {
		return ShimFleetRecord{}, err
	}
	return record, nil
}

func validateShimFleetRecord(record ShimFleetRecord) error {
	if record.Version != ShimFleetRecordVersion {
		return fmt.Errorf("fleet record version is %d; expected %d", record.Version, ShimFleetRecordVersion)
	}
	if err := config.ValidateSessionName(record.Session); err != nil {
		return err
	}
	if !filepath.IsAbs(record.Directory) {
		return errors.New("fleet record directory must be absolute")
	}
	if record.Presentation != PresentationDetached && record.Presentation != PresentationTmux {
		return fmt.Errorf("fleet record presentation %q must be detached or tmux", record.Presentation)
	}
	if len(record.Roster) == 0 {
		return errors.New("fleet record roster must contain at least one role")
	}
	if len(record.Roles) != len(record.Roster) {
		return errors.New("fleet record roles must exactly match the roster")
	}
	seen := make(map[string]bool, len(record.Roster))
	for _, role := range record.Roster {
		if err := config.ValidateRoleName(role); err != nil {
			return err
		}
		if seen[role] {
			return fmt.Errorf("fleet record roster contains duplicate role %q", role)
		}
		seen[role] = true
		entry, ok := record.Roles[role]
		if !ok {
			return fmt.Errorf("fleet record roster role %q has no configuration", role)
		}
		if _, err := config.ParseHarness(entry.Harness); err != nil {
			return err
		}
		if entry.Model != "" {
			if err := config.ValidateModelName(entry.Model); err != nil {
				return err
			}
		}
		if entry.Effort != "" {
			if err := config.ValidateEffort(entry.Effort); err != nil {
				return err
			}
		}
	}
	for role := range record.Roles {
		if !seen[role] {
			return fmt.Errorf("fleet record configuration role %q is outside the roster", role)
		}
	}
	return nil
}

// ShimFleetRecordStore retains descriptor-verified durable state roots.
type ShimFleetRecordStore struct {
	stateRoot   string
	root        *os.Root
	sessions    *os.Root
	mu          sync.Mutex
	syncDir     func(*os.Root) error
	writeRecord func(*os.Root, []byte, func(*os.Root) error) (bool, error)
}

// OpenShimFleetRecordStore opens the resolved state root and prepares its
// shared sessions directory without creating a session record.
func OpenShimFleetRecordStore(stateRoot string) (*ShimFleetRecordStore, error) {
	if stateRoot == "" || !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot || len(stateRoot) > shim.RootPathMaxBytes {
		return nil, fmt.Errorf("invalid state root %q", stateRoot)
	}
	root, err := openVerifiedShimFleetDirectory(stateRoot)
	if err != nil {
		return nil, err
	}
	created := false
	if err := root.Mkdir("sessions", 0o700); err == nil {
		created = true
	} else if !errors.Is(err, os.ErrExist) {
		_ = root.Close()
		return nil, fmt.Errorf("create durable sessions directory: %w", err)
	}
	sessionsPath := filepath.Join(stateRoot, "sessions")
	sessions, err := openVerifiedShimFleetChild(root, "sessions", sessionsPath)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	store := &ShimFleetRecordStore{
		stateRoot: stateRoot, root: root, sessions: sessions,
		syncDir: syncShimFleetDirectory, writeRecord: writeShimFleetRecordAtomic,
	}
	if created {
		if err := store.syncDir(root); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("commit durable sessions directory: %w", err)
		}
	}
	return store, nil
}

func openVerifiedShimFleetDirectory(path string) (*os.Root, error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	declared, lstatErr := os.Lstat(path)
	retained, statErr := root.Stat(".")
	if lstatErr != nil || statErr != nil {
		_ = root.Close()
		return nil, errors.Join(lstatErr, statErr)
	}
	if declared.Mode()&os.ModeSymlink != 0 || !declared.IsDir() || !os.SameFile(declared, retained) {
		_ = root.Close()
		return nil, fmt.Errorf("state root %q is not the retained directory", path)
	}
	if declared.Mode().Perm() != 0o700 {
		_ = root.Close()
		return nil, fmt.Errorf("state root %q has mode %#o; expected 0700", path, declared.Mode().Perm())
	}
	if stat, ok := retained.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		_ = root.Close()
		return nil, fmt.Errorf("state root %q is not owned by effective uid %d", path, os.Geteuid())
	}
	return root, nil
}

func openVerifiedShimFleetChild(parent *os.Root, name, path string) (*os.Root, error) {
	declared, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if declared.Mode()&os.ModeSymlink != 0 || !declared.IsDir() || declared.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("durable directory %q must be an owned mode-0700 directory", path)
	}
	root, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	retained, err := root.Stat(".")
	if err != nil || !os.SameFile(declared, retained) {
		_ = root.Close()
		return nil, fmt.Errorf("durable directory %q was substituted", path)
	}
	if stat, ok := retained.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		_ = root.Close()
		return nil, fmt.Errorf("durable directory %q is not owned by effective uid %d", path, os.Geteuid())
	}
	return root, nil
}

// Create atomically creates the session directory, making mkdir the one
// kernel winner between concurrent launch contenders, then commits fleet.json.
func (s *ShimFleetRecordStore) Create(record ShimFleetRecord) error {
	if err := validateShimFleetRecord(record); err != nil {
		return err
	}
	payload, err := marshalShimFleetRecord(record)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		return errors.New("fleet record store is closed")
	}
	if err := s.sessions.Mkdir(record.Session, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return &ShimFleetExistsError{Session: record.Session, Path: filepath.Join(s.stateRoot, "sessions", record.Session, shimFleetRecordName)}
		}
		return fmt.Errorf("reserve durable fleet session: %w", err)
	}
	sessionPath := filepath.Join(s.stateRoot, "sessions", record.Session)
	session, err := openVerifiedShimFleetChild(s.sessions, record.Session, sessionPath)
	if err != nil {
		return errors.Join(err, s.rollbackSessionReservation(record.Session))
	}
	visible, writeErr := s.writeRecord(session, payload, s.syncDir)
	closeErr := session.Close()
	if !visible {
		if writeErr == nil {
			writeErr = errors.New("fleet record writer returned no visible record and no error")
		}
		return errors.Join(writeErr, closeErr, s.rollbackSessionReservation(record.Session))
	}
	if writeErr != nil || closeErr != nil {
		return errors.Join(writeErr, closeErr)
	}
	if err := s.syncDir(s.sessions); err != nil {
		return &shim.RecordCommitUncertainError{Err: err}
	}
	return nil
}

func (s *ShimFleetRecordStore) rollbackSessionReservation(session string) error {
	if err := s.sessions.Remove(session); err != nil {
		return fmt.Errorf("remove pre-visible durable fleet session reservation: %w", err)
	}
	if err := s.syncDir(s.sessions); err != nil {
		return fmt.Errorf("commit removal of pre-visible durable fleet session reservation: %w", err)
	}
	return nil
}

func marshalShimFleetRecord(record ShimFleetRecord) ([]byte, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	payload = append(payload, '\n')
	if len(payload) > shimFleetRecordMaxBytes {
		return nil, fmt.Errorf("durable fleet record exceeds %d bytes", shimFleetRecordMaxBytes)
	}
	return payload, nil
}

func writeShimFleetRecordAtomic(directory *os.Root, payload []byte, syncDirectory func(*os.Root) error) (bool, error) {
	var random [8]byte
	if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
		return false, fmt.Errorf("generate fleet record temporary name: %w", err)
	}
	temporary := ".fleet.json.tmp-" + hex.EncodeToString(random[:])
	file, err := directory.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return false, fmt.Errorf("create fleet record temporary file: %w", err)
	}
	removeTemporary := true
	fileOpen := true
	defer func() {
		if fileOpen {
			_ = file.Close()
		}
		if removeTemporary {
			_ = directory.Remove(temporary)
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return false, errors.New("fleet record temporary file must be a mode-0600 regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return false, fmt.Errorf("fleet record temporary file is not owned by effective uid %d", os.Geteuid())
	}
	written := 0
	for written < len(payload) {
		count, writeErr := file.Write(payload[written:])
		written += count
		if writeErr != nil {
			return false, fmt.Errorf("write fleet record: %w", writeErr)
		}
		if count == 0 {
			return false, io.ErrShortWrite
		}
	}
	if err := file.Sync(); err != nil {
		return false, fmt.Errorf("sync fleet record: %w", err)
	}
	if err := file.Close(); err != nil {
		return false, fmt.Errorf("close fleet record: %w", err)
	}
	fileOpen = false
	if err := directory.Rename(temporary, shimFleetRecordName); err != nil {
		return false, fmt.Errorf("replace fleet record: %w", err)
	}
	removeTemporary = false
	if err := syncDirectory(directory); err != nil {
		return true, &shim.RecordCommitUncertainError{Err: err}
	}
	return true, nil
}

// Read returns one strict, complete session fleet record.
func (s *ShimFleetRecordStore) Read(session string) (ShimFleetRecord, error) {
	if err := config.ValidateSessionName(session); err != nil {
		return ShimFleetRecord{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readLocked(session)
}

// List returns every durable sessions-directory entry in lexical order. It
// deliberately does not open, validate, lock, heal, or remove any entry;
// callers perform per-entry reads so malformed trees remain reportable facts.
func (s *ShimFleetRecordStore) List() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		return nil, errors.New("fleet record store is closed")
	}
	directory, err := s.sessions.Open(".")
	if err != nil {
		return nil, err
	}
	names, readErr := directory.Readdirnames(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	sort.Strings(names)
	return names, nil
}

// ReplaceOwned performs one version-checked session-record read/modify/write
// while holding a nonblocking session-directory flock only for the mutation.
// The role-lifetime ownership arbiter remains the separate role lockfile.
func (s *ShimFleetRecordStore) ReplaceOwned(expected, replacement ShimFleetRecord) error {
	return s.replaceOwned(expected, replacement, false)
}

// ExtendOwned appends exactly one role to an unchanged roster under the same
// session-scoped mutation flock and version-checked commit as ReplaceOwned.
func (s *ShimFleetRecordStore) ExtendOwned(expected, replacement ShimFleetRecord) error {
	return s.replaceOwned(expected, replacement, true)
}

func (s *ShimFleetRecordStore) replaceOwned(expected, replacement ShimFleetRecord, extend bool) error {
	if err := validateShimFleetRecord(expected); err != nil {
		return err
	}
	if err := validateShimFleetRecord(replacement); err != nil {
		return err
	}
	if expected.Session != replacement.Session || expected.Version != replacement.Version {
		return errors.New("fleet replacement must retain version and session")
	}
	if extend {
		if len(replacement.Roster) != len(expected.Roster)+1 || !reflect.DeepEqual(replacement.Roster[:len(expected.Roster)], expected.Roster) {
			return errors.New("fleet extension must append exactly one role to the existing roster")
		}
		added := replacement.Roster[len(expected.Roster)]
		if _, existed := expected.Roles[added]; existed {
			return errors.New("fleet extension role already exists")
		}
	} else if !reflect.DeepEqual(expected.Roster, replacement.Roster) {
		return errors.New("fleet replacement must retain roster")
	}
	payload, err := marshalShimFleetRecord(replacement)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions == nil {
		return errors.New("fleet record store is closed")
	}
	sessionPath := filepath.Join(s.stateRoot, "sessions", expected.Session)
	sessionRoot, err := openVerifiedShimFleetChild(s.sessions, expected.Session, sessionPath)
	if err != nil {
		return err
	}
	defer func() { _ = sessionRoot.Close() }()
	claim, err := acquireShimFleetMutationClaim(sessionRoot, expected.Session)
	if err != nil {
		return err
	}
	defer releaseShimFleetMutationClaim(claim)

	observed, err := s.readLocked(expected.Session)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, expected) {
		return &ShimFleetMutationConflictError{
			Session: expected.Session, Cause: "durable fleet record changed after the caller read it",
		}
	}
	_, err = writeShimFleetRecordAtomic(sessionRoot, payload, s.syncDir)
	return err
}

func acquireShimFleetMutationClaim(sessionRoot *os.Root, session string) (*os.File, error) {
	claim, err := sessionRoot.Open(".")
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(claim.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = claim.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, &ShimFleetMutationConflictError{
				Session: session, Cause: "session mutation flock is held by another mutator",
			}
		}
		return nil, fmt.Errorf("acquire session fleet mutation flock: %w", err)
	}
	return claim, nil
}

func releaseShimFleetMutationClaim(claim *os.File) {
	_ = unix.Flock(int(claim.Fd()), unix.LOCK_UN)
	_ = claim.Close()
}

func (s *ShimFleetRecordStore) readLocked(session string) (ShimFleetRecord, error) {
	if s.sessions == nil {
		return ShimFleetRecord{}, errors.New("fleet record store is closed")
	}
	sessionPath := filepath.Join(s.stateRoot, "sessions", session)
	sessionRoot, err := openVerifiedShimFleetChild(s.sessions, session, sessionPath)
	if err != nil {
		return ShimFleetRecord{}, err
	}
	defer func() { _ = sessionRoot.Close() }()
	declared, err := sessionRoot.Lstat(shimFleetRecordName)
	if err != nil {
		return ShimFleetRecord{}, err
	}
	if declared.Mode()&os.ModeSymlink != 0 || !declared.Mode().IsRegular() {
		return ShimFleetRecord{}, errors.New("durable fleet record must be a nonsymlink regular file")
	}
	file, err := sessionRoot.Open(shimFleetRecordName)
	if err != nil {
		return ShimFleetRecord{}, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ShimFleetRecord{}, err
	}
	if !os.SameFile(declared, info) {
		return ShimFleetRecord{}, errors.New("durable fleet record was substituted")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return ShimFleetRecord{}, fmt.Errorf("durable fleet record must be a mode-0600 regular file")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return ShimFleetRecord{}, fmt.Errorf("durable fleet record is not owned by effective uid %d", os.Geteuid())
	}
	payload, err := io.ReadAll(io.LimitReader(file, shimFleetRecordMaxBytes+1))
	if err != nil {
		return ShimFleetRecord{}, err
	}
	if len(payload) > shimFleetRecordMaxBytes {
		return ShimFleetRecord{}, fmt.Errorf("durable fleet record exceeds %d bytes", shimFleetRecordMaxBytes)
	}
	return decodeShimFleetRecord(payload, session)
}

func decodeShimFleetRecord(payload []byte, expectedSession string) (ShimFleetRecord, error) {
	fields, err := shimFleetObjectFields(payload)
	if err != nil {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: err.Error()}
	}
	versions := fields["version"]
	if len(versions) != 1 || strings.TrimSpace(string(versions[0])) != "1" {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: "fleet record protocol version is not exactly 1"}
	}
	allowed := map[string]bool{"version": true, "session": true, "directory": true, "presentation": true, "roster": true, "roles": true}
	for name, values := range fields {
		if len(values) != 1 {
			return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: fmt.Sprintf("duplicate field %q", name)}
		}
		if !allowed[name] {
			return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: fmt.Sprintf("unknown field %q", name)}
		}
	}
	for _, required := range []string{"version", "session", "directory", "presentation", "roster", "roles"} {
		if len(fields[required]) == 0 {
			return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: fmt.Sprintf("missing required field %q", required)}
		}
	}
	if err := validateShimFleetRoleObjects(fields["roles"][0]); err != nil {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: err.Error()}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var record ShimFleetRecord
	if err := decoder.Decode(&record); err != nil {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: err.Error()}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: "trailing JSON"}
	}
	if err := validateShimFleetRecord(record); err != nil {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: err.Error()}
	}
	if record.Session != expectedSession {
		return ShimFleetRecord{}, &ShimFleetRecordParseError{Cause: "fleet record session differs from requested session"}
	}
	return record, nil
}

func shimFleetObjectFields(payload []byte) (map[string][]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	if delimiter, ok := first.(json.Delim); !ok || delimiter != '{' {
		return nil, errors.New("top level is not an object")
	}
	fields := make(map[string][]json.RawMessage)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, errors.New("object field name is not a string")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[name] = append(fields[name], raw)
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(payload[decoder.InputOffset():])) != 0 {
		return nil, errors.New("trailing JSON")
	}
	return fields, nil
}

func validateShimFleetRoleObjects(payload json.RawMessage) error {
	roles, err := shimFleetObjectFields(payload)
	if err != nil {
		return fmt.Errorf("roles: %w", err)
	}
	for role, values := range roles {
		if len(values) != 1 {
			return fmt.Errorf("roles: duplicate role field %q", role)
		}
		entry, err := shimFleetObjectFields(values[0])
		if err != nil {
			return fmt.Errorf("roles[%q]: %w", role, err)
		}
		allowed := map[string]bool{"harness": true, "model": true, "effort": true}
		for name, nested := range entry {
			if len(nested) != 1 {
				return fmt.Errorf("roles[%q]: duplicate field %q", role, name)
			}
			if !allowed[name] {
				return fmt.Errorf("roles[%q]: unknown field %q", role, name)
			}
		}
		for _, required := range []string{"harness", "model", "effort"} {
			if len(entry[required]) == 0 {
				return fmt.Errorf("roles[%q]: missing required field %q", role, required)
			}
		}
	}
	return nil
}

// RemoveOwned removes only an unchanged record created by the caller, and
// only after the caller has separately proved every child absent.
func (s *ShimFleetRecordStore) RemoveOwned(expected ShimFleetRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	observed, err := s.readLocked(expected.Session)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, expected) {
		return errors.New("durable fleet record differs from the record owned by this invocation")
	}
	sessionPath := filepath.Join(s.stateRoot, "sessions", expected.Session)
	sessionRoot, err := openVerifiedShimFleetChild(s.sessions, expected.Session, sessionPath)
	if err != nil {
		return err
	}
	if rolesInfo, lstatErr := sessionRoot.Lstat("roles"); lstatErr == nil {
		if rolesInfo.Mode()&os.ModeSymlink != 0 || !rolesInfo.IsDir() {
			_ = sessionRoot.Close()
			return errors.New("durable fleet roles artifact must be a nonsymlink directory")
		}
		roles, openErr := sessionRoot.Open("roles")
		if openErr != nil {
			_ = sessionRoot.Close()
			return openErr
		}
		names, readErr := roles.Readdirnames(-1)
		_ = roles.Close()
		if readErr != nil {
			_ = sessionRoot.Close()
			return readErr
		}
		if len(names) != 0 {
			sort.Strings(names)
			_ = sessionRoot.Close()
			return fmt.Errorf("durable fleet roles retain artifacts: %s", strings.Join(names, ", "))
		}
		if err := sessionRoot.Remove("roles"); err != nil {
			_ = sessionRoot.Close()
			return err
		}
		if err := s.syncDir(sessionRoot); err != nil {
			_ = sessionRoot.Close()
			return &shim.RecordCommitUncertainError{Err: err}
		}
	} else if !errors.Is(lstatErr, os.ErrNotExist) {
		_ = sessionRoot.Close()
		return lstatErr
	}
	entries, err := sessionRoot.Open(".")
	if err != nil {
		_ = sessionRoot.Close()
		return err
	}
	names, readErr := entries.Readdirnames(-1)
	_ = entries.Close()
	if readErr != nil {
		_ = sessionRoot.Close()
		return readErr
	}
	if len(names) != 1 || names[0] != shimFleetRecordName {
		sort.Strings(names)
		_ = sessionRoot.Close()
		return fmt.Errorf("durable fleet session retains artifacts: %s", strings.Join(names, ", "))
	}
	if err := sessionRoot.Remove(shimFleetRecordName); err != nil {
		_ = sessionRoot.Close()
		return err
	}
	if err := s.syncDir(sessionRoot); err != nil {
		_ = sessionRoot.Close()
		return &shim.RecordCommitUncertainError{Err: err}
	}
	if err := sessionRoot.Close(); err != nil {
		return err
	}
	if err := s.sessions.Remove(expected.Session); err != nil {
		return err
	}
	if err := s.syncDir(s.sessions); err != nil {
		return &shim.RecordCommitUncertainError{Err: err}
	}
	return nil
}

func syncShimFleetDirectory(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

// Close releases retained durable directory descriptors.
func (s *ShimFleetRecordStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	if s.sessions != nil {
		errs = append(errs, s.sessions.Close())
		s.sessions = nil
	}
	if s.root != nil {
		errs = append(errs, s.root.Close())
		s.root = nil
	}
	return errors.Join(errs...)
}

func joinShimRoster(roster []string) string { return strings.Join(roster, ",") }
