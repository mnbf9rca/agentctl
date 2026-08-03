package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestResolveExplicitSessionWinsWithoutInspectingLowerSources(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$1\texplicit\n$2\tenvironment\n")})
	explicit := "explicit"
	got, err := New(tmuxx.New(runner), mapLookup(map[string]string{
		"AGENTCTL_SESSION": "environment",
		"TMUX_PANE":        "%9",
	})).Resolve(context.Background(), &explicit)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := (tmuxx.Session{ID: "$1", Name: "explicit"}); got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}})
}

func TestResolveEnvironmentSessionWinsWithoutInspectingCurrentTmux(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$2\tenvironment\n")})
	got, err := New(tmuxx.New(runner), mapLookup(map[string]string{
		"AGENTCTL_SESSION": "environment",
		"TMUX_PANE":        "%9",
	})).Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := (tmuxx.Session{ID: "$2", Name: "environment"}); got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}})
}

func TestResolveCurrentTmuxDisplaysNameThenListsForExactID(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("current\n")},
		tmuxx.Response{Stdout: []byte("$3\tcurrent\n")},
	)
	got, err := New(tmuxx.New(runner), mapLookup(map[string]string{
		"AGENTCTL_SESSION": "",
		"TMUX_PANE":        "%9",
	})).Resolve(context.Background(), nil)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := (tmuxx.Session{ID: "$3", Name: "current"}); got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
	assertCalls(t, runner,
		tmuxx.Call{Executable: "tmux", Args: []string{"display-message", "-p", "-t", "%9", "#{session_name}"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-sessions", "-F", "#{session_id}\t#{session_name}"}},
	)
}

func TestResolveNeverInfersFromForbiddenSourcesOrFirstSession(t *testing.T) {
	suggestive := filepath.Join(t.TempDir(), "epic123")
	if err := os.Mkdir(suggestive, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(suggestive)

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$1\tepic123\n")})
	_, err := New(tmuxx.New(runner), mapLookup(map[string]string{
		"AM_ROOT":    "/tmp/epic123/.agent-mail/collab",
		"AM_SESSION": "epic123",
		"TMUX":       "/tmp/tmux-501/default,1,0",
	})).Resolve(context.Background(), nil)
	var resolution *ResolutionError
	if !errors.As(err, &resolution) {
		t.Fatalf("Resolve() error = %T %v, want *ResolutionError", err, err)
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("Calls = %#v, want no tmux calls", runner.Calls)
	}
}

func TestResolveInvalidHigherPrioritySourceBlocksFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		explicit  *string
		env       map[string]string
		wantUsage bool
	}{
		{name: "explicit", explicit: stringPointer("INVALID"), env: map[string]string{"AGENTCTL_SESSION": "environment", "TMUX_PANE": "%9"}, wantUsage: true},
		{name: "environment", env: map[string]string{"AGENTCTL_SESSION": "INVALID", "TMUX_PANE": "%9"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$1\tenvironment\n")})
			_, err := New(tmuxx.New(runner), mapLookup(tt.env)).Resolve(context.Background(), tt.explicit)
			if tt.wantUsage {
				var usage *UsageError
				if !errors.As(err, &usage) {
					t.Fatalf("Resolve() error = %T %v, want *UsageError", err, err)
				}
			} else {
				var resolution *ResolutionError
				if !errors.As(err, &resolution) {
					t.Fatalf("Resolve() error = %T %v, want *ResolutionError", err, err)
				}
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("Calls = %#v, want no fallback calls", runner.Calls)
			}
		})
	}
}

func TestResolveInvalidDisplayedNameBlocksListing(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("INVALID\n")})
	_, err := New(tmuxx.New(runner), mapLookup(map[string]string{"TMUX_PANE": "%9"})).Resolve(context.Background(), nil)
	var resolution *ResolutionError
	if !errors.As(err, &resolution) {
		t.Fatalf("Resolve() error = %T %v, want *ResolutionError", err, err)
	}
	assertCalls(t, runner, tmuxx.Call{Executable: "tmux", Args: []string{"display-message", "-p", "-t", "%9", "#{session_name}"}})
}

func TestResolveMatchesNamesExactly(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("$1\tepic1234\n$2\tepic123\n")})
	explicit := "epic123"
	got, err := New(tmuxx.New(runner), mapLookup(nil)).Resolve(context.Background(), &explicit)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if want := (tmuxx.Session{ID: "$2", Name: "epic123"}); got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveRejectsMissingAndAmbiguousExactNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		stdout     string
		wantPieces []string
	}{
		{name: "missing", stdout: "$1\tother\n", wantPieces: []string{"epic123", "not found"}},
		{name: "ambiguous", stdout: "$1\tepic123\n$2\tepic123\n", wantPieces: []string{"epic123", "$1", "$2", "ambiguous"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte(tt.stdout)})
			explicit := "epic123"
			_, err := New(tmuxx.New(runner), mapLookup(nil)).Resolve(context.Background(), &explicit)
			var resolution *ResolutionError
			if !errors.As(err, &resolution) {
				t.Fatalf("Resolve() error = %T %v, want *ResolutionError", err, err)
			}
			for _, piece := range tt.wantPieces {
				if !strings.Contains(err.Error(), piece) {
					t.Fatalf("Resolve() error = %q, want substring %q", err, piece)
				}
			}
		})
	}
}

