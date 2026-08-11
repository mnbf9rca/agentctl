package main

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

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

func markdownInvocations(tree fs.FS, root string) ([]documentedInvocation, error) {
	var invocations []documentedInvocation
	err := fs.WalkDir(tree, root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path.Ext(filePath) != ".md" {
			return nil
		}
		raw, err := fs.ReadFile(tree, filePath)
		if err != nil {
			return fmt.Errorf("read %s: %w", filePath, err)
		}
		var lines []string
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		for scanner.Scan() {
			lines = append(lines, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scan %s: %w", filePath, err)
		}
		for _, line := range fencedCommands(lines) {
			invocation, err := parseDocumentedInvocation(line)
			if err != nil {
				return fmt.Errorf("%s: %w", filePath, err)
			}
			invocations = append(invocations, invocation)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return invocations, nil
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

func parseStatusStateTable(lines []string) (map[status.RuntimeState]struct{}, error) {
	rows, err := strictTableRows(lines, []string{"Order", "State", "The claim it makes", "What it does not claim"})
	if err != nil {
		return nil, err
	}
	documented := make(map[status.RuntimeState]struct{}, len(rows))
	for _, cells := range rows {
		stateName, err := backtickedCell(cells[1])
		if err != nil {
			return nil, fmt.Errorf("status State cell: %w", err)
		}
		state := status.RuntimeState(stateName)
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

type sourceConstant struct {
	name  string
	value string
}

func sourceConstants(filename string, source []byte, prefix string) ([]sourceConstant, error) {
	parsed, err := parser.ParseFile(token.NewFileSet(), filename, source, 0)
	if err != nil {
		return nil, err
	}
	var constants []sourceConstant
	var inspectErr error
	ast.Inspect(parsed, func(node ast.Node) bool {
		if inspectErr != nil {
			return false
		}
		general, ok := node.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			return true
		}
		for _, specification := range general.Specs {
			values := specification.(*ast.ValueSpec)
			for index, identifier := range values.Names {
				if !strings.HasPrefix(identifier.Name, prefix) {
					continue
				}
				if index >= len(values.Values) {
					inspectErr = fmt.Errorf("%s: %s has no explicit evaluable value", filename, identifier.Name)
					return false
				}
				value, err := constantLiteral(values.Values[index])
				if err != nil {
					inspectErr = fmt.Errorf("%s: %s: %w", filename, identifier.Name, err)
					return false
				}
				constants = append(constants, sourceConstant{name: identifier.Name, value: value})
			}
		}
		return true
	})
	if inspectErr != nil {
		return nil, inspectErr
	}
	return constants, nil
}

func constantLiteral(expression ast.Expr) (string, error) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		switch value.Kind {
		case token.INT:
			parsed, err := strconv.ParseInt(value.Value, 0, 64)
			if err != nil {
				return "", err
			}
			return strconv.FormatInt(parsed, 10), nil
		case token.STRING:
			parsed, err := strconv.Unquote(value.Value)
			if err != nil {
				return "", err
			}
			return parsed, nil
		default:
			return "", fmt.Errorf("literal kind %s is not an integer or string", value.Kind)
		}
	case *ast.ParenExpr:
		return constantLiteral(value.X)
	case *ast.UnaryExpr:
		if value.Op != token.ADD && value.Op != token.SUB {
			return "", fmt.Errorf("operator %s is not evaluable", value.Op)
		}
		literal, err := constantLiteral(value.X)
		if err != nil {
			return "", err
		}
		integer, err := strconv.ParseInt(literal, 10, 64)
		if err != nil {
			return "", fmt.Errorf("unary operand %q is not an integer", literal)
		}
		if value.Op == token.SUB {
			integer = -integer
		}
		return strconv.FormatInt(integer, 10), nil
	default:
		return "", fmt.Errorf("expression %T is not a supported constant literal", expression)
	}
}

func packageConstants(directory, prefix string) ([]sourceConstant, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]string)
	var constants []sourceConstant
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		declared, err := sourceConstants(path, raw, prefix)
		if err != nil {
			return nil, err
		}
		for _, constant := range declared {
			if first, duplicate := seen[constant.name]; duplicate {
				return nil, fmt.Errorf("duplicate constant %s in %s and %s", constant.name, first, path)
			}
			seen[constant.name] = path
			constants = append(constants, constant)
		}
	}
	return constants, nil
}

func sourceDirectories(t *testing.T) (string, string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate skill_contract_test.go")
	}
	commandDirectory := filepath.Dir(filename)
	return commandDirectory, filepath.Join(commandDirectory, "..", "..", "internal", "status")
}

