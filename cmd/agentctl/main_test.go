package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunRejectsUnknownCommandWithUsage(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"bogus"}, &stdout, &stderr)

	if code != exitUsage {
		t.Fatalf("run() = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "unknown command") || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want concise error and usage", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunRejectsDuplicateCommandOption(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"launch", "--session", "one", "--session", "two", "--roles", "planner:claude"}, &stdout, &stderr)

	if code != exitUsage {
		t.Fatalf("run() = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--session provided more than once") || !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("stderr = %q, want duplicate error and launch usage", stderr.String())
	}
}

func TestRunRejectsDuplicateBoolOption(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"status", "--json", "--json"}, &stdout, &stderr)

	if code != exitUsage {
		t.Fatalf("run() = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "--json provided more than once") {
		t.Fatalf("stderr = %q, want duplicate --json error", stderr.String())
	}
}

func TestRunRejectsExplicitlyEmptyModels(t *testing.T) {
	tests := [][]string{
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--models="},
		{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", ""},
	}

	for _, arguments := range tests {
		var stdout, stderr bytes.Buffer
		if code := run(arguments, &stdout, &stderr); code != exitUsage {
			t.Fatalf("run(%q) = %d, want %d", arguments, code, exitUsage)
		}
		if !strings.Contains(stderr.String(), "--models must not be empty") || !strings.Contains(stderr.String(), "Usage:") {
			t.Fatalf("stderr = %q, want empty-model error and usage", stderr.String())
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q, want empty", stdout.String())
		}
	}
}

func TestRunAcceptsOmittedModelsBeforeReachingStub(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"launch", "--session", "fleet", "--roles", "planner:claude"}, &stdout, &stderr)

	if code == exitUsage {
		t.Fatalf("run() = usage error; stderr = %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "launch: not implemented") {
		t.Fatalf("stderr = %q, want launch stub message", stderr.String())
	}
}

func TestRunAcceptsEachCommandShapeBeforeReachingStub(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "launch", args: []string{"launch", "--session", "fleet", "--roles", "planner:claude", "--models", "planner:fable", "--dir", "/tmp"}},
		{name: "attach", args: []string{"attach", "--session", "fleet"}},
		{name: "status", args: []string{"status", "--session", "fleet", "--json"}},
		{name: "clear", args: []string{"clear", "--session", "fleet", "planner"}},
		{name: "compact", args: []string{"compact", "--session", "fleet", "planner"}},
		{name: "kill", args: []string{"kill", "--session", "fleet"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := run(tt.args, &stdout, &stderr)
			if code == exitUsage {
				t.Fatalf("run(%q) = usage error; stderr = %q", tt.args, stderr.String())
			}
			if !strings.Contains(stderr.String(), tt.name+": not implemented") {
				t.Fatalf("stderr = %q, want command stub message", stderr.String())
			}
		})
	}
}

func TestRunRejectsInvalidCommandShapes(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "launch requires session", args: []string{"launch", "--roles", "planner:claude"}},
		{name: "launch requires roles", args: []string{"launch", "--session", "fleet"}},
		{name: "attach rejects positional", args: []string{"attach", "extra"}},
		{name: "status rejects positional", args: []string{"status", "extra"}},
		{name: "clear requires role", args: []string{"clear", "--session", "fleet"}},
		{name: "compact rejects extra role", args: []string{"compact", "planner", "extra"}},
		{name: "kill rejects positional", args: []string{"kill", "extra"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != exitUsage {
				t.Fatalf("run(%q) = %d, want %d; stderr = %q", tt.args, code, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stderr = %q, want usage", stderr.String())
			}
		})
	}
}

func TestRunHelpWritesUsageToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := run([]string{"status", "--help"}, &stdout, &stderr)

	if code != exitOK {
		t.Fatalf("run() = %d, want %d", code, exitOK)
	}
	if !strings.Contains(stdout.String(), "Usage: agentctl status") {
		t.Fatalf("stdout = %q, want status usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}
