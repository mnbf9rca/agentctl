package harness

import (
	"reflect"
	"testing"
)

func TestAgentArgv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		session string
		role    string
		harness string
		model   string
		want    []string
	}{
		{
			name:    "claude with model",
			session: "epic123",
			role:    "planner",
			harness: "claude",
			model:   "fable",
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "planner", "claude", "--", "--model", "fable"},
		},
		{
			name:    "claude without model",
			session: "epic123",
			role:    "planner",
			harness: "claude",
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "planner", "claude"},
		},
		{
			name:    "codex with model",
			session: "epic123",
			role:    "codex-r",
			harness: "codex",
			model:   "gpt5.6-sol-xhigh",
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "codex-r", "codex", "--", "--model", "gpt5.6-sol-xhigh"},
		},
		{
			name:    "codex without model",
			session: "epic123",
			role:    "codex1",
			harness: "codex",
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "codex1", "codex"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := AgentArgv(tt.session, tt.role, tt.harness, tt.model)
			if err != nil {
				t.Fatalf("AgentArgv() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AgentArgv() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		executable string
		wantModel  []string
	}{
		{name: "claude", executable: "claude", wantModel: []string{"--model", "test-model"}},
		{name: "codex", executable: "codex", wantModel: []string{"--model", "test-model"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec, ok := Lookup(tt.name)
			if !ok {
				t.Fatalf("Lookup(%q) did not find registered harness", tt.name)
			}
			if spec.Executable != tt.executable {
				t.Errorf("Executable = %q, want %q", spec.Executable, tt.executable)
			}
			if got := spec.ModelArgs("test-model"); !reflect.DeepEqual(got, tt.wantModel) {
				t.Errorf("ModelArgs() = %#v, want %#v", got, tt.wantModel)
			}
			if want := []string{"C-u"}; !reflect.DeepEqual(spec.InputClearKeys, want) {
				t.Errorf("InputClearKeys = %#v, want %#v", spec.InputClearKeys, want)
			}
		})
	}
}

func TestUnknownHarness(t *testing.T) {
	t.Parallel()

	if _, ok := Lookup("other"); ok {
		t.Fatal("Lookup(other) found an unregistered harness")
	}
	if got, err := AgentArgv("epic123", "worker", "other", ""); err == nil || got != nil {
		t.Fatalf("AgentArgv() = %#v, %v; want nil argv and an error", got, err)
	}
}