func exitConstantsFromSource(t *testing.T) map[string]int {
	t.Helper()
	commandDirectory, _ := sourceDirectories(t)
	declared, err := packageConstants(commandDirectory, "exit")
	if err != nil {
		t.Fatal(err)
	}
	constants := make(map[string]int, len(declared))
	for _, constant := range declared {
		value, err := strconv.Atoi(constant.value)
		if err != nil {
			t.Fatalf("exit constant %s value %q is not numeric: %v", constant.name, constant.value, err)
		}
		constants[constant.name] = value
	}
	return constants
}

func stateConstantsFromSource(t *testing.T) map[string]status.RuntimeState {
	t.Helper()
	_, statusDirectory := sourceDirectories(t)
	declared, err := packageConstants(statusDirectory, "RuntimeState")
	if err != nil {
		t.Fatal(err)
	}
	constants := make(map[string]status.RuntimeState, len(declared))
	for _, constant := range declared {
		constants[constant.name] = status.RuntimeState(constant.value)
	}
	return constants
}

func agentCommandInventory(registry map[string]parsedCommandSpec) map[string]map[string]struct{} {
	inventory := make(map[string]map[string]struct{})
	for command, specification := range registry {
		if !specification.agentFacing {
			continue
		}
		flags := make(map[string]struct{}, len(specification.flags))
		for _, registered := range specification.flags {
			flags["--"+registered] = struct{}{}
		}
		inventory[command] = flags
	}
	return inventory
}

func compareAgentDocumentation(invocations []documentedInvocation, registry map[string]parsedCommandSpec) []string {
	expected := agentCommandInventory(registry)
	documented := make(map[string]map[string]struct{})
	var mismatches []string
	for _, invocation := range invocations {
		expectedFlags, knownCommand := expected[invocation.command]
		if !knownCommand {
			mismatches = append(mismatches, fmt.Sprintf("skill documents non-agent command %q", invocation.command))
		}
		for _, flag := range invocation.flags {
			if _, ok := expectedFlags[flag]; !ok {
				mismatches = append(mismatches, fmt.Sprintf("skill documents non-agent flag %s %s", invocation.command, flag))
			}
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
			mismatches = append(mismatches, fmt.Sprintf("agent command %q is undocumented", command))
			continue
		}
		for flag := range expectedFlags {
			if _, ok := documentedFlags[flag]; !ok {
				mismatches = append(mismatches, fmt.Sprintf("agent flag %s %s is undocumented", command, flag))
			}
		}
	}
	return mismatches
}

func compareExitConstants(declared, documented map[string]int) []string {
	var mismatches []string
	for name, value := range declared {
		documentedValue, ok := documented[name]
		if !ok {
			mismatches = append(mismatches, fmt.Sprintf("exit constant %s is undocumented", name))
		} else if documentedValue != value {
			mismatches = append(mismatches, fmt.Sprintf("exit constant %s is %d; documentation says %d", name, value, documentedValue))
		}
	}
	for name := range documented {
		if _, ok := declared[name]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("documented exit constant %s is undeclared", name))
		}
	}
	return mismatches
}

func compareStateConstants(declared map[string]status.RuntimeState, accessor []status.RuntimeState, documented map[status.RuntimeState]struct{}) []string {
	var mismatches []string
	declaredValues := make(map[status.RuntimeState]string)
	for name, state := range declared {
		if first, duplicate := declaredValues[state]; duplicate {
			mismatches = append(mismatches, fmt.Sprintf("RuntimeState constants %s and %s both declare %q", first, name, state))
			continue
		}
		declaredValues[state] = name
		if _, ok := documented[state]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("declared state %s=%q is undocumented", name, state))
		}
	}
	for state := range documented {
		if _, ok := declaredValues[state]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("documented state %q has no State constant", state))
		}
	}

	accessorValues := make(map[status.RuntimeState]struct{}, len(accessor))
	for _, state := range accessor {
		if _, duplicate := accessorValues[state]; duplicate {
			mismatches = append(mismatches, fmt.Sprintf("status.RuntimeStates() reports duplicate state %q", state))
			continue
		}
		accessorValues[state] = struct{}{}
		if _, ok := declaredValues[state]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("status.RuntimeStates() returns undeclared state %q", state))
		}
	}
	for state, name := range declaredValues {
		if _, ok := accessorValues[state]; !ok {
			mismatches = append(mismatches, fmt.Sprintf("RuntimeState constant %s=%q is missing from status.RuntimeStates()", name, state))
		}
	}
	return mismatches
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

