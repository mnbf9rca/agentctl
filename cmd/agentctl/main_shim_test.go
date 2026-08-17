package main

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/skills"
)

func TestHiddenShimDispatchRunsBeforePublicCommandUsageLookup(t *testing.T) {
	called := false
	handler := hiddenShimCommandFunc(func(_ context.Context, arguments []string, stdout, stderr io.Writer) int {
		called = true
		if got, want := strings.Join(arguments, " "), "--session fleet --role planner --harness codex"; got != want {
			t.Fatalf("hidden arguments = %q, want %q", got, want)
		}
		return 37
	})
	var stdout, stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"__shim", "--session", "fleet", "--role", "planner", "--harness", "codex"}, &stdout, &stderr, dependencies{hiddenShim: handler})
	if !called || code != 37 {
		t.Fatalf("hidden dispatch called=%t code=%d, want true/37", called, code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("hidden dispatch output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestHiddenShimRouteIsAbsentFromAgentFacingInventories(t *testing.T) {
	if strings.Contains(globalUsage, "__shim") {
		t.Fatal("global usage exposes __shim")
	}
	if _, ok := commandUsage["__shim"]; ok {
		t.Fatal("commandUsage exposes __shim")
	}
	if _, ok := parsedCommandRegistry["__shim"]; ok {
		t.Fatal("parsedCommandRegistry exposes __shim")
	}
	err := fs.WalkDir(skills.Tree, skills.Root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		contents, err := fs.ReadFile(skills.Tree, path)
		if err != nil {
			return err
		}
		if bytes.Contains(contents, []byte("__shim")) {
			t.Fatalf("embedded agent-facing skill %s exposes __shim", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded skill: %v", err)
	}
}
