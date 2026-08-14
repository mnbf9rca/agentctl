//go:build darwin

package fleet

import (
	"context"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/shim"
)

func TestShimForegroundRunnerCreatesOrExtendsFleetOnlyAtOwnedReadinessBoundary(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name             string
		existing         *ShimFleetRecord
		role             config.RoleConfig
		wantEvents       []string
		wantRoster       []string
		wantPresentation Presentation
	}{
		{
			name:             "new fleet record precedes role ownership",
			role:             config.RoleConfig{Name: "planner", Harness: config.HarnessClaude},
			wantEvents:       []string{"read", "create", "server", "observe"},
			wantRoster:       []string{"planner"},
			wantPresentation: PresentationDetached,
		},
		{
			name: "new role extends only after ready observation",
			existing: &ShimFleetRecord{Version: 1, Session: "fleet", Directory: "/work", Presentation: PresentationTmux, Roster: []string{"planner"}, Roles: map[string]ShimFleetRoleRecord{
				"planner": {Harness: "claude"},
			}},
			role:             config.RoleConfig{Name: "coder", Harness: config.HarnessCodex, Model: "gpt-5.6-sol", Effort: "high"},
			wantEvents:       []string{"read", "inspect", "server", "observe", "extend"},
			wantRoster:       []string{"planner", "coder"},
			wantPresentation: PresentationTmux,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			events := &foregroundEvents{}
			records := &fakeForegroundRecords{events: events, existing: test.existing}
			server := &fakeForegroundServer{events: events, release: make(chan struct{})}
			lifecycle := fakeForegroundLifecycle{events: events}
			inspector := fakeForegroundInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}
			runner := NewShimForegroundRunner(server, lifecycle, records, inspector, ShimLaunchDependencies{
				LookPath:   func(name string) (string, error) { return "/bin/" + name, nil },
				Executable: func() (string, error) { return "/bin/agentctl", nil },
				Now:        time.Now, Sleep: func(time.Duration) {},
			})
			done := make(chan error, 1)
			go func() {
				done <- runner.Run(context.Background(), ShimForegroundRequest{
					Session: "fleet", Role: test.role, Directory: "/work",
					ServerRequest: shim.RunRequest{Session: "fleet", Role: test.role.Name, Harness: string(test.role.Harness)},
				})
			}()
			deadline := time.Now().Add(time.Second)
			for !events.contains("observe") {
				if time.Now().After(deadline) {
					t.Fatalf("foreground runner did not observe readiness; events=%#v", events.snapshot())
				}
				time.Sleep(time.Millisecond)
			}
			close(server.release)
			if err := <-done; err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			if got := events.snapshot(); !reflect.DeepEqual(got, test.wantEvents) {
				t.Fatalf("events = %#v, want %#v", got, test.wantEvents)
			}
			if got := records.current.Roster; !reflect.DeepEqual(got, test.wantRoster) {
				t.Fatalf("roster = %#v, want %#v", got, test.wantRoster)
			}
			if got := records.current.Presentation; got != test.wantPresentation {
				t.Fatalf("presentation = %q, want %q", got, test.wantPresentation)
			}
		})
	}
}

func TestShimForegroundRunnerStopsOwnedRoleWhenFleetExtensionConflicts(t *testing.T) {
	t.Parallel()

	events := &foregroundEvents{}
	existing := &ShimFleetRecord{Version: 1, Session: "fleet", Directory: "/work", Presentation: PresentationTmux, Roster: []string{"planner"}, Roles: map[string]ShimFleetRoleRecord{"planner": {Harness: "claude"}}}
	records := &fakeForegroundRecords{events: events, existing: existing, extendErr: &ShimFleetMutationConflictError{Session: "fleet", Cause: "changed"}}
	server := &fakeForegroundServer{events: events, release: make(chan struct{})}
	lifecycle := fakeForegroundLifecycle{events: events, stop: true, release: server.release}
	runner := NewShimForegroundRunner(server, lifecycle, records, fakeForegroundInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}, ShimLaunchDependencies{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil }, Executable: func() (string, error) { return "/bin/agentctl", nil },
		Now: time.Now, Sleep: func(time.Duration) {},
	})
	err := runner.Run(context.Background(), ShimForegroundRequest{Session: "fleet", Role: config.RoleConfig{Name: "coder", Harness: config.HarnessCodex}, Directory: "/work", ServerRequest: shim.RunRequest{Session: "fleet", Role: "coder", Harness: "codex"}})
	var conflict *ShimFleetMutationConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("Run() error = %T %v, want fleet mutation conflict", err, err)
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, []string{"read", "inspect", "server", "observe", "extend", "stop"}) {
		t.Fatalf("events = %#v, want owned stop after failed extension", got)
	}
}

