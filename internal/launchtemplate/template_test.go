package launchtemplate

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

type stubFile struct {
	reader   io.Reader
	info     fs.FileInfo
	statErr  error
	closeErr error
	events   *[]string
}

func (f *stubFile) Read(p []byte) (int, error) {
	*f.events = append(*f.events, "read")
	return f.reader.Read(p)
}

func (f *stubFile) Stat() (fs.FileInfo, error) {
	*f.events = append(*f.events, "stat")
	return f.info, f.statErr
}

func (f *stubFile) Close() error {
	*f.events = append(*f.events, "close")
	return f.closeErr
}

type stubFileInfo struct{ mode fs.FileMode }

func (stubFileInfo) Name() string           { return "fixture" }
func (stubFileInfo) Size() int64            { return 0 }
func (info stubFileInfo) Mode() fs.FileMode { return info.mode }
func (stubFileInfo) ModTime() time.Time     { return time.Time{} }
func (info stubFileInfo) IsDir() bool       { return info.mode.IsDir() }
func (stubFileInfo) Sys() any               { return nil }

func TestDecoderOpensThenStatsTheDescriptorBeforeReading(t *testing.T) {
	t.Parallel()

	var events []string
	decoder := Decoder{Open: func(path string) (File, error) {
		if path != "/fleet.json" {
			t.Fatalf("Open path = %q, want /fleet.json", path)
		}
		events = append(events, "open")
		return &stubFile{
			reader: strings.NewReader(`{"version":1}`),
			info:   stubFileInfo{mode: 0o644},
			events: &events,
		}, nil
	}}

	_, err := decoder.Decode("/fleet.json")
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if want := []string{"open", "stat", "read", "read", "close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestDecoderFileBoundaryFailuresAreTemplateShapedAndCloseOpenedFiles(t *testing.T) {
	t.Parallel()

	openFailure := errors.New("permission denied")
	statFailure := errors.New("descriptor vanished")
	tests := []struct {
		name     string
		open     OpenFunc
		want     string
		wantRead bool
	}{
		{
			name: "open",
			open: func(string) (File, error) {
				return nil, openFailure
			},
			want: `template /fleet.json: cannot open: permission denied`,
		},
		{
			name: "descriptor stat",
			open: func(string) (File, error) {
				events := []string{}
				return &stubFile{reader: strings.NewReader(`{"version":1}`), statErr: statFailure, events: &events}, nil
			},
			want: `template /fleet.json: cannot inspect opened file: descriptor vanished`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := (Decoder{Open: test.open}).Decode("/fleet.json")
			assertTemplateError(t, err, test.want)
		})
	}
}

func TestDecoderRefusesEveryNonRegularDescriptorBeforeReading(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode fs.FileMode
		kind string
	}{
		{name: "directory", mode: fs.ModeDir | 0o755, kind: "directory"},
		{name: "named pipe", mode: fs.ModeNamedPipe | 0o600, kind: "named pipe"},
		{name: "socket", mode: fs.ModeSocket | 0o600, kind: "socket"},
		{name: "device", mode: fs.ModeDevice | 0o600, kind: "device"},
		{name: "character device", mode: fs.ModeDevice | fs.ModeCharDevice | 0o600, kind: "character device"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var events []string
			file := &stubFile{reader: strings.NewReader(`{"version":1}`), info: stubFileInfo{mode: test.mode}, events: &events}
			_, err := (Decoder{Open: func(string) (File, error) { return file, nil }}).Decode("/fleet.json")
			assertTemplateError(t, err, `template /fleet.json: must be a regular file; opened object is `+test.kind)
			if want := []string{"stat", "close"}; !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %#v, want %#v", events, want)
			}
		})
	}
}

func TestDecodeRefusesWriterlessFIFOWithoutBlocking(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "fleet.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := Decode(fifo)
		done <- err
	}()

	select {
	case err := <-done:
		assertTemplateError(t, err, "template "+fifo+": must be a regular file; opened object is named pipe")
	case <-time.After(time.Second):
		// Release a decoder blocked in a plain read-only open so a failing test
		// still returns instead of leaking the goroutine indefinitely.
		unblock, err := os.OpenFile(fifo, os.O_RDWR|syscall.O_NONBLOCK, 0)
		if err != nil {
			t.Fatalf("Decode() did not return within one second; cleanup open: %v", err)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
		if err := unblock.Close(); err != nil {
			t.Fatal(err)
		}
		t.Fatal("Decode() did not return within one second")
	}
}

func TestDecoderFollowsSymlinksAndTreatsDashAsAnOrdinaryPath(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "fleet.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(link); err != nil {
		t.Fatalf("Decode(symlink) error = %v", err)
	}

	dash := filepath.Join(directory, "-")
	if err := os.WriteFile(dash, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(dash); err != nil {
		t.Fatalf("Decode(dash path) error = %v", err)
	}
}

