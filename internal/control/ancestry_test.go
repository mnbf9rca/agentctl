package control

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestAncestryObserverUsesOneExactSnapshotAndClassifiesCompleteWalks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		caller    int
		target    int
		snapshot  string
		wantSelf  bool
		wantUndet bool
	}{
		{name: "caller is target", caller: 40, target: 40, snapshot: "1 0\n40 1\n", wantSelf: true},
		{name: "target is ancestor", caller: 44, target: 40, snapshot: "1 0\n40 1\n42 40\n44 42\n", wantSelf: true},
		{name: "sibling", caller: 44, target: 45, snapshot: "1 0\n40 1\n44 40\n45 40\n"},
		{name: "human terminal chain", caller: 44, target: 90, snapshot: "1 0\n20 1\n44 20\n90 1\n"},
		{name: "caller disappeared", caller: 44, target: 90, snapshot: "1 0\n90 1\n", wantUndet: true},
		{name: "target disappeared", caller: 44, target: 90, snapshot: "1 0\n20 1\n44 20\n", wantUndet: true},
		{name: "broken parent chain", caller: 44, target: 90, snapshot: "1 0\n44 20\n90 1\n", wantUndet: true},
		{name: "duplicate pid", caller: 44, target: 90, snapshot: "1 0\n44 20\n44 1\n90 1\n", wantUndet: true},
		{name: "loop", caller: 44, target: 90, snapshot: "1 0\n20 44\n44 20\n90 1\n", wantUndet: true},
		{name: "malformed row", caller: 44, target: 90, snapshot: "1 0\nnot-a-pid 20\n44 1\n90 1\n", wantUndet: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runner := tmuxx.NewFakeRunner(tmuxx.Response{Stdout: []byte(test.snapshot)})
			err := NewAncestryObserver(runner).Guard(context.Background(), test.caller, test.target)

			var self *ObservedSelfTargetError
			var undetermined *AncestryUndeterminedError
			if got := errors.As(err, &self); got != test.wantSelf {
				t.Fatalf("Guard() self-target = %v (%T %v), want %v", got, err, err, test.wantSelf)
			}
			if got := errors.As(err, &undetermined); got != test.wantUndet {
				t.Fatalf("Guard() undetermined = %v (%T %v), want %v", got, err, err, test.wantUndet)
			}
			if !test.wantSelf && !test.wantUndet && err != nil {
				t.Fatalf("Guard() error = %T %v, want safe complete walk", err, err)
			}
			wantCalls := []tmuxx.Call{{Executable: "ps", Args: []string{"-eo", "pid=,ppid="}}}
			if !reflect.DeepEqual(runner.Calls, wantCalls) {
				t.Fatalf("snapshot calls = %#v, want exactly %#v", runner.Calls, wantCalls)
			}
		})
	}
}

func TestAncestryObserverToolFailureIsUndeterminedNotSelfTarget(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("ps unavailable")
	runner := tmuxx.NewFakeRunner(tmuxx.Response{Err: wantErr})
	err := NewAncestryObserver(runner).Guard(context.Background(), 44, 40)

	var undetermined *AncestryUndeterminedError
	if !errors.As(err, &undetermined) || !errors.Is(err, wantErr) {
		t.Fatalf("Guard() error = %T %v, want ancestry-undetermined wrapping %v", err, err, wantErr)
	}
	var self *ObservedSelfTargetError
	if errors.As(err, &self) {
		t.Fatalf("Guard() error = %T %v, must not claim observed self-target", err, err)
	}
}
