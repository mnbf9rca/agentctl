//go:build darwin

package shim

import (
	"context"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/harness"
	"github.com/mnbf9rca/agentctl/internal/ptyx"
)

func TestShimLifecycleOrdersClaimReservationChildIdentityListenerAndReadiness(t *testing.T) {
	calls := &lifecycleCallLog{}
	child := newLifecycleFakeChild(t, 456)
	terminal := &lifecycleFakeTerminal{calls: calls, ready: observedSettledState(t)}
	relay := &lifecycleFakeRelay{calls: calls}
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			calls.add("role-path:" + session + ":" + role)
			return &RolePath{Session: session, Role: role, StateRoot: "/state", Socket: "/runtime/fleet/planner.sock"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(_ *RolePath, advisory Advisory) (claimHandle, error) {
			calls.add("claim")
			if advisory.ShimPID != 123 || advisory.Nonce != "nonce" || advisory.StateRoot != "/state" {
				t.Fatalf("advisory = %#v", advisory)
			}
			return &lifecycleFakeClaim{calls: calls}, nil
		},
		writeRecord: func(_ *RolePath, record Record) error {
			calls.add("record:" + string(record.State))
			return nil
		},
		startToken: func(pid int) (StartToken, error) {
			calls.add("start-token")
			if pid != 456 {
				t.Fatalf("start-token PID = %d", pid)
			}
			return StartToken{Sec: 9, Usec: 10}, nil
		},
		starter: lifecycleStarterFunc(func(_ context.Context, request ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("child-start")
			want := []string{"amq", "coop", "exec", "--session", "fleet", "--me", "planner", "codex", "--", "--model", "gpt-5.6-sol", "--config", `model_reasoning_effort="high"`}
			if !reflect.DeepEqual(request.Argv, want) {
				t.Fatalf("child argv = %#v, want %#v", request.Argv, want)
			}
			return child, nil
		}),
		listen: func(path string) (roleListener, error) {
			calls.add("listen:" + path)
			return &lifecycleFakeListener{}, nil
		},
		newMasterEndpoint: func(*os.File) (ptyx.ContextReadWriter, func() error, error) {
			calls.add("master-endpoint")
			return &lifecycleFakeEndpoint{}, func() error { return nil }, nil
		},
		newRelay: func(ptyx.ContextReader, ptyx.ContextWriter, ptyx.ContextReadWriter) lifecycleRelay {
			calls.add("relay")
			return relay
		},
		terminal: terminal,
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	runtime, err := lifecycle.start(context.Background(), RunRequest{
		Session: "fleet", Role: "planner", Harness: "codex",
		HarnessOptions: harness.Options{Model: "gpt-5.6-sol", Effort: "high"},
		Environment:    []string{"A=B"}, InitialSize: ptyx.WindowSize{Rows: 24, Cols: 80},
		OperatorInput: &lifecycleFakeEndpoint{}, OperatorOutput: &lifecycleFakeEndpoint{},
		OuterTerminal: child.Master(), OuterState: ptyx.TerminalState{},
	}, spec)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if err := runtime.waitReady(context.Background()); err != nil {
		t.Fatalf("waitReady() error = %v", err)
	}

	wantCalls := []string{
		"role-path:fleet:planner", "claim", "record:child-starting", "child-start", "start-token",
		"record:child-recorded", "listen:/runtime/fleet/planner.sock", "master-endpoint", "relay",
		"wait-ready", "outer-set-termios", "mark-ready",
	}
	if got := calls.snapshot(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, wantCalls)
	}
}

