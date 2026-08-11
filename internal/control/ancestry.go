package control

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// ObservedSelfTargetError reports that the target shim was observed in the
// caller's parent chain. It is deliberately distinct from an incomplete walk.
type ObservedSelfTargetError struct {
	CallerPID int
	TargetPID int
}

func (e *ObservedSelfTargetError) Error() string {
	return fmt.Sprintf("target shim PID %d is an ancestor of caller PID %d (observed-self-target)", e.TargetPID, e.CallerPID)
}

// AncestryUndeterminedError reports that one fail-closed snapshot could not
// establish a complete caller chain. It must never be rendered as observed
// self-target.
type AncestryUndeterminedError struct {
	CallerPID int
	TargetPID int
	Cause     error
}

func (e *AncestryUndeterminedError) Error() string {
	return fmt.Sprintf("could not determine whether caller PID %d descends from target shim PID %d: %v (ancestry-undetermined)", e.CallerPID, e.TargetPID, e.Cause)
}

func (e *AncestryUndeterminedError) Unwrap() error { return e.Cause }

// AncestryObserver takes exactly one process snapshot for each Guard call.
type AncestryObserver struct{ runner tmuxx.Runner }

// NewAncestryObserver constructs the fail-closed snapshot guard.
func NewAncestryObserver(runner tmuxx.Runner) AncestryObserver {
	return AncestryObserver{runner: runner}
}

// Guard walks from caller toward PID 0 looking only for the connected peer's
// kernel-observed target PID.
func (o AncestryObserver) Guard(ctx context.Context, callerPID, targetPID int) error {
	if o.runner == nil {
		return ancestryUndetermined(callerPID, targetPID, errors.New("process runner is unavailable"))
	}
	payload, err := tmuxx.ParentPIDs(ctx, o.runner)
	if err != nil {
		return ancestryUndetermined(callerPID, targetPID, err)
	}
	parents, err := parseParentPIDs(payload)
	if err != nil {
		return ancestryUndetermined(callerPID, targetPID, err)
	}
	if _, ok := parents[callerPID]; !ok {
		return ancestryUndetermined(callerPID, targetPID, fmt.Errorf("caller PID %d disappeared from process snapshot", callerPID))
	}
	if _, ok := parents[targetPID]; !ok {
		return ancestryUndetermined(callerPID, targetPID, fmt.Errorf("target shim PID %d disappeared from process snapshot", targetPID))
	}

	seen := make(map[int]bool)
	for current := callerPID; current != 0; {
		if current == targetPID {
			return &ObservedSelfTargetError{CallerPID: callerPID, TargetPID: targetPID}
		}
		if seen[current] {
			return ancestryUndetermined(callerPID, targetPID, fmt.Errorf("process ancestry loops at PID %d", current))
		}
		seen[current] = true
		parent, ok := parents[current]
		if !ok {
			return ancestryUndetermined(callerPID, targetPID, fmt.Errorf("process ancestry is missing PID %d", current))
		}
		current = parent
	}
	return nil
}

func parseParentPIDs(payload []byte) (map[int]int, error) {
	parents := make(map[int]int)
	scanner := bufio.NewScanner(strings.NewReader(string(payload)))
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return nil, fmt.Errorf("malformed ps row %d", line)
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("malformed PID in ps row %d", line)
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil || parent < 0 {
			return nil, fmt.Errorf("malformed parent PID in ps row %d", line)
		}
		if _, duplicate := parents[pid]; duplicate {
			return nil, fmt.Errorf("duplicate PID %d in process snapshot", pid)
		}
		parents[pid] = parent
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read process snapshot: %w", err)
	}
	if len(parents) == 0 {
		return nil, errors.New("process snapshot was empty")
	}
	return parents, nil
}

func ancestryUndetermined(callerPID, targetPID int, cause error) error {
	return &AncestryUndeterminedError{CallerPID: callerPID, TargetPID: targetPID, Cause: cause}
}
