package hack_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a temp git repo containing the script, a VERSION file, and tags.
func initRepo(t *testing.T, version string, tags []string) string {
	t.Helper()
	return initRepoRaw(t, &version, tags)
}

// initRepoNoVersionFile creates a temp git repo containing the script and
// tags, but no VERSION file at all — distinct from a malformed VERSION file.
func initRepoNoVersionFile(t *testing.T, tags []string) string {
	t.Helper()
	return initRepoRaw(t, nil, tags)
}

// initRepoRaw is the shared setup for initRepo and initRepoNoVersionFile.
// version == nil means: do not write a VERSION file at all.
func initRepoRaw(t *testing.T, version *string, tags []string) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("git", "init", "-q")
	run("git", "config", "commit.gpgsign", "false")
	run("git", "config", "tag.gpgsign", "false")
	script, err := os.ReadFile("next-version.sh")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "hack"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hack", "next-version.sh"), script, 0o755); err != nil {
		t.Fatal(err)
	}
	if version != nil {
		if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte(*version+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run("git", "add", "-A")
	run("git", "commit", "-q", "-m", "init")
	for _, tag := range tags {
		run("git", "tag", tag)
	}
	return dir
}

func nextVersion(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./hack/next-version.sh")
	cmd.Dir = dir
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// nextVersionStderr runs the script and returns its stderr, for tests that
// assert on the exact failure message rather than just err != nil.
func nextVersionStderr(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./hack/next-version.sh")
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stderr.String(), err
}

func TestNextVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		tags    []string
		want    string
	}{
		{"no tags uses VERSION", "0.1.0", nil, "0.1.0"},
		{"VERSION ahead wins", "0.2.0", []string{"v0.1.0", "v0.1.1"}, "0.2.0"},
		{"VERSION equal bumps patch", "0.1.1", []string{"v0.1.0", "v0.1.1"}, "0.1.2"},
		{"VERSION behind bumps patch", "0.1.0", []string{"v0.1.0", "v0.1.4"}, "0.1.5"},
		{"sorts semver not lexically", "0.1.0", []string{"v0.1.9", "v0.1.10"}, "0.1.11"},
		{"ignores non-version tags", "0.1.0", []string{"v0.1.0", "vendor-snapshot"}, "0.1.1"},
		{"major jump sorts numerically", "1.0.0", []string{"v1.9.0", "v2.0.0"}, "2.0.1"},
		{"leading-zero patch component forces base 10", "1.2.0", []string{"v1.2.08"}, "1.2.9"},
		{
			"tag glob admits rc-suffixed tags but they are filtered out",
			"0.1.0",
			[]string{"v0.1.0", "v0.2.0-rc1"},
			"0.1.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextVersion(t, initRepo(t, tc.version, tc.tags))
			if err != nil {
				t.Fatalf("script failed: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNextVersionRejectsMalformed(t *testing.T) {
	stderr, err := nextVersionStderr(t, initRepo(t, "not-a-version", nil))
	if err == nil {
		t.Fatal("expected failure on malformed VERSION")
	}
	want := "next-version: VERSION file is not X.Y.Z: 'not-a-version'\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}

func TestNextVersionRejectsMissingVersionFile(t *testing.T) {
	dir := initRepoNoVersionFile(t, nil)
	stderr, err := nextVersionStderr(t, dir)
	if err == nil {
		t.Fatal("expected failure with no VERSION file")
	}
	resolvedDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "next-version: no VERSION file at " + filepath.Join(resolvedDir, "VERSION") + "\n"
	if stderr != want {
		t.Fatalf("stderr = %q, want %q", stderr, want)
	}
}
