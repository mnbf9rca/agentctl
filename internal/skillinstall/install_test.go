package skillinstall

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

func testTree(files map[string]string) fstest.MapFS {
	tree := fstest.MapFS{}
	for name, content := range files {
		tree["agentctl/"+name] = &fstest.MapFile{Data: []byte(content), Mode: 0o444}
	}
	return tree
}

func TestTargetsAreFixedInHarnessOrder(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "operator")
	want := []Target{
		{Harness: "claude", Dir: filepath.Join(home, ".claude", "skills", "agentctl")},
		{Harness: "codex", Dir: filepath.Join(home, ".agents", "skills", "agentctl")},
	}
	if got := Targets(home); !reflect.DeepEqual(got, want) {
		t.Fatalf("Targets(%q) = %#v, want %#v", home, got, want)
	}
}

func TestInstallFreshWritesBothTargetsWithExactModes(t *testing.T) {
	home := t.TempDir()
	tree := testTree(map[string]string{
		"SKILL.md":                    "skill-v1\n",
		"references/status-states.md": "states-v1\n",
	})
	targets := Targets(home)

	outcomes, err := Install(tree, "agentctl", "0.3.0", targets, false)
	if err != nil {
		t.Fatalf("Install(): %v", err)
	}
	if got, want := len(outcomes), 2; got != want {
		t.Fatalf("len(outcomes) = %d, want %d", got, want)
	}
	for i, outcome := range outcomes {
		target := targets[i]
		if outcome.Target != target || outcome.Action != "installed" || outcome.Detail != "" || len(outcome.Removed) != 0 {
			t.Errorf("outcome[%d] = %#v, want installed outcome for %#v", i, outcome, target)
		}
		wantWritten := []string{
			filepath.Join(target.Dir, "SKILL.md"),
			filepath.Join(target.Dir, "references", "status-states.md"),
			filepath.Join(target.Dir, ManifestName),
		}
		if !reflect.DeepEqual(outcome.Written, wantWritten) {
			t.Errorf("outcome[%d].Written = %#v, want %#v", i, outcome.Written, wantWritten)
		}
		for path, wantContent := range map[string]string{
			filepath.Join(target.Dir, "SKILL.md"):                       "skill-v1\n",
			filepath.Join(target.Dir, "references", "status-states.md"): "states-v1\n",
		} {
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Errorf("read %q: %v", path, readErr)
				continue
			}
			if string(content) != wantContent {
				t.Errorf("%s content = %q, want %q", path, content, wantContent)
			}
			assertPermission(t, path, 0o644)
		}
		assertPermission(t, target.Dir, 0o755)
		assertPermission(t, filepath.Join(target.Dir, "references"), 0o755)
		assertPermission(t, filepath.Join(target.Dir, ManifestName), 0o644)
		manifest, ok, readErr := ReadManifest(target.Dir)
		if readErr != nil || !ok || manifest.Version != "0.3.0" || len(manifest.Files) != 2 {
			t.Errorf("ReadManifest(%q) = %#v, %v, %v; want version 0.3.0 with two files", target.Dir, manifest, ok, readErr)
		}
	}
}

func TestInstallCurrentDoesNotRewriteFiles(t *testing.T) {
	tree := testTree(map[string]string{"SKILL.md": "skill-v1\n"})
	target := Target{Harness: "claude", Dir: filepath.Join(t.TempDir(), "agentctl")}
	if _, err := Install(tree, "agentctl", "0.3.0", []Target{target}, false); err != nil {
		t.Fatalf("first Install(): %v", err)
	}
	paths := []string{filepath.Join(target.Dir, "SKILL.md"), filepath.Join(target.Dir, ManifestName)}
	fixedTime := time.Unix(1_700_000_000, 0)
	for _, path := range paths {
		if err := os.Chtimes(path, fixedTime, fixedTime); err != nil {
			t.Fatalf("Chtimes(%q): %v", path, err)
		}
	}

	outcomes, err := Install(tree, "agentctl", "0.3.0", []Target{target}, false)
	if err != nil {
		t.Fatalf("second Install(): %v", err)
	}
	if got := outcomes[0]; got.Action != "current" || len(got.Written) != 0 || len(got.Removed) != 0 {
		t.Fatalf("current outcome = %#v, want current with zero writes/removals", got)
	}
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("Stat(%q): %v", path, statErr)
		}
		if !info.ModTime().Equal(fixedTime) {
			t.Errorf("%s mtime = %v, want unchanged %v", path, info.ModTime(), fixedTime)
		}
	}
}

