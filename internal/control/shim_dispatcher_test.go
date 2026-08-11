package control

import (
	"context"
	"errors"
	"testing"
)

func TestShimDispatcherCarriesOnlyOperationSessionRoleAndGuardsConnectedPeer(t *testing.T) {
	t.Parallel()

	guard := &fakeAncestryGuard{}
	client := &fakeGuardedShimClient[string]{targetPID: 71, response: "submitted"}
	dispatcher := NewShimDispatcher[string](client, guard, func() int { return 83 })

	got, err := dispatcher.Execute(context.Background(), "clear", "fleet", "planner")
	if err != nil || got != "submitted" {
		t.Fatalf("Execute() = %q, %v, want submitted", got, err)
	}
	if client.operation != "clear" || client.session != "fleet" || client.role != "planner" {
		t.Fatalf("client inputs = (%q, %q, %q), want operation/session/role only", client.operation, client.session, client.role)
	}
	if guard.callerPID != 83 || guard.targetPID != 71 {
		t.Fatalf("ancestry inputs = caller %d target %d, want caller 83 and connected peer 71", guard.callerPID, guard.targetPID)
	}
}

func TestShimDispatcherRefusesUnknownOrNonPayloadOperationBeforeClient(t *testing.T) {
	t.Parallel()

	for _, operation := range []string{"rename", "observe", "stop"} {
		client := &fakeGuardedShimClient[string]{response: "unexpected"}
		dispatcher := NewShimDispatcher[string](client, &fakeAncestryGuard{}, func() int { return 83 })
		_, err := dispatcher.Execute(context.Background(), operation, "fleet", "planner")
		if err == nil {
			t.Fatalf("Execute(%q) error = nil, want closed payload-operation refusal", operation)
		}
		if client.called {
			t.Fatalf("Execute(%q) called shim client", operation)
		}
	}
}

func TestShimDispatcherPreservesDistinctAncestryRefusals(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "observed self target", err: &ObservedSelfTargetError{CallerPID: 83, TargetPID: 71}},
		{name: "ancestry undetermined", err: &AncestryUndeterminedError{CallerPID: 83, TargetPID: 71, Cause: errors.New("broken parent chain")}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			guard := &fakeAncestryGuard{err: test.err}
			client := &fakeGuardedShimClient[string]{targetPID: 71, response: "unexpected"}
			_, err := NewShimDispatcher[string](client, guard, func() int { return 83 }).Execute(context.Background(), "compact", "fleet", "review")
			if !errors.Is(err, test.err) {
				t.Fatalf("Execute() error = %T %v, want exact typed refusal %T %v", err, err, test.err, test.err)
			}
		})
	}
}

type fakeAncestryGuard struct {
	callerPID int
	targetPID int
	err       error
}

func (f *fakeAncestryGuard) Guard(_ context.Context, callerPID, targetPID int) error {
	f.callerPID = callerPID
	f.targetPID = targetPID
	return f.err
}

type fakeGuardedShimClient[T any] struct {
	targetPID int
	response  T
	err       error
	called    bool
	operation string
	session   string
	role      string
}

func (f *fakeGuardedShimClient[T]) DeliverOperationGuarded(
	ctx context.Context,
	session string,
	role string,
	operation string,
	guard func(context.Context, int) error,
) (T, error) {
	f.called = true
	f.session = session
	f.role = role
	f.operation = operation
	if err := guard(ctx, f.targetPID); err != nil {
		var zero T
		return zero, err
	}
	return f.response, f.err
}
