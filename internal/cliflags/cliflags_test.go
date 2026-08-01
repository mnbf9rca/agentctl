package cliflags

import (
	"strings"
	"testing"
)

func TestSetRejectsDuplicateStringOption(t *testing.T) {
	flags := New("launch")
	flags.String("session", "", "session name")

	err := flags.Parse([]string{"--session", "one", "--session", "two"})
	if err == nil || !strings.Contains(err.Error(), "--session provided more than once") {
		t.Fatalf("Parse() error = %v, want duplicate --session error", err)
	}
}

func TestSetRejectsDuplicateBoolOption(t *testing.T) {
	flags := New("status")
	flags.Bool("json", false, "emit JSON")

	err := flags.Parse([]string{"--json", "--json"})
	if err == nil || !strings.Contains(err.Error(), "--json provided more than once") {
		t.Fatalf("Parse() error = %v, want duplicate --json error", err)
	}
}

func TestSetReturnsParsedValuesAndArguments(t *testing.T) {
	flags := New("clear")
	session := flags.String("session", "", "session name")

	err := flags.Parse([]string{"--session", "fleet", "planner"})
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if got, want := *session, "fleet"; got != want {
		t.Fatalf("session = %q, want %q", got, want)
	}
	if got, want := strings.Join(flags.Args(), ","), "planner"; got != want {
		t.Fatalf("Args() = %q, want %q", got, want)
	}
}
