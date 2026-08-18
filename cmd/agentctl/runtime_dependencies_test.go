//go:build darwin

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestBuildShimDependenciesCarriesRunnerIntoLaunchAMQProvisioning(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTCTL_RUNTIME_ROOT", filepath.Join(root, "runtime"))
	t.Setenv("AGENTCTL_STATE_ROOT", filepath.Join(root, "state"))
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"amq", "claude"} {
		if err := os.WriteFile(filepath.Join(bin, name), nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)

	wantErr := errors.New("production runner reached")
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: wantErr})
	deps, closeRuntime, err := buildShimDependencies(runner, os.LookupEnv, nil, nil)
	if err != nil {
		t.Fatalf("buildShimDependencies() error = %v", err)
	}
	t.Cleanup(func() {
		if err := closeRuntime(); err != nil {
			t.Errorf("close runtime: %v", err)
		}
	})

	directory := filepath.Join(root, "repo")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err = deps.launcher.Launch(context.Background(), "fleet", config.FleetConfig{
		Roles: []config.RoleConfig{{Name: "planner", Harness: config.HarnessClaude}},
	}, fleet.PresentationDetached, &directory)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Launch() error = %v, want %v", err, wantErr)
	}
	wantCalls := []tmuxx.Call{{
		Executable: "amq",
		Args:       []string{"init", "--root", filepath.Join(directory, ".agent-mail", "fleet"), "--agents", "planner"},
	}}
	if !reflect.DeepEqual(runner.Calls, wantCalls) {
		t.Fatalf("runner calls = %#v, want %#v", runner.Calls, wantCalls)
	}
}

func TestPrepareForegroundTerminalClassifiesNonTerminalStandardOutput(t *testing.T) {
	t.Parallel()

	stdin := new(os.File)
	stdout := new(os.File)
	_, err := prepareForegroundTerminalWithObserver(stdin, stdout, func(file *os.File) (ptyx.TerminalState, error) {
		if file == stdout {
			return ptyx.TerminalState{}, syscall.ENOTSUP
		}
		return ptyx.TerminalState{}, nil
	})
	var notTerminal *foregroundNotTerminalError
	if !errors.As(err, &notTerminal) {
		t.Fatalf("error=%T %v, want foregroundNotTerminalError", err, err)
	}
}

func TestPrepareForegroundTerminalPreservesOtherStandardOutputObservationFailures(t *testing.T) {
	t.Parallel()

	stdin := new(os.File)
	stdout := new(os.File)
	cause := errors.New("TIOCGWINSZ: input/output error")
	_, err := prepareForegroundTerminalWithObserver(stdin, stdout, func(file *os.File) (ptyx.TerminalState, error) {
		if file == stdout {
			return ptyx.TerminalState{}, cause
		}
		return ptyx.TerminalState{}, nil
	})
	var observation *foregroundTerminalObservationError
	if !errors.As(err, &observation) || !errors.Is(err, cause) {
		t.Fatalf("error=%T %v, want wrapped foregroundTerminalObservationError", err, err)
	}
}