func TestDecoderAcceptsTheSizeCapAndRefusesOneByteMore(t *testing.T) {
	t.Parallel()

	validPrefix := `{"version":1}`
	for _, test := range []struct {
		name string
		size int
		want string
	}{
		{name: "at cap", size: MaxBytes},
		{name: "one over", size: MaxBytes + 1, want: `template /fleet.json: exceeds 1048576-byte limit`},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			contents := validPrefix + strings.Repeat(" ", test.size-len(validPrefix))
			var events []string
			file := &stubFile{reader: strings.NewReader(contents), info: stubFileInfo{mode: 0o600}, events: &events}
			_, err := (Decoder{Open: func(string) (File, error) { return file, nil }}).Decode("/fleet.json")
			if test.want == "" {
				if err != nil {
					t.Fatalf("Decode() error = %v", err)
				}
				return
			}
			assertTemplateError(t, err, test.want)
		})
	}
}

func TestDecoderFixtureErrorsPinStrictnessOrderAndMessageShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		fixture string
		want    string
	}{
		{fixture: "malformed.json", want: `invalid JSON:`},
		{fixture: "missing-version.json", want: `version: is required`},
		{fixture: "null-version.json", want: `version: must be exactly 1, got null`},
		{fixture: "version-array.json", want: `version: must be exactly 1, got []`},
		{fixture: "version-object.json", want: `version: must be exactly 1, got {"major":1}`},
		{fixture: "unsupported-version-with-unknown.json", want: `version 2 is not supported by this agentctl (supports 1)`},
		{fixture: "duplicate-root.json", want: `duplicate field "dir"`},
		{fixture: "duplicate-role-field.json", want: `roles[0]: duplicate field "effort"`},
		{fixture: "unknown-root.json", want: `unknown field "efort"`},
		{fixture: "unknown-role.json", want: `roles[0]: unknown field "efort"`},
		{fixture: "session-field.json", want: `"session" is not a template field; session identity is supplied per invocation with --session`},
		{fixture: "trailing-document.json", want: `must contain exactly one JSON document`},
		{fixture: "dir-number.json", want: `dir: must be a string`},
		{fixture: "roles-object.json", want: `roles: must be an array`},
		{fixture: "role-number.json", want: `roles[0]: must be an object`},
		{fixture: "role-field-number.json", want: `roles[0].model: must be a string`},
		{fixture: "null-dir.json", want: `dir: must not be null; omit the field instead`},
		{fixture: "empty-dir.json", want: `dir: must not be empty; omit the field instead`},
		{fixture: "null-roles.json", want: `roles: must not be null; omit the field instead`},
		{fixture: "missing-role-name.json", want: `roles[0].role: is required`},
		{fixture: "null-role-name.json", want: `roles[0].role: must not be null`},
		{fixture: "empty-role-name.json", want: `roles[0].role: must not be empty`},
		{fixture: "null-optional-role-field.json", want: `roles[0].model: must not be null; omit the field instead`},
		{fixture: "empty-optional-role-field.json", want: `roles[0].model: must not be empty; omit the field instead`},
		{fixture: "duplicate-role.json", want: `roles[2]: duplicate role "planner"`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.fixture, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("testdata", test.fixture)
			_, err := Decode(path)
			if err == nil || !strings.Contains(err.Error(), "template "+path+": "+test.want) {
				t.Fatalf("Decode(%q) error = %v, want containing %q", path, err, "template "+path+": "+test.want)
			}
		})
	}
}

func TestDecoderSchemaFailuresKeepAgentctlDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "relative directory",
			contents: `{"version":1,"dir":"relative/work"}`,
			want:     `template /fleet.json: dir: path "relative/work" must be absolute; omit dir and supply --dir at invocation`,
		},
		{
			name:     "invalid role name",
			contents: `{"version":1,"roles":[{"role":"Planner"}]}`,
			want:     `template /fleet.json: roles[0].role: value "Planner" must match ^[a-z0-9][a-z0-9_-]*$`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertTemplateError(t, decodeTemplateContents(t, test.contents), test.want)
		})
	}
}

func TestDecoderRejectsSchemaKeywordBeforeGenericUnknownFieldHandling(t *testing.T) {
	t.Parallel()

	assertTemplateError(t,
		decodeTemplateContents(t, `{"version":1,"$schema":"https://example.invalid/fleet-template.schema.json"}`),
		`template /fleet.json: "$schema" is not a template field; the schema is applied automatically; see references/fleet-template.schema.json`,
	)
}

func TestDecoderKeepsVersionOneLexicallyExact(t *testing.T) {
	t.Parallel()

	assertTemplateError(t,
		decodeTemplateContents(t, `{"version":1.0}`),
		`template /fleet.json: version: must be exactly 1, got 1.0`,
	)
}

func TestDecoderTreatsMalformedTrailingContentAsAnotherDocument(t *testing.T) {
	t.Parallel()

	assertTemplateError(t,
		decodeTemplateContents(t, `{"version":1} trailing`),
		`template /fleet.json: must contain exactly one JSON document`,
	)
}

