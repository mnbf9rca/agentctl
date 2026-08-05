package main

import (
	"bufio"
	"bytes"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/skills"
)

type documentedInvocation struct {
	command string
	argv    []string
	flags   []string
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

func parseDocumentedInvocation(invocation string) (documentedInvocation, error) {
	fields := strings.Fields(invocation)
	if len(fields) < 2 || fields[0] != "agentctl" {
		return documentedInvocation{}, fmt.Errorf("invalid documented invocation %q", invocation)
	}

	parsed := documentedInvocation{command: fields[1], argv: append([]string(nil), fields[2:]...)}
	for _, argument := range parsed.argv {
		if strings.HasPrefix(argument, "-") {
			parsed.flags = append(parsed.flags, strings.SplitN(argument, "=", 2)[0])
		}
	}
	return parsed, nil
}

func substituteDocumentedMetavariables(argv []string) []string {
	arguments := make([]string, len(argv))
	for index, argument := range argv {
		switch argument {
		case "SESSION":
			arguments[index] = "fixture"
		case "ROLE":
			arguments[index] = "role"
		default:
			arguments[index] = argument
		}
	}
	return arguments
}

func isUnknownFlagError(err error) bool {
	// cliflags delegates unknown-flag reporting to the stable stdlib flag
	// package phrase. Matching only this phrase distinguishes an unsupported
	// documented flag from other parse failures; every parse error still fails
	// the exact documented invocation.
	return err != nil && strings.Contains(err.Error(), "flag provided but not defined")
}

func strictTableRows(lines, header []string) ([][]string, error) {
	headerSeen := false
	ruleSeen := false
	var rows [][]string
	for lineNumber, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "|") {
			continue
		}
		if !strings.HasPrefix(trimmed, "|") || !strings.HasSuffix(trimmed, "|") {
			return nil, fmt.Errorf("line %d: pipe-looking table row is malformed", lineNumber+1)
		}
		cells := strings.Split(trimmed, "|")
		cells = cells[1 : len(cells)-1]
		if len(cells) != len(header) {
			return nil, fmt.Errorf("line %d: table has %d cells, want %d", lineNumber+1, len(cells), len(header))
		}
		for index := range cells {
			cells[index] = strings.TrimSpace(cells[index])
			if cells[index] == "" {
				return nil, fmt.Errorf("line %d: table cell %d is empty", lineNumber+1, index+1)
			}
		}
		if !headerSeen {
			if !reflect.DeepEqual(cells, header) {
				return nil, fmt.Errorf("line %d: table header = %#v, want %#v", lineNumber+1, cells, header)
			}
			headerSeen = true
			continue
		}
		if !ruleSeen {
			for _, cell := range cells {
				if strings.Trim(cell, "-") != "" {
					return nil, fmt.Errorf("line %d: table separator cell %q is invalid", lineNumber+1, cell)
				}
			}
			ruleSeen = true
			continue
		}
		rows = append(rows, cells)
	}
	if !headerSeen {
		return nil, fmt.Errorf("table header %#v is missing", header)
	}
	if !ruleSeen {
		return nil, fmt.Errorf("table separator after %#v is missing", header)
	}
	return rows, nil
}

func backtickedCell(cell string) (string, error) {
	if !strings.HasPrefix(cell, "`") || !strings.HasSuffix(cell, "`") || strings.Count(cell, "`") != 2 {
		return "", fmt.Errorf("%q must contain exactly one backticked token", cell)
	}
	value := strings.Trim(cell, "`")
	if value == "" {
		return "", fmt.Errorf("%q contains an empty token", cell)
	}
	return value, nil
}

func parseExitCodeTable(lines []string) (map[string]int, error) {
	rows, err := strictTableRows(lines, []string{"Code", "Constant", "Claim"})
	if err != nil {
		return nil, err
	}
	documented := make(map[string]int, len(rows))
	codes := make(map[int]struct{}, len(rows))
	for _, cells := range rows {
		code, err := strconv.Atoi(cells[0])
		if err != nil {
			return nil, fmt.Errorf("exit-code cell %q: %w", cells[0], err)
		}
		if _, duplicate := codes[code]; duplicate {
			return nil, fmt.Errorf("duplicate exit code %d", code)
		}
		codes[code] = struct{}{}
		name, err := backtickedCell(cells[1])
		if err != nil {
			return nil, fmt.Errorf("exit constant cell: %w", err)
		}
		if _, duplicate := documented[name]; duplicate {
			return nil, fmt.Errorf("duplicate exit constant %q", name)
		}
		documented[name] = code
	}
	return documented, nil
}

func parseStatusStateTable(lines []string) (map[status.State]struct{}, error) {
	rows, err := strictTableRows(lines, []string{"Order", "State", "The claim it makes", "What it does not claim"})
	if err != nil {
		return nil, err
	}
	documented := make(map[status.State]struct{}, len(rows))
	for _, cells := range rows {
		stateName, err := backtickedCell(cells[1])
		if err != nil {
			return nil, fmt.Errorf("status State cell: %w", err)
		}
		state := status.State(stateName)
		if _, duplicate := documented[state]; duplicate {
			return nil, fmt.Errorf("duplicate status state %q", state)
		}
		documented[state] = struct{}{}
	}
	return documented, nil
}

