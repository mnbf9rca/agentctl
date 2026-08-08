package skillinstall

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
)

type State string

const (
	StateCurrent   State = "current"
	StateStale     State = "stale"
	StateModified  State = "modified"
	StateAbsent    State = "absent"
	StateUnmanaged State = "unmanaged"
)

type Report struct {
	Target           Target
	State            State
	InstalledVersion string
}

func Status(tree fs.FS, root, version string, targets []Target) ([]Report, error) {
	current, err := BuildManifest(tree, root, version)
	if err != nil {
		return nil, err
	}
	reports := make([]Report, 0, len(targets))
	for _, target := range targets {
		report, err := statusTarget(target, current)
		if err != nil {
			return reports, err
		}
		reports = append(reports, report)
	}
	return reports, nil
}

func statusTarget(target Target, current Manifest) (Report, error) {
	report := Report{Target: target}
	info, err := os.Lstat(target.Dir)
	if errors.Is(err, fs.ErrNotExist) {
		report.State = StateAbsent
		return report, nil
	}
	if err != nil {
		return report, fmt.Errorf("inspect skill target %q: %w", target.Dir, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		report.State = StateUnmanaged
		return report, nil
	}

	installed, ok, err := ReadManifest(target.Dir)
	if err != nil {
		return report, fmt.Errorf("inspect skill target %q: %w", target.Dir, err)
	}
	if !ok {
		report.State = StateUnmanaged
		return report, nil
	}
	report.InstalledVersion = installed.Version
	if installed.Version != current.Version {
		report.State = StateStale
		return report, nil
	}
	if !reflect.DeepEqual(installed.Files, current.Files) {
		report.State = StateModified
		return report, nil
	}
	for relative, wantHash := range current.Files {
		gotHash, err := hashFile(filepath.Join(target.Dir, filepath.FromSlash(relative)))
		if errors.Is(err, fs.ErrNotExist) || (err == nil && gotHash != wantHash) {
			report.State = StateModified
			return report, nil
		}
		if err != nil {
			return report, fmt.Errorf("hash installed skill file %q: %w", relative, err)
		}
	}
	report.State = StateCurrent
	return report, nil
}
