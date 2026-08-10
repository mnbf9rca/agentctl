//go:build darwin

package shim

import (
	"context"
	"errors"
	"fmt"
	"net"

	"github.com/mnbf9rca/agentctl/internal/control"
)

// Client performs one versioned, answerer-checked Unix-socket round trip per
// operation. Its public methods cannot carry PTY bytes.
type Client struct {
	namespace *Namespace
	dial      func(context.Context, string) (*net.UnixConn, error)
}

// NewClient constructs a client over one descriptor-verified namespace.
func NewClient(namespace *Namespace) *Client {
	return &Client{namespace: namespace, dial: dialRoleSocket}
}

func dialRoleSocket(ctx context.Context, path string) (*net.UnixConn, error) {
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		return nil, fmt.Errorf("connected role socket has type %T; expected Unix connection", connection)
	}
	return unixConnection, nil
}

// Observe requests the closed, non-payload runtime observation operation.
func (c *Client) Observe(ctx context.Context, session, role string) (Response, error) {
	return c.roundTrip(ctx, session, role, "observe")
}

// DeliverOperation requests one closed payload operation by name. Payload
// bytes are resolved only inside the server.
func (c *Client) DeliverOperation(ctx context.Context, session, role, operation string) (Response, error) {
	command, err := control.Lookup(operation)
	if err != nil {
		return Response{}, err
	}
	if command.Kind != control.OperationPayload {
		return Response{}, ErrOperationHasNoPayload
	}
	return c.roundTrip(ctx, session, role, operation)
}

// Stop requests the closed, non-payload lifecycle stop operation.
func (c *Client) Stop(ctx context.Context, session, role string) (Response, error) {
	return c.roundTrip(ctx, session, role, "stop")
}

func (c *Client) roundTrip(ctx context.Context, session, role, operation string) (Response, error) {
	if c == nil || c.namespace == nil {
		return Response{}, errors.New("shim client requires a namespace")
	}
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	path, err := c.namespace.ExistingRolePath(session, role)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = path.Close() }()
	advisory, err := ReadAdvisory(path)
	if err != nil {
		return Response{}, err
	}
	if err := advisory.CompareStateRoot(c.namespace.StateRoot); err != nil {
		return Response{}, err
	}
	connection, err := c.dial(ctx, path.Socket)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = connection.Close() }()
	stopCancellationClose := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellationClose()

	helloPayload, err := ReadFrame(connection)
	if err != nil {
		return Response{}, clientContextError(ctx, err)
	}
	if err := DecodeHello(helloPayload); err != nil {
		return Response{}, err
	}
	answererPID, err := LocalPeerPID(connection)
	if err != nil {
		return Response{}, err
	}
	if err := advisory.CompareAnswerer(answererPID); err != nil {
		return Response{}, err
	}

	requestPayload, err := EncodeRequest(Request{
		Version: ShimProtocolVersion, Session: session, Role: role, Operation: operation,
	})
	if err != nil {
		return Response{}, err
	}
	if _, err := WriteFrame(connection, requestPayload); err != nil {
		return Response{}, clientContextError(ctx, err)
	}
	responsePayload, err := ReadFrame(connection)
	if err != nil {
		return Response{}, clientContextError(ctx, err)
	}
	return DecodeResponse(responsePayload)
}

func clientContextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return err
}