func TestInstallVersionChangeOverwritesOwnedTree(t *testing.T) {
	tree := testTree(map[string]string{"SKILL.md": "same bytes\n"})
	target := Target{Harness: "claude", Dir: filepath.Join(t.TempDir(), "agentctl")}
	if _, err := Install(tree, "agentctl", "0.2.0", []Target{target}, false); err != nil {
		t.Fatalf("Install(old): %v", err)
	}

	outcomes, err := Install(tree, "agentctl", "0.3.0", []Target{target}, false)
	if err != nil {
		t.Fatalf("Install(new): %v", err)
	}
	if outcomes[0].Action != "installed" || len(outcomes[0].Written) != 2 {
		t.Fatalf("version-change outcome = %#v, want installed with file and manifest writes", outcomes[0])
	}
	manifest, ok, err := ReadManifest(target.Dir)
	if err != nil || !ok || manifest.Version != "0.3.0" {
		t.Fatalf("ReadManifest() = %#v, %v, %v; want version 0.3.0", manifest, ok, err)
	}
}

func TestInstallRemovesOnlyDroppedManifestOwnedFiles(t *testing.T) {
	oldTree := testTree(map[string]string{"SKILL.md": "skill\n", "references/dropped.md": "owned\n"})
	newTree := testTree(map[string]string{"SKILL.md": "skill\n"})
	target := Target{Harness: "claude", Dir: filepath.Join(t.TempDir(), "agentctl")}
	if _, err := Install(oldTree, "agentctl", "0.2.0", []Target{target}, false); err != nil {
		t.Fatalf("Install(old): %v", err)
	}
	dropped := filepath.Join(target.Dir, "references", "dropped.md")

	outcomes, err := Install(newTree, "agentctl", "0.3.0", []Target{target}, false)
	if err != nil {
		t.Fatalf("Install(new): %v", err)
	}
	if got, want := outcomes[0].Removed, []string{dropped}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Removed = %#v, want %#v", got, want)
	}
	if _, err := os.Stat(dropped); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Stat(dropped) error = %v, want not exist", err)
	}
}

func TestInstallRefusesModifiedDroppedFileBeforeAnyWrite(t *testing.T) {
	oldTree := testTree(map[string]string{"SKILL.md": "skill-v1\n", "dropped.md": "owned\n"})
	newTree := testTree(map[string]string{"SKILL.md": "skill-v2\n"})
	target := Target{Harness: "claude", Dir: filepath.Join(t.TempDir(), "agentctl")}
	if _, err := Install(oldTree, "agentctl", "0.2.0", []Target{target}, false); err != nil {
		t.Fatalf("Install(old): %v", err)
	}
	dropped := filepath.Join(target.Dir, "dropped.md")
	if err := os.WriteFile(dropped, []byte("user edit\n"), 0o644); err != nil {
		t.Fatalf("modify dropped file: %v", err)
	}

	outcomes, err := Install(newTree, "agentctl", "0.3.0", []Target{target}, false)
	if !errors.Is(err, ErrUnowned) {
		t.Fatalf("Install() error = %v, want ErrUnowned", err)
	}
	if got := outcomes[0]; got.Action != "refused" || got.Written != nil || got.Removed != nil || !strings.Contains(got.Detail, dropped) {
		t.Fatalf("refusal outcome = %#v, want offending dropped path and no mutations", got)
	}
	content, readErr := os.ReadFile(filepath.Join(target.Dir, "SKILL.md"))
	if readErr != nil || string(content) != "skill-v1\n" {
		t.Fatalf("SKILL.md after refusal = %q, %v; want old content", content, readErr)
	}
}