func TestDecoderSelectsMixedSchemaFailuresInLegacyOrder(t *testing.T) {
	t.Parallel()

	const contents = `{"version":1,"dir":7,"roles":[{"role":"Planner","model":8}]}`
	const want = `template /fleet.json: dir: must be a string`
	for range 400 {
		assertTemplateError(t, decodeTemplateContents(t, contents), want)
	}
}

func TestDecoderPreservesStrictnessPrecedenceAroundSchemaValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "root unknown before trailing document",
			contents: `{"version":1,"efort":"low"} {}`,
			want:     `template /fleet.json: unknown field "efort"`,
		},
		{
			name:     "root unknown before session refusal",
			contents: `{"version":1,"efort":"low","session":"fleet"}`,
			want:     `template /fleet.json: unknown field "efort"`,
		},
		{
			name:     "directory before role unknown",
			contents: `{"version":1,"dir":7,"roles":[{"role":"planner","efort":"low"}]}`,
			want:     `template /fleet.json: dir: must be a string`,
		},
		{
			name:     "schema keyword before generic unknown",
			contents: `{"version":1,"$schema":"ignored","efort":"low"}`,
			want:     `template /fleet.json: "$schema" is not a template field; the schema is applied automatically; see references/fleet-template.schema.json`,
		},
		{
			name:     "duplicate role before later optional field",
			contents: `{"version":1,"roles":[{"role":"planner"},{"role":"planner","model":8}]}`,
			want:     `template /fleet.json: roles[1]: duplicate role "planner"`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for range 100 {
				assertTemplateError(t, decodeTemplateContents(t, test.contents), test.want)
			}
		})
	}
}

func TestDecoderPreservesLegacyStagesForNestedSpecialsAndSchemaValueRules(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{
			name:     "nested session remains unknown",
			contents: `{"version":1,"roles":[{"role":"planner","session":"fleet"}]}`,
			want:     `template /fleet.json: roles[0]: unknown field "session"`,
		},
		{
			name:     "nested schema remains unknown",
			contents: `{"version":1,"roles":[{"role":"planner","$schema":"ignored"}]}`,
			want:     `template /fleet.json: roles[0]: unknown field "$schema"`,
		},
		{
			name:     "role unknown before missing required name",
			contents: `{"version":1,"roles":[{"efort":"low"}]}`,
			want:     `template /fleet.json: roles[0]: unknown field "efort"`,
		},
		{
			name:     "duplicate before nonempty relative directory pattern",
			contents: `{"version":1,"dir":"relative","roles":[{"role":"planner"},{"role":"planner"}]}`,
			want:     `template /fleet.json: roles[1]: duplicate role "planner"`,
		},
		{
			name:     "duplicate before nonempty role pattern",
			contents: `{"version":1,"roles":[{"role":"Planner"},{"role":"Planner"}]}`,
			want:     `template /fleet.json: roles[1]: duplicate role "Planner"`,
		},
		{
			name:     "optional structural failure before nonempty role pattern",
			contents: `{"version":1,"roles":[{"role":"Planner","model":8}]}`,
			want:     `template /fleet.json: roles[0].model: must be a string`,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for range 100 {
				assertTemplateError(t, decodeTemplateContents(t, test.contents), test.want)
			}
		})
	}
}

func TestDecoderReturnsPartialSourceWithoutDefaultingOrValidatingValues(t *testing.T) {
	t.Parallel()

	got, err := Decode(filepath.Join("testdata", "valid-partial.json"))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	want := Document{
		Path:      filepath.Join("testdata", "valid-partial.json"),
		Directory: stringPointer("/schema-valid-directory"),
		Roles: []Role{
			{Name: "planner"},
			{Name: "reviewer", Harness: stringPointer("future-harness"), Model: stringPointer("bad model"), Effort: stringPointer("extreme")},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode() = %#v, want %#v", got, want)
	}
}

func TestDecoderAcceptsAbsentAndEmptyRoles(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{"valid-no-roles.json", "valid-empty-roles.json"} {
		fixture := fixture
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			document, err := Decode(filepath.Join("testdata", fixture))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			if len(document.Roles) != 0 {
				t.Fatalf("roles = %#v, want empty", document.Roles)
			}
		})
	}
}

func assertTemplateError(t *testing.T, err error, want string) {
	t.Helper()
	var templateError *Error
	if !errors.As(err, &templateError) {
		t.Fatalf("error = %T %v, want *Error", err, err)
	}
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func stringPointer(value string) *string { return &value }

func decodeTemplateContents(t *testing.T, contents string) error {
	t.Helper()

	var events []string
	_, err := (Decoder{Open: func(path string) (File, error) {
		if path != "/fleet.json" {
			t.Fatalf("Open path = %q, want /fleet.json", path)
		}
		return &stubFile{
			reader: strings.NewReader(contents),
			info:   stubFileInfo{mode: 0o600},
			events: &events,
		}, nil
	}}).Decode("/fleet.json")
	return err
}
