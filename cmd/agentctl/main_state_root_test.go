//go:build darwin

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/session"
	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestBareStatusAcceptsFactoryDefaultMacOSHome(t *testing.T) {
	parent := t.TempDir()
	home := filepath.Join(parent, "home")
	if err := os.Mkdir(home, 0o750); err != nil {
		t.Fatal(err)
	}
	staff, err := user.LookupGroup("staff")
	if err != nil {
		t.Fatalf("look up stock macOS staff group: %v", err)
	}
	staffGID, err := strconv.Atoi(staff.Gid)
	if err != nil {
		t.Fatalf("parse staff gid %q: %v", staff.Gid, err)
	}
	if err := os.Chown(home, os.Geteuid(), staffGID); err != nil {
		t.Fatalf("set stock macOS home group: %v", err)
	}
	if err := os.Chmod(home, 0o750); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(home, "Library", "Application Support")
	if err := os.MkdirAll(configRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(home)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0o750 || int(stat.Gid) != staffGID {
		t.Fatalf("stock home fixture mode=%04o stat=%T %#v, want mode 0750 group staff gid %d", info.Mode().Perm(), info.Sys(), info.Sys(), staffGID)
	}

	t.Setenv("HOME", home)
	t.Setenv("AGENTCTL_RUNTIME_ROOT", filepath.Join(parent, "runtime"))
	unsetEnvironment(t, "AGENTCTL_STATE_ROOT")
	unsetEnvironment(t, "TMUX_PANE")
	runner := tmuxx.NewFakeRunner()
	var stdout, stderr bytes.Buffer
	code := runWithRunner(context.Background(), []string{"status", "--json"}, &stdout, &stderr, runner, session.LookupEnv(os.LookupEnv))
	if code != exitOK || stdout.String() != "{\"schema\":1,\"sessions\":[]}\n" || stderr.Len() != 0 {
		t.Fatalf("bare status code=%d stdout=%q stderr=%q, want exact empty report", code, stdout.String(), stderr.String())
	}
	if len(runner.Calls) != 0 {
		t.Fatalf("bare empty status tmux calls = %#v, want none", runner.Calls)
	}
	for _, path := range []string{filepath.Join(configRoot, "agentctl"), filepath.Join(configRoot, "agentctl", "state-v1")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("new durable directory mode(%q) = %04o, want 0700", path, got)
		}
	}
}

func TestStatusRefusesUnsafeFinalStateRoot(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, string)
		reason  string
	}{
		{
			name: "symlink",
			prepare: func(t *testing.T, stateRoot string) {
				if err := os.Mkdir(stateRoot+"-target", 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(stateRoot+"-target", stateRoot); err != nil {
					t.Fatal(err)
				}
			},
			reason: "must not be a symbolic link",
		},
		{
			name: "wrong mode",
			prepare: func(t *testing.T, stateRoot string) {
				if err := os.Mkdir(stateRoot, 0o750); err != nil {
					t.Fatal(err)
				}
			},
			reason: "mode is 0750; expected 0700",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			stateRoot := filepath.Join(parent, "state")
			test.prepare(t, stateRoot)
			t.Setenv("AGENTCTL_RUNTIME_ROOT", filepath.Join(parent, "runtime"))
			t.Setenv("AGENTCTL_STATE_ROOT", stateRoot)
			var stdout, stderr bytes.Buffer
			code := runWithRunner(context.Background(), []string{"status", "--json"}, &stdout, &stderr, tmuxx.NewFakeRunner(), session.LookupEnv(os.LookupEnv))
			want := "agentctl: invalid state root " + strconv.Quote(stateRoot) + ": " + strconv.Quote(test.reason) + "; no role was mutated\n"
			if code != exitUsage || stdout.Len() != 0 || stderr.String() != want {
				t.Fatalf("status code=%d stdout=%q stderr=%q, want %d empty stdout and %q", code, stdout.String(), stderr.String(), exitUsage, want)
			}
		})
	}
}

func TestShimSetupErrorKeepsObservationAndSubstitutionUnclassified(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{
			name: "substitution",
			err:  &shim.RootSubstitutedError{Kind: "state", Path: "/tmp/state"},
			want: "agentctl: status failed for session \"\": \"state root \\\"/tmp/state\\\" was substituted after descriptor verification\" (unclassified)\n",
		},
		{
			name: "observation",
			err:  &shim.FilesystemObservationError{Kind: "state", Path: "/tmp/state", Operation: "stat retained directory", Err: errors.New("input/output error")},
			want: "agentctl: status failed for session \"\": \"could not stat retained directory for state \\\"/tmp/state\\\": input/output error\" (unclassified)\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := shimSetupError(&stderr, "status", test.err); code != exitUnclassified || stderr.String() != test.want {
				t.Fatalf("shimSetupError() code=%d stderr=%q, want %d %q", code, stderr.String(), exitUnclassified, test.want)
			}
		})
	}
}

func unsetEnvironment(t *testing.T, name string) {
	t.Helper()
	value, wasSet := os.LookupEnv(name)
	if err := os.Unsetenv(name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv(name, value)
		} else {
			_ = os.Unsetenv(name)
		}
	})
}
