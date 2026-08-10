// Package harness contains all harness-specific launch and input behavior.
package harness

import "fmt"

// Spec describes one supported agent harness.
type Spec struct {
	Executable      string
	InputClearKeys  []string
	inputClearBytes []byte
	submitBytes     []byte
	modelArgs       func(string) []string
	effortArgs      func(string) []string
}

// Options are the optional per-role harness settings. An empty value renders no
// arguments at all.
type Options struct {
	Model  string
	Effort string
}

var registry = map[string]Spec{
	"claude": {
		Executable:      "claude",
		InputClearKeys:  []string{"C-u"},
		inputClearBytes: []byte{0x15},
		submitBytes:     []byte{'\r'},
		modelArgs:       longModelArgs,
		effortArgs:      claudeEffortArgs,
	},
	"codex": {
		Executable:      "codex",
		InputClearKeys:  []string{"C-u"},
		inputClearBytes: []byte{0x15},
		submitBytes:     []byte{'\r'},
		modelArgs:       longModelArgs,
		effortArgs:      codexEffortArgs,
	},
}

// Lookup returns the specification for a supported harness.
func Lookup(name string) (Spec, bool) {
	spec, ok := registry[name]
	if ok {
		spec.InputClearKeys = append([]string(nil), spec.InputClearKeys...)
		spec.inputClearBytes = append([]byte(nil), spec.inputClearBytes...)
		spec.submitBytes = append([]byte(nil), spec.submitBytes...)
	}
	return spec, ok
}

// InputClearBytes returns the closed harness-specific PTY sequence that clears
// pending input. No caller value participates in this sequence.
func (s Spec) InputClearBytes() []byte {
	return append([]byte(nil), s.inputClearBytes...)
}

// SubmitBytes returns the closed harness-specific PTY submit sequence.
func (s Spec) SubmitBytes() []byte {
	return append([]byte(nil), s.submitBytes...)
}

// ModelArgs renders model as harness-specific command-line arguments.
func (s Spec) ModelArgs(model string) []string {
	if model == "" {
		return nil
	}
	return s.modelArgs(model)
}

// EffortArgs renders effort as harness-specific command-line arguments.
func (s Spec) EffortArgs(effort string) []string {
	if effort == "" {
		return nil
	}
	return s.effortArgs(effort)
}

// AgentArgv builds argv for launching one agent through amq coop exec.
func AgentArgv(session, role, harnessName string, options Options) ([]string, error) {
	spec, ok := Lookup(harnessName)
	if !ok {
		return nil, fmt.Errorf("unknown harness %q", harnessName)
	}

	argv := []string{"amq", "coop", "exec", "--session", session, "--me", role, spec.Executable}
	harnessArgs := append(spec.ModelArgs(options.Model), spec.EffortArgs(options.Effort)...)
	if len(harnessArgs) > 0 {
		argv = append(argv, "--")
		argv = append(argv, harnessArgs...)
	}
	return argv, nil
}

func longModelArgs(model string) []string {
	return []string{"--model", model}
}

// claudeEffortArgs renders Claude Code's own effort flag, verified against
// Claude Code 2.1.220: `--effort <level>`.
func claudeEffortArgs(effort string) []string {
	return []string{"--effort", effort}
}

// codexEffortArgs renders a codex configuration override, verified against
// codex-cli 0.146.0: the main codex CLI has no --effort flag, so the level is
// supplied as `--config 'model_reasoning_effort="<level>"'`. The value portion
// is parsed by codex as TOML, so it is rendered with %q; agentctl additionally
// applies the same alphanumeric, dot, underscore and hyphen charset used for
// model identifiers (internal/config), so the quoted string cannot be escaped.
func codexEffortArgs(effort string) []string {
	return []string{"--config", fmt.Sprintf("model_reasoning_effort=%q", effort)}
}