func TestSourceConstantInventoryIncludesSyntheticDeclarations(t *testing.T) {
	for name, test := range map[string]struct {
		source string
		prefix string
		want   sourceConstant
	}{
		"exit": {
			source: "package main\nconst (\nexitOK = 0\nexitSynthetic = 9\n)\n",
			prefix: "exit",
			want:   sourceConstant{name: "exitSynthetic", value: "9"},
		},
		"state": {
			source: "package status\ntype State string\nconst StateSynthetic State = \"synthetic\"\n",
			prefix: "State",
			want:   sourceConstant{name: "StateSynthetic", value: "synthetic"},
		},
		"function local exit": {
			source: "package main\nfunc local() {\nconst exitSynthetic = 9\n}\n",
			prefix: "exit",
			want:   sourceConstant{name: "exitSynthetic", value: "9"},
		},
		"function local state": {
			source: "package status\ntype State string\nfunc local() {\nconst StateSynthetic State = \"synthetic\"\n}\n",
			prefix: "State",
			want:   sourceConstant{name: "StateSynthetic", value: "synthetic"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			constants, err := sourceConstants(name+".go", []byte(test.source), test.prefix)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, constant := range constants {
				if constant == test.want {
					found = true
				}
			}
			if !found {
				t.Fatalf("sourceConstants() = %#v, missing %#v", constants, test.want)
			}
		})
	}

	t.Run("unsupported expression fails closed", func(t *testing.T) {
		_, err := sourceConstants("exit.go", []byte("package main\nconst exitSynthetic = 1 << 2\n"), "exit")
		if err == nil {
			t.Fatal("sourceConstants() error = nil, want unevaluable declaration rejection")
		}
	})

	t.Run("synthetic exit enters comparison", func(t *testing.T) {
		constants, err := sourceConstants("exit.go", []byte("package main\nconst (\nexitOK = 0\nexitSynthetic = 9\n)\n"), "exit")
		if err != nil {
			t.Fatal(err)
		}
		declared := make(map[string]int)
		for _, constant := range constants {
			value, err := strconv.Atoi(constant.value)
			if err != nil {
				t.Fatal(err)
			}
			declared[constant.name] = value
		}
		mismatches := compareExitConstants(declared, map[string]int{"exitOK": 0})
		if !containsMismatch(mismatches, "exit constant exitSynthetic is undocumented") {
			t.Fatalf("compareExitConstants() = %#v, want synthetic declaration drift", mismatches)
		}
	})

	t.Run("synthetic state enters comparisons", func(t *testing.T) {
		constants, err := sourceConstants("status.go", []byte("package status\ntype RuntimeState string\nconst (\nRuntimeStateRunning RuntimeState = \"running\"\nRuntimeStateSynthetic RuntimeState = \"synthetic\"\n)\n"), "RuntimeState")
		if err != nil {
			t.Fatal(err)
		}
		declared := make(map[string]status.RuntimeState)
		for _, constant := range constants {
			declared[constant.name] = status.RuntimeState(constant.value)
		}
		mismatches := compareStateConstants(declared, []status.RuntimeState{status.RuntimeStateRunning}, map[status.RuntimeState]struct{}{status.RuntimeStateRunning: {}})
		if !containsMismatch(mismatches, `declared state RuntimeStateSynthetic="synthetic" is undocumented`) ||
			!containsMismatch(mismatches, `RuntimeState constant RuntimeStateSynthetic="synthetic" is missing from status.RuntimeStates()`) {
			t.Fatalf("compareStateConstants() = %#v, want synthetic declaration drift", mismatches)
		}
	})
}

func containsMismatch(mismatches []string, want string) bool {
	for _, mismatch := range mismatches {
		if mismatch == want {
			return true
		}
	}
	return false
}

func TestMarkdownInvocationWalkIncludesReferences(t *testing.T) {
	tree := fstest.MapFS{
		"agentctl/SKILL.md":              {Data: []byte("```\nagentctl status --json\n```\n")},
		"agentctl/references/control.md": {Data: []byte("```sh\nagentctl clear --session SESSION ROLE\n```\n")},
	}
	invocations, err := markdownInvocations(tree, "agentctl")
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for _, invocation := range invocations {
		commands = append(commands, invocation.command)
	}
	if !reflect.DeepEqual(commands, []string{"status", "clear"}) {
		t.Fatalf("markdownInvocations() commands = %#v, want root and reference commands", commands)
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
		invocations, err := markdownInvocations(skills.Tree, skills.Root)
		if err != nil {
			t.Fatal(err)
		}
		for _, invocation := range invocations {
			if _, ok := commandUsage[invocation.command]; !ok {
				t.Errorf("skill documents %q; not in commandUsage", invocation.command)
			}
			if _, err := parseCommand(invocation.command, substituteDocumentedMetavariables(invocation.argv)); err != nil {
				if isUnknownFlagError(err) {
					t.Errorf("skill documents unsupported flag in %s %#v: %v", invocation.command, invocation.argv, err)
					continue
				}
				t.Errorf("parseCommand rejects documented invocation %s %#v: %v", invocation.command, invocation.argv, err)
			}
		}
		for _, mismatch := range compareAgentDocumentation(invocations, parsedCommandRegistry) {
			t.Error(mismatch)
		}
	})
}

