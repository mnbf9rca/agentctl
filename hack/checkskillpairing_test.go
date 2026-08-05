package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

type pairingRepo struct {
	dir string
}

func newPairingRepo(t *testing.T, initialMessage string) (*pairingRepo, string) {
	t.Helper()

	dir := t.TempDir()
	r := &pairingRepo{dir: dir}
	r.git(t, "init", "-q", "-b", "main")
	r.git(t, "config", "user.name", "Test")
	r.git(t, "config", "user.email", "test@example.com")
	r.git(t, "config", "commit.gpgsign", "false")

	script, err := os.ReadFile("check-skill-pairing.sh")
	if err != nil {
		t.Fatal(err)
	}
	r.write(t, "hack/check-skill-pairing.sh", script, 0o755)
	r.write(t, "README.md", []byte("fixture\n"), 0o644)
	return r, r.commit(t, initialMessage)
}

func (r *pairingRepo) git(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (r *pairingRepo) write(t *testing.T, name string, body []byte, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(r.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		t.Fatal(err)
	}
}

func (r *pairingRepo) commit(t *testing.T, message string) string {
	t.Helper()
	r.git(t, "add", "-A")
	r.git(t, "commit", "-qm", message)
	return r.rev(t, "HEAD")
}

func (r *pairingRepo) rev(t *testing.T, ref string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = r.dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse %s: %v", ref, err)
	}
	return string(out[:len(out)-1])
}

func (r *pairingRepo) check(t *testing.T, base, head string) error {
	t.Helper()
	cmd := exec.Command("./hack/check-skill-pairing.sh", base, head)
	cmd.Dir = r.dir
	return cmd.Run()
}

func TestCheckSkillPairing(t *testing.T) {
	tests := []struct {
		name     string
		wantFail bool
		setup    func(t *testing.T, r *pairingRepo, initial string) (base, head string)
	}{
		{
			name:     "command surface change without skill fails",
			wantFail: true,
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "cmd/agentctl/main.go", []byte("package main\n"), 0o644)
				return initial, r.commit(t, "change command surface")
			},
		},
		{
			name: "command surface and skill change pass",
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "cmd/agentctl/main.go", []byte("package main\n"), 0o644)
				r.write(t, "skills/agentctl/SKILL.md", []byte("updated\n"), 0o644)
				return initial, r.commit(t, "change command surface and skill")
			},
		},
		{
			name: "override in PR commit passes",
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "cmd/agentctl/main.go", []byte("package main\n"), 0o644)
				return initial, r.commit(t, "surface-neutral refactor [skill-unaffected]")
			},
		},
		{
			name:     "override outside PR range fails",
			wantFail: true,
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "cmd/agentctl/main.go", []byte("package main\n"), 0o644)
				return initial, r.commit(t, "change command surface")
			},
		},
		{
			name: "unrelated change passes",
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "docs/guide.md", []byte("updated\n"), 0o644)
				return initial, r.commit(t, "update docs")
			},
		},
		{
			name:     "config surface change without skill fails",
			wantFail: true,
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "internal/config/config.go", []byte("package config\n"), 0o644)
				return initial, r.commit(t, "change config surface")
			},
		},
		{
			name: "base advance is excluded from PR diff",
			setup: func(t *testing.T, r *pairingRepo, initial string) (string, string) {
				r.git(t, "switch", "-qc", "feature")
				r.write(t, "docs/guide.md", []byte("feature docs\n"), 0o644)
				head := r.commit(t, "update feature docs")
				r.git(t, "switch", "-q", "main")
				r.write(t, "cmd/agentctl/main.go", []byte("package main\n"), 0o644)
				base := r.commit(t, "advance base command surface")
				return base, head
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			initialMessage := "initial fixture"
			if tc.name == "override outside PR range fails" {
				initialMessage += " [skill-unaffected]"
			}
			r, initial := newPairingRepo(t, initialMessage)
			base, head := tc.setup(t, r, initial)
			err := r.check(t, base, head)
			if tc.wantFail && err == nil {
				t.Fatal("expected pairing check to fail")
			}
			if !tc.wantFail && err != nil {
				t.Fatalf("pairing check failed: %v", err)
			}
		})
	}
}
