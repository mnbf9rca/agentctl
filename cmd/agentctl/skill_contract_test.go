package main

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/skills"
)

type documentedInvocation struct {
	command string
	flags   map[string]struct{}
}

func skillLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := skills.Tree.ReadFile(path)
	if err != nil {
		t.Fatalf("embedded %s: %v", path, err)
	}

	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read embedded %s: %v", path, err)
	}
	return lines
}

// fencedCommands returns every line beginning "agentctl " inside fenced blocks.
func fencedCommands(lines []string) []string {
	var commands []string
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence && strings.HasPrefix(trimmed, "agentctl ") {
			commands = append(commands, trimmed)
		}
	}
	return commands
}

func parseDocumentedInvocation(t *testing.T, invocation string) documentedInvocation {
	t.Helper()
	fields := strings.Fields(invocation)
	if len(fields) < 2 || fields[0] != "agentctl" {
		t.Fatalf("invalid documented invocation %q", invocation)
	}

	parsed := documentedInvocation{command: fields[1], flags: make(map[string]struct{})}
	for _, field := range fields[2:] {
		if strings.HasPrefix(field, "--") {
			parsed.flags[strings.SplitN(field, "=", 2)[0]] = struct{}{}
		}
	}
	return parsed
}

func invocationArguments(invocation documentedInvocation) []string {
	arguments := make([]string, 0, len(invocation.flags)+1)
	for flag := range invocation.flags {
		switch flag {
		case "--json":
			arguments = append(arguments, flag)
		default:
			arguments = append(arguments, flag+"=fixture")
		}
	}
	if invocation.command == "clear" || invocation.command == "compact" {
		arguments = append(arguments, "role")
	}
	return arguments
}

func tableRows(lines []string) [][]string {
	var rows [][]string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			continue
		}
		cells := strings.Split(trimmed, "|")
		cells = cells[1 : len(cells)-1]
		for index := range cells {
			cells[index] = strings.TrimSpace(cells[index])
		}
		rows = append(rows, cells)
	}
	return rows
}

func isUnknownFlagError(err error) bool {
	// cliflags delegates unknown-flag reporting to the stable stdlib flag
	// package phrase. Matching only this phrase avoids treating other parse
	// failures as evidence that a documented flag is unsupported.
	return err != nil && strings.Contains(err.Error(), "flag provided but not defined")
}

func TestSkillBudget(t *testing.T) {
	t.Run("SKILL.md line count", func(t *testing.T) {
		if lines := len(skillLines(t, skills.Root+"/SKILL.md")); lines > 150 {
			t.Fatalf("SKILL.md is %d lines; budget is 150 (spec §3.1)", lines)
		}
	})
}

func TestSkillVersionParses(t *testing.T) {
	t.Run("metadata version is numeric dotted version", func(t *testing.T) {
		var version string
		for _, line := range skillLines(t, skills.Root+"/SKILL.md") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "version:") {
				if version != "" {
					t.Fatal("SKILL.md carries more than one metadata.version line")
				}
				version = strings.Trim(strings.TrimPrefix(trimmed, "version:"), ` "`)
			}
		}
		if version == "" {
			t.Fatal("SKILL.md carries no metadata.version line")
		}

		components := strings.Split(version, ".")
		if len(components) < 2 {
			t.Fatalf("metadata.version = %q, want a dotted numeric version", version)
		}
		for _, component := range components {
			if component == "" {
				t.Fatalf("metadata.version = %q, has an empty component", version)
			}
			for _, character := range component {
				if character < '0' || character > '9' {
					t.Fatalf("metadata.version = %q, component %q is not numeric", version, component)
				}
			}
			if _, err := strconv.Atoi(component); err != nil {
				t.Fatalf("metadata.version = %q, component %q: %v", version, component, err)
			}
		}
	})
}

