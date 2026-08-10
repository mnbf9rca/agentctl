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
		options Options
		want    []string
	}{
		{
			name:    "claude with model",
			session: "epic123",
			role:    "planner",
			harness: "claude",
			options: Options{Model: "fable"},
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
			name:    "claude with effort only",
			session: "epic123",
			role:    "planner",
			harness: "claude",
			options: Options{Effort: "xhigh"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "planner", "claude", "--", "--effort", "xhigh"},
		},
		{
			name:    "claude with model and effort",
			session: "epic123",
			role:    "planner",
			harness: "claude",
			options: Options{Model: "fable", Effort: "max"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "planner", "claude", "--", "--model", "fable", "--effort", "max"},
		},
		{
			name:    "codex with model",
			session: "epic123",
			role:    "codex-r",
			harness: "codex",
			options: Options{Model: "gpt5.6-sol-xhigh"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "codex-r", "codex", "--", "--model", "gpt5.6-sol-xhigh"},
		},
		{
			name:    "codex without model",
			session: "epic123",
			role:    "codex1",
			harness: "codex",
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "codex1", "codex"},
		},
		{
			name:    "codex with effort only",
			session: "epic123",
			role:    "codex1",
			harness: "codex",
			options: Options{Effort: "high"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "codex1", "codex", "--", "--config", `model_reasoning_effort="high"`},
		},
		{
			name:    "codex with model and effort",
			session: "epic123",
			role:    "codex-r",
			harness: "codex",
			options: Options{Model: "gpt-5.6-sol", Effort: "high"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "codex-r", "codex", "--", "--model", "gpt-5.6-sol", "--config", `model_reasoning_effort="high"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := AgentArgv(tt.session, tt.role, tt.harness, tt.options)
			if err != nil {
				t.Fatalf("AgentArgv() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AgentArgv() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestAgentArgvUsesHarnessModelRenderer(t *testing.T) {
	registry["different-model-syntax"] = Spec{
		Executable: "different-model-syntax",
		modelArgs: func(model string) []string {
			if model == "implicit-default" {
				return nil
			}
			return []string{"-m=" + model}
		},
		effortArgs: func(effort string) []string { return []string{"-e=" + effort} },
	}
	t.Cleanup(func() { delete(registry, "different-model-syntax") })

	tests := []struct {
		name    string
		options Options
		want    []string
	}{
		{
			name:    "different syntax",
			options: Options{Model: "fable"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "worker", "different-model-syntax", "--", "-m=fable"},
		},
		{
			name:    "renderer omits model arguments",
			options: Options{Model: "implicit-default"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "worker", "different-model-syntax"},
		},
		{
			name:    "different effort syntax",
			options: Options{Model: "implicit-default", Effort: "high"},
			want:    []string{"amq", "coop", "exec", "--session", "epic123", "--me", "worker", "different-model-syntax", "--", "-e=high"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := AgentArgv("epic123", "worker", "different-model-syntax", tt.options)
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
		wantEffort []string
	}{
		{name: "claude", executable: "claude", wantModel: []string{"--model", "test-model"}, wantEffort: []string{"--effort", "high"}},
		{name: "codex", executable: "codex", wantModel: []string{"--model", "test-model"}, wantEffort: []string{"--config", `model_reasoning_effort="high"`}},
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
			if got := spec.EffortArgs("high"); !reflect.DeepEqual(got, tt.wantEffort) {
				t.Errorf("EffortArgs() = %#v, want %#v", got, tt.wantEffort)
			}
			if got := spec.EffortArgs(""); got != nil {
				t.Errorf("EffortArgs(\"\") = %#v, want nil", got)
			}
			if want := []string{"C-u"}; !reflect.DeepEqual(spec.InputClearKeys, want) {
				t.Errorf("InputClearKeys = %#v, want %#v", spec.InputClearKeys, want)
			}
		})
	}
}

func TestLookupReturnsIndependentInputClearKeys(t *testing.T) {
	first, ok := Lookup("claude")
	if !ok {
		t.Fatal("Lookup(claude) did not find registered harness")
	}
	first.InputClearKeys[0] = "changed"

	second, ok := Lookup("claude")
	if !ok {
		t.Fatal("Lookup(claude) did not find registered harness")
	}
	if want := []string{"C-u"}; !reflect.DeepEqual(second.InputClearKeys, want) {
		t.Fatalf("InputClearKeys = %#v after caller mutation, want %#v", second.InputClearKeys, want)
	}
}

func TestPTYControlBytesAreClosedHarnessConstants(t *testing.T) {
	for _, name := range []string{"claude", "codex"} {
		spec, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) = false", name)
		}
		if got, want := spec.InputClearBytes(), []byte{0x15}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s InputClearBytes() = %v, want %v", name, got, want)
		}
		if got, want := spec.SubmitBytes(), []byte{'\r'}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s SubmitBytes() = %v, want %v", name, got, want)
		}
	}
}

func TestUnknownHarness(t *testing.T) {
	t.Parallel()

	if _, ok := Lookup("other"); ok {
		t.Fatal("Lookup(other) found an unregistered harness")
	}
	if got, err := AgentArgv("epic123", "worker", "other", Options{}); err == nil || got != nil {
		t.Fatalf("AgentArgv() = %#v, %v; want nil argv and an error", got, err)
	}
}
