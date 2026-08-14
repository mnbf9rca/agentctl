//go:build darwin

package fleet

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestShimFleetRecordStorePersistsCompleteVersionedConfig(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	record, err := NewShimFleetRecord("fleet", "/repo path", PresentationTmux, shimTestFleet())
	if err != nil {
		t.Fatalf("NewShimFleetRecord() error = %v", err)
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	recordPath := filepath.Join(stateRoot, "sessions", "fleet", "fleet.json")
	payload, err := os.ReadFile(recordPath)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", recordPath, err)
	}
	wantPayload := "{\"version\":1,\"session\":\"fleet\",\"directory\":\"/repo path\",\"presentation\":\"tmux\",\"roster\":[\"planner\",\"coder\"],\"roles\":{\"coder\":{\"harness\":\"codex\",\"model\":\"gpt-5.6-sol\",\"effort\":\"high\"},\"planner\":{\"harness\":\"claude\",\"model\":\"\",\"effort\":\"max\"}}}\n"
	if string(payload) != wantPayload {
		t.Fatalf("fleet record payload = %q, want %q", payload, wantPayload)
	}
	info, err := os.Stat(recordPath)
	if err != nil {
		t.Fatalf("Stat(record): %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("fleet record mode = %#o, want 0600", got)
	}

	got, err := store.Read("fleet")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("Read() = %#v, want %#v", got, record)
	}
}

// This catches a writer that omits presentation, changes its durable spelling,
// or moves it out of the version-1 schema order.
func TestShimFleetRecordPresentationIsRequiredAndEncodedInSchemaOrder(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	record, err := NewShimFleetRecord("fleet", "/repo", PresentationTmux, config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}})
	if err != nil {
		t.Fatalf("NewShimFleetRecord() error = %v", err)
	}
	if err := store.Create(record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	payload, err := os.ReadFile(filepath.Join(stateRoot, "sessions", "fleet", "fleet.json"))
	if err != nil {
		t.Fatalf("ReadFile(fleet record): %v", err)
	}
	want := "{\"version\":1,\"session\":\"fleet\",\"directory\":\"/repo\",\"presentation\":\"tmux\",\"roster\":[\"planner\"],\"roles\":{\"planner\":{\"harness\":\"claude\",\"model\":\"\",\"effort\":\"\"}}}\n"
	if got := string(payload); got != want {
		t.Fatalf("fleet record bytes = %q, want %q", got, want)
	}
	got, err := store.Read("fleet")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if got.Presentation != PresentationTmux {
		t.Fatalf("Read().Presentation = %q, want tmux", got.Presentation)
	}
}

// This catches accepting the pre-cutover record dialect, accepting a third
// presentation mode, or interpreting foreign versions beyond the version pass.
func TestDecodeShimFleetRecordStrictlyRequiresCurrentPresentationSchema(t *testing.T) {
	t.Parallel()

	valid := "{\"version\":1,\"session\":\"fleet\",\"directory\":\"/repo\",\"presentation\":\"detached\",\"roster\":[\"planner\"],\"roles\":{\"planner\":{\"harness\":\"claude\",\"model\":\"\",\"effort\":\"\"}}}"
	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{name: "missing presentation does not adopt old record", payload: "{\"version\":1,\"session\":\"fleet\",\"directory\":\"/repo\",\"roster\":[\"planner\"],\"roles\":{\"planner\":{\"harness\":\"claude\",\"model\":\"\",\"effort\":\"\"}}}", want: "missing required field \"presentation\""},
		{name: "invalid presentation", payload: strings.Replace(valid, "\"detached\"", "\"screen\"", 1), want: "presentation"},
		{name: "wrong presentation type", payload: strings.Replace(valid, "\"detached\"", "true", 1), want: "cannot unmarshal bool"},
		{name: "duplicate presentation", payload: strings.Replace(valid, "\"roster\"", "\"presentation\":\"tmux\",\"roster\"", 1), want: "duplicate field \"presentation\""},
		{name: "foreign version wins over unknown schema", payload: strings.Replace(valid, "\"version\":1", "\"version\":2,\"unknown\":true", 1), want: "fleet record protocol version is not exactly 1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeShimFleetRecord([]byte(test.payload), "fleet")
			var malformed *ShimFleetRecordParseError
			if !errors.As(err, &malformed) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("decodeShimFleetRecord() error = %T %v, want invalid record containing %q", err, err, test.want)
			}
		})
	}
}

// This catches a foreground/relaunch record mutation that silently resets the
// launch presentation while replacing or extending role configuration.
func TestForegroundReplacementPreservesFleetPresentation(t *testing.T) {
	t.Parallel()

	expected := ShimFleetRecord{
		Version: 1, Session: "fleet", Directory: "/repo", Presentation: PresentationTmux,
		Roster: []string{"planner"}, Roles: map[string]ShimFleetRoleRecord{"planner": {Harness: "claude"}},
	}
	replacement := foregroundReplacement(expected, config.RoleConfig{Name: "coder", Harness: config.HarnessCodex}, false)
	if replacement.Presentation != PresentationTmux {
		t.Fatalf("foregroundReplacement().Presentation = %q, want tmux", replacement.Presentation)
	}
}

// This catches a size limit accidentally bypassed by a future writer change.
func TestMarshalShimFleetRecordRefusesPayloadOverMaximum(t *testing.T) {
	t.Parallel()

	record := ShimFleetRecord{Version: 1, Session: "fleet", Directory: "/repo", Presentation: PresentationDetached, Roster: []string{"planner"}, Roles: map[string]ShimFleetRoleRecord{
		"planner": {Harness: "claude", Model: strings.Repeat("m", shimFleetRecordMaxBytes)},
	}}
	if _, err := marshalShimFleetRecord(record); err == nil {
		t.Fatal("marshalShimFleetRecord() error = nil, want maximum-size refusal")
	}
}