func TestShimDetachedLifecycleStartsBothListenersBeforeResidentDrain(t *testing.T) {
	calls := &lifecycleCallLog{}
	child := newLifecycleFakeChild(t, 456)
	resident := &lifecycleFakeResidentRelay{calls: calls}
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			return &RolePath{Session: session, Role: role, StateRoot: "/state", Socket: "/runtime/fleet/planner.sock", Attach: "/runtime/fleet/planner.attach"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("claim")
			return &lifecycleFakeClaim{calls: calls}, nil
		},
		writeRecord: func(_ *RolePath, record Record) error { calls.add("record:" + string(record.State)); return nil },
		startToken:  func(int) (StartToken, error) { return StartToken{Sec: 9, Usec: 10}, nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("child-start")
			return child, nil
		}),
		listen: func(path string) (roleListener, error) {
			calls.add("listen-control:" + path)
			return &lifecycleFakeListener{}, nil
		},
		listenAttach: func(path string) (roleListener, error) {
			calls.add("listen-attach:" + path)
			return &lifecycleFakeListener{}, nil
		},
		newMasterEndpoint: func(*os.File) (ptyx.ContextReadWriter, func() error, error) {
			calls.add("master-endpoint")
			return &lifecycleFakeEndpoint{}, func() error { return nil }, nil
		},
		newResidentRelay: func(*os.File, ptyx.ContextWriter) (lifecycleResidentRelay, func() error, error) {
			calls.add("resident-relay")
			return resident, func() error { return nil }, nil
		},
		terminal: &lifecycleFakeTerminal{calls: calls},
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")
	runtime, err := lifecycle.start(context.Background(), RunRequest{
		Session: "fleet", Role: "planner", Harness: "codex", OperatorMode: OperatorDetached,
		InitialSize: ptyx.WindowSize{Rows: 24, Cols: 80},
	}, spec)
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if runtime.attachListener == nil || runtime.resident != resident {
		t.Fatalf("detached runtime = %#v, want attach listener and resident relay", runtime)
	}
	want := []string{
		"claim", "record:child-starting", "child-start", "record:child-recorded",
		"listen-control:/runtime/fleet/planner.sock", "listen-attach:/runtime/fleet/planner.attach",
		"master-endpoint", "resident-relay",
	}
	if got := calls.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("detached lifecycle calls = %#v, want %#v", got, want)
	}
}

func TestShimLifecycleFailsClosedOnUncertainReservationCommitBeforeFork(t *testing.T) {
	calls := &lifecycleCallLog{}
	claim := &lifecycleFakeClaim{calls: calls}
	uncertain := &RecordCommitUncertainError{Err: errors.New("directory sync failed")}
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			return &RolePath{Session: session, Role: role, StateRoot: "/state"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("claim")
			return claim, nil
		},
		writeRecord: func(*RolePath, Record) error {
			calls.add("record:child-starting")
			return uncertain
		},
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("BUG:child-start")
			return nil, nil
		}),
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	_, err := lifecycle.start(context.Background(), RunRequest{Session: "fleet", Role: "planner", Harness: "codex"}, spec)
	var got *LifecycleCommitUncertainError
	if !errors.As(err, &got) || !errors.Is(err, uncertain) {
		t.Fatalf("start() error = %T %v, want LifecycleCommitUncertainError wrapping RecordCommitUncertainError", err, err)
	}
	wantCalls := []string{"claim", "record:child-starting", "claim-close"}
	if got := calls.snapshot(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, wantCalls)
	}
}

func TestShimLifecycleCleansClaimAfterDefiniteReservationWriteFailure(t *testing.T) {
	calls := &lifecycleCallLog{}
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			return &RolePath{Session: session, Role: role, StateRoot: "/state"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("claim")
			return &lifecycleFakeClaim{calls: calls}, nil
		},
		writeRecord: func(*RolePath, Record) error {
			calls.add("record:child-starting")
			return errors.New("write failed before rename")
		},
		removeRecord: func(*RolePath) error { calls.add("record-remove"); return nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("BUG:child-start")
			return nil, nil
		}),
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	_, err := lifecycle.start(context.Background(), RunRequest{Session: "fleet", Role: "planner", Harness: "codex"}, spec)
	if err == nil || !strings.Contains(err.Error(), "write failed before rename") {
		t.Fatalf("start() error = %v, want definite record failure", err)
	}
	wantCalls := []string{"claim", "record:child-starting", "claim-runtime-clean", "record-remove", "claim-clean"}
	if got := calls.snapshot(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, wantCalls)
	}
}

