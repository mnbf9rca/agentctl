package control

import (
	"context"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// TargetResolver resolves one validated role to an exact pane ID. It has no
// delivery capability and receives no operation or payload information.
type TargetResolver interface {
	Resolve(context.Context, tmuxx.Session, string) (tmuxx.PaneID, error)
}

// Deliverer exposes only complete payload delivery to an exact pane ID.
type Deliverer interface {
	DeliverPayload(context.Context, tmuxx.PaneID, string) error
}

// Dispatcher owns the resolve-before-deliver ordering for control operations.
type Dispatcher struct {
	resolver  TargetResolver
	deliverer Deliverer
}

// New constructs a Dispatcher from independently constrained dependencies.
func New(resolver TargetResolver, deliverer Deliverer) Dispatcher {
	return Dispatcher{resolver: resolver, deliverer: deliverer}
}

// Execute resolves operation and role before delivering the registry literal.
func (d Dispatcher) Execute(ctx context.Context, operation string, session tmuxx.Session, role string) error {
	command, err := Lookup(operation)
	if err != nil {
		return err
	}
	paneID, err := d.resolver.Resolve(ctx, session, role)
	if err != nil {
		return err
	}
	if err := d.deliverer.DeliverPayload(ctx, paneID, command.Payload); err != nil {
		return tmuxx.ClassifyError(err)
	}
	return nil
}
