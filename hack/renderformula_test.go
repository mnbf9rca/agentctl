package hack_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
)

// render runs render-formula.sh and returns its stdout. On failure, the
// returned error wraps the process error together with captured stderr, so
// a test failure message shows *why* the script failed rather than just
// that it did.
func render(t *testing.T, version, checksums string) (string, error) {
	t.Helper()
	cmd := exec.Command("./render-formula.sh", version, checksums)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil && stderr.Len() > 0 {
		err = fmt.Errorf("%w: %s", err, stderr.String())
	}
	return string(out), err
}

func TestRenderFormulaMatchesGolden(t *testing.T) {
	got, err := render(t, "0.1.0", "testdata/checksums.txt")
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	golden, err := os.ReadFile("testdata/agentctl.rb.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got != string(golden) {
		t.Fatalf("output does not match golden.\n--- got ---\n%s\n--- want ---\n%s", got, string(golden))
	}
}

func TestRenderFormulaRejects(t *testing.T) {
	cases := []struct {
		name      string
		version   string
		checksums string
	}{
		{"v-prefixed version", "v0.1.0", "testdata/checksums.txt"},
		{"missing checksums file", "0.1.0", "testdata/absent.txt"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := render(t, tc.version, tc.checksums)
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 1 {
				t.Fatalf("want exit status 1, got %v", err)
			}
		})
	}
}

func TestRenderFormulaRejectsMissingArch(t *testing.T) {
	tmp := t.TempDir() + "/checksums.txt"
	only := "1111111111111111111111111111111111111111111111111111111111111111  agentctl_0.1.0_darwin_amd64.tar.gz\n"
	if err := os.WriteFile(tmp, []byte(only), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := render(t, "0.1.0", tmp)
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("want exit status 1, got %v", err)
	}
}
