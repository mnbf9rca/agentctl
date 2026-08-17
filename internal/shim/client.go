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
	namespace  *Namespace
	dial       func(context.Context, string) (*net.UnixConn, error)
	readFrame  func(net.Conn) ([]byte, error)
	writeFrame func(net.Conn, []byte) (int, error)
}

// NewClient constructs a client over one descriptor-verified namespace.
func NewClient(namespace *Namespace) *Client {
	return &Client{namespace: namespace, dial: dialRoleSocket, readFrame: ReadFrame, writeFrame: WriteFrame}
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
	return c.roundTrip(ctx, session, role, "observe", nil)
}

// DeliverOperation requests one closed payload operation by name. Payload
// bytes are resolved only inside the server.
func (c *Client) DeliverOperation(ctx context.Context, session, role, operation string) (Response, error) {
	return c.DeliverOperationGuarded(ctx, session, role, operation, nil)
}

// DeliverOperationGuarded obtains LOCAL_PEERPID from the connected socket and
// runs guard before writing any request bytes. The callback cannot supply or
// alter operation payload; payload resolution remains inside the shim server.
func (c *Client) DeliverOperationGuarded(
	ctx context.Context,
	session string,
	role string,
	operation string,
	guard func(context.Context, int) error,
) (Response, error) {
	command, err := control.Lookup(operation)
	if err != nil {
		return Response{}, err
	}
	if command.Kind != control.OperationPayload {
		return Response{}, ErrOperationHasNoPayload
	}
	return c.roundTrip(ctx, session, role, operation, guard)
}

// Stop requests the closed, non-payload lifecycle stop operation.
func (c *Client) Stop(ctx context.Context, session, role string) (Response, error) {
	return c.roundTrip(ctx, session, role, "stop", nil)
}

func (c *Client) roundTrip(
	ctx context.Context,
	session string,
	role string,
	operation string,
	guard func(context.Context, int) error,
) (Response, error) {
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

	helloPayload, err := c.readFrame(connection)
	if err != nil {
		return Response{}, clientProtocolError(ctx, ProtocolFrameRead, err)
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
	if guard != nil {
		if err := guard(ctx, answererPID); err != nil {
			return Response{}, err
		}
	}

	requestPayload, err := EncodeRequest(Request{
		Version: ShimProtocolVersion, Session: session, Role: role, Operation: operation,
	})
	if err != nil {
		return Response{}, err
	}
	if _, err := c.writeFrame(connection, requestPayload); err != nil {
		return Response{}, clientProtocolError(ctx, ProtocolFrameWrite, err)
	}
	responsePayload, err := c.readFrame(connection)
	if err != nil {
		return Response{}, clientProtocolError(ctx, ProtocolFrameRead, err)
	}
	return DecodeResponse(responsePayload)
}

func clientProtocolError(ctx context.Context, direction ProtocolFrameDirection, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	return &ProtocolFrameError{Direction: direction, Peer: ProtocolPeerShim, Err: err}
}
