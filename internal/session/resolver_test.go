package session

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestSelectExplicitAndEnvironmentNamesWithoutRequiringTmuxPresentation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		explicit *string
		env      map[string]string
		want     string
	}{
		{name: "explicit", explicit: stringPointer("fleet"), env: map[string]string{"AGENTCTL_SESSION": "other"}, want: "fleet"},
		{name: "environment", env: map[string]string{"AGENTCTL_SESSION": "fleet"}, want: "fleet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()
			got, err := New(tmuxx.New(runner), mapLookup(test.env)).Select(context.Background(), test.explicit)
			if err != nil || got != test.want || len(runner.Calls) != 0 {
				t.Fatalf("Select() = %q, %v; calls=%#v", got, err, runner.Calls)
			}
		})
	}
}

func TestSelectCurrentTmuxUsesOnlyDisplayedSessionName(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte("fleet\n")})
	got, err := New(tmuxx.New(runner), mapLookup(map[string]string{"TMUX_PANE": "%9"})).Select(context.Background(), nil)
	if err != nil || got != "fleet" {
		t.Fatalf("Select() = %q, %v", got, err)
	}
	want := []tmuxx.Call{{Executable: "tmux", Args: []string{"display-message", "-p", "-t", "%9", "#{session_name}"}}}
	if !reflect.DeepEqual(runner.Calls, want) {
		t.Fatalf("calls=%#v, want %#v", runner.Calls, want)
	}
}

func TestSelectRejectsInvalidHigherPrioritySourceWithoutFallback(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		explicit *string
		env      map[string]string
		usage    bool
	}{
		{explicit: stringPointer("INVALID"), env: map[string]string{"AGENTCTL_SESSION": "fleet", "TMUX_PANE": "%9"}, usage: true},
		{env: map[string]string{"AGENTCTL_SESSION": "INVALID", "TMUX_PANE": "%9"}},
	} {
		runner := tmuxx.NewFakeRunner()
		_, err := New(tmuxx.New(runner), mapLookup(test.env)).Select(context.Background(), test.explicit)
		var usage *UsageError
		var resolution *ResolutionError
		if (test.usage && !errors.As(err, &usage)) || (!test.usage && !errors.As(err, &resolution)) || len(runner.Calls) != 0 {
			t.Fatalf("Select() error=%T %v calls=%#v", err, err, runner.Calls)
		}
	}
}

func TestSelectNeverUsesForbiddenAmbientSources(t *testing.T) {
	t.Parallel()

	runner := tmuxx.NewFakeRunner()
	_, err := New(tmuxx.New(runner), mapLookup(map[string]string{"AM_ROOT": "/tmp/fleet", "AM_SESSION": "fleet", "TMUX": "server"})).Select(context.Background(), nil)
	var resolution *ResolutionError
	if !errors.As(err, &resolution) || !resolution.Unresolved() || len(runner.Calls) != 0 {
		t.Fatalf("Select() error=%T %v calls=%#v", err, err, runner.Calls)
	}
}

func stringPointer(value string) *string { return &value }

func mapLookup(values map[string]string) LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
