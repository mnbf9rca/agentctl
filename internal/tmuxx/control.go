package tmuxx

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// ErrProcessUnavailable means ps could not provide a nonempty process identity.
var ErrProcessUnavailable = errors.New("process identity unavailable")

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
