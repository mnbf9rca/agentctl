// Package harness contains all harness-specific launch and input behavior.
package harness

import "fmt"

// Spec describes one supported agent harness.
type Spec struct {
	Executable     string
	InputClearKeys []string
	modelArgs      func(string) []string
}

var registry = map[string]Spec{
	"claude": {
		Executable:     "claude",
		InputClearKeys: []string{"C-u"},
		modelArgs:      longModelArgs,
	},
	"codex": {
		Executable:     "codex",
		InputClearKeys: []string{"C-u"},
		modelArgs:      longModelArgs,
	},
}

// Lookup returns the specification for a supported harness.
func Lookup(name string) (Spec, bool) {
	spec, ok := registry[name]
	if ok {
		spec.InputClearKeys = append([]string(nil), spec.InputClearKeys...)
	}
	return spec, ok
}

// ModelArgs renders model as harness-specific command-line arguments.
func (s Spec) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return s.modelArgs(model)
}

// AgentArgv builds argv for launching one agent through amq coop exec.
func AgentArgv(session, role, harnessName, model string) ([]string, error) {
	spec, ok := Lookup(harnessName)
	if !ok {
		return nil, fmt.Errorf("unknown harness %q", harnessName)
	}

	argv := []string{"amq", "coop", "exec", "--session", session, "--me", role, spec.Executable}
	if model != "" {
		argv = append(argv, "--")
		argv = append(argv, spec.ModelArgs(model)...)
	}
	return argv, nil
}

func longModelArgs(model string) []string {
	return []string{"--model", model}
}
