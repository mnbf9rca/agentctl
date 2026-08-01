package main

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunControlDeliversRegisteredOperationToResolvedRole(t *testing.T) {
	tests := []struct {
		operation string
		role      string
		payload   string
	}{
		{operation: "clear", role: "planner", payload: "/clear"},
		{operation: "compact", role: "reviewer", payload: "/compact"},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			session := tmuxx.Session{ID: "$4", Name: "epic123"}
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				return session, nil
			})
			type invocation struct {
				operation string
				session   tmuxx.Session
				role      string
			}
			var got invocation
			controller := controlExecutorFunc(func(_ context.Context, operation string, target tmuxx.Session, role string) error {
				got = invocation{operation: operation, session: target, role: role}
				return nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithControlDependencies(context.Background(), []string{tt.operation, "--session", "epic123", tt.role}, &stdout, &stderr, resolver, controller)

			if code != exitOK {
				t.Fatalf("runWithControlDependencies() = %d, want %d; stderr = %q", code, exitOK, stderr.String())
			}
			want := invocation{operation: tt.operation, session: session, role: tt.role}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Execute() invocation = %#v, want %#v", got, want)
			}
			wantOutput := "agentctl: delivered " + tt.payload + " to epic123:" + tt.role + "\n"
			if stdout.String() != wantOutput {
				t.Fatalf("stdout = %q, want %q", stdout.String(), wantOutput)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestRunControlRejectsCallerPayloadInputsBeforeDependencies(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "extra positional text", args: []string{"clear", "planner", "erase this"}},
		{name: "text option", args: []string{"clear", "--text", "erase this", "planner"}},
		{name: "command option", args: []string{"compact", "--command=/rename", "planner"}},
		{name: "raw option", args: []string{"clear", "--raw=/rename", "planner"}},
		{name: "keys option", args: []string{"compact", "--keys=C-u", "planner"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolverCalled := false
			controllerCalled := false
			resolver := resolverFunc(func(context.Context, *string) (tmuxx.Session, error) {
				resolverCalled = true
				return tmuxx.Session{ID: "$4", Name: "epic123"}, nil
			})
			controller := controlExecutorFunc(func(context.Context, string, tmuxx.Session, string) error {
				controllerCalled = true
				return nil
			})
			var stdout, stderr bytes.Buffer

			code := runWithControlDependencies(context.Background(), tt.args, &stdout, &stderr, resolver, controller)

			if code != exitUsage {
				t.Fatalf("runWithControlDependencies(%q) = %d, want %d", tt.args, code, exitUsage)
			}
			if resolverCalled || controllerCalled {
				t.Fatalf("dependency calls = resolver:%v controller:%v, want neither", resolverCalled, controllerCalled)
			}
			if stdout.Len() != 0 || !strings.Contains(stderr.String(), "Usage:") {
				t.Fatalf("stdout = %q, stderr = %q, want usage error only", stdout.String(), stderr.String())
			}
		})
	}
}

type controlExecutorFunc func(context.Context, string, tmuxx.Session, string) error

func (f controlExecutorFunc) Execute(ctx context.Context, operation string, session tmuxx.Session, role string) error {
	return f(ctx, operation, session, role)
}
