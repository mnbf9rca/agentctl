package skillinstall

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

type Target struct {
	Harness string
	Dir     string
}

func Targets(home string) []Target {
	return []Target{
		{Harness: "claude", Dir: filepath.Join(home, ".claude", "skills", "agentctl")},
		{Harness: "codex", Dir: filepath.Join(home, ".agents", "skills", "agentctl")},
	}
}

type Outcome struct {
	Target  Target
	Action  string
	Detail  string
	Written []string
	Removed []string
}

var ErrUnowned = errors.New("existing files not written by agentctl")

func Install(tree fs.FS, root, version string, targets []Target, force bool) ([]Outcome, error) {
	manifest, err := BuildManifest(tree, root, version)
	if err != nil {
		return nil, err
	}

	outcomes := make([]Outcome, 0, len(targets))
	var failures []error
	for _, target := range targets {
		outcome, targetErr := installTarget(tree, root, manifest, target, force)
		outcomes = append(outcomes, outcome)
		if targetErr != nil {
			failures = append(failures, targetErr)
		}
	}
	return outcomes, errors.Join(failures...)
}

func installTarget(tree fs.FS, root string, next Manifest, target Target, force bool) (Outcome, error) {
	outcome := Outcome{Target: target}
	existing, replace, err := inspectTarget(target.Dir, next)
	if err != nil {
		if !force || !errors.Is(err, ErrUnowned) {
			outcome.Action = actionForError(err)
			outcome.Detail = err.Error()
			return outcome, err
		}
		replace = true
	}

	if !replace && existing != nil && manifestIsCurrent(target.Dir, *existing, next) {
		outcome.Action = "current"
		return outcome, nil
	}

	writePaths := manifestWritePaths(target.Dir, next)
	if replace {
		removed, err := replacementFiles(target.Dir)
		if err != nil {
			failure := fmt.Errorf("enumerate unowned target %q before replacement: %w", target.Dir, err)
			outcome.Action = "failed"
			outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
			return outcome, failure
		}
		if err := os.RemoveAll(target.Dir); err != nil {
			failure := fmt.Errorf("remove unowned target %q: %w", target.Dir, err)
			outcome.Action = "failed"
			outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
			return outcome, failure
		}
		outcome.Removed = append(outcome.Removed, removed...)
		existing = nil
	}

	if err := ensureDirectory(target.Dir); err != nil {
		failure := fmt.Errorf("create target directory %q: %w", target.Dir, err)
		outcome.Action = "failed"
		outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
		return outcome, failure
	}

	if existing != nil {
		dropped := droppedFiles(*existing, next)
		for _, relative := range dropped {
			filename := filepath.Join(target.Dir, filepath.FromSlash(relative))
			if err := os.Remove(filename); err != nil && !errors.Is(err, fs.ErrNotExist) {
				failure := fmt.Errorf("remove dropped skill file %q: %w", filename, err)
				outcome.Action = "failed"
				outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
				return outcome, failure
			}
			if err == nil {
				outcome.Removed = append(outcome.Removed, filename)
			}
		}
	}

	for _, relative := range sortedManifestPaths(next) {
		content, err := fs.ReadFile(tree, path.Join(root, relative))
		if err != nil {
			failure := fmt.Errorf("read embedded skill file %q: %w", relative, err)
			outcome.Action = "failed"
			outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
			return outcome, failure
		}
		filename := filepath.Join(target.Dir, filepath.FromSlash(relative))
		if err := ensureDirectory(filepath.Dir(filename)); err != nil {
			failure := fmt.Errorf("create skill directory for %q: %w", filename, err)
			outcome.Action = "failed"
			outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
			return outcome, failure
		}
		if err := os.WriteFile(filename, content, 0o644); err != nil {
			failure := fmt.Errorf("write skill file %q: %w", filename, err)
			outcome.Action = "failed"
			outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
			return outcome, failure
		}
		if err := os.Chmod(filename, 0o644); err != nil {
			failure := fmt.Errorf("set skill file permissions %q: %w", filename, err)
			outcome.Action = "failed"
			outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
			return outcome, failure
		}
		outcome.Written = append(outcome.Written, filename)
	}

	if err := WriteManifest(target.Dir, next); err != nil {
		failure := fmt.Errorf("write skill manifest %q: %w", filepath.Join(target.Dir, ManifestName), err)
		outcome.Action = "failed"
		outcome.Detail = describeWriteFailure(failure, writePaths, outcome.Written)
		return outcome, failure
	}
	outcome.Written = append(outcome.Written, filepath.Join(target.Dir, ManifestName))
	outcome.Action = "installed"
	return outcome, nil
}

func replacementFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			files = append(files, filename)
		}
		return nil
	})
	return files, err
}

func inspectTarget(dir string, next Manifest) (*Manifest, bool, error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect target %q: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, fmt.Errorf("%s: symlinked target directory: %w", dir, ErrUnowned)
	}
	if !info.IsDir() {
		return nil, false, fmt.Errorf("%s: target is not a directory: %w", dir, ErrUnowned)
	}

	existing, ok, err := ReadManifest(dir)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %v: %w", filepath.Join(dir, ManifestName), err, ErrUnowned)
	}
	if !ok {
		offending, err := firstTargetFile(dir)
		if err != nil {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%s: target directory has no manifest: %w", offending, ErrUnowned)
	}
	if err := validateOwnedFiles(dir, existing, next); err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

func firstTargetFile(dir string) (string, error) {
	offending := dir
	err := filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect unmanaged target path %q: %w", filename, walkErr)
		}
		if filename != dir && !entry.IsDir() {
			offending = filename
			return fs.SkipAll
		}
		return nil
	})
	return offending, err
}

func validateOwnedFiles(dir string, existing, next Manifest) error {
	return filepath.WalkDir(dir, func(filename string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("inspect target path %q: %w", filename, walkErr)
		}
		if entry.IsDir() || filename == filepath.Join(dir, ManifestName) {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("%s: target entry is not a regular file: %w", filename, ErrUnowned)
		}
		relative, err := filepath.Rel(dir, filename)
		if err != nil {
			return fmt.Errorf("resolve target path %q: %w", filename, err)
		}
		relative = filepath.ToSlash(relative)
		hash, err := hashFile(filename)
		if err != nil {
			return fmt.Errorf("hash target file %q: %w", filename, err)
		}
		if hash == existing.Files[relative] || hash == next.Files[relative] {
			return nil
		}
		return fmt.Errorf("%s: content matches neither installed nor embedded skill: %w", filename, ErrUnowned)
	})
}

func manifestIsCurrent(dir string, existing, next Manifest) bool {
	if existing.Version != next.Version || !reflect.DeepEqual(existing.Files, next.Files) {
		return false
	}
	for relative, wantHash := range next.Files {
		gotHash, err := hashFile(filepath.Join(dir, filepath.FromSlash(relative)))
		if err != nil || gotHash != wantHash {
			return false
		}
	}
	return true
}

func droppedFiles(existing, next Manifest) []string {
	var dropped []string
	for relative := range existing.Files {
		if _, stillShipped := next.Files[relative]; !stillShipped {
			dropped = append(dropped, relative)
		}
	}
	sort.Strings(dropped)
	return dropped
}

func sortedManifestPaths(manifest Manifest) []string {
	paths := make([]string, 0, len(manifest.Files))
	for relative := range manifest.Files {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	return paths
}

func manifestWritePaths(dir string, manifest Manifest) []string {
	paths := make([]string, 0, len(manifest.Files)+1)
	for _, relative := range sortedManifestPaths(manifest) {
		paths = append(paths, filepath.Join(dir, filepath.FromSlash(relative)))
	}
	return append(paths, filepath.Join(dir, ManifestName))
}

func ensureDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.Chmod(dir, 0o755)
}

func hashFile(filename string) (string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(content)), nil
}

func describeWriteFailure(cause error, expected, written []string) string {
	writtenSet := make(map[string]bool, len(written))
	for _, filename := range written {
		writtenSet[filename] = true
	}
	var notWritten []string
	for _, filename := range expected {
		if !writtenSet[filename] {
			notWritten = append(notWritten, filename)
		}
	}
	return fmt.Sprintf("%v; written: %s; not written: %s", cause, pathList(written), pathList(notWritten))
}

func pathList(paths []string) string {
	if len(paths) == 0 {
		return "none"
	}
	return strings.Join(paths, ", ")
}

func actionForError(err error) string {
	if errors.Is(err, ErrUnowned) {
		return "refused"
	}
	return "failed"
}