func TestResolveClassifiesRowFailuresAsTmuxErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		explicit  *string
		env       map[string]string
		responses []tmuxx.Response
	}{
		{name: "list runner", explicit: stringPointer("epic123"), responses: []tmuxx.Response{{Err: errors.New("no server running")}}},
		{name: "list parse", explicit: stringPointer("epic123"), responses: []tmuxx.Response{{Stdout: []byte("$1\n")}}},
		{name: "list invalid returned id", explicit: stringPointer("epic123"), responses: []tmuxx.Response{{Stdout: []byte("$bad\tepic123\n")}}},
		{name: "display runner", env: map[string]string{"TMUX_PANE": "%9"}, responses: []tmuxx.Response{{Err: errors.New("lost server")}}},
		{name: "display parse", env: map[string]string{"TMUX_PANE": "%9"}, responses: []tmuxx.Response{{Stdout: []byte("one\ntwo\n")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)
			_, err := New(tmuxx.New(runner), mapLookup(tt.env)).Resolve(context.Background(), tt.explicit)
			var tmuxError *tmuxx.TmuxError
			if !errors.As(err, &tmuxError) {
				t.Fatalf("Resolve() error = %T %v, want *tmuxx.TmuxError", err, err)
			}
		})
	}
}

func TestResolveMalformedTMUXPaneIsInvalidSourceWithoutTmuxCall(t *testing.T) {
	t.Parallel()

	for _, pane := range []string{"", "%", "7", "$1", "%abc", "not-a-pane"} {
		pane := pane
		t.Run(pane, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner()
			_, err := New(tmuxx.New(runner), mapLookup(map[string]string{"TMUX_PANE": pane})).Resolve(context.Background(), nil)
			var resolution *ResolutionError
			if !errors.As(err, &resolution) {
				t.Fatalf("Resolve() error = %T %v, want *ResolutionError", err, err)
			}
			if resolution.Source != SourceCurrent {
				t.Fatalf("ResolutionError.Source = %q, want %q", resolution.Source, SourceCurrent)
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("Calls = %#v, want no tmux calls", runner.Calls)
			}
		})
	}
}

func TestResolvePreservesContextErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		explicit  *string
		env       map[string]string
		response  tmuxx.Response
		wantError error
	}{
		{name: "list canceled", explicit: stringPointer("epic123"), response: tmuxx.Response{Err: context.Canceled}, wantError: context.Canceled},
		{name: "display deadline", env: map[string]string{"TMUX_PANE": "%9"}, response: tmuxx.Response{Err: context.DeadlineExceeded}, wantError: context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.response)
			_, err := New(tmuxx.New(runner), mapLookup(tt.env)).Resolve(context.Background(), tt.explicit)
			if err != tt.wantError {
				t.Fatalf("Resolve() error = %T %v, want preserved %v", err, err, tt.wantError)
			}
		})
	}
}

func TestResolutionErrorUnresolvedOnlyWhenNoPermittedSourceNamedASession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		env           map[string]string
		explicit      *string
		responses     []tmuxx.Response
		wantUnresolve bool
	}{
		{name: "no permitted source", env: map[string]string{"AM_SESSION": "fleet", "TMUX": "server"}, wantUnresolve: true},
		{name: "invalid environment source", env: map[string]string{"AGENTCTL_SESSION": "INVALID"}},
		{name: "invalid current source", env: map[string]string{"TMUX_PANE": "%bad"}},
		{name: "named session not found", explicit: stringPointer("epic123"), responses: []tmuxx.Response{{Stdout: []byte("$1\tother\n")}}},
		{name: "named session ambiguous", explicit: stringPointer("epic123"), responses: []tmuxx.Response{{Stdout: []byte("$1\tepic123\n$2\tepic123\n")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tt.responses...)
			_, err := New(tmuxx.New(runner), mapLookup(tt.env)).Resolve(context.Background(), tt.explicit)
			var resolution *ResolutionError
			if !errors.As(err, &resolution) {
				t.Fatalf("Resolve() error = %T %v, want *ResolutionError", err, err)
			}
			if got := resolution.Unresolved(); got != tt.wantUnresolve {
				t.Fatalf("ResolutionError.Unresolved() = %t, want %t for %v", got, tt.wantUnresolve, err)
			}
		})
	}
}

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func stringPointer(value string) *string {
	return &value
}

func assertCalls(t *testing.T, runner *tmuxx.FakeRunner, want ...tmuxx.Call) {
	t.Helper()
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("Calls = %#v, want %#v", runner.Calls, want)
	}
}
