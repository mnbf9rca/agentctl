package preflight

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
)

func TestCheckExecutables(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		roles       []config.RoleConfig
		missing     string
		wantCalls   []string
		wantMissing string
	}{
		{
			name:        "missing tmux",
			roles:       []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}},
			missing:     "tmux",
			wantCalls:   []string{"tmux"},
			wantMissing: "tmux",
		},
		{
			name:        "missing amq",
			roles:       []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}},
			missing:     "amq",
			wantCalls:   []string{"tmux", "amq"},
			wantMissing: "amq",
		},
		{
			name:        "missing requested claude",
			roles:       []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}},
			missing:     "claude",
			wantCalls:   []string{"tmux", "amq", "claude"},
			wantMissing: "claude",
		},
		{
			name:        "missing requested codex",
			roles:       []config.RoleConfig{{Name: "worker", Harness: config.HarnessCodex}},
			missing:     "codex",
			wantCalls:   []string{"tmux", "amq", "codex"},
			wantMissing: "codex",
		},
		{
			name:      "unrequested codex is ignored",
			roles:     []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}},
			missing:   "codex",
			wantCalls: []string{"tmux", "amq", "claude"},
		},
		{
			name:      "unrequested claude is ignored",
			roles:     []config.RoleConfig{{Name: "worker", Harness: config.HarnessCodex}},
			missing:   "claude",
			wantCalls: []string{"tmux", "amq", "codex"},
		},
		{
			name: "requested harnesses preserve first occurrence and deduplicate",
			roles: []config.RoleConfig{
				{Name: "codex1", Harness: config.HarnessCodex},
				{Name: "planner", Harness: config.HarnessClaude},
				{Name: "codex2", Harness: config.HarnessCodex},
				{Name: "reviewer", Harness: config.HarnessClaude},
			},
			wantCalls: []string{"tmux", "amq", "codex", "claude"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls []string
			lookPath := func(name string) (string, error) {
				calls = append(calls, name)
				if name == tt.missing {
					return "", errors.New("not found")
				}
				return "/fake/bin/" + name, nil
			}

			err := CheckExecutables(config.FleetConfig{Roles: tt.roles}, lookPath)
			if tt.wantMissing == "" {
				if err != nil {
					t.Fatalf("CheckExecutables() error = %v", err)
				}
			} else {
				var missing *MissingExecutableError
				if !errors.As(err, &missing) {
					t.Fatalf("CheckExecutables() error = %T %v, want *MissingExecutableError", err, err)
				}
				if missing.Name != tt.wantMissing {
					t.Fatalf("MissingExecutableError.Name = %q, want %q", missing.Name, tt.wantMissing)
				}
			}

			if !reflect.DeepEqual(calls, tt.wantCalls) {
				t.Fatalf("LookPath calls = %#v, want %#v", calls, tt.wantCalls)
			}
		})
	}
}
