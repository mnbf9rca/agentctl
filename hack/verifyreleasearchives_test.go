package hack_test

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var requiredReleaseArchiveFiles = []string{
	"LICENSE",
	"LICENSES/README.md",
	"LICENSES/github.com/santhosh-tekuri/jsonschema/v6/LICENSE",
	"LICENSES/golang.org/x/text/LICENSE",
	"LICENSES/golang.org/x/text/PATENTS",
}

func TestVerifyReleaseArchivesAcceptsEveryRequiredLicenseMaterial(t *testing.T) {
	archive := writeReleaseArchive(t, requiredReleaseArchiveFiles)
	command := exec.Command("./verify-release-archives.sh", archive)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("verify-release-archives.sh error = %v, output = %s", err, output)
	}
	want := "archive " + archive + ": required license materials present\n"
	if got := string(output); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestVerifyReleaseArchivesRefusesAnArchiveMissingOneRequiredLicense(t *testing.T) {
	archive := writeReleaseArchive(t, requiredReleaseArchiveFiles[:len(requiredReleaseArchiveFiles)-1])
	command := exec.Command("./verify-release-archives.sh", archive)
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 1 {
		t.Fatalf("verify-release-archives.sh error = %v, output = %s; want exit 1", err, output)
	}
	want := "archive " + archive + ": missing required file LICENSES/golang.org/x/text/PATENTS\n"
	if got := string(output); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func writeReleaseArchive(t *testing.T, names []string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "agentctl.tar.gz")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(file)
	archive := tar.NewWriter(compressed)
	for _, name := range names {
		contents := []byte("fixture for " + name + "\n")
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	var closeErrors []string
	if err := archive.Close(); err != nil {
		closeErrors = append(closeErrors, err.Error())
	}
	if err := compressed.Close(); err != nil {
		closeErrors = append(closeErrors, err.Error())
	}
	if err := file.Close(); err != nil {
		closeErrors = append(closeErrors, err.Error())
	}
	if len(closeErrors) != 0 {
		t.Fatal(strings.Join(closeErrors, "; "))
	}
	return path
}
