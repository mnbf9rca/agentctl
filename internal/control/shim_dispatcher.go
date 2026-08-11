package control

import (
	"context"
	"errors"
)

// ErrOperationHasNoPayload refuses lifecycle operations at the payload-only
// dispatcher boundary.
var ErrOperationHasNoPayload = errors.New("operation has no PTY payload")

// ConnectedPeerGuard receives only the peer PID observed from the already
// connected Unix socket. It has no advisory or environment input.
type ConnectedPeerGuard interface {
	Guard(context.Context, int, int) error
}

// GuardedShimClient keeps the ancestry decision inside the same connected
// round trip that supplies LOCAL_PEERPID. The type parameter is the client's
// closed response type; no request payload is admitted here.
type GuardedShimClient[T any] interface {
	DeliverOperationGuarded(context.Context, string, string, string, func(context.Context, int) error) (T, error)
}

// ShimDispatcher is the operation-name-only compatibility dispatcher used by
// the PR 7 cutover. The legacy Dispatcher remains unchanged until then.
type ShimDispatcher[T any] struct {
	client    GuardedShimClient[T]
	ancestry  ConnectedPeerGuard
	callerPID func() int
}

// NewShimDispatcher constructs an operation-name-only guarded dispatcher.
func NewShimDispatcher[T any](client GuardedShimClient[T], ancestry ConnectedPeerGuard, callerPID func() int) ShimDispatcher[T] {
	return ShimDispatcher[T]{client: client, ancestry: ancestry, callerPID: callerPID}
}

// Execute validates the closed payload operation, then asks the client to run
// ancestry against the LOCAL_PEERPID from that same connection before it
// sends the argument-free wire request.
func (d ShimDispatcher[T]) Execute(ctx context.Context, operation, session, role string) (T, error) {
	var zero T
	command, err := Lookup(operation)
	if err != nil {
		return zero, err
	}
	if command.Kind != OperationPayload {
		return zero, ErrOperationHasNoPayload
	}
	if d.client == nil || d.ancestry == nil || d.callerPID == nil {
		return zero, errors.New("shim dispatcher requires client, ancestry, and caller PID dependencies")
	}
	callerPID := d.callerPID()
	return d.client.DeliverOperationGuarded(ctx, session, role, operation, func(ctx context.Context, targetPID int) error {
		return d.ancestry.Guard(ctx, callerPID, targetPID)
	})
}
