package main

import (
	"bytes"
	"os"
	"testing"
)

func TestAgentctlSourceContainsNoNotImplementedDiagnostics(t *testing.T) {
	t.Parallel()

	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("not implemented")) {
		t.Fatal("main.go contains a user-visible not-implemented diagnostic")
	}
}