func TestShimLifecycleRetainsUncertainChildRecordEvenWhenCleanupObservesESRCH(t *testing.T) {
	calls := &lifecycleCallLog{}
	child := newLifecycleFakeChild(t, 456)
	child.calls = calls
	writes := 0
	uncertain := &RecordCommitUncertainError{Err: errors.New("directory sync failed")}
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			return &RolePath{Session: session, Role: role, StateRoot: "/state"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("claim")
			return &lifecycleFakeClaim{calls: calls}, nil
		},
		writeRecord: func(*RolePath, Record) error {
			writes++
			calls.add("record-write")
			if writes == 2 {
				return uncertain
			}
			return nil
		},
		removeRecord: func(*RolePath) error { calls.add("BUG:record-remove"); return nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("child-start")
			return child, nil
		}),
		startToken: func(int) (StartToken, error) {
			calls.add("start-token")
			return StartToken{Sec: 9, Usec: 10}, nil
		},
		observeProcess: func(int, StartToken) ProcessResult {
			calls.add("observe:absent")
			return ProcessResult{Observation: ProcessAbsent}
		},
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	_, err := lifecycle.start(context.Background(), RunRequest{Session: "fleet", Role: "planner", Harness: "codex"}, spec)
	var got *LifecycleCommitUncertainError
	if !errors.As(err, &got) || got.Phase != "child-recorded" || !errors.Is(err, uncertain) {
		t.Fatalf("start() error = %T %v, want child-recorded LifecycleCommitUncertainError", err, err)
	}
	wantCalls := []string{
		"claim", "record-write", "child-start", "start-token", "record-write",
		"child-signal", "observe:absent", "child-master-close", "claim-close",
	}
	if got := calls.snapshot(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, wantCalls)
	}
}

func TestShimLifecycleRefusesExistingDurableRecordBeforeClaim(t *testing.T) {
	calls := &lifecycleCallLog{}
	existing := NewChildStartingRecord("fleet", "planner", 99, "existing")
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			calls.add("role-path")
			return &RolePath{Session: session, Role: role, StateRoot: "/state"}, nil
		},
		readRecord: func(*RolePath) (Record, error) {
			calls.add("record-read")
			return existing, nil
		},
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("BUG:claim")
			return nil, nil
		},
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	_, err := lifecycle.start(context.Background(), RunRequest{Session: "fleet", Role: "planner", Harness: "codex"}, spec)
	var blocked *ExistingRoleRecordError
	if !errors.As(err, &blocked) || blocked.State != RecordStateChildStarting {
		t.Fatalf("start() error = %T %v, want ExistingRoleRecordError(child-starting)", err, err)
	}
	if got, want := calls.snapshot(), []string{"role-path", "record-read"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, want)
	}
}

func TestShimLifecycleRemovesReservationWhenChildStartProvesNoChild(t *testing.T) {
	calls := &lifecycleCallLog{}
	claim := &lifecycleFakeClaim{calls: calls}
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			return &RolePath{Session: session, Role: role, StateRoot: "/state"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("claim")
			return claim, nil
		},
		writeRecord:  func(*RolePath, Record) error { calls.add("record:child-starting"); return nil },
		removeRecord: func(*RolePath) error { calls.add("record-remove"); return nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("child-start")
			return nil, errors.New("fork failed")
		}),
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	_, err := lifecycle.start(context.Background(), RunRequest{Session: "fleet", Role: "planner", Harness: "codex"}, spec)
	if err == nil || !strings.Contains(err.Error(), "fork failed") {
		t.Fatalf("start() error = %v, want fork failure", err)
	}
	wantCalls := []string{"claim", "record:child-starting", "child-start", "claim-runtime-clean", "record-remove", "claim-clean"}
	if got := calls.snapshot(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, wantCalls)
	}
}