func parseMetadataVersion(lines []string) (string, error) {
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", fmt.Errorf("SKILL.md must begin with YAML frontmatter")
	}
	closing := -1
	for index := 1; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == "---" {
			closing = index
			break
		}
	}
	if closing == -1 {
		return "", fmt.Errorf("SKILL.md frontmatter is unterminated")
	}

	metadataSeen := false
	inMetadata := false
	versionSeen := false
	var version string
	for index, raw := range lines[1:closing] {
		lineNumber := index + 2
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t") {
			if !inMetadata || !strings.HasPrefix(raw, "  ") || strings.HasPrefix(raw, "   ") {
				return "", fmt.Errorf("frontmatter line %d has unsupported nesting", lineNumber)
			}
			key, value, found := strings.Cut(strings.TrimSpace(raw), ":")
			if !found || key == "" {
				return "", fmt.Errorf("frontmatter line %d is not a mapping", lineNumber)
			}
			if key != "version" {
				continue
			}
			if versionSeen {
				return "", fmt.Errorf("frontmatter carries duplicate metadata.version")
			}
			versionSeen = true
			parsed, err := parseVersionScalar(value)
			if err != nil {
				return "", fmt.Errorf("metadata.version: %w", err)
			}
			version = parsed
			continue
		}

		key, value, found := strings.Cut(raw, ":")
		if !found || key == "" {
			return "", fmt.Errorf("frontmatter line %d is not a mapping", lineNumber)
		}
		inMetadata = false
		if key != "metadata" {
			continue
		}
		if metadataSeen {
			return "", fmt.Errorf("frontmatter carries duplicate metadata mapping")
		}
		if strings.TrimSpace(value) != "" {
			return "", fmt.Errorf("frontmatter metadata must be a mapping")
		}
		metadataSeen = true
		inMetadata = true
	}
	if !metadataSeen {
		return "", fmt.Errorf("SKILL.md frontmatter carries no metadata mapping")
	}
	if !versionSeen {
		return "", fmt.Errorf("SKILL.md frontmatter carries no metadata.version")
	}
	return version, nil
}

func parseVersionScalar(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("value is empty")
	}
	if strings.HasPrefix(value, `"`) || strings.HasPrefix(value, `'`) {
		quote := value[0]
		if len(value) < 2 || value[len(value)-1] != quote {
			return "", fmt.Errorf("value %q has unmatched quote", value)
		}
		value = value[1 : len(value)-1]
	} else if strings.ContainsAny(value, `"'`) {
		return "", fmt.Errorf("value %q has unmatched quote", value)
	}
	if value == "" {
		return "", fmt.Errorf("value is empty")
	}
	components := strings.Split(value, ".")
	if len(components) < 2 {
		return "", fmt.Errorf("value %q is not dotted", value)
	}
	for _, component := range components {
		if component == "" {
			return "", fmt.Errorf("value %q has an empty component", value)
		}
		for _, character := range component {
			if character < '0' || character > '9' {
				return "", fmt.Errorf("value %q component %q is not numeric", value, component)
			}
		}
		if _, err := strconv.Atoi(component); err != nil {
			return "", fmt.Errorf("value %q component %q: %w", value, component, err)
		}
	}
	return value, nil
}

func TestSkillBudget(t *testing.T) {
	t.Run("SKILL.md line count", func(t *testing.T) {
		if lines := len(skillLines(t, skills.Root+"/SKILL.md")); lines > 150 {
			t.Fatalf("SKILL.md is %d lines; budget is 150 (spec §3.1)", lines)
		}
	})
}

func TestSkillVersionParses(t *testing.T) {
	t.Run("leading frontmatter metadata version is numeric and dotted", func(t *testing.T) {
		version, err := parseMetadataVersion(skillLines(t, skills.Root+"/SKILL.md"))
		if err != nil {
			t.Fatal(err)
		}
		if version == "" {
			t.Fatal("metadata.version is empty")
		}
	})
}

func TestMetadataVersionParsingRejectsInvalidFrontmatter(t *testing.T) {
	valid := func(body string) []string { return strings.Split(body, "\n") }
	for name, lines := range map[string][]string{
		"body version does not substitute": valid("---\nname: agentctl\nmetadata:\n---\nversion: 1.2.3"),
		"duplicate metadata":               valid("---\nmetadata:\n  version: 1.2\nmetadata:\n  version: 1.3\n---"),
		"duplicate version":                valid("---\nmetadata:\n  version: 1.2\n  version: 1.3\n---"),
		"blank version":                    valid("---\nmetadata:\n  version: \"\"\n---"),
		"unmatched quote":                  valid("---\nmetadata:\n  version: \"1.2\n---"),
		"non numeric component":            valid("---\nmetadata:\n  version: 1.x\n---"),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseMetadataVersion(lines); err == nil {
				t.Fatal("parseMetadataVersion() error = nil, want rejection")
			}
		})
	}
}