func TestShimFleetRecordStoreListsEveryDurableSessionEntryWithoutOpeningOrMutatingIt(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := os.Mkdir(filepath.Join(stateRoot, "sessions", "zeta"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateRoot, "sessions", "broken"), []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(stateRoot, "sessions", "alpha"), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := store.List()
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []string{"alpha", "broken", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("List() = %#v, want %#v", got, want)
	}
	if payload, err := os.ReadFile(filepath.Join(stateRoot, "sessions", "broken")); err != nil || string(payload) != "not a directory\n" {
		t.Fatalf("List() mutated unreadable entry: payload=%q err=%v", payload, err)
	}
}

func TestShimFleetRecordStoreConcurrentCreateHasOneKernelWinner(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	first, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore(first): %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore(second): %v", err)
	}
	defer func() { _ = second.Close() }()
	record, err := NewShimFleetRecord("fleet", "/repo", PresentationTmux, shimTestFleet())
	if err != nil {
		t.Fatalf("NewShimFleetRecord() error = %v", err)
	}

	start := make(chan struct{})
	errorsSeen := make(chan error, 2)
	for _, store := range []*ShimFleetRecordStore{first, second} {
		go func(store *ShimFleetRecordStore) {
			<-start
			errorsSeen <- store.Create(record)
		}(store)
	}
	close(start)
	results := []error{<-errorsSeen, <-errorsSeen}
	winners := 0
	losers := 0
	for _, result := range results {
		switch result {
		case nil:
			winners++
		default:
			var exists *ShimFleetExistsError
			if !errors.As(result, &exists) {
				t.Fatalf("Create() contender error = %T %v, want *ShimFleetExistsError", result, result)
			}
			losers++
		}
	}
	if winners != 1 || losers != 1 {
		t.Fatalf("concurrent Create() winners=%d losers=%d, want 1/1", winners, losers)
	}
}