func TestInstallRefusesUnmanagedAndModifiedTargetsUnlessForced(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*testing.T, Target, fstest.MapFS)
	}{
		{
			name: "unmanaged stray file",
			setup: func(t *testing.T, target Target, _ fstest.MapFS) {
				t.Helper()
				if err := os.MkdirAll(target.Dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target.Dir, "stray.txt"), []byte("mine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "modified managed file",
			setup: func(t *testing.T, target Target, tree fstest.MapFS) {
				t.Helper()
				if _, err := Install(tree, "agentctl", "0.2.0", []Target{target}, false); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(target.Dir, "SKILL.md"), []byte("mine\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tree := testTree(map[string]string{"SKILL.md": "shipped\n"})
			target := Target{Harness: "claude", Dir: filepath.Join(t.TempDir(), "agentctl")}
			tt.setup(t, target, tree)

			outcomes, err := Install(tree, "agentctl", "0.3.0", []Target{target}, false)
			if !errors.Is(err, ErrUnowned) {
				t.Fatalf("Install() error = %v, want ErrUnowned", err)
			}
			if got := outcomes[0]; got.Action != "refused" || len(got.Written) != 0 || got.Detail == "" {
				t.Fatalf("refusal outcome = %#v, want refusal detail and no writes", got)
			}

			outcomes, err = Install(tree, "agentctl", "0.3.0", []Target{target}, true)
			if err != nil {
				t.Fatalf("Install(force): %v", err)
			}
			if outcomes[0].Action != "installed" {
				t.Fatalf("force outcome = %#v, want installed", outcomes[0])
			}
			content, readErr := os.ReadFile(filepath.Join(target.Dir, "SKILL.md"))
			if readErr != nil || string(content) != "shipped\n" {
				t.Fatalf("forced SKILL.md = %q, %v; want shipped content", content, readErr)
			}
			entries, readDirErr := os.ReadDir(target.Dir)
			if readDirErr != nil {
				t.Fatal(readDirErr)
			}
			if got, want := len(entries), 2; got != want {
				t.Fatalf("forced target entries = %d, want %d (SKILL.md and manifest only)", got, want)
			}
		})
	}
}

func TestInstallRefusesSymlinkedTargetDirectoryWithoutFollowingIt(t *testing.T) {
	base := t.TempDir()
	external := filepath.Join(base, "external")
	if err := os.Mkdir(external, 0o755); err != nil {
		t.Fatal(err)
	}
	target := Target{Harness: "claude", Dir: filepath.Join(base, "agentctl")}
	if err := os.Symlink(external, target.Dir); err != nil {
		t.Fatal(err)
	}

	outcomes, err := Install(testTree(map[string]string{"SKILL.md": "shipped\n"}), "agentctl", "0.3.0", []Target{target}, false)
	if !errors.Is(err, ErrUnowned) {
		t.Fatalf("Install() error = %v, want ErrUnowned", err)
	}
	if got := outcomes[0]; got.Action != "refused" || !strings.Contains(got.Detail, target.Dir) || len(got.Written) != 0 {
		t.Fatalf("symlink refusal outcome = %#v, want target path and no writes", got)
	}
	entries, readErr := os.ReadDir(external)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("external directory entries = %#v, %v; want untouched", entries, readErr)
	}
}

func TestInstallReportsSuccessfulFirstTargetWhenSecondTargetFails(t *testing.T) {
	home := t.TempDir()
	targets := Targets(home)
	blockedParent := filepath.Dir(targets[1].Dir)
	if err := os.MkdirAll(blockedParent, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blockedParent, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blockedParent, 0o755) })

	outcomes, err := Install(testTree(map[string]string{"SKILL.md": "shipped\n"}), "agentctl", "0.3.0", targets, false)
	if err == nil || errors.Is(err, ErrUnowned) {
		t.Fatalf("Install() error = %v, want ordinary write failure", err)
	}
	if got, want := len(outcomes), 2; got != want {
		t.Fatalf("len(outcomes) = %d, want %d", got, want)
	}
	if got := outcomes[0]; got.Action != "installed" || len(got.Written) != 2 {
		t.Fatalf("first outcome = %#v, want complete installed report", got)
	}
	if got := outcomes[1]; got.Action != "failed" || got.Detail == "" {
		t.Fatalf("second outcome = %#v, want factual failure", got)
	}
}

func assertPermission(t *testing.T, path string, want fs.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q): %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %o, want %o", path, got, want)
	}
}