func TestDocumentedInvocationsPreserveExactArgv(t *testing.T) {
	for name, test := range map[string]struct {
		line      string
		wantArgv  []string
		wantFlags []string
	}{
		"single dash flag": {
			line:      "agentctl status -bogus",
			wantArgv:  []string{"-bogus"},
			wantFlags: []string{"-bogus"},
		},
		"duplicate flag": {
			line:      "agentctl status --json --json",
			wantArgv:  []string{"--json", "--json"},
			wantFlags: []string{"--json", "--json"},
		},
		"metavariables only": {
			line:      "agentctl clear --session SESSION ROLE",
			wantArgv:  []string{"--session", "SESSION", "ROLE"},
			wantFlags: []string{"--session"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			invocation, err := parseDocumentedInvocation(test.line)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(invocation.argv, test.wantArgv) {
				t.Fatalf("argv = %#v, want %#v", invocation.argv, test.wantArgv)
			}
			if !reflect.DeepEqual(invocation.flags, test.wantFlags) {
				t.Fatalf("flags = %#v, want %#v", invocation.flags, test.wantFlags)
			}
		})
	}

	for _, line := range []string{"agentctl status -bogus", "agentctl status --json --json"} {
		invocation, err := parseDocumentedInvocation(line)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseCommand(invocation.command, substituteDocumentedMetavariables(invocation.argv)); err == nil {
			t.Fatalf("parseCommand(%q, %#v) error = nil, want rejection", invocation.command, invocation.argv)
		}
	}
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
			invocation, err := parseDocumentedInvocation(line)
			if err != nil {
				t.Error(err)
				continue
			}
			if _, ok := commandUsage[invocation.command]; !ok {
				t.Errorf("skill documents %q; not in commandUsage", invocation.command)
			}
			expectedFlags, knownCommand := expected[invocation.command]
			if !knownCommand {
				t.Errorf("skill documents agent command %q; it is not in the agent-facing command contract", invocation.command)
			}
			for _, flag := range invocation.flags {
				if _, ok := expectedFlags[flag]; !ok {
					t.Errorf("skill documents %s %s; it is not in the agent-facing flag contract", invocation.command, flag)
				}
			}
			if _, err := parseCommand(invocation.command, substituteDocumentedMetavariables(invocation.argv)); err != nil {
				if isUnknownFlagError(err) {
					t.Errorf("skill documents unsupported flag in %q: %v", line, err)
					continue
				}
				t.Errorf("parseCommand rejects documented invocation %q: %v", line, err)
			}

			if documented[invocation.command] == nil {
				documented[invocation.command] = make(map[string]struct{})
			}
			for _, flag := range invocation.flags {
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

func TestTableParsersRejectMalformedAndDuplicateRows(t *testing.T) {
	exitTable := func(rows ...string) []string {
		return append([]string{"| Code | Constant | Claim |", "| --- | --- | --- |"}, rows...)
	}
	for name, lines := range map[string][]string{
		"pipe-looking malformed": exitTable("| 0 | `exitOK` | claim"),
		"wrong arity":            exitTable("| 0 | `exitOK` |"),
		"empty required cell":    exitTable("| 0 |  | claim |"),
		"duplicate constant": exitTable(
			"| 0 | `exitOK` | claim |",
			"| 1 | `exitOK` | another claim |",
		),
	} {
		t.Run("exit "+name, func(t *testing.T) {
			if _, err := parseExitCodeTable(lines); err == nil {
				t.Fatal("parseExitCodeTable() error = nil, want rejection")
			}
		})
	}

	statusTable := func(rows ...string) []string {
		return append([]string{"| Order | State | The claim it makes | What it does not claim |", "| --- | --- | --- | --- |"}, rows...)
	}
	for name, lines := range map[string][]string{
		"pipe-looking malformed": statusTable("| 1 | `running` | claim | not claim"),
		"wrong arity":            statusTable("| 1 | `running` | claim |"),
		"empty required cell":    statusTable("| 1 |  | claim | not claim |"),
		"duplicate state": statusTable(
			"| 1 | `running` | claim | not claim |",
			"| 2 | `running` | another claim | another non-claim |",
		),
	} {
		t.Run("status "+name, func(t *testing.T) {
			if _, err := parseStatusStateTable(lines); err == nil {
				t.Fatal("parseStatusStateTable() error = nil, want rejection")
			}
		})
	}
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
		documented, err := parseExitCodeTable(skillLines(t, skills.Root+"/references/exit-codes.md"))
		if err != nil {
			t.Fatal(err)
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
		documented, err := parseStatusStateTable(skillLines(t, skills.Root+"/references/status-states.md"))
		if err != nil {
			t.Fatal(err)
		}

		states := status.States()
		if len(states) == 0 {
			t.Fatal("status.States() returned no states")
		}
		first := states[0]
		states[0] = ""
		if fresh := status.States(); fresh[0] != first {
			t.Fatalf("status.States() returned shared mutable storage: fresh first state = %q, want %q", fresh[0], first)
		}

		binary := make(map[status.State]struct{}, len(states))
		for _, state := range status.States() {
			if _, duplicate := binary[state]; duplicate {
				t.Errorf("status.States() reports duplicate state %q", state)
				continue
			}
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