func TestShimLifecycleRetainsRecordWhenStartedChildCannotBeObservedAbsent(t *testing.T) {
	calls := &lifecycleCallLog{}
	claim := &lifecycleFakeClaim{calls: calls}
	child := newLifecycleFakeChild(t, 456)
	child.calls = calls
	deps := lifecycleDependencies{
		rolePath: func(session, role string) (*RolePath, error) {
			return &RolePath{Session: session, Role: role, StateRoot: "/state"}, nil
		},
		pid:   func() int { return 123 },
		nonce: func() (string, error) { return "nonce", nil },
		acquireClaim: func(*RolePath, Advisory) (claimHandle, error) {
			calls.add("claim")
			return claim, nil
		},
		writeRecord: func(_ *RolePath, record Record) error {
			calls.add("record:" + string(record.State))
			if record.State == RecordStateCleanupFailed {
				want := &CleanupFailure{
					Cause:       "token failed",
					Observation: CleanupObservationCouldNotObserve,
					Remaining:   []string{"child", "socket", "record", "lock"},
				}
				if record.ChildPID != 456 || record.ChildStartToken != nil || !reflect.DeepEqual(record.Cleanup, want) {
					t.Fatalf("cleanup-failed record = %#v, want child and exact retained facts %#v", record, want)
				}
			}
			return nil
		},
		removeRecord: func(*RolePath) error { calls.add("BUG:record-remove"); return nil },
		starter: lifecycleStarterFunc(func(context.Context, ptyx.StartRequest) (ptyx.Child, error) {
			calls.add("child-start")
			return child, nil
		}),
		startToken: func(int) (StartToken, error) {
			calls.add("start-token")
			return StartToken{}, errors.New("token failed")
		},
		observeProcess: func(int, StartToken) ProcessResult {
			t.Fatal("token comparison called after start-token observation failed")
			return ProcessResult{}
		},
		observePresence: func(int) ProcessResult {
			calls.add("observe:present-without-token")
			return ProcessResult{Observation: ProcessCouldNotObserve, Err: errors.New("start token unavailable")}
		},
		observeRemaining: func(*RolePath) ([]string, error) {
			calls.add("observe-remaining")
			return []string{"socket", "record", "lock"}, nil
		},
	}
	lifecycle := roleLifecycle{deps: deps}
	spec, _ := harness.Lookup("codex")

	_, err := lifecycle.start(context.Background(), RunRequest{Session: "fleet", Role: "planner", Harness: "codex"}, spec)
	var retained *LifecycleOwnershipRetainedError
	if !errors.As(err, &retained) || retained.Observation != ProcessCouldNotObserve {
		t.Fatalf("start() error = %T %v, want LifecycleOwnershipRetainedError(could-not-observe)", err, err)
	}
	wantCalls := []string{
		"claim", "record:child-starting", "child-start", "start-token", "child-signal", "observe:present-without-token", "child-master-close",
		"observe-remaining", "record:cleanup-failed", "claim-close",
	}
	if got := calls.snapshot(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("lifecycle calls = %#v, want %#v", got, wantCalls)
	}
}

type lifecycleCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *lifecycleCallLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *lifecycleCallLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type lifecycleFakeClaim struct{ calls *lifecycleCallLog }

func (c *lifecycleFakeClaim) Close() error          { c.calls.add("claim-close"); return nil }
func (c *lifecycleFakeClaim) CloseAndRemove() error { c.calls.add("claim-clean"); return nil }
func (c *lifecycleFakeClaim) RemoveRuntimeArtifacts() error {
	c.calls.add("claim-runtime-clean")
	return nil
}
func (c *lifecycleFakeClaim) CloseAndRemoveLock() error { c.calls.add("claim-clean"); return nil }

type lifecycleStarterFunc func(context.Context, ptyx.StartRequest) (ptyx.Child, error)

func (f lifecycleStarterFunc) Start(ctx context.Context, request ptyx.StartRequest) (ptyx.Child, error) {
	return f(ctx, request)
}

type lifecycleFakeListener struct{}

func (*lifecycleFakeListener) Accept() (net.Conn, error) { return nil, net.ErrClosed }
func (*lifecycleFakeListener) Close() error              { return nil }

