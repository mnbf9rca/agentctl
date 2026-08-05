// Package skillinstall installs and inspects agentctl's embedded agent skill.
package skillinstall

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const ManifestName = ".agentctl-skill.json"

type Manifest struct {
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

func BuildManifest(tree fs.FS, root, version string) (Manifest, error) {
	manifest := Manifest{Version: version, Files: make(map[string]string)}
	err := fs.WalkDir(tree, root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(tree, name)
		if err != nil {
			return fmt.Errorf("read embedded skill file %q: %w", name, err)
		}
		relative, ok := strings.CutPrefix(name, root+"/")
		if !ok || relative == "" {
			return fmt.Errorf("embedded skill file %q is outside root %q", name, root)
		}
		manifest.Files[relative] = fmt.Sprintf("%x", sha256.Sum256(content))
		return nil
	})
	if err != nil {
		return Manifest{}, fmt.Errorf("build skill manifest: %w", err)
	}
	return manifest, nil
}

func ReadManifest(dir string) (Manifest, bool, error) {
	content, err := os.ReadFile(filepath.Join(dir, ManifestName))
	if errors.Is(err, fs.ErrNotExist) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(content, &manifest); err != nil {
		return Manifest{}, false, fmt.Errorf("parse manifest: %w", err)
	}
	for relative := range manifest.Files {
		if !fs.ValidPath(relative) || relative == "." || relative == ManifestName || strings.Contains(relative, `\`) {
			return Manifest{}, false, fmt.Errorf("parse manifest: invalid file path %q", relative)
		}
	}
	return manifest, true, nil
}

func WriteManifest(dir string, manifest Manifest) error {
	content, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	content = append(content, '\n')

	temporary, err := os.CreateTemp(dir, ".agentctl-skill.json.tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()

	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set temporary manifest permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary manifest: %w", err)
	}
	if err := os.Rename(temporaryName, filepath.Join(dir, ManifestName)); err != nil {
		return fmt.Errorf("replace manifest: %w", err)
	}
	return nil
}
