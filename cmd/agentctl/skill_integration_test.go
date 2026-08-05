//go:build integration

package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/skillinstall"
	"github.com/mnbf9rca/agentctl/skills"
)

func TestIntegrationSkillInstallAndStatusMatchEmbeddedTree(t *testing.T) {
	restoreBuildStamp(t, "v0.3.0")
	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer

	if code := run([]string{"skill", "install"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("skill install exit = %d, want %d; stdout = %q, stderr = %q", code, exitOK, stdout.String(), stderr.String())
	}
	wantFiles := embeddedSkillHashes(t)
	for _, target := range skillinstall.Targets(home) {
		assertInstalledTreeMatchesEmbedded(t, target.Dir)
		manifest, ok, err := skillinstall.ReadManifest(target.Dir)
		if err != nil || !ok {
			t.Fatalf("ReadManifest(%q) = %#v, %v, %v; want manifest", target.Dir, manifest, ok, err)
		}
		if manifest.Version != "0.3.0" || !reflect.DeepEqual(manifest.Files, wantFiles) {
			t.Errorf("manifest at %q = %#v, want version 0.3.0 and files %#v", target.Dir, manifest, wantFiles)
		}
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"skill", "status"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("skill status exit = %d, want %d; stdout = %q, stderr = %q", code, exitOK, stdout.String(), stderr.String())
	}
	wantStatus := skillinstall.Targets(home)[0].Dir + ": current (installed 0.3.0, binary 0.3.0)\n" +
		skillinstall.Targets(home)[1].Dir + ": current (installed 0.3.0, binary 0.3.0)\n"
	if got := stdout.String(); got != wantStatus {
		t.Fatalf("skill status stdout = %q, want %q", got, wantStatus)
	}
	if stderr.Len() != 0 {
		t.Fatalf("skill status stderr = %q, want empty", stderr.String())
	}
}

func embeddedSkillHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	err := fs.WalkDir(skills.Tree, skills.Root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, ok := cutSkillRoot(name)
		if !ok {
			return fmt.Errorf("embedded path %q is outside root %q", name, skills.Root)
		}
		content, err := fs.ReadFile(skills.Tree, name)
		if err != nil {
			return err
		}
		hashes[relative] = fmt.Sprintf("%x", sha256.Sum256(content))
		return nil
	})
	if err != nil {
		t.Fatalf("hash embedded skill: %v", err)
	}
	return hashes
}

func assertInstalledTreeMatchesEmbedded(t *testing.T, targetDir string) {
	t.Helper()
	err := fs.WalkDir(skills.Tree, skills.Root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, ok := cutSkillRoot(name)
		if !ok {
			t.Fatalf("embedded path %q is outside root %q", name, skills.Root)
		}
		want, err := fs.ReadFile(skills.Tree, name)
		if err != nil {
			return err
		}
		got, err := os.ReadFile(filepath.Join(targetDir, filepath.FromSlash(relative)))
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			t.Errorf("installed %q differs from embedded %q", filepath.Join(targetDir, relative), name)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("compare installed tree %q: %v", targetDir, err)
	}
}

func cutSkillRoot(name string) (string, bool) {
	prefix := skills.Root + "/"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return "", false
	}
	relative := name[len(prefix):]
	return path.Clean(relative), true
}
