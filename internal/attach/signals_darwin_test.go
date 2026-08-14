//go:build darwin

package attach

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestSignalProviderDoesNotNotifyForEmptyEligibleSet(t *testing.T) {
	notifyCalls := 0
	locked, unlocked := 0, 0
	provider := signalProvider{system: signalSystem{
		lockThread: func() { locked++ }, unlockThread: func() { unlocked++ },
		disposition: func(os.Signal) (bool, error) { return true, nil }, mask: func(syscall.Signal) (bool, error) { return false, nil },
		notify: func(chan<- os.Signal, ...os.Signal) { notifyCalls++ },
		stop:   func(chan<- os.Signal) {}, reset: func(...os.Signal) {}, kill: func(int, syscall.Signal) error { return nil }, pid: func() int { return 1 },
	}}
	plan, err := provider.observe()
	if err != nil {
		t.Fatal(err)
	}
	if events := plan.install(); events != nil || notifyCalls != 0 {
		t.Fatalf("events=%v notifyCalls=%d, want nil/0", events, notifyCalls)
	}
	plan.close()
	if locked != 1 || unlocked != 1 {
		t.Fatalf("lock/unlock=%d/%d", locked, unlocked)
	}
}

func TestSignalProviderSubscribesOnlyObservedOrdinaryUnblockedCandidates(t *testing.T) {
	var got []os.Signal
	provider := signalProvider{system: signalSystem{
		lockThread: func() {}, unlockThread: func() {},
		disposition: func(value os.Signal) (bool, error) { return value == syscall.SIGHUP, nil },
		mask:        func(value syscall.Signal) (bool, error) { return value == syscall.SIGTERM, nil },
		notify:      func(_ chan<- os.Signal, values ...os.Signal) { got = append(got, values...) },
		stop:        func(chan<- os.Signal) {}, reset: func(...os.Signal) {}, kill: func(int, syscall.Signal) error { return nil }, pid: func() int { return 1 },
	}}
	plan, err := provider.observe()
	if err != nil {
		t.Fatal(err)
	}
	plan.install()
	plan.close()
	if len(got) != 2 || got[0] != syscall.SIGINT || got[1] != syscall.SIGQUIT {
		t.Fatalf("eligible=%v, want SIGINT,SIGQUIT", got)
	}
}

func TestSignalMaskObservationFailureUnlocksAndNamesCanonicalSignal(t *testing.T) {
	want := errors.New("mask failed")
	unlocked := false
	provider := signalProvider{system: signalSystem{
		lockThread: func() {}, unlockThread: func() { unlocked = true }, disposition: func(os.Signal) (bool, error) { return false, nil },
		mask: func(syscall.Signal) (bool, error) { return false, want },
	}}
	_, err := provider.observe()
	var observation *SignalObservationError
	if !errors.As(err, &observation) || observation.Observation != "mask" || canonicalSignalName(observation.Signal) != "SIGINT" || !unlocked {
		t.Fatalf("error=%T %v unlocked=%t", err, err, unlocked)
	}
}

func TestSignalObservationUsesFixedCandidateThenDispositionMaskTraversal(t *testing.T) {
	var observations []string
	want := errors.New("sigterm disposition failed")
	provider := signalProvider{system: signalSystem{
		lockThread: func() {}, unlockThread: func() {},
		disposition: func(value os.Signal) (bool, error) {
			observations = append(observations, canonicalSignalName(value.(syscall.Signal))+":disposition")
			if value == syscall.SIGTERM {
				return false, want
			}
			return false, nil
		},
		mask: func(value syscall.Signal) (bool, error) {
			observations = append(observations, canonicalSignalName(value)+":mask")
			return false, nil
		},
	}}
	_, err := provider.observe()
	var observation *SignalObservationError
	if !errors.As(err, &observation) || observation.Signal != syscall.SIGTERM || observation.Observation != "disposition" || !errors.Is(err, want) {
		t.Fatalf("error=%T %v", err, err)
	}
	wantOrder := []string{"SIGINT:disposition", "SIGINT:mask", "SIGTERM:disposition"}
	if len(observations) != len(wantOrder) {
		t.Fatalf("observations=%v, want %v", observations, wantOrder)
	}
	for index := range wantOrder {
		if observations[index] != wantOrder[index] {
			t.Fatalf("observations=%v, want %v", observations, wantOrder)
		}
	}
}

func TestProductionSignalMaskObservationRunsOnDarwin(t *testing.T) {
	plan, err := newSignalProvider().observe()
	if err != nil {
		t.Fatal(err)
	}
	plan.close()
}

func TestSignalPlanReraiseForcesDefaultAfterStopAndResetBeforeKill(t *testing.T) {
	var order []string
	plan := &signalPlan{system: signalSystem{
		stop:               func(chan<- os.Signal) { order = append(order, "stop") },
		reset:              func(...os.Signal) { order = append(order, "reset") },
		defaultDisposition: func(syscall.Signal) error { order = append(order, "default"); return nil },
		kill:               func(int, syscall.Signal) error { order = append(order, "kill"); return nil },
		pid:                func() int { return 1 },
	}, events: make(chan os.Signal, 1), installed: true}
	if err := plan.reraise(syscall.SIGQUIT); err != nil {
		t.Fatal(err)
	}
	want := []string{"stop", "reset", "default", "kill"}
	if len(order) != len(want) {
		t.Fatalf("order=%v, want %v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("order=%v, want %v", order, want)
		}
	}
}