func TestDocumentedAgentCommandContract(t *testing.T) {
	t.Run("fenced agent commands and flags match the binary", func(t *testing.T) {
		expected := map[string]map[string]struct{}{
			"status":  {"--session": {}, "--json": {}},
			"clear":   {"--session": {}},
			"compact": {"--session": {}},
			"kill":    {"--session": {}},
		}

		documented := make(map[string]map[string]struct{})
		for _, line := range fencedCommands(skillLines(t, skills.Root+"/SKILL.md")) {
			invocation := parseDocumentedInvocation(t, line)
			if _, ok := commandUsage[invocation.command]; !ok {
				t.Errorf("skill documents %q; not in commandUsage", invocation.command)
			}
			if _, ok := expected[invocation.command]; !ok {
				t.Errorf("skill documents agent command %q; it is not in the agent-facing command contract", invocation.command)
			}

			for flag := range invocation.flags {
				if _, ok := expected[invocation.command][flag]; !ok {
					t.Errorf("skill documents %s %s; it is not in the agent-facing flag contract", invocation.command, flag)
				}
			}
			if err := func() error {
				_, err := parseCommand(invocation.command, invocationArguments(invocation))
				return err
			}(); err != nil {
				if isUnknownFlagError(err) {
					t.Errorf("skill documents unsupported flag in %q: %v", line, err)
					continue
				}
				t.Errorf("parseCommand rejects documented invocation %q: %v", line, err)
			}

			if documented[invocation.command] == nil {
				documented[invocation.command] = make(map[string]struct{})
			}
			for flag := range invocation.flags {
				documented[invocation.command][flag] = struct{}{}
			}
		}

		for command, expectedFlags := range expected {
			documentedFlags, ok := documented[command]
			if !ok {
				t.Errorf("agent-facing command %q is undocumented in fenced invocations", command)
				continue
			}
			for flag := range expectedFlags {
				if _, ok := documentedFlags[flag]; !ok {
					t.Errorf("agent-facing flag %s %s is undocumented in fenced invocations", command, flag)
				}
			}
		}
	})
}

func TestExitCodeTableMatchesConstants(t *testing.T) {
	t.Run("exit-code reference and binary constants match", func(t *testing.T) {
		constants := map[string]int{
			"exitOK": exitOK, "exitUnclassified": exitUnclassified,
			"exitUsage": exitUsage, "exitSession": exitSession,
			"exitRole": exitRole, "exitUnsafe": exitUnsafe,
			"exitTmux": exitTmux, "exitMissingExecutable": exitMissingExecutable,
			"exitLaunch": exitLaunch,
		}
		documented := make(map[string]int)
		for _, cells := range tableRows(skillLines(t, skills.Root+"/references/exit-codes.md")) {
			if len(cells) != 3 || cells[0] == "Code" || strings.HasPrefix(cells[0], "---") {
				continue
			}
			code, err := strconv.Atoi(cells[0])
			if err != nil {
				t.Errorf("exit-codes.md code cell %q: %v", cells[0], err)
				continue
			}
			name := strings.Trim(cells[1], "`")
			if name == "" {
				t.Errorf("exit-codes.md row %q has an empty constant name", fmt.Sprint(cells))
				continue
			}
			documented[name] = code
		}
		for name, code := range constants {
			got, ok := documented[name]
			if !ok {
				t.Errorf("exit constant %s undocumented in exit-codes.md", name)
			} else if got != code {
				t.Errorf("exit-codes.md says %s=%d; binary says %d", name, got, code)
			}
		}
		for name := range documented {
			if _, ok := constants[name]; !ok {
				t.Errorf("exit-codes.md documents %s; no such binary constant", name)
			}
		}
	})
}

func TestStatusStatesMatch(t *testing.T) {
	t.Run("status-state table and binary state set match", func(t *testing.T) {
		documented := make(map[status.State]struct{})
		for _, cells := range tableRows(skillLines(t, skills.Root+"/references/status-states.md")) {
			if len(cells) != 4 || cells[1] == "State" || strings.HasPrefix(cells[0], "---") {
				continue
			}
			cell := cells[1]
			if !strings.HasPrefix(cell, "`") || !strings.HasSuffix(cell, "`") || strings.Count(cell, "`") != 2 {
				t.Errorf("status-states.md State cell %q must contain exactly one backticked state", cell)
				continue
			}
			documented[status.State(strings.Trim(cell, "`"))] = struct{}{}
		}

		binary := make(map[status.State]struct{}, len(status.States))
		for _, state := range status.States {
			binary[state] = struct{}{}
			if _, ok := documented[state]; !ok {
				t.Errorf("status-states.md missing binary state %q", state)
			}
		}
		for state := range documented {
			if _, ok := binary[state]; !ok {
				t.Errorf("status-states.md documents state %q; status package cannot emit it", state)
			}
		}
	})
}
