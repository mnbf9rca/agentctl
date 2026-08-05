package hack_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestVerifyInjectionHelpNeverShowsAttachFirst locks in that --help exits
// with usage text alone. The ATTACH command line is only meaningful once a
// real run is underway; printing it ahead of --help's usage text would be a
// paste trap for an operator skimming past it. Both --help spellings (as the
// sole argument, and after MODE) are covered since each has its own early
// exit in the script.
func TestVerifyInjectionHelpNeverShowsAttachFirst(t *testing.T) {
	for _, args := range [][]string{
		{"--help"},
		{"-h"},
		{"verify", "--help"},
		{"measure", "-h"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			cmd := exec.Command("./verify-injection.sh", args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("--help failed: %v\n%s", err, out)
			}
			output := string(out)
			if strings.Contains(output, "ATTACH") {
				t.Fatalf("--help output unexpectedly contains ATTACH:\n%s", output)
			}
			if !strings.Contains(output, "Usage:") {
				t.Fatalf("--help output missing usage text:\n%s", output)
			}
		})
	}
}