func TestParsedCommandRegistryCouplesParserAndAgentDocumentation(t *testing.T) {
	original := parsedCommandRegistry["status"]
	mutated := original
	mutated.flags = append(append([]string(nil), original.flags...), "synthetic")
	parsedCommandRegistry["status"] = mutated
	t.Cleanup(func() { parsedCommandRegistry["status"] = original })

	_, err := parseCommand("status", []string{"--synthetic"})
	if err == nil || !strings.Contains(err.Error(), "unsupported registered flag") {
		t.Fatalf("parseCommand() error = %v, want fail-closed unsupported registered flag", err)
	}
	invocations, err := markdownInvocations(skills.Tree, skills.Root)
	if err != nil {
		t.Fatal(err)
	}
	mismatches := compareAgentDocumentation(invocations, parsedCommandRegistry)
	found := false
	for _, mismatch := range mismatches {
		if strings.Contains(mismatch, "status --synthetic is undocumented") {
			found = true
		}
	}
	if !found {
		t.Fatalf("compareAgentDocumentation() = %#v, want registered synthetic flag drift", mismatches)
	}
}

func TestParsedCommandRegistryProjectsRegisteredOptions(t *testing.T) {
	statusOptions, err := parseCommand("status", []string{"--session", "fleet", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if statusOptions.session != "fleet" || !statusOptions.sessionSet || !statusOptions.json {
		t.Fatalf("status options = %#v, want session fleet explicitly set and JSON true", statusOptions)
	}
	omittedStatus, err := parseCommand("status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if omittedStatus.sessionSet || omittedStatus.json {
		t.Fatalf("omitted status options = %#v, want unset session and false JSON", omittedStatus)
	}

	relaunchOptions, err := parseCommand("relaunch", []string{
		"--session", "fleet", "--harness", "claude", "--model", "model",
		"--effort", "high", "--dir", "/repo", "planner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if relaunchOptions.session != "fleet" || !relaunchOptions.sessionSet || relaunchOptions.role != "planner" ||
		relaunchOptions.harness == nil || *relaunchOptions.harness != "claude" ||
		relaunchOptions.model == nil || *relaunchOptions.model != "model" ||
		relaunchOptions.effort == nil || *relaunchOptions.effort != "high" ||
		relaunchOptions.directory == nil || *relaunchOptions.directory != "/repo" {
		t.Fatalf("relaunch options = %#v, want every registered value projected", relaunchOptions)
	}

	explicitEmpty, err := parseCommand("relaunch", []string{"--model=", "planner"})
	if err != nil {
		t.Fatal(err)
	}
	if explicitEmpty.model == nil || *explicitEmpty.model != "" {
		t.Fatalf("explicit empty model = %#v, want non-nil pointer to empty value", explicitEmpty.model)
	}
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
		constants := exitConstantsFromSource(t)
		documented, err := parseExitCodeTable(skillLines(t, skills.Root+"/references/exit-codes.md"))
		if err != nil {
			t.Fatal(err)
		}
		for _, mismatch := range compareExitConstants(constants, documented) {
			t.Error(mismatch)
		}
	})
}

func TestStatusStatesMatch(t *testing.T) {
	t.Run("status-state table and binary state set match", func(t *testing.T) {
		documented, err := parseStatusStateTable(skillLines(t, skills.Root+"/references/status-states.md"))
		if err != nil {
			t.Fatal(err)
		}

		states := status.RuntimeStates()
		if len(states) == 0 {
			t.Fatal("status.RuntimeStates() returned no states")
		}
		first := states[0]
		states[0] = ""
		if fresh := status.RuntimeStates(); fresh[0] != first {
			t.Fatalf("status.RuntimeStates() returned shared mutable storage: fresh first state = %q, want %q", fresh[0], first)
		}

		for _, mismatch := range compareStateConstants(stateConstantsFromSource(t), status.RuntimeStates(), documented) {
			t.Error(mismatch)
		}
	})
}
