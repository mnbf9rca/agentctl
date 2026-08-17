package tmuxx

import "context"

// ParentPIDs captures the one permitted process-ancestry snapshot without a
// shell. Parsing and disposition stay with the caller that owns the guard.
func ParentPIDs(ctx context.Context, runner Runner) ([]byte, error) {
	return runner.Output(ctx, "ps", "-eo", "pid=,ppid=")
}
