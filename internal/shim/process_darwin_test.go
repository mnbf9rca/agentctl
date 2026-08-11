//go:build darwin

package shim

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestProcessObservationUsesESRCHAsTheSoleAbsencePermission(t *testing.T) {
	recorded := StartToken{Sec: 10, Usec: 20}
	tests := []struct {
		name        string
		killErr     error
		observed    StartToken
		tokenErr    error
		want        ProcessObservation
		mayRelaunch bool
	}{
		{name: "ESRCH", killErr: unix.ESRCH, want: ProcessAbsent, mayRelaunch: true},
		{name: "matching token", observed: recorded, want: ProcessPresentMatch},
		{name: "different seconds", observed: StartToken{Sec: 11, Usec: 20}, want: ProcessPresentTokenDisagreement},
		{name: "different microseconds", observed: StartToken{Sec: 10, Usec: 21}, want: ProcessPresentTokenDisagreement},
		{name: "sysctl failure", tokenErr: errors.New("sysctl failed"), want: ProcessCouldNotObserve},
		{name: "short kinfo proc", tokenErr: ErrShortKinfoProc, want: ProcessCouldNotObserve},
		{name: "EPERM", killErr: unix.EPERM, want: ProcessPresentNotOurs},
		{name: "other kill error", killErr: unix.EINVAL, want: ProcessCouldNotObserve},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tokenReads := 0
			result := observeProcess(123, recorded,
				func(pid int, signal syscall.Signal) error {
					if pid != 123 || signal != 0 {
						t.Fatalf("kill inputs = (%d, %d), want (123, 0)", pid, signal)
					}
					return tt.killErr
				},
				func(pid int) (StartToken, error) {
					tokenReads++
					return tt.observed, tt.tokenErr
				},
			)
			if result.Observation != tt.want {
				t.Fatalf("Observation = %q, want %q (error %v)", result.Observation, tt.want, result.Err)
			}
			if result.MayReportAbsent() != tt.mayRelaunch || result.MayAuthorizeRelaunch() != tt.mayRelaunch {
				t.Fatalf("absence permissions = (%v, %v), want %v", result.MayReportAbsent(), result.MayAuthorizeRelaunch(), tt.mayRelaunch)
			}
			wantTokenReads := 0
			if tt.killErr == nil {
				wantTokenReads = 1
			}
			if tokenReads != wantTokenReads {
				t.Fatalf("token reads = %d, want %d", tokenReads, wantTokenReads)
			}
		})
	}
}

func TestProcessObservationRejectsNonPositivePIDBeforeKill(t *testing.T) {
	for _, pid := range []int{0, -1} {
		killCalls := 0
		result := observeProcess(pid, StartToken{Sec: 1}, func(int, syscall.Signal) error {
			killCalls++
			return unix.ESRCH
		}, func(int) (StartToken, error) {
			t.Fatal("token reader called for invalid PID")
			return StartToken{}, nil
		})
		if result.Observation != ProcessCouldNotObserve || result.Err == nil {
			t.Fatalf("observeProcess(%d) = %#v, want could-not-observe with cause", pid, result)
		}
		if result.MayReportAbsent() || result.MayAuthorizeRelaunch() {
			t.Fatalf("observeProcess(%d) authorized absence: %#v", pid, result)
		}
		if killCalls != 0 {
			t.Fatalf("observeProcess(%d) called kill %d times", pid, killCalls)
		}
	}
}

func TestProcessPresenceWithoutRecordedTokenNeverClaimsMatchOrDisagreement(t *testing.T) {
	tests := []struct {
		name    string
		killErr error
		want    ProcessObservation
	}{
		{name: "present but identity unavailable", want: ProcessCouldNotObserve},
		{name: "absent", killErr: unix.ESRCH, want: ProcessAbsent},
		{name: "present not ours", killErr: unix.EPERM, want: ProcessPresentNotOurs},
		{name: "other failure", killErr: unix.EINVAL, want: ProcessCouldNotObserve},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := observeProcessPresence(123, func(pid int, signal syscall.Signal) error {
				if pid != 123 || signal != 0 {
					t.Fatalf("kill inputs = (%d, %d), want (123, 0)", pid, signal)
				}
				return test.killErr
			})
			if got.Observation != test.want {
				t.Fatalf("observeProcessPresence() = %#v, want %q", got, test.want)
			}
			if got.Observation == ProcessPresentMatch || got.Observation == ProcessPresentTokenDisagreement || got.ObservedToken != nil {
				t.Fatalf("tokenless presence invented identity comparison: %#v", got)
			}
		})
	}
}

func TestProcessObservationRejectsPIDOutsideDarwinPIDTBeforeSyscalls(t *testing.T) {
	pid := int(math.MaxInt32) + 1
	killCalls := 0
	result := observeProcess(pid, StartToken{Sec: 1}, func(int, syscall.Signal) error {
		killCalls++
		return unix.ESRCH
	}, func(int) (StartToken, error) {
		t.Fatal("token reader called for oversized PID")
		return StartToken{}, nil
	})
	if result.Observation != ProcessCouldNotObserve || result.Err == nil || result.MayAuthorizeRelaunch() {
		t.Fatalf("observeProcess(%d) = %#v, want refusing could-not-observe", pid, result)
	}
	if killCalls != 0 {
		t.Fatalf("oversized PID reached kill %d times", killCalls)
	}
	if _, err := ReadStartToken(pid); err == nil {
		t.Fatal("ReadStartToken accepted PID above signed Darwin pid_t")
	}
}

// TestReadStartTokenObservesRawDarwinKinfoProc is a live kernel probe for
// sysctl(KERN_PROC_PID)'s unformatted p_starttime timeval.
func TestReadStartTokenObservesRawDarwinKinfoProc(t *testing.T) {
	first, err := ReadStartToken(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first.Sec <= 0 || first.Usec < 0 || first.Usec >= 1_000_000 {
		t.Fatalf("raw start token = %#v, want a valid timeval", first)
	}
	t.Setenv("TZ", "Pacific/Kiritimati")
	t.Setenv("LC_ALL", "C")
	second, err := ReadStartToken(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("raw token changed with TZ/LC_ALL: first=%#v second=%#v", first, second)
	}
}

// TestProcessObservationLivePresentMatch probes the complete Darwin nil-kill
// then raw-token path against this test process.
func TestProcessObservationLivePresentMatch(t *testing.T) {
	token, err := ReadStartToken(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	result := ObserveProcess(os.Getpid(), token)
	if result.Observation != ProcessPresentMatch || result.Err != nil {
		t.Fatalf("ObserveProcess(self) = %#v, want present-match", result)
	}
}

// TestProcessObservationLiveForeignProcessAndReapedAbsence probes both the
// nil-kill foreign kinfo_proc path and the post-reap ESRCH absence permission.
func TestProcessObservationLiveForeignProcessAndReapedAbsence(t *testing.T) {
	command := exec.Command("/bin/sleep", "5")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	token, err := ReadStartToken(pid)
	if err != nil {
		t.Fatal(err)
	}
	if got := ObserveProcess(pid, token); got.Observation != ProcessPresentMatch {
		t.Fatalf("live foreign observation = %#v, want present-match", got)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed child exited successfully")
	}
	if got := ObserveProcess(pid, token); got.Observation != ProcessAbsent || !got.MayAuthorizeRelaunch() {
		t.Fatalf("reaped foreign observation = %#v, want relaunchable absent", got)
	}
}
