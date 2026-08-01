package cliflags

import (
	"errors"
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

func TestSetReturnsTypedDuplicateOptionError(t *testing.T) {
	flags := New("launch")
	flags.String("session", "", "session name")

	err := flags.Parse([]string{"--session=one", "--session=two"})
	var duplicate *DuplicateOptionError
	if !errors.As(err, &duplicate) {
		t.Fatalf("Parse() error = %T %v, want *DuplicateOptionError", err, err)
	}
	if duplicate.Name != "session" {
		t.Fatalf("DuplicateOptionError.Name = %q, want %q", duplicate.Name, "session")
	}
	if got, want := err.Error(), "--session provided more than once"; got != want {
		t.Fatalf("Parse() error = %q, want %q", got, want)
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

func TestSetDistinguishesOmittedFromExplicitlyEmptyOption(t *testing.T) {
	omitted := New("launch")
	omitted.String("models", "", "role and model assignments")
	if err := omitted.Parse(nil); err != nil {
		t.Fatalf("Parse(nil) error = %v", err)
	}
	if omitted.WasSet("models") {
		t.Fatal("WasSet(models) = true for omitted option")
	}

	provided := New("launch")
	models := provided.String("models", "", "role and model assignments")
	if err := provided.Parse([]string{"--models="}); err != nil {
		t.Fatalf("Parse(--models=) error = %v", err)
	}
	if !provided.WasSet("models") {
		t.Fatal("WasSet(models) = false for explicitly empty option")
	}
	if *models != "" {
		t.Fatalf("models = %q, want empty", *models)
	}
}