func TestShimForegroundRunnerRetainsReadyRoleOnUncertainFleetCommit(t *testing.T) {
	t.Parallel()

	events := &foregroundEvents{}
	existing := &ShimFleetRecord{Version: 1, Session: "fleet", Directory: "/work", Presentation: PresentationTmux, Roster: []string{"planner"}, Roles: map[string]ShimFleetRoleRecord{"planner": {Harness: "claude"}}}
	records := &fakeForegroundRecords{events: events, existing: existing, extendErr: &shim.RecordCommitUncertainError{Err: errors.New("sync failed")}}
	server := &fakeForegroundServer{events: events, release: make(chan struct{})}
	lifecycle := fakeForegroundLifecycle{events: events, stop: true, release: server.release}
	runner := NewShimForegroundRunner(server, lifecycle, records, fakeForegroundInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}}, ShimLaunchDependencies{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil }, Executable: func() (string, error) { return "/bin/agentctl", nil },
		Now: time.Now, Sleep: func(time.Duration) {},
	})
	done := make(chan error, 1)
	go func() {
		done <- runner.Run(context.Background(), ShimForegroundRequest{Session: "fleet", Role: config.RoleConfig{Name: "coder", Harness: config.HarnessCodex}, Directory: "/work", ServerRequest: shim.RunRequest{Session: "fleet", Role: "coder", Harness: "codex"}})
	}()
	deadline := time.Now().Add(time.Second)
	for !events.contains("extend") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !events.contains("extend") {
		t.Fatalf("foreground runner did not reach uncertain commit; events=%#v", events.snapshot())
	}
	if events.contains("stop") {
		t.Fatalf("uncertain fleet commit stopped the retained role; events=%#v", events.snapshot())
	}
	close(server.release)
	err := <-done
	var uncertain *shim.RecordCommitUncertainError
	if !errors.As(err, &uncertain) {
		t.Fatalf("Run() error = %T %v, want retained uncertain commit", err, err)
	}
}

func TestShimForegroundRunnerRefusesDirectoryDisagreementBeforeRoleStartOrFleetMutation(t *testing.T) {
	t.Parallel()

	events := &foregroundEvents{}
	existing := &ShimFleetRecord{Version: 1, Session: "fleet", Directory: "/stored", Presentation: PresentationTmux, Roster: []string{"planner"}, Roles: map[string]ShimFleetRoleRecord{"planner": {Harness: "claude"}}}
	records := &fakeForegroundRecords{events: events, existing: existing}
	runner := NewShimForegroundRunner(&fakeForegroundServer{events: events, release: make(chan struct{})}, fakeForegroundLifecycle{events: events}, records, fakeForegroundInspector{events: events}, ShimLaunchDependencies{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil }, Executable: func() (string, error) { return "/bin/agentctl", nil },
	})
	err := runner.Run(context.Background(), ShimForegroundRequest{Session: "fleet", Role: config.RoleConfig{Name: "planner", Harness: config.HarnessClaude}, Directory: "/current"})
	var mismatch *ShimForegroundDirectoryMismatchError
	if !errors.As(err, &mismatch) || mismatch.Stored != "/stored" || mismatch.Current != "/current" {
		t.Fatalf("Run() error = %T %v, want both-side directory mismatch", err, err)
	}
	if got := events.snapshot(); !reflect.DeepEqual(got, []string{"read"}) {
		t.Fatalf("events = %#v, want refusal before inspect, server, or record mutation", got)
	}
}

func TestShimForegroundReadinessLetsServerOwnTheBoundedFailureOutcome(t *testing.T) {
	t.Parallel()

	want := errors.New("readiness-timeout with cleanup facts")
	done := make(chan error, 1)
	lifecycle := foregroundStartingThenServerError{done: done, err: want}
	nowCalls := 0
	runner := ShimForegroundRunner{
		lifecycle: lifecycle,
		launcher: NewShimLauncher(nil, lifecycle, nil, ShimLaunchDependencies{
			Now: func() time.Time {
				nowCalls++
				return time.Unix(int64(nowCalls*10), 0)
			},
			Sleep: func(time.Duration) {},
		}),
	}
	if err := runner.waitForegroundReady(context.Background(), "fleet", "planner", done); !errors.Is(err, want) {
		t.Fatalf("waitForegroundReady() error = %T %v, want server-owned failure", err, err)
	}
}

