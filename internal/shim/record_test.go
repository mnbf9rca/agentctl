package shim

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecordPersistsChildStartingThenAtomicallyUpgradesChildIdentity(t *testing.T) {
	rolePath := newTestRolePath(t)
	starting := NewChildStartingRecord("session", "role", 4101, "nonce-4101")
	if err := WriteRecord(rolePath, starting); err != nil {
		t.Fatal(err)
	}
	readStarting, err := ReadRecord(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	if readStarting.State != RecordStateChildStarting || readStarting.ChildPID != 0 || readStarting.ChildStartToken != nil {
		t.Fatalf("starting record = %#v", readStarting)
	}

	token := StartToken{Sec: 1723300000, Usec: 123456}
	upgraded, err := starting.WithChild(4202, token)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteRecord(rolePath, upgraded); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRecord(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != RecordStateChildRecorded || got.ChildPID != 4202 || got.ChildStartToken == nil || *got.ChildStartToken != token {
		t.Fatalf("upgraded record = %#v, want child PID/token", got)
	}
	info, err := os.Stat(rolePath.Record)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("record mode = %04o, want %04o", got, want)
	}
}

func TestRecordFailedReplacementNeverExposesPartialJSON(t *testing.T) {
	rolePath := newTestRolePath(t)
	original := NewChildStartingRecord("session", "role", 100, "original")
	if err := WriteRecord(rolePath, original); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(rolePath.Record), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Dir(rolePath.Record), 0o700) })
	replacement := NewChildStartingRecord("session", "role", 200, "replacement")
	if err := WriteRecord(rolePath, replacement); err == nil {
		t.Fatal("WriteRecord succeeded without directory write permission")
	}
	got, err := ReadRecord(rolePath)
	if err != nil {
		t.Fatalf("existing record became partial: %v", err)
	}
	if got != original {
		t.Fatalf("record = %#v, want original %#v", got, original)
	}
}

