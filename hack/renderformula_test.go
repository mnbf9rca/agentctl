package hack_test

import (
	"os"
	"os/exec"
	"testing"
)

func render(t *testing.T, version, checksums string) (string, error) {
	t.Helper()
	cmd := exec.Command("./render-formula.sh", version, checksums)
	out, err := cmd.Output()
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
			if _, err := render(t, tc.version, tc.checksums); err == nil {
				t.Fatal("expected failure")
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
	if _, err := render(t, "0.1.0", tmp); err == nil {
		t.Fatal("expected failure when an arch checksum is absent")
	}
}
