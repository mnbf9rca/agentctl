package control

import (
	"context"
	"errors"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestDispatcherResolvesBeforeDeliveringRegisteredLiteral(t *testing.T) {
	tests := []struct {
		operation string
		payload   string
	}{
		{operation: "clear", payload: "/clear"},
		{operation: "compact", payload: "/compact"},
	}
	for _, tt := range tests {
		t.Run(tt.operation, func(t *testing.T) {
			target := tmuxx.Session{ID: "$4", Name: "epic123"}
			var resolvedSession tmuxx.Session
			var resolvedRole string
			resolver := targetResolverFunc(func(_ context.Context, session tmuxx.Session, role string) (tmuxx.PaneID, error) {
				resolvedSession = session
				resolvedRole = role
				return "%9", nil
			})
			var deliveredPane tmuxx.PaneID
			var deliveredPayload string
			deliverer := delivererFunc(func(_ context.Context, pane tmuxx.PaneID, payload string) error {
				deliveredPane = pane
				deliveredPayload = payload
				return nil
			})

			err := New(resolver, deliverer).Execute(context.Background(), tt.operation, target, "planner")

			if err != nil {
				t.Fatalf("Execute() error = %v", err)
			}
			if resolvedSession != target || resolvedRole != "planner" {
				t.Fatalf("Resolve() inputs = (%#v, %q), want (%#v, %q)", resolvedSession, resolvedRole, target, "planner")
			}
			if deliveredPane != "%9" || deliveredPayload != tt.payload {
				t.Fatalf("DeliverPayload() inputs = (%q, %q), want (%q, %q)", deliveredPane, deliveredPayload, "%9", tt.payload)
			}
		})
	}
}

func TestDispatcherUnknownOperationIsInternalDefenseInDepthBeforeDependencies(t *testing.T) {
	resolverCalled := false
	delivererCalled := false
	dispatcher := New(
		targetResolverFunc(func(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error) {
			resolverCalled = true
			return "%9", nil
		}),
		delivererFunc(func(context.Context, tmuxx.PaneID, string) error {
			delivererCalled = true
			return nil
		}),
	)

	err := dispatcher.Execute(context.Background(), "rename", tmuxx.Session{ID: "$4", Name: "epic123"}, "planner")

	var unknown *UnknownOperationError
	if !errors.As(err, &unknown) {
		t.Fatalf("Execute() error = %T %v, want *UnknownOperationError", err, err)
	}
	if resolverCalled || delivererCalled {
		t.Fatalf("dependency calls = resolver:%v deliverer:%v, want neither", resolverCalled, delivererCalled)
	}
}

func TestDispatcherResolutionFailurePreventsDelivery(t *testing.T) {
	wantErr := errors.New("unsafe target")
	delivererCalled := false
	dispatcher := New(
		targetResolverFunc(func(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error) {
			return "", wantErr
		}),
		delivererFunc(func(context.Context, tmuxx.PaneID, string) error {
			delivererCalled = true
			return nil
		}),
	)

	err := dispatcher.Execute(context.Background(), "compact", tmuxx.Session{ID: "$4", Name: "epic123"}, "reviewer")

	if !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %v, want resolver error %v", err, wantErr)
	}
	if delivererCalled {
		t.Fatal("DeliverPayload() called after resolution failure")
	}
}

func TestDispatcherClassifiesDeliveryFailure(t *testing.T) {
	wantErr := errors.New("tmux rejected keys")
	dispatcher := New(
		targetResolverFunc(func(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error) {
			return "%9", nil
		}),
		delivererFunc(func(context.Context, tmuxx.PaneID, string) error {
			return wantErr
		}),
	)

	err := dispatcher.Execute(context.Background(), "compact", tmuxx.Session{ID: "$4", Name: "epic123"}, "reviewer")

	var tmuxFailure *tmuxx.TmuxError
	if !errors.As(err, &tmuxFailure) || !errors.Is(err, wantErr) {
		t.Fatalf("Execute() error = %T %v, want classified tmux error wrapping %v", err, err, wantErr)
	}
}

func TestDispatcherPreservesDeliveryContextSentinel(t *testing.T) {
	dispatcher := New(
		targetResolverFunc(func(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error) {
			return "%9", nil
		}),
		delivererFunc(func(context.Context, tmuxx.PaneID, string) error {
			return context.Canceled
		}),
	)

	if err := dispatcher.Execute(context.Background(), "clear", tmuxx.Session{ID: "$4", Name: "epic123"}, "planner"); err != context.Canceled {
		t.Fatalf("Execute() error = %T %v, want exact context.Canceled", err, err)
	}
}

type targetResolverFunc func(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error)

func (f targetResolverFunc) Resolve(ctx context.Context, session tmuxx.Session, role string) (tmuxx.PaneID, error) {
	return f(ctx, session, role)
}

type delivererFunc func(context.Context, tmuxx.PaneID, string) error

func (f delivererFunc) DeliverPayload(ctx context.Context, pane tmuxx.PaneID, payload string) error {
	return f(ctx, pane, payload)
}
