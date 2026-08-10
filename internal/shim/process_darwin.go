//go:build darwin

package shim

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

// ErrShortKinfoProc identifies a raw sysctl result too short to contain the
// complete Darwin kinfo_proc structure.
var ErrShortKinfoProc = errors.New("sysctl returned a short kinfo_proc")

// ProcessObservation is the closed factual result of the presence/token gate.
type ProcessObservation string

const (
	ProcessAbsent                   ProcessObservation = "absent"
	ProcessPresentMatch             ProcessObservation = "present-match"
	ProcessPresentTokenDisagreement ProcessObservation = "present-token-disagreement"
	ProcessPresentNotOurs           ProcessObservation = "present-not-ours"
	ProcessCouldNotObserve          ProcessObservation = "could-not-observe"
)

// ProcessResult preserves the observation, raw token when available, and the
// syscall cause when observation failed.
type ProcessResult struct {
	Observation   ProcessObservation
	ObservedToken *StartToken
	Err           error
}

// MayReportAbsent is true only for a directly observed kill(pid, 0) ESRCH.
func (r ProcessResult) MayReportAbsent() bool { return r.Observation == ProcessAbsent }

// MayAuthorizeRelaunch is deliberately identical to MayReportAbsent.
func (r ProcessResult) MayAuthorizeRelaunch() bool { return r.Observation == ProcessAbsent }

// ObserveProcess applies the sole absence oracle and, only after observed
// presence, compares the raw Darwin process start token.
func ObserveProcess(pid int, recorded StartToken) ProcessResult {
	return observeProcess(pid, recorded, unix.Kill, ReadStartToken)
}

func observeProcess(
	pid int,
	recorded StartToken,
	killZero func(int, syscall.Signal) error,
	readToken func(int) (StartToken, error),
) ProcessResult {
	err := killZero(pid, 0)
	switch {
	case errors.Is(err, unix.ESRCH):
		return ProcessResult{Observation: ProcessAbsent}
	case errors.Is(err, unix.EPERM):
		return ProcessResult{Observation: ProcessPresentNotOurs, Err: err}
	case err != nil:
		return ProcessResult{Observation: ProcessCouldNotObserve, Err: err}
	}

	observed, err := readToken(pid)
	if err != nil {
		return ProcessResult{Observation: ProcessCouldNotObserve, Err: err}
	}
	result := ProcessResult{Observation: ProcessPresentMatch, ObservedToken: &observed}
	if !recorded.Equal(observed) {
		result.Observation = ProcessPresentTokenDisagreement
	}
	return result
}

// ReadStartToken reads raw kinfo_proc.p_starttime through
// sysctl(KERN_PROC_PID). It performs no formatting or time conversion.
func ReadStartToken(pid int) (StartToken, error) {
	if pid <= 0 {
		return StartToken{}, fmt.Errorf("PID must be positive")
	}
	process, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return StartToken{}, err
	}
	if process == nil {
		return StartToken{}, ErrShortKinfoProc
	}
	if got := int(process.Proc.P_pid); got != pid {
		return StartToken{}, fmt.Errorf("kinfo_proc PID is %d; expected %d", got, pid)
	}
	timeval := process.Proc.P_starttime
	token := StartToken{Sec: timeval.Sec, Usec: int64(timeval.Usec)}
	if token.Sec <= 0 || token.Usec < 0 || token.Usec >= 1_000_000 {
		return StartToken{}, fmt.Errorf("kinfo_proc p_starttime is malformed: sec=%d usec=%d", token.Sec, token.Usec)
	}
	return token, nil
}