func TestShimForegroundRunnerPreservesFleetCleanupFailureSeparatelyFromLifecycleFailure(t *testing.T) {
	t.Parallel()

	events := &foregroundEvents{}
	lifecycleErr := &shim.LifecycleRunError{
		Outcome: shim.OutcomeReadinessTimeout, CleanupObservation: shim.ProcessAbsent,
	}
	removeErr := errors.New("fleet record removal failed")
	release := make(chan struct{})
	close(release)
	records := &fakeForegroundRecords{events: events, removeErr: removeErr}
	runner := NewShimForegroundRunner(
		&fakeForegroundServer{events: events, release: release, err: lifecycleErr},
		foregroundAlwaysStarting{}, records,
		fakeForegroundInspector{events: events, observation: ShimRoleObservation{Outcome: shim.OutcomeMissing}},
		ShimLaunchDependencies{
			LookPath:   func(name string) (string, error) { return "/bin/" + name, nil },
			Executable: func() (string, error) { return "/bin/agentctl", nil },
			Sleep:      time.Sleep,
		},
	)
	err := runner.Run(context.Background(), ShimForegroundRequest{
		Session: "fleet", Role: config.RoleConfig{Name: "planner", Harness: config.HarnessClaude}, Directory: "/work",
		ServerRequest: shim.RunRequest{Session: "fleet", Role: "planner", Harness: "claude"},
	})
	var rollback *ShimForegroundRollbackError
	if !errors.As(err, &rollback) || !errors.Is(rollback.Cause, lifecycleErr) || !errors.Is(rollback.FleetCleanupErr, removeErr) {
		t.Fatalf("Run() error = %T %v, want separately typed lifecycle and fleet cleanup failures", err, err)
	}
}

type foregroundEvents struct {
	mu     sync.Mutex
	values []string
}

func (e *foregroundEvents) add(value string) {
	e.mu.Lock()
	e.values = append(e.values, value)
	e.mu.Unlock()
}
func (e *foregroundEvents) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.values...)
}
func (e *foregroundEvents) contains(value string) bool {
	for _, candidate := range e.snapshot() {
		if candidate == value {
			return true
		}
	}
	return false
}

type fakeForegroundServer struct {
	events  *foregroundEvents
	release chan struct{}
	err     error
}

func (s *fakeForegroundServer) Run(context.Context, shim.RunRequest) error {
	s.events.add("server")
	<-s.release
	return s.err
}

type fakeForegroundLifecycle struct {
	events  *foregroundEvents
	stop    bool
	release chan struct{}
}

type foregroundStartingThenServerError struct {
	done chan<- error
	err  error
}

type foregroundAlwaysStarting struct{}

func (foregroundAlwaysStarting) Observe(context.Context, string, string) (shim.Response, error) {
	return shim.Response{Version: 1, Outcome: shim.OutcomeStarting}, nil
}

func (foregroundAlwaysStarting) Stop(context.Context, string, string) (shim.Response, error) {
	return shim.Response{}, errors.New("unexpected stop")
}

func (l foregroundStartingThenServerError) Observe(context.Context, string, string) (shim.Response, error) {
	l.done <- l.err
	return shim.Response{Version: 1, Outcome: shim.OutcomeStarting}, nil
}

func (l foregroundStartingThenServerError) Stop(context.Context, string, string) (shim.Response, error) {
	return shim.Response{}, errors.New("unexpected stop")
}

func (l fakeForegroundLifecycle) Observe(context.Context, string, string) (shim.Response, error) {
	deadline := time.Now().Add(time.Second)
	for !l.events.contains("server") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	l.events.add("observe")
	pid := 1
	return shim.Response{Version: 1, Outcome: shim.OutcomeRunning, ShimPID: &pid}, nil
}
func (l fakeForegroundLifecycle) Stop(context.Context, string, string) (shim.Response, error) {
	l.events.add("stop")
	if l.release != nil {
		close(l.release)
	}
	attempted, exited := true, true
	signal := "SIGHUP"
	return shim.Response{Version: 1, Outcome: shim.OutcomeStopChildExited, SignalAttempted: &attempted, Signal: &signal, ChildExitObserved: &exited}, nil
}

type fakeForegroundInspector struct {
	events      *foregroundEvents
	observation ShimRoleObservation
}

func (i fakeForegroundInspector) Inspect(context.Context, string, string) (ShimRoleObservation, error) {
	i.events.add("inspect")
	return i.observation, nil
}
func (i fakeForegroundInspector) RemoveStale(context.Context, string, string, int) (shim.ProcessResult, error) {
	return shim.ProcessResult{Observation: shim.ProcessAbsent}, nil
}

type fakeForegroundRecords struct {
	events    *foregroundEvents
	existing  *ShimFleetRecord
	current   ShimFleetRecord
	extendErr error
	removeErr error
}

func (r *fakeForegroundRecords) Read(string) (ShimFleetRecord, error) {
	r.events.add("read")
	if r.existing == nil {
		return ShimFleetRecord{}, os.ErrNotExist
	}
	return *r.existing, nil
}
func (r *fakeForegroundRecords) Create(record ShimFleetRecord) error {
	r.events.add("create")
	r.current = record
	return nil
}
func (r *fakeForegroundRecords) ReplaceOwned(ShimFleetRecord, ShimFleetRecord) error { return nil }
func (r *fakeForegroundRecords) RemoveOwned(ShimFleetRecord) error {
	r.events.add("remove")
	return r.removeErr
}
func (r *fakeForegroundRecords) ExtendOwned(_ ShimFleetRecord, replacement ShimFleetRecord) error {
	r.events.add("extend")
	if r.extendErr != nil {
		return r.extendErr
	}
	r.current = replacement
	return nil
}
