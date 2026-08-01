package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const payloadDelay = 100 * time.Millisecond

// ErrProcessUnavailable means ps could not provide a nonempty process identity.
var ErrProcessUnavailable = errors.New("process identity unavailable")

// DeliverPayload clears pending input, types one literal payload, waits for the
// fixed package delay, and submits it. No partial send-keys API is exposed.
func (c Client) DeliverPayload(ctx context.Context, paneID PaneID, payload string) error {
	if err := validateID(string(paneID), '%'); err != nil {
		return fmt.Errorf("deliver payload target: %w", err)
	}
	if _, err := c.tmuxOutput(ctx, "clear pane input", "send-keys", "-t", string(paneID), "C-u"); err != nil {
		return err
	}
	if _, err := c.tmuxOutput(ctx, "type literal payload", "send-keys", "-t", string(paneID), "-l", "--", payload); err != nil {
		return err
	}

	timer := time.NewTimer(payloadDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}

	_, err := c.tmuxOutput(ctx, "submit payload", "send-keys", "-t", string(paneID), "Enter")
	return err
}

// ProcessName returns the process executable reported by ps with exactly one
// trailing newline removed.
func (c Client) ProcessName(ctx context.Context, pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid process id %d: expected a positive decimal", pid)
	}
	output, err := c.runner.Output(ctx, "ps", "-o", "comm=", "-p", strconv.Itoa(pid))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		var exitError interface{ ExitCode() int }
		if errors.As(err, &exitError) {
			return "", fmt.Errorf("%w: ps exited with status %d", ErrProcessUnavailable, exitError.ExitCode())
		}
		return "", fmt.Errorf("inspect process %d: %w", pid, err)
	}

	name := string(trimOneTrailingNewline(output))
	if name == "" {
		return "", fmt.Errorf("%w: ps returned empty output", ErrProcessUnavailable)
	}
	return name, nil
}
