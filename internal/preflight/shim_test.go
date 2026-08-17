package preflight

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
)

func TestCheckShimExecutablesResolvesCurrentBinaryBeforeExternalRequirements(t *testing.T) {
	t.Parallel()

	var calls []string
	got, err := CheckShimExecutables(
		config.FleetConfig{Roles: []config.RoleConfig{
			{Name: "planner", Harness: config.HarnessCodex},
			{Name: "reviewer", Harness: config.HarnessClaude},
			{Name: "worker", Harness: config.HarnessCodex},
		}}, true,
		func(name string) (string, error) {
			calls = append(calls, "look:"+name)
			return "/tools/" + name, nil
		},
		func() (string, error) {
			calls = append(calls, "self")
			return "/Applications/agentctl current", nil
		},
	)
	if err != nil {
		t.Fatalf("CheckShimExecutables() error = %v", err)
	}
	if got != "/Applications/agentctl current" {
		t.Fatalf("CheckShimExecutables() = %q, want current executable", got)
	}
	wantCalls := []string{"self", "look:tmux", "look:amq", "look:codex", "look:claude"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

// Production mutation caught: ignoring requireTmux would make detached shim
// preflight insert a tmux lookup before its common requirements.
func TestCheckShimExecutablesDetachedPreservesCommonRequirementOrder(t *testing.T) {
	t.Parallel()

	var calls []string
	_, err := CheckShimExecutables(
		config.FleetConfig{Roles: []config.RoleConfig{
			{Name: "planner", Harness: config.HarnessClaude},
			{Name: "coder", Harness: config.HarnessCodex},
		}},
		false,
		func(name string) (string, error) {
			calls = append(calls, "look:"+name)
			return "/tools/" + name, nil
		},
		func() (string, error) {
			calls = append(calls, "self")
			return "/Applications/agentctl", nil
		},
	)
	if err != nil {
		t.Fatalf("CheckShimExecutables() error = %v", err)
	}
	want := []string{"self", "look:amq", "look:claude", "look:codex"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCheckShimExecutablesRefusesUnresolvedOrRelativeCurrentBinaryBeforeLookups(t *testing.T) {
	t.Parallel()

	resolutionErr := errors.New("executable unavailable")
	tests := []struct {
		name       string
		executable func() (string, error)
		wantCause  error
	}{
		{
			name: "resolution error",
			executable: func() (string, error) {
				return "", resolutionErr
			},
			wantCause: resolutionErr,
		},
		{
			name: "relative result",
			executable: func() (string, error) {
				return "bin/agentctl", nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookups := 0
			_, err := CheckShimExecutables(
				config.FleetConfig{Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}}},
				true,
				func(string) (string, error) {
					lookups++
					return "/unused", nil
				},
				tt.executable,
			)
			var executableErr *ShimExecutableError
			if !errors.As(err, &executableErr) {
				t.Fatalf("CheckShimExecutables() error = %T %v, want *ShimExecutableError", err, err)
			}
			if tt.wantCause != nil && !errors.Is(err, tt.wantCause) {
				t.Fatalf("CheckShimExecutables() error = %v, want wrapped %v", err, tt.wantCause)
			}
			if lookups != 0 {
				t.Fatalf("external lookups = %d, want none before current executable resolves", lookups)
			}
		})
	}
}