func TestRecordPartialTemporaryWriteNeverReplacesCompleteRecord(t *testing.T) {
	rolePath := newTestRolePath(t)
	original := NewChildStartingRecord("session", "role", 100, "original")
	if err := WriteRecord(rolePath, original); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected partial write")
	payload := []byte(`{"version":1,"state":"child-starting","session":"session","role":"role","shim_pid":200,"nonce":"replacement"}` + "\n")
	err := writeRecordAtomicWith(rolePath.stateRoles, "role.json", payload, func(file *os.File, payload []byte) error {
		if _, err := file.Write(payload[:len(payload)/2]); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("write error = %v, want injected partial-write error", err)
	}
	got, err := ReadRecord(rolePath)
	if err != nil {
		t.Fatalf("existing record became partial: %v", err)
	}
	if got != original {
		t.Fatalf("record = %#v, want original %#v", got, original)
	}
	entries, err := os.ReadDir(filepath.Dir(rolePath.Record))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "role.json" {
		t.Fatalf("record directory entries = %v, want only role.json", entries)
	}
}

func TestRecordDirectorySyncFailureReportsCommitUncertain(t *testing.T) {
	rolePath := newTestRolePath(t)
	original := NewChildStartingRecord("session", "role", 100, "original")
	if err := WriteRecord(rolePath, original); err != nil {
		t.Fatal(err)
	}
	replacement := NewChildStartingRecord("session", "role", 200, "replacement")
	payload, err := json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	wantErr := errors.New("injected directory sync failure")
	err = writeRecordAtomicWithSync(rolePath.stateRoles, "role.json", payload, writeAllRecordPayload, func(*os.Root) error {
		return wantErr
	})
	var uncertain *RecordCommitUncertainError
	if !errors.As(err, &uncertain) || !errors.Is(err, wantErr) {
		t.Fatalf("write error = %T %v, want commit-uncertain wrapping injected error", err, err)
	}
	got, err := ReadRecord(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != replacement {
		t.Fatalf("visible record = %#v, want replacement %#v", got, replacement)
	}
}

func TestRecordObservationFailureIsNotReportedAsSubstitution(t *testing.T) {
	rolePath := newTestRolePath(t)
	if err := rolePath.stateRoles.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := ReadRecord(rolePath)
	if err == nil {
		t.Fatal("ReadRecord succeeded after retained descriptor observation failed")
	}
	var substituted *RootSubstitutedError
	if errors.As(err, &substituted) {
		t.Fatalf("descriptor observation error was reported as substitution: %v", err)
	}
	var observation *FilesystemObservationError
	if !errors.As(err, &observation) {
		t.Fatalf("error = %T %v, want *FilesystemObservationError", err, err)
	}
}

func TestRecordSurvivesRuntimeTreeDeletionAndSimulatedRebootResidue(t *testing.T) {
	parent := shortTempDir(t)
	runtimeRoot := filepath.Join(parent, "runtime")
	stateRoot := filepath.Join(parent, "state")
	namespace, err := openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	rolePath, err := namespace.RolePath("session", "role")
	if err != nil {
		t.Fatal(err)
	}
	record := NewChildStartingRecord("session", "role", 300, "survivor")
	if err := WriteRecord(rolePath, record); err != nil {
		t.Fatal(err)
	}
	_ = rolePath.Close()
	_ = namespace.Close()
	if err := os.RemoveAll(runtimeRoot); err != nil {
		t.Fatal(err)
	}

	namespace, err = openNamespaceRoots(namespaceRoots{Runtime: runtimeRoot, State: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = namespace.Close() }()
	rolePath, err = namespace.RolePath("session", "role")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rolePath.Close() }()
	got, err := ReadRecord(rolePath)
	if err != nil {
		t.Fatal(err)
	}
	if got != record {
		t.Fatalf("record after runtime deletion = %#v, want %#v", got, record)
	}
}

func TestRemoveRecordObservesDurableRoleRecordAbsence(t *testing.T) {
	path := newTestRolePath(t)
	record := NewChildStartingRecord(path.Session, path.Role, os.Getpid(), "remove-record")
	if err := WriteRecord(path, record); err != nil {
		t.Fatalf("WriteRecord() error = %v", err)
	}
	if err := RemoveRecord(path); err != nil {
		t.Fatalf("RemoveRecord() error = %v", err)
	}
	if _, err := ReadRecord(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadRecord() after removal error = %v, want os.ErrNotExist", err)
	}
	if err := RemoveRecord(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second RemoveRecord() error = %v, want os.ErrNotExist", err)
	}
}

func TestRecordRejectsMalformedAndInconsistentDurableData(t *testing.T) {
	rolePath := newTestRolePath(t)
	if err := os.WriteFile(rolePath.Record, []byte(`{"version":1,"state":"child-recorded","session":"session"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(rolePath); err == nil {
		t.Fatal("ReadRecord accepted malformed/incomplete record")
	}

	invalid := NewChildStartingRecord("session", "role", 400, "nonce")
	invalid.ChildPID = 401
	if err := WriteRecord(rolePath, invalid); err == nil {
		t.Fatal("WriteRecord accepted child-starting with a child PID")
	}
	malformedToken := StartToken{Sec: 1, Usec: 1_000_000}
	if _, err := NewChildStartingRecord("session", "role", 400, "nonce").WithChild(401, malformedToken); err == nil {
		t.Fatal("WithChild accepted malformed raw timeval")
	}
	invalid = NewChildStartingRecord("session", "role", 400, "nonce")
	invalid.State = RecordStateChildRecorded
	invalid.ChildPID = 401
	invalid.ChildStartToken = &malformedToken
	if err := WriteRecord(rolePath, invalid); err == nil {
		t.Fatal("WriteRecord accepted malformed raw timeval")
	}
}

func TestRecordRejectsDuplicateIdentityFields(t *testing.T) {
	rolePath := newTestRolePath(t)
	tests := []string{
		`{"version":1,"state":"child-starting","state":"child-starting","session":"session","role":"role","shim_pid":100,"nonce":"nonce"}`,
		`{"version":1,"state":"child-starting","session":"session","role":"role","shim_pid":100,"shim_pid":101,"nonce":"nonce","nonce":"other"}`,
		`{"version":1,"state":"child-recorded","session":"session","role":"role","shim_pid":100,"nonce":"nonce","child_pid":101,"child_pid":102,"child_start_token":{"sec":1,"sec":2,"usec":3}}`,
	}
	for _, payload := range tests {
		if err := os.WriteFile(rolePath.Record, []byte(payload), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadRecord(rolePath); err == nil {
			t.Fatalf("ReadRecord accepted duplicate identity fields: %s", payload)
		} else {
			var parse *RecordParseError
			if !errors.As(err, &parse) {
				t.Fatalf("error = %T %v, want *RecordParseError", err, err)
			}
			var schema *ProtocolSchemaError
			if errors.As(err, &schema) {
				t.Fatalf("record parse leaked ProtocolSchemaError: %v", err)
			}
		}
	}
}

func TestRecordRejectsPIDOutsideDarwinPIDT(t *testing.T) {
	rolePath := newTestRolePath(t)
	tooLarge := int(math.MaxInt32) + 1
	record := NewChildStartingRecord("session", "role", tooLarge, "nonce")
	if err := WriteRecord(rolePath, record); err == nil {
		t.Fatal("WriteRecord accepted shim PID above signed Darwin pid_t")
	}
	record = NewChildStartingRecord("session", "role", 100, "nonce")
	if _, err := record.WithChild(tooLarge, StartToken{Sec: 1}); err == nil {
		t.Fatal("WithChild accepted child PID above signed Darwin pid_t")
	}
	payload := `{"version":1,"state":"child-recorded","session":"session","role":"role","shim_pid":100,"nonce":"nonce","child_pid":` + fmt.Sprint(tooLarge) + `,"child_start_token":{"sec":1,"usec":0}}`
	if err := os.WriteFile(rolePath.Record, []byte(payload), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecord(rolePath); err == nil {
		t.Fatal("ReadRecord accepted child PID above signed Darwin pid_t")
	}
}

func TestRecordWriterEnforcesLimitBeforeFilesystemMutation(t *testing.T) {
	for _, size := range []int{recordMaxBytes, recordMaxBytes + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			rolePath := newTestRolePath(t)
			record := recordWithEncodedSize(t, size)
			err := WriteRecord(rolePath, record)
			if size == recordMaxBytes {
				if err != nil {
					t.Fatal(err)
				}
				info, statErr := os.Stat(rolePath.Record)
				if statErr != nil {
					t.Fatal(statErr)
				}
				if info.Size() != int64(size) {
					t.Fatalf("record size = %d, want %d", info.Size(), size)
				}
				return
			}
			if err == nil {
				t.Fatal("WriteRecord accepted oversized escaped record")
			}
			if _, statErr := os.Lstat(rolePath.Record); !os.IsNotExist(statErr) {
				t.Fatalf("oversized record mutated record path: %v", statErr)
			}
		})
	}
}

func recordWithEncodedSize(t *testing.T, size int) Record {
	t.Helper()
	record := NewChildStartingRecord("session", "role", 100, "nonce")
	record.Nonce = `"\`
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	filler := size - (len(payload) + 1)
	if filler < 0 {
		t.Fatalf("record base exceeds requested %d-byte size", size)
	}
	record.Nonce += strings.Repeat("n", filler)
	payload, err = json.Marshal(record)
	if err != nil || len(payload)+1 != size {
		t.Fatalf("constructed record size = %d, error = %v, want %d", len(payload)+1, err, size)
	}
	return record
}

func TestStartTokenUsesRawTimevalEquality(t *testing.T) {
	first := StartToken{Sec: 1, Usec: 999999}
	if !first.Equal(StartToken{Sec: 1, Usec: 999999}) {
		t.Fatal("equal raw timevals compared unequal")
	}
	if first.Equal(StartToken{Sec: 2, Usec: 999999}) || first.Equal(StartToken{Sec: 1, Usec: 999998}) {
		t.Fatal("different raw timeval components compared equal")
	}
}