func TestShimFleetRecordStoreRejectsFleetRecordSymlinkSubstitution(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	record := mustShimFleetRecord(t, "fleet", "/repo")
	if err := store.Create(record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	sessionDirectory := filepath.Join(stateRoot, "sessions", "fleet")
	if err := os.Rename(filepath.Join(sessionDirectory, "fleet.json"), filepath.Join(sessionDirectory, "substitute.json")); err != nil {
		t.Fatalf("rename fleet record: %v", err)
	}
	if err := os.Symlink("substitute.json", filepath.Join(sessionDirectory, "fleet.json")); err != nil {
		t.Fatalf("symlink fleet record: %v", err)
	}

	if _, err := store.Read("fleet"); err == nil {
		t.Fatal("Read() error = nil, want symlink-substitution refusal")
	}
}

func TestShimFleetRecordStoreRetainsFleetRecordWhileRoleArtifactsRemain(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	record := mustShimFleetRecord(t, "fleet", "/repo")
	if err := store.Create(record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	rolesPath := filepath.Join(stateRoot, "sessions", "fleet", "roles")
	if err := os.Mkdir(rolesPath, 0o700); err != nil {
		t.Fatalf("Mkdir(roles): %v", err)
	}
	if err := os.WriteFile(filepath.Join(rolesPath, "planner.json"), []byte("retained\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(role artifact): %v", err)
	}

	if err := store.RemoveOwned(record); err == nil {
		t.Fatal("RemoveOwned() error = nil, want retained-role refusal")
	}
	if _, err := store.Read("fleet"); err != nil {
		t.Fatalf("RemoveOwned() removed fleet record before retained role artifact check: %v", err)
	}
}

func TestShimFleetRecordStoreRetainsVisibleRecordOnCommitUncertainty(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	store.syncDir = func(*os.Root) error { return errors.New("injected directory sync failure") }
	record := mustShimFleetRecord(t, "fleet", "/repo")

	err = store.Create(record)
	var uncertain *shim.RecordCommitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("Create() error = %T %v, want *shim.RecordCommitUncertainError", err, err)
	}
	got, err := store.Read("fleet")
	if err != nil {
		t.Fatalf("Read() visible uncertain record: %v", err)
	}
	if !reflect.DeepEqual(got, record) {
		t.Fatalf("uncertain visible record = %#v, want %#v", got, record)
	}
}

func TestShimFleetRecordStoreRollsBackAndSyncsOnlyPreVisibleSessionReservation(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	store.writeRecord = func(*os.Root, []byte, func(*os.Root) error) (bool, error) {
		return false, errors.New("injected failure before fleet record rename")
	}
	syncCalls := 0
	store.syncDir = func(*os.Root) error {
		syncCalls++
		return nil
	}

	err = store.Create(mustShimFleetRecord(t, "fleet", "/repo"))
	if err == nil {
		t.Fatal("Create() error = nil, want injected pre-visible failure")
	}
	if _, statErr := os.Stat(filepath.Join(stateRoot, "sessions", "fleet")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("pre-visible session reservation remains: %v", statErr)
	}
	if syncCalls != 1 {
		t.Fatalf("rollback parent sync calls = %d, want 1", syncCalls)
	}
}

func TestShimFleetRecordStoreDecodesVersionBeforeUnknownFieldsAndRejectsNestedUnknownFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		cause   string
	}{
		{
			name:    "foreign version wins before unknown field",
			payload: `{"version":2,"future":true}`,
			cause:   "fleet record protocol version is not exactly 1",
		},
		{
			name:    "unknown role configuration field",
			payload: `{"version":1,"session":"fleet","directory":"/repo","presentation":"tmux","roster":["planner"],"roles":{"planner":{"harness":"claude","model":"","effort":"","future":true}}}`,
			cause:   `roles["planner"]: unknown field "future"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeShimFleetRecord([]byte(tt.payload), "fleet")
			var parse *ShimFleetRecordParseError
			if !errors.As(err, &parse) || parse.Cause != tt.cause {
				t.Fatalf("decodeShimFleetRecord() error = %T %v, want cause %q", err, err, tt.cause)
			}
		})
	}
}

func TestShimFleetRecordStoreConcurrentVersionCheckedReplacementHasOneMutationWinner(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	first, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore(first): %v", err)
	}
	defer func() { _ = first.Close() }()
	second, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore(second): %v", err)
	}
	defer func() { _ = second.Close() }()
	original := mustShimFleetRecord(t, "fleet", "/repo")
	if err := first.Create(original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	replacements := []ShimFleetRecord{
		shimFleetRecordWithRelaunchOverride(original, config.RoleConfig{Name: "planner", Harness: config.HarnessClaude, Model: "first", Effort: "max"}, "/repo"),
		shimFleetRecordWithRelaunchOverride(original, config.RoleConfig{Name: "planner", Harness: config.HarnessClaude, Model: "second", Effort: "max"}, "/repo"),
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for index, store := range []*ShimFleetRecordStore{first, second} {
		index, store := index, store
		go func() {
			<-start
			results <- store.ReplaceOwned(original, replacements[index])
		}()
	}
	close(start)
	errorsSeen := []error{<-results, <-results}
	winners, refusals := 0, 0
	for _, result := range errorsSeen {
		if result == nil {
			winners++
			continue
		}
		var conflict *ShimFleetMutationConflictError
		if !errors.As(result, &conflict) {
			t.Fatalf("ReplaceOwned() loser error = %T %v, want typed mutation conflict", result, result)
		}
		refusals++
	}
	if winners != 1 || refusals != 1 {
		t.Fatalf("concurrent ReplaceOwned() winners=%d refusals=%d, want 1/1", winners, refusals)
	}
	got, err := first.Read("fleet")
	if err != nil {
		t.Fatalf("Read() final record: %v", err)
	}
	model := got.Roles["planner"].Model
	if model != "first" && model != "second" {
		t.Fatalf("final planner model = %q, want one complete contender value", model)
	}
}

// This catches valid roster/config mutations silently changing the durable
// presentation chosen at fleet creation.
func TestShimFleetRecordStoreRejectsPresentationChangeForEveryMutationAPI(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		mutate func(ShimFleetRecord) ShimFleetRecord
		apply  func(*ShimFleetRecordStore, ShimFleetRecord, ShimFleetRecord) error
	}{
		{
			name: "replace",
			mutate: func(record ShimFleetRecord) ShimFleetRecord {
				return shimFleetRecordWithRelaunchOverride(record, config.RoleConfig{Name: "planner", Harness: config.HarnessClaude, Model: "next"}, "/repo")
			},
			apply: func(store *ShimFleetRecordStore, expected, replacement ShimFleetRecord) error {
				return store.ReplaceOwned(expected, replacement)
			},
		},
		{
			name: "extend",
			mutate: func(record ShimFleetRecord) ShimFleetRecord {
				replacement := record
				replacement.Roster = append(append([]string(nil), record.Roster...), "reviewer")
				replacement.Roles = map[string]ShimFleetRoleRecord{"planner": record.Roles["planner"], "coder": record.Roles["coder"], "reviewer": {Harness: "codex"}}
				return replacement
			},
			apply: func(store *ShimFleetRecordStore, expected, replacement ShimFleetRecord) error {
				return store.ExtendOwned(expected, replacement)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stateRoot := t.TempDir()
			if err := os.Chmod(stateRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			store, err := OpenShimFleetRecordStore(stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			expected := mustShimFleetRecord(t, "fleet", "/repo")
			if err := store.Create(expected); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(stateRoot, "sessions", "fleet", "fleet.json"))
			if err != nil {
				t.Fatal(err)
			}
			replacement := test.mutate(expected)
			replacement.Presentation = PresentationDetached
			err = test.apply(store, expected, replacement)
			var presentation *ShimFleetPresentationMutationError
			if !errors.As(err, &presentation) || presentation.Expected != PresentationTmux || presentation.Replacement != PresentationDetached {
				t.Fatalf("mutation error = %T %#v, want typed tmux-to-detached refusal", err, presentation)
			}
			after, err := os.ReadFile(filepath.Join(stateRoot, "sessions", "fleet", "fleet.json"))
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("mutation changed durable bytes: after=%q before=%q err=%v", after, before, err)
			}
			observed, err := store.Read("fleet")
			if err != nil || !reflect.DeepEqual(observed, expected) {
				t.Fatalf("Read() after mutation = %#v, %v; want unchanged %#v", observed, err, expected)
			}
		})
	}
}

func TestShimFleetRecordStoreReplacementCommitUncertaintyRetainsVisibleReplacement(t *testing.T) {
	t.Parallel()

	stateRoot := t.TempDir()
	if err := os.Chmod(stateRoot, 0o700); err != nil {
		t.Fatalf("Chmod(state root): %v", err)
	}
	store, err := OpenShimFleetRecordStore(stateRoot)
	if err != nil {
		t.Fatalf("OpenShimFleetRecordStore() error = %v", err)
	}
	defer func() { _ = store.Close() }()
	original := mustShimFleetRecord(t, "fleet", "/repo")
	if err := store.Create(original); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	replacement := shimFleetRecordWithRelaunchOverride(
		original,
		config.RoleConfig{Name: "planner", Harness: config.HarnessClaude, Model: "override", Effort: "max"},
		"/override",
	)
	store.syncDir = func(*os.Root) error { return errors.New("injected replacement directory sync failure") }

	err = store.ReplaceOwned(original, replacement)
	var uncertain *shim.RecordCommitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("ReplaceOwned() error = %T %v, want *shim.RecordCommitUncertainError", err, err)
	}
	got, err := store.Read("fleet")
	if err != nil {
		t.Fatalf("Read() visible uncertain replacement: %v", err)
	}
	if !reflect.DeepEqual(got, replacement) {
		t.Fatalf("visible uncertain replacement = %#v, want %#v", got, replacement)
	}
}

func TestShimLauncherRecordsWholeFleetBeforeStartingShimWindows(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events}
	presentation := &fakeShimPresentation{
		events:  events,
		session: tmuxx.CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321},
		windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
	}
	lifecycle := &fakeShimLifecycle{
		events: events,
		observe: []shim.Response{
			runningShimResponse(4321, 7001),
			runningShimResponse(5432, 7002),
		},
	}
	launcher := NewShimLauncher(presentation, lifecycle, records, ShimLaunchDependencies{
		LookPath: func(name string) (string, error) {
			events.add("look:" + name)
			return "/tools/" + name, nil
		},
		Executable: func() (string, error) {
			events.add("self")
			return "/current agentctl", nil
		},
		Getwd: func() (string, error) { return "/repo path", nil },
		Stat:  func(string) (os.FileInfo, error) { return testFileInfo{mode: os.ModeDir | 0o755}, nil },
	})

	result, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if result.Session != (tmuxx.Session{ID: "$4", Name: "fleet"}) || result.TotalRoles != 2 {
		t.Fatalf("Launch() = %#v, want presentation $4 and two ready roles", result)
	}
	wantEvents := []string{
		"self", "look:tmux", "look:amq", "look:claude", "look:codex",
		"record:fleet:planner,coder",
		"presentation-session:planner:exec '/current agentctl' '__shim' '--session' 'fleet' '--role' 'planner' '--harness' 'claude' '--effort' 'max'",
		"observe:planner",
		"presentation-window:coder:exec '/current agentctl' '__shim' '--session' 'fleet' '--role' 'coder' '--harness' 'codex' '--model' 'gpt-5.6-sol' '--effort' 'high'",
		"observe:coder",
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, wantEvents) {
		t.Fatalf("launch events = %#v, want %#v", got, wantEvents)
	}
}

// This catches a presentation-blind launcher that creates tmux windows or
// requires tmux before directly spawning detached roles in durable roster
// order, and a launcher that compares readiness to anything but spawn PID.
func TestShimLauncherStartsDetachedRolesDirectlyInRosterOrder(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}, {pid: 5432, wait: make(chan error)}}}
	records := &fakeShimFleetRecords{events: events}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001), runningShimResponse(5432, 7002)}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	dependencies.Environment = func() []string {
		return []string{"PATH=/usr/bin", "AGENTCTL_RUNTIME_ROOT=/runtime", "AGENTCTL_STATE_ROOT=/state"}
	}
	launcher := NewShimLauncher(nil, lifecycle, records, dependencies)

	result, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationDetached, nil)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if result.Session.ID != "" || result.TotalRoles != 2 || result.Directory != "/repo path" {
		t.Fatalf("Launch() = %#v, want detached no-presentation result for two roles", result)
	}
	if got, want := events.snapshot(), []string{
		"self", "look:amq", "look:claude", "look:codex", "record:fleet:planner,coder",
		"detached-start:planner", "observe:planner", "detached-start:coder", "observe:coder",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detached launch events = %#v, want %#v", got, want)
	}
	if len(starter.requests) != 2 {
		t.Fatalf("detached starts = %d, want 2", len(starter.requests))
	}
	if got := starter.processesSeen[0].waitCalls; got != 1 {
		t.Fatalf("planner waiter registrations = %d, want one", got)
	}
	first := starter.requests[0]
	if !reflect.DeepEqual(first.Argv, []string{"/current agentctl", "__shim", "--session", "fleet", "--role", "planner", "--harness", "claude", "--effort", "max"}) || first.Directory != "/repo path" || !reflect.DeepEqual(first.Environment, []string{"PATH=/usr/bin", "AGENTCTL_RUNTIME_ROOT=/runtime", "AGENTCTL_STATE_ROOT=/state", "AGENTCTL_SESSION=fleet", "AGENTCTL_ROLE=planner", "AGENTCTL_MANAGED=1"}) || first.Stdin == nil || first.Stdout == nil || first.Stderr == nil {
		t.Fatalf("first detached request = %#v, want direct fully-typed planner invocation", first)
	}
}

// This catches detached launch routing through a shell or tmux presentation
// instead of the sole typed direct-starter boundary with element-separated
// argv and no tmux executable lookup.
func TestShimLauncherDetachedUsesOnlyTypedDirectStarterBoundary(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	dependencies.LookPath = func(name string) (string, error) {
		events.add("look:" + name)
		if name == "tmux" || name == "sh" {
			return "", fmt.Errorf("unexpected detached launcher dependency %q", name)
		}
		return "/tools/" + name, nil
	}
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}}, &fakeShimFleetRecords{events: events}, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude, Model: "gpt-5.6-sol", Effort: "high"}}}, PresentationDetached, nil)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if got, want := events.snapshot(), []string{"self", "look:amq", "look:claude", "record:fleet:planner", "detached-start:planner", "observe:planner"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("detached boundary events = %#v, want %#v", got, want)
	}
	if got, want := starter.requests[0].Argv, []string{"/current agentctl", "__shim", "--session", "fleet", "--role", "planner", "--harness", "claude", "--model", "gpt-5.6-sol", "--effort", "high"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("typed direct argv = %#v, want %#v", got, want)
	}
}

// This catches detached rollback treating a failed Start as creation
// provenance and issuing a stop against a role for which it owns no process.
func TestShimLauncherDetachedSecondStartFailureStopsOnlyStartedRole(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}, errs: []error{nil, errors.New("start failed")}}
	records := &fakeShimFleetRecords{events: events}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}, stop: []shim.Response{stoppedShimResponse(7001)}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	launcher := NewShimLauncher(nil, lifecycle, records, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationDetached, nil)
	var failed *ShimDetachedStartFailedError
	if !errors.As(err, &failed) {
		t.Fatalf("Launch() error = %T %v, want no-child detached start failure", err, err)
	}
	if got, want := events.snapshot(), []string{"self", "look:amq", "look:claude", "look:codex", "record:fleet:planner,coder", "detached-start:planner", "observe:planner", "detached-start:coder", "stop:planner", "record-remove"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("no-child rollback events = %#v, want %#v", got, want)
	}
}

// This catches pre-start descriptor failures leaking through to the detached
// starter, or retaining a fleet record after no process creation was proved.
func TestShimLauncherDetachedPreStartDescriptorFailuresStartNoChild(t *testing.T) {
	t.Parallel()

	for _, failure := range []int{1, 2, 3} {
		failure := failure
		t.Run(fmt.Sprintf("descriptor %d", failure), func(t *testing.T) {
			events := &shimEventLog{}
			starter := &fakeDetachedShimStarter{events: events}
			dependencies := shimLaunchTestDependencies(events)
			dependencies.DetachedStarter = starter
			opened := 0
			dependencies.OpenDevNull = func() (*os.File, error) {
				opened++
				if opened == failure {
					return nil, errors.New("open /dev/null denied")
				}
				return os.OpenFile("/dev/null", os.O_RDWR, 0)
			}
			launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: events}, &fakeShimFleetRecords{events: events}, dependencies)

			_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}, PresentationDetached, nil)
			var failed *ShimDetachedStartFailedError
			if !errors.As(err, &failed) || failed.Cause == nil || failed.Cause.Error() != "open /dev/null denied" {
				t.Fatalf("Launch() error = %T %v, want typed descriptor start failure", err, err)
			}
			if len(starter.requests) != 0 {
				t.Fatalf("detached starter requests = %#v, want no child start", starter.requests)
			}
			if got, want := events.snapshot(), []string{"self", "look:amq", "look:claude", "record:fleet:planner", "record-remove"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("pre-start failure events = %#v, want %#v", got, want)
			}
		})
	}
}

// This catches startDetached leaking a parent-owned descriptor that was opened
// before a later standard-descriptor open fails.
func TestShimLauncherDetachedPartialDescriptorFailureClosesEveryOpenedHandle(t *testing.T) {
	t.Parallel()

	for _, failure := range []int{1, 2, 3} {
		failure := failure
		t.Run(fmt.Sprintf("descriptor %d", failure), func(t *testing.T) {
			events := &shimEventLog{}
			starter := &fakeDetachedShimStarter{events: events}
			dependencies := shimLaunchTestDependencies(events)
			dependencies.DetachedStarter = starter
			openErr := errors.New("open /dev/null denied")
			opened := make([]*os.File, 0, failure-1)
			calls := 0
			path := filepath.Join(t.TempDir(), "descriptor")
			dependencies.OpenDevNull = func() (*os.File, error) {
				calls++
				if calls == failure {
					return nil, openErr
				}
				file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
				if err == nil {
					opened = append(opened, file)
				}
				return file, err
			}
			launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: events}, &fakeShimFleetRecords{events: events}, dependencies)

			_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}, PresentationDetached, nil)
			var failed *ShimDetachedStartFailedError
			if !errors.As(err, &failed) || !errors.Is(err, openErr) {
				t.Fatalf("Launch() error = %T %v, want typed partial-open failure", err, err)
			}
			if len(starter.requests) != 0 {
				t.Fatalf("detached starter requests = %#v, want no child start", starter.requests)
			}
			if got, want := events.snapshot(), []string{"self", "look:amq", "look:claude", "record:fleet:planner", "record-remove"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("partial-open failure events = %#v, want %#v", got, want)
			}
			for _, file := range opened {
				if _, statErr := file.Stat(); statErr == nil {
					t.Fatalf("opened descriptor FD %d remains open after partial failure", file.Fd())
				}
			}
		})
	}
}

// This catches a later no-child Start failure with failed earlier-role cleanup
// escaping through generic rollback instead of retaining exact detached facts.
func TestShimLauncherDetachedLaterStartFailureReportsRemainingArtifacts(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}, errs: []error{nil, errors.New("second start failed")}}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}, stop: []shim.Response{stoppedShimRetainedResponse(7001, shim.ProcessPresentMatch)}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	launcher := NewShimLauncher(nil, lifecycle, &fakeShimFleetRecords{events: events}, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationDetached, nil)
	var failed *ShimDetachedStartFailedError
	if !errors.As(err, &failed) || failed.Remaining != "role planner and durable fleet record" || failed.CleanupErr == nil {
		t.Fatalf("Launch() error = %#v, want typed no-child failure retaining planner and record", err)
	}
}

// This catches the production mutation that includes a role whose ready PID
// disagrees with direct-spawn provenance in role-addressed rollback.
func TestShimLauncherDetachedOwnerDisagreementDoesNotStopRespondingPeerOrRemoveRecord(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}
	records := &fakeShimFleetRecords{events: events}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(9001, 7001)}, stop: []shim.Response{stoppedShimResponse(7001)}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	launcher := NewShimLauncher(nil, lifecycle, records, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}, PresentationDetached, nil)
	var disagreement *ShimReadyOwnerDisagreementError
	if !errors.As(err, &disagreement) {
		t.Fatalf("Launch() error = %T %v, want ready-owner disagreement", err, err)
	}
	for _, event := range events.snapshot() {
		if event == "stop:planner" || event == "record-remove" {
			t.Fatalf("owner disagreement mutated responding peer or fleet record: events=%#v", events.snapshot())
		}
	}
}

// This catches a detached request that inherits conflicting ambient metadata
// instead of replacing it with the exact session, role, and managed facts.
func TestShimLauncherDetachedRequestAddsRoleInformationalEnvironment(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	dependencies.Environment = func() []string {
		return []string{"PATH=/usr/bin", "AGENTCTL_SESSION=wrong", "AGENTCTL_ROLE=wrong", "AGENTCTL_MANAGED=0", "AGENTCTL_RUNTIME_ROOT=/runtime", "AGENTCTL_STATE_ROOT=/state"}
	}
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}}, &fakeShimFleetRecords{events: events}, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}, PresentationDetached, nil)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	want := []string{"PATH=/usr/bin", "AGENTCTL_RUNTIME_ROOT=/runtime", "AGENTCTL_STATE_ROOT=/state", "AGENTCTL_SESSION=fleet", "AGENTCTL_ROLE=planner", "AGENTCTL_MANAGED=1"}
	if got := starter.requests[0].Environment; !reflect.DeepEqual(got, want) {
		t.Fatalf("detached environment = %#v, want %#v", got, want)
	}
}

// This catches opening every detached standard descriptor read-only, which
// prevents a hidden shim from writing diagnostics on stdout or stderr.
func TestShimLauncherDetachedOpensWritableStandardDescriptors(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	starter := &writingDetachedShimStarter{fakeDetachedShimStarter: fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: make(chan error)}}}}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}}, &fakeShimFleetRecords{events: events}, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}, PresentationDetached, nil)
	if err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if starter.writeErr != nil {
		t.Fatalf("detached stdout/stderr write = %v, want writable descriptors", starter.writeErr)
	}
	request := starter.requests[0]
	for _, descriptor := range []*os.File{request.Stdin, request.Stdout, request.Stderr} {
		if _, err := descriptor.Stat(); err == nil {
			t.Fatalf("launcher retained detached descriptor %v after Start", descriptor.Fd())
		}
	}
}

// This catches accepting an observed running response when the spawned shim's
// asynchronous waiter reports exit during the runtime observation.
func TestShimLauncherDetachedWaiterExitDuringObserveBeatsRunning(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	exited := make(chan error, 1)
	starter := &fakeDetachedShimStarter{events: events, processes: []*fakeDetachedShimProcess{{pid: 4321, wait: exited}}}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{runningShimResponse(4321, 7001)}, onObserve: func() { exited <- errors.New("shim exited") }}
	dependencies := shimLaunchTestDependencies(events)
	dependencies.DetachedStarter = starter
	launcher := NewShimLauncher(nil, lifecycle, &fakeShimFleetRecords{events: events}, dependencies)

	_, err := launcher.Launch(context.Background(), "fleet", config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}}, PresentationDetached, nil)
	if err == nil || strings.Contains(err.Error(), "reported running") {
		t.Fatalf("Launch() error = %v, want waiter exit to outrank running", err)
	}
}

// This catches selecting cancellation or deadline nondeterministically when
// the direct child's waiter has already established its exact exit fact.
func TestShimLauncherDetachedWaiterExitHasDeterministicPriorityOverCancellationAndDeadline(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		ctx  func() context.Context
		now  func() time.Time
	}{
		{
			name: "cancelled context", ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			}, now: func() time.Time { return time.Unix(1000, 0) },
		},
		{
			name: "deadline", ctx: context.Background,
			now: func() time.Time { return time.Unix(1000, 0).Add(shimLaunchObservationTimeout) },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := &shimEventLog{}
			wait := make(chan error, 1)
			wait <- errors.New("exit status 1")
			launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: events}, nil, ShimLaunchDependencies{Now: test.now})

			err := launcher.waitDetachedReady(test.ctx(), "fleet", "planner", &fakeDetachedShimProcess{pid: 4321, wait: wait})
			var exited *detachedShimExitedError
			got, want := "", "detached shim PID 4321 exited before readiness: exit status 1"
			if err != nil {
				got = err.Error()
			}
			if !errors.As(err, &exited) || got != want {
				t.Fatalf("waitDetachedReady() error = %v, want authoritative waiter exit", err)
			}
			if got := events.snapshot(); len(got) != 0 {
				t.Fatalf("waiter-established exit observed runtime anyway: events=%#v", got)
			}
		})
	}
}

// This catches a final readiness response losing to a cancellation delivered
// during Observe after the waiter has been reconciled for that observation.
func TestShimLauncherDetachedMatchingRunningBeatsCancellationAfterObserve(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{
		events: &shimEventLog{}, observe: []shim.Response{runningShimResponse(4321, 7001)}, onObserve: cancel,
	}, nil, ShimLaunchDependencies{})

	if err := launcher.waitDetachedReady(ctx, "fleet", "planner", &fakeDetachedShimProcess{pid: 4321, wait: make(chan error)}); err != nil {
		t.Fatalf("waitDetachedReady() error = %v, want matching running response", err)
	}
}

// This catches a timeout path that returns uncertainty without one final
// waiter sample after an observation reaches the readiness deadline.
func TestShimLauncherDetachedFinalWaiterCheckBeatsDeadline(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	nowCalls := 0
	wait := make(chan error, 1)
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{
		events: &shimEventLog{}, observe: []shim.Response{{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStarting}},
		onObserve: func() { wait <- errors.New("exit status 1") },
	}, nil, ShimLaunchDependencies{
		Now: func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return start
			}
			return start.Add(shimLaunchObservationTimeout)
		},
	})

	err := launcher.waitDetachedReady(context.Background(), "fleet", "planner", &fakeDetachedShimProcess{pid: 4321, wait: wait})
	var exited *detachedShimExitedError
	if !errors.As(err, &exited) {
		t.Fatalf("waitDetachedReady() error = %T %v, want final waiter exit", err, err)
	}
}

// This catches returning uncertainty after the deadline clock reports arrival
// without reconciling a direct-child waiter completion delivered at that exact
// deadline decision.
func TestShimLauncherDetachedWaiterExitAtDeadlineDecisionBeatsUncertainty(t *testing.T) {
	t.Parallel()

	start := time.Unix(1000, 0)
	nowCalls := 0
	wait := make(chan error, 1)
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{
		events: &shimEventLog{}, observe: []shim.Response{{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStarting}},
	}, nil, ShimLaunchDependencies{
		Now: func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return start
			}
			wait <- errors.New("exit status 1")
			return start.Add(shimLaunchObservationTimeout)
		},
	})

	err := launcher.waitDetachedReady(context.Background(), "fleet", "planner", &fakeDetachedShimProcess{pid: 4321, wait: wait})
	var exited *detachedShimExitedError
	if !errors.As(err, &exited) {
		t.Fatalf("waitDetachedReady() error = %T %v, want deadline waiter exit", err, err)
	}
}

// This catches applying the readiness deadline before the matching response
// observed on its inclusive boundary, turning a ready direct child uncertain.
func TestShimLauncherDetachedAcceptsMatchingRunningAtInclusiveDeadline(t *testing.T) {
	t.Parallel()

	nowCalls := 0
	start := time.Unix(1000, 0)
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: &shimEventLog{}, observe: []shim.Response{runningShimResponse(4321, 7001)}}, nil, ShimLaunchDependencies{
		Now: func() time.Time {
			nowCalls++
			if nowCalls == 1 {
				return start
			}
			return start.Add(shimLaunchObservationTimeout)
		},
	})
	err := launcher.waitDetachedReady(context.Background(), "fleet", "planner", &fakeDetachedShimProcess{pid: 4321, wait: make(chan error)})
	if err != nil {
		t.Fatalf("waitDetachedReady() error = %v, want inclusive-boundary running", err)
	}
}

// This catches an exact-child exit error that hides a non-nil Wait cause from
// the factual detached-start renderer and callers.
func TestDetachedShimExitedErrorIncludesWaitCause(t *testing.T) {
	t.Parallel()

	err := detachedExitError(41, errors.New("exit status 1"))
	if got, want := err.Error(), "detached shim PID 41 exited before readiness: exit status 1"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestShimLauncherWaitsPastInnerReadinessBoundaryForPublishedRunning(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	responses := make([]shim.Response, 0, 103)
	for range 102 {
		responses = append(responses, shim.Response{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStarting})
	}
	responses = append(responses, runningShimResponse(4321, 7001))
	lifecycle := &fakeShimLifecycle{events: events, observe: responses}
	now := time.Unix(1000, 0)
	launcher := NewShimLauncher(nil, lifecycle, nil, ShimLaunchDependencies{
		Now:   func() time.Time { return now },
		Sleep: func(duration time.Duration) { now = now.Add(duration) },
	})

	if err := launcher.waitReady(context.Background(), "fleet", "coder", 4321); err != nil {
		t.Fatalf("waitReady() error = %v, want running published just after the inner 5s readiness boundary", err)
	}
	if elapsed := now.Sub(time.Unix(1000, 0)); elapsed != 102*ptyx.ReadinessPollInterval {
		t.Fatalf("waitReady() elapsed = %s, want %s", elapsed, 102*ptyx.ReadinessPollInterval)
	}
}

func TestShimLauncherTreatsPreStartMissingAsTransientUntilCreatedShimRuns(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	lifecycle := &fakeShimLifecycle{events: events, observe: []shim.Response{
		{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeMissing},
		runningShimResponse(4321, 7001),
	}}
	now := time.Unix(1000, 0)
	launcher := NewShimLauncher(nil, lifecycle, nil, ShimLaunchDependencies{
		Now:   func() time.Time { return now },
		Sleep: func(duration time.Duration) { now = now.Add(duration) },
	})

	if err := launcher.waitReady(context.Background(), "fleet", "coder", 4321); err != nil {
		t.Fatalf("waitReady() error = %v, want pre-start missing retried until the created shim publishes running", err)
	}
	if elapsed := now.Sub(time.Unix(1000, 0)); elapsed != ptyx.ReadinessPollInterval {
		t.Fatalf("waitReady() elapsed = %s, want one poll interval", elapsed)
	}
}

func TestShimLauncherReportsLastObservedOutcomeWhenReadinessTimesOut(t *testing.T) {
	t.Parallel()

	observationCount := int(shimLaunchObservationTimeout/ptyx.ReadinessPollInterval) + 1
	responses := make([]shim.Response, observationCount)
	for index := range responses {
		responses[index] = shim.Response{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeMissing}
	}
	now := time.Unix(1000, 0)
	launcher := NewShimLauncher(nil, &fakeShimLifecycle{events: &shimEventLog{}, observe: responses}, nil, ShimLaunchDependencies{
		Now:   func() time.Time { return now },
		Sleep: func(duration time.Duration) { now = now.Add(duration) },
	})

	err := launcher.waitReady(context.Background(), "fleet", "coder", 4321)
	want := "role \"coder\" in session \"fleet\" reported missing while launch waited for running"
	if err == nil || err.Error() != want {
		t.Fatalf("waitReady() error = %v, want %q", err, want)
	}
}

func TestShimLauncherRetainsFleetRecordWhenPresentationFailureReturnsNoOwnerIDs(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events}
	presentation := &fakeShimPresentation{
		events:     events,
		sessionErr: errors.New("tmux command result was indeterminate"),
	}
	launcher := NewShimLauncher(
		presentation,
		&fakeShimLifecycle{events: events},
		records,
		shimLaunchTestDependencies(events),
	)

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	var rollback *ShimLaunchRollbackError
	if !errors.As(err, &rollback) || rollback.CleanupErr == nil {
		t.Fatalf("Launch() error = %T %v, want retained ambiguous-ownership rollback", err, err)
	}
	for _, event := range events.snapshot() {
		if strings.HasPrefix(event, "stop:") || strings.HasPrefix(event, "presentation-session-remove:") || event == "record-remove" {
			t.Fatalf("ownerless presentation failure allowed destructive rollback: events=%#v", events.snapshot())
		}
	}
}

func TestShimLauncherRetainsReadyPeersWhenLaterPresentationFailureReturnsNoOwnerIDs(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events}
	presentation := &fakeShimPresentation{
		events:     events,
		session:    tmuxx.CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321},
		windowErrs: []error{errors.New("tmux command result was indeterminate")},
	}
	lifecycle := &fakeShimLifecycle{
		events:  events,
		observe: []shim.Response{runningShimResponse(4321, 7001)},
		stop:    []shim.Response{stoppedShimResponse(7001)},
	}
	launcher := NewShimLauncher(presentation, lifecycle, records, shimLaunchTestDependencies(events))

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	var rollback *ShimLaunchRollbackError
	if !errors.As(err, &rollback) || rollback.CleanupErr == nil {
		t.Fatalf("Launch() error = %T %v, want retained ambiguous-ownership rollback", err, err)
	}
	for _, event := range events.snapshot() {
		if strings.HasPrefix(event, "stop:") || strings.HasPrefix(event, "presentation-session-remove:") || event == "record-remove" {
			t.Fatalf("later ownerless presentation failure allowed destructive rollback: events=%#v", events.snapshot())
		}
	}
}

func TestShimLauncherRollbackStopsChildrenBeforeRemovingOwnedPresentationAndRecord(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events}
	presentation := &fakeShimPresentation{
		events:  events,
		session: tmuxx.CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321},
		windows: []tmuxx.CreatedWindow{{WindowID: "@8", PaneID: "%10", PanePID: 5432}},
	}
	lifecycle := &fakeShimLifecycle{
		events:  events,
		observe: []shim.Response{runningShimResponse(4321, 7001), {Version: 1, Outcome: shim.OutcomeOrphan}},
		stop:    []shim.Response{stoppedShimResponse(7002), stoppedShimResponse(7001)},
	}
	launcher := NewShimLauncher(presentation, lifecycle, records, shimLaunchTestDependencies(events))

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	var rollback *ShimLaunchRollbackError
	if !errors.As(err, &rollback) {
		t.Fatalf("Launch() error = %T %v, want *ShimLaunchRollbackError", err, err)
	}
	if rollback.Role != "coder" || rollback.CleanupErr != nil {
		t.Fatalf("ShimLaunchRollbackError = %#v, want clean rollback of coder failure", rollback)
	}
	wantTail := []string{
		"presentation-window:coder:exec '/current agentctl' '__shim' '--session' 'fleet' '--role' 'coder' '--harness' 'codex' '--model' 'gpt-5.6-sol' '--effort' 'high'",
		"observe:coder",
		"stop:coder",
		"stop:planner",
		"presentation-session-remove:$4",
		"record-remove",
	}
	got := events.snapshot()
	if len(got) < len(wantTail) || !reflect.DeepEqual(got[len(got)-len(wantTail):], wantTail) {
		t.Fatalf("rollback event tail = %#v, want %#v (child stop before presentation/record removal)", got, wantTail)
	}
}

func TestShimLauncherRetainsEverythingWhenRollbackDoesNotObserveChildExit(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events}
	presentation := &fakeShimPresentation{
		events:  events,
		session: tmuxx.CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321},
	}
	retained := stoppedShimRetainedResponse(7001, shim.ProcessPresentMatch)
	lifecycle := &fakeShimLifecycle{
		events:  events,
		observe: []shim.Response{{Version: 1, Outcome: shim.OutcomeOrphan}},
		stop:    []shim.Response{retained},
	}
	launcher := NewShimLauncher(presentation, lifecycle, records, shimLaunchTestDependencies(events))

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	var rollback *ShimLaunchRollbackError
	if !errors.As(err, &rollback) || rollback.CleanupErr == nil {
		t.Fatalf("Launch() error = %T %v, want incomplete *ShimLaunchRollbackError", err, err)
	}
	for _, event := range events.snapshot() {
		if event == "presentation-session-remove:$4" || event == "record-remove" {
			t.Fatalf("indeterminate rollback mutated retained ownership: events=%#v", events.snapshot())
		}
	}
}

func TestShimLauncherRetainsEverythingWhenRollbackExitWasObservedWithoutSignalAttempt(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{events: events}
	presentation := &fakeShimPresentation{
		events:  events,
		session: tmuxx.CreatedSession{SessionID: "$4", WindowID: "@7", PaneID: "%9", PanePID: 4321},
	}
	attempted := false
	signal := "SIGHUP"
	exited := true
	lifecycle := &fakeShimLifecycle{
		events:  events,
		observe: []shim.Response{{Version: 1, Outcome: shim.OutcomeOrphan}},
		stop: []shim.Response{{
			Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStopChildExited,
			SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exited,
		}},
	}
	launcher := NewShimLauncher(presentation, lifecycle, records, shimLaunchTestDependencies(events))

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	var rollback *ShimLaunchRollbackError
	if !errors.As(err, &rollback) || rollback.CleanupErr == nil {
		t.Fatalf("Launch() error = %T %v, want incomplete rollback without signal-attempt fact", err, err)
	}
	for _, event := range events.snapshot() {
		if event == "presentation-session-remove:$4" || event == "record-remove" {
			t.Fatalf("signal-attempt/exit fact collapse allowed cleanup: events=%#v", events.snapshot())
		}
	}
}

func TestShimLauncherRecordCommitUncertainStopsBeforePresentationMutation(t *testing.T) {
	t.Parallel()

	events := &shimEventLog{}
	records := &fakeShimFleetRecords{
		events:    events,
		createErr: &shim.RecordCommitUncertainError{Err: errors.New("directory sync failed")},
	}
	presentation := &fakeShimPresentation{events: events}
	lifecycle := &fakeShimLifecycle{events: events}
	launcher := NewShimLauncher(presentation, lifecycle, records, shimLaunchTestDependencies(events))

	_, err := launcher.Launch(context.Background(), "fleet", shimTestFleet(), PresentationTmux, nil)
	var uncertain *shim.RecordCommitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("Launch() error = %T %v, want *shim.RecordCommitUncertainError", err, err)
	}
	for _, event := range events.snapshot() {
		if strings.HasPrefix(event, "presentation-") || strings.HasPrefix(event, "observe:") || strings.HasPrefix(event, "stop:") || event == "record-remove" {
			t.Fatalf("uncertain fleet commit allowed mutation: events=%#v", events.snapshot())
		}
	}
}

func shimTestFleet() config.FleetConfig {
	return config.FleetConfig{Roles: []config.RoleConfig{
		{Name: "planner", Harness: config.HarnessClaude, Effort: "max"},
		{Name: "coder", Harness: config.HarnessCodex, Model: "gpt-5.6-sol", Effort: "high"},
	}}
}

func runningShimResponse(shimPID, childPID int) shim.Response {
	state := "running"
	return shim.Response{
		Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeRunning, State: &state,
		ShimPID: &shimPID, ChildPID: &childPID,
	}
}

func stoppedShimResponse(childPID int) shim.Response {
	attempted := true
	signal := "SIGHUP"
	exited := true
	return shim.Response{
		Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStopChildExited,
		ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exited,
	}
}

func stoppedShimRetainedResponse(childPID int, observation shim.ProcessObservation) shim.Response {
	attempted := true
	signal := "SIGHUP"
	exited := false
	state := string(observation)
	return shim.Response{
		Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeStopChildRetained,
		ChildPID: &childPID, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exited,
		State: &state,
	}
}

func shimLaunchTestDependencies(events *shimEventLog) ShimLaunchDependencies {
	return ShimLaunchDependencies{
		LookPath: func(name string) (string, error) {
			events.add("look:" + name)
			return "/tools/" + name, nil
		},
		Executable: func() (string, error) {
			events.add("self")
			return "/current agentctl", nil
		},
		Getwd: func() (string, error) { return "/repo path", nil },
		Stat:  func(string) (os.FileInfo, error) { return testFileInfo{mode: os.ModeDir | 0o755}, nil },
	}
}

type shimEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *shimEventLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *shimEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fakeShimFleetRecords struct {
	events     *shimEventLog
	record     ShimFleetRecord
	createErr  error
	replaceErr error
	replaced   *ShimFleetRecord
}

func (f *fakeShimFleetRecords) Create(record ShimFleetRecord) error {
	f.record = record
	f.events.add("record:" + record.Session + ":" + joinShimRoster(record.Roster))
	return f.createErr
}

func (f *fakeShimFleetRecords) Read(session string) (ShimFleetRecord, error) {
	f.events.add("record-read:" + session)
	return f.record, nil
}

func (f *fakeShimFleetRecords) ReplaceOwned(_ ShimFleetRecord, replacement ShimFleetRecord) error {
	f.events.add("record-replace:" + replacement.Session)
	f.replaced = &replacement
	if f.replaceErr == nil {
		f.record = replacement
	}
	return f.replaceErr
}

func (f *fakeShimFleetRecords) RemoveOwned(ShimFleetRecord) error {
	f.events.add("record-remove")
	return nil
}

type fakeShimPresentation struct {
	events      *shimEventLog
	session     tmuxx.CreatedSession
	sessionErr  error
	windows     []tmuxx.CreatedWindow
	windowErrs  []error
	found       *tmuxx.Session
	directories []string
}

func (f *fakeShimPresentation) FindPresentationSession(_ context.Context, name string) (tmuxx.Session, bool, error) {
	f.events.add("presentation-find:" + name)
	if f.found == nil {
		return tmuxx.Session{}, false, nil
	}
	return *f.found, true, nil
}

func (f *fakeShimPresentation) CreatePresentationSession(_ context.Context, _, role, _, command string) (tmuxx.CreatedSession, error) {
	f.events.add("presentation-session:" + role + ":" + command)
	return f.session, f.sessionErr
}

func (f *fakeShimPresentation) CreatePresentationWindow(_ context.Context, _ tmuxx.SessionID, role, directory, command string) (tmuxx.CreatedWindow, error) {
	f.events.add("presentation-window:" + role + ":" + command)
	f.directories = append(f.directories, directory)
	if len(f.windowErrs) > 0 {
		err := f.windowErrs[0]
		f.windowErrs = f.windowErrs[1:]
		if err != nil {
			return tmuxx.CreatedWindow{}, err
		}
	}
	window := f.windows[0]
	f.windows = f.windows[1:]
	return window, nil
}

func (f *fakeShimPresentation) RemovePresentationWindow(_ context.Context, id tmuxx.WindowID) error {
	f.events.add("presentation-window-remove:" + string(id))
	return nil
}

func (f *fakeShimPresentation) RemovePresentationSession(_ context.Context, id tmuxx.SessionID) error {
	f.events.add("presentation-session-remove:" + string(id))
	return nil
}

type fakeShimLifecycle struct {
	events    *shimEventLog
	observe   []shim.Response
	stop      []shim.Response
	onObserve func()
}

type fakeDetachedShimStarter struct {
	events        *shimEventLog
	processes     []*fakeDetachedShimProcess
	processesSeen []*fakeDetachedShimProcess
	requests      []DetachedShimRequest
	errs          []error
}

func (f *fakeDetachedShimStarter) Start(request DetachedShimRequest) (DetachedShimProcess, error) {
	f.requests = append(f.requests, request)
	role := "invalid"
	for index, argument := range request.Argv {
		if argument == "--role" && index+1 < len(request.Argv) {
			role = request.Argv[index+1]
			break
		}
	}
	f.events.add("detached-start:" + role)
	if len(f.errs) > 0 {
		err := f.errs[0]
		f.errs = f.errs[1:]
		if err != nil {
			return nil, err
		}
	}
	process := f.processes[0]
	f.processes = f.processes[1:]
	f.processesSeen = append(f.processesSeen, process)
	return process, nil
}

type fakeDetachedShimProcess struct {
	pid       int
	wait      chan error
	waitCalls int
}

func (p *fakeDetachedShimProcess) PID() int           { return p.pid }
func (p *fakeDetachedShimProcess) Wait() <-chan error { p.waitCalls++; return p.wait }

type writingDetachedShimStarter struct {
	fakeDetachedShimStarter
	writeErr error
}

func (f *writingDetachedShimStarter) Start(request DetachedShimRequest) (DetachedShimProcess, error) {
	if _, err := request.Stdout.WriteString("stdout\n"); err != nil {
		f.writeErr = err
	}
	if _, err := request.Stderr.WriteString("stderr\n"); err != nil && f.writeErr == nil {
		f.writeErr = err
	}
	return f.fakeDetachedShimStarter.Start(request)
}

func (f *fakeShimLifecycle) Observe(_ context.Context, _, role string) (shim.Response, error) {
	f.events.add("observe:" + role)
	if f.onObserve != nil {
		f.onObserve()
	}
	if len(f.observe) == 0 {
		return shim.Response{Version: shim.ShimProtocolVersion, Outcome: shim.OutcomeMissing}, nil
	}
	response := f.observe[0]
	f.observe = f.observe[1:]
	return response, nil
}

func (f *fakeShimLifecycle) Stop(_ context.Context, _, role string) (shim.Response, error) {
	f.events.add("stop:" + role)
	response := f.stop[0]
	f.stop = f.stop[1:]
	return response, nil
}
