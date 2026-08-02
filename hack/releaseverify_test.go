package hack_test

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func renderResults(t *testing.T, versions, artifactDir string) (string, error) {
	t.Helper()
	cmd := exec.Command("./release-verify.sh", "--render-results", versions, artifactDir)
	out, err := cmd.Output()
	return string(out), err
}

func TestRenderResultsMatchesGolden(t *testing.T) {
	got, err := renderResults(t, "testdata/release-verify-versions.txt", "testdata/release-verify-artifact")
	if err != nil {
		t.Fatalf("render-results failed: %v", err)
	}
	golden, err := os.ReadFile("testdata/release-verify-results.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(golden) {
		t.Fatalf("output does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, string(golden))
	}
}

func TestRenderResultsRejects(t *testing.T) {
	cases := []struct {
		name        string
		versions    string
		artifactDir string
	}{
		{"missing versions file", "testdata/absent-versions.txt", "testdata/release-verify-artifact"},
		{"missing artifact dir", "testdata/release-verify-versions.txt", "testdata/absent-artifact"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := renderResults(t, tc.versions, tc.artifactDir)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}
