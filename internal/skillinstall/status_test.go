package skillinstall

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStatusUsesFirstMatchingStatePrecedence(t *testing.T) {
	tree := testTree(map[string]string{"SKILL.md": "shipped\n"})
	for _, tt := range []struct {
		name             string
		setup            func(*testing.T, Target)
		wantState        State
		wantInstalledVer string
	}{
		{
			name:      "absent",
			wantState: StateAbsent,
		},
		{
			name: "unmanaged",
			setup: func(t *testing.T, target Target) {
				t.Helper()
				if err := os.MkdirAll(target.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target.Dir, "SKILL.md"), []byte("shipped\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantState: StateUnmanaged,
		},
		{
			name: "stale wins before missing content",
			setup: func(t *testing.T, target Target) {
				t.Helper()
				if err := os.MkdirAll(target.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := WriteManifest(target.Dir, Manifest{Version: "0.2.0", Files: map[string]string{"missing.md": "not-a-hash"}}); err != nil {
					t.Fatal(err)
				}
			},
			wantState:        StateStale,
			wantInstalledVer: "0.2.0",
		},
		{
			name: "modified",
			setup: func(t *testing.T, target Target) {
				t.Helper()
				if _, err := Install(tree, "agentctl", "0.3.0", []Target{target}, false); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target.Dir, "SKILL.md"), []byte("edited\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantState:        StateModified,
			wantInstalledVer: "0.3.0",
		},
		{
			name: "current",
			setup: func(t *testing.T, target Target) {
				t.Helper()
				if _, err := Install(tree, "agentctl", "0.3.0", []Target{target}, false); err != nil {
					t.Fatal(err)
				}
			},
			wantState:        StateCurrent,
			wantInstalledVer: "0.3.0",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			target := Target{Harness: "claude", Dir: filepath.Join(t.TempDir(), "agentctl")}
			if tt.setup != nil {
				tt.setup(t, target)
			}

			reports, err := Status(tree, "agentctl", "0.3.0", []Target{target})
			if err != nil {
				t.Fatalf("Status(): %v", err)
			}
			if got, want := len(reports), 1; got != want {
				t.Fatalf("len(reports) = %d, want %d", got, want)
			}
			got := reports[0]
			if got.Target != target || got.State != tt.wantState || got.InstalledVersion != tt.wantInstalledVer {
				t.Fatalf("Report = %#v, want target %#v, state %q, installed version %q", got, target, tt.wantState, tt.wantInstalledVer)
			}
		})
	}
}

func TestStatusCallsEveryTargetInOrder(t *testing.T) {
	home := t.TempDir()
	targets := Targets(home)
	reports, err := Status(testTree(map[string]string{"SKILL.md": "shipped\n"}), "agentctl", "0.3.0", targets)
	if err != nil {
		t.Fatalf("Status(): %v", err)
	}
	if len(reports) != len(targets) {
		t.Fatalf("len(reports) = %d, want %d", len(reports), len(targets))
	}
	for i := range targets {
		if reports[i].Target != targets[i] || reports[i].State != StateAbsent {
			t.Errorf("reports[%d] = %#v, want absent target %#v", i, reports[i], targets[i])
		}
	}
}