type lifecycleFakeEndpoint struct{}

func (*lifecycleFakeEndpoint) Read(context.Context, []byte) (int, error) { return 0, context.Canceled }
func (*lifecycleFakeEndpoint) Write(_ context.Context, value []byte) (int, error) {
	return len(value), nil
}

type lifecycleFakeRelay struct{ calls *lifecycleCallLog }

func (r *lifecycleFakeRelay) Run(context.Context) error { return nil }
func (r *lifecycleFakeRelay) Writer() operationWriter   { return &lifecycleFakeEndpoint{} }
func (r *lifecycleFakeRelay) MarkReady(ptyx.TerminalState) error {
	r.calls.add("mark-ready")
	return nil
}

type lifecycleFakeResidentRelay struct{ calls *lifecycleCallLog }

func (r *lifecycleFakeResidentRelay) Run(context.Context) error { return nil }
func (r *lifecycleFakeResidentRelay) Writer() operationWriter   { return &lifecycleFakeEndpoint{} }
func (r *lifecycleFakeResidentRelay) MarkReady(ptyx.TerminalState) error {
	r.calls.add("resident-ready")
	return nil
}
func (*lifecycleFakeResidentRelay) AdmitViewer(ptyx.ContextWriter) (*ptyx.ResidentViewer, error) {
	return nil, nil
}
func (*lifecycleFakeResidentRelay) Flush(context.Context) ptyx.ResidentFlushResult {
	return ptyx.ResidentFlushResult{Confirmed: true}
}

type lifecycleFakeTerminal struct {
	calls *lifecycleCallLog
	ready ptyx.TerminalState
}

func (t *lifecycleFakeTerminal) Observe(*os.File) (ptyx.TerminalState, error) {
	return ptyx.TerminalState{}, nil
}
func (t *lifecycleFakeTerminal) WaitReady(context.Context, *os.File) (ptyx.TerminalState, error) {
	t.calls.add("wait-ready")
	return t.ready, nil
}
func (*lifecycleFakeTerminal) ForwardWindowSize(*os.File, *os.File) error { return nil }
func (*lifecycleFakeTerminal) ForwardTermios(*os.File, *os.File) error    { return nil }
func (t *lifecycleFakeTerminal) SetTermios(*os.File, ptyx.TerminalState) error {
	t.calls.add("outer-set-termios")
	return nil
}
func (*lifecycleFakeTerminal) SetWindowSize(*os.File, ptyx.WindowSize) error { return nil }

func observedSettledState(t *testing.T) ptyx.TerminalState {
	t.Helper()
	child := newLifecycleFakeChild(t, 999)
	state, err := ptyx.NewTerminal().Observe(child.Master())
	if err == nil && state.Settled() {
		return state
	}
	// A real settled state is supplied by the fake terminal and consumed only
	// by the fake relay; its concrete private fields remain owned by ptyx.
	return state
}

type lifecycleFakeChild struct {
	pid    int
	master *os.File
	calls  *lifecycleCallLog
}

func newLifecycleFakeChild(t *testing.T, pid int) *lifecycleFakeChild {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close() })
	return &lifecycleFakeChild{pid: pid, master: reader}
}

func (c *lifecycleFakeChild) PID() int         { return c.pid }
func (c *lifecycleFakeChild) Master() *os.File { return c.master }
func (c *lifecycleFakeChild) Wait(context.Context) (ptyx.ExitObservation, error) {
	return ptyx.ExitObservation{}, context.Canceled
}
func (c *lifecycleFakeChild) SignalProcessGroup(os.Signal) ptyx.SignalObservation {
	if c.calls != nil {
		c.calls.add("child-signal")
	}
	return ptyx.SignalObservation{}
}
func (c *lifecycleFakeChild) Terminate(context.Context, os.Signal) (ptyx.TerminationObservation, error) {
	return ptyx.TerminationObservation{}, nil
}
func (c *lifecycleFakeChild) CloseMaster() error {
	if c.calls != nil {
		c.calls.add("child-master-close")
	}
	return nil
}
