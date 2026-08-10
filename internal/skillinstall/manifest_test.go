package skillinstall

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/skills"
)

func TestBuildManifestHashesEveryEmbeddedFile(t *testing.T) {
	manifest, err := BuildManifest(skills.Tree, skills.Root, "0.3.0")
	if err != nil {
		t.Fatalf("BuildManifest(): %v", err)
	}
	if manifest.Version != "0.3.0" {
		t.Fatalf("Version = %q, want 0.3.0", manifest.Version)
	}
	if got, want := len(manifest.Files), 4; got != want {
		t.Fatalf("len(Files) = %d, want %d; files = %#v", got, want, manifest.Files)
	}
	content, err := skills.Tree.ReadFile("agentctl/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	wantHash := fmt.Sprintf("%x", sha256.Sum256(content))
	if got := manifest.Files["SKILL.md"]; got != wantHash {
		t.Fatalf("SKILL.md hash = %q, want %q", got, wantHash)
	}
	for _, path := range []string{
		"SKILL.md",
		"references/exit-codes.md",
		"references/fleet-template.schema.json",
		"references/status-states.md",
	} {
		if _, ok := manifest.Files[path]; !ok {
			t.Errorf("Files missing %q: %#v", path, manifest.Files)
		}
	}
}

func TestReadManifestReportsAbsence(t *testing.T) {
	manifest, ok, err := ReadManifest(t.TempDir())
	if err != nil {
		t.Fatalf("ReadManifest(): %v", err)
	}
	if ok {
		t.Fatalf("ReadManifest() ok = true, want false; manifest = %#v", manifest)
	}
}

func TestWriteManifestRoundTripsAtFilePermission(t *testing.T) {
	dir := t.TempDir()
	want := Manifest{
		Version: "0.3.0",
		Files: map[string]string{
			"SKILL.md":                    "abc123",
			"references/status-states.md": "def456",
			"references/exit-codes.md":    "789abc",
		},
	}

	if err := WriteManifest(dir, want); err != nil {
		t.Fatalf("WriteManifest(): %v", err)
	}
	got, ok, err := ReadManifest(dir)
	if err != nil {
		t.Fatalf("ReadManifest(): %v", err)
	}
	if !ok {
		t.Fatal("ReadManifest() ok = false, want true")
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadManifest() = %#v, want %#v", got, want)
	}
	info, err := os.Stat(filepath.Join(dir, ManifestName))
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o644 {
		t.Fatalf("manifest mode = %o, want 644", gotMode)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read manifest directory: %v", err)
	}
	if got, want := len(entries), 1; got != want {
		t.Fatalf("manifest directory entries = %d, want %d (no temporary file left behind)", got, want)
	}
}
