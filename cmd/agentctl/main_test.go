package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/buildinfo"
	"github.com/mnbf9rca/agentctl/internal/fleet"
	"github.com/mnbf9rca/agentctl/internal/kill"
	"github.com/mnbf9rca/agentctl/internal/session"
	"github.com/mnbf9rca/agentctl/internal/shim"
	statuspkg "github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

func TestRunVersionAndHelpDoNotConstructRuntime(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{{"version"}, {"--version"}, {"--help"}} {
		runner := tmuxx.NewFakeRunner()
		var stdout, stderr bytes.Buffer
		if code := runWithRunner(context.Background(), arguments, &stdout, &stderr, runner, lookupValues(nil)); code != exitOK {
			t.Fatalf("arguments=%q code=%d stderr=%q", arguments, code, stderr.String())
		}
		if len(runner.Calls) != 0 {
			t.Fatalf("arguments=%q calls=%#v, want none", arguments, runner.Calls)
		}
	}
	if !strings.Contains(globalUsage, "run       run one foreground role without tmux") || strings.Contains(globalUsage, "__shim") {
		t.Fatalf("globalUsage does not expose exactly the public run surface: %q", globalUsage)
	}
}

func TestHelpDescribesRuntimeCutoverWithoutLegacyWindowRecovery(t *testing.T) {
	t.Parallel()

	for _, command := range []string{"relaunch", "status"} {
		var stdout, stderr bytes.Buffer
		code := runWithRunner(context.Background(), []string{command, "--help"}, &stdout, &stderr, tmuxx.NewFakeRunner(), lookupValues(nil))
		if code != exitOK || stderr.Len() != 0 {
			t.Fatalf("%s --help code=%d stderr=%q", command, code, stderr.String())
		}
		if strings.Contains(stdout.String(), "no-baseline") || strings.Contains(stdout.String(), "every session") || strings.Contains(stdout.String(), "Exited agents normally") {
			t.Fatalf("%s --help retains pre-cutover text: %q", command, stdout.String())
		}
	}
}

func TestRunRejectsMalformedPublicCommandBeforeRuntimeConstruction(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{
		{"launch", "--session", "Invalid", "--roles", "planner:claude"},
		{"run", "--session", "fleet", "--role", "planner", "--harness", "unknown"},
		{"clear", "--session", "fleet", "INVALID"},
		{"status", "--json", "--json"},
	} {
		runner := tmuxx.NewFakeRunner()
		var stdout, stderr bytes.Buffer
		if code := runWithRunner(context.Background(), arguments, &stdout, &stderr, runner, lookupValues(nil)); code != exitUsage {
			t.Fatalf("arguments=%q code=%d stderr=%q", arguments, code, stderr.String())
		}
		if len(runner.Calls) != 0 {
			t.Fatalf("arguments=%q calls=%#v, want none", arguments, runner.Calls)
		}
	}
}

func TestRunRejectsUnknownAndMissingCommands(t *testing.T) {
	t.Parallel()

	for _, arguments := range [][]string{nil, {"invented"}, {"version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := runWithDependencies(context.Background(), arguments, &stdout, &stderr, dependencies{}); code != exitUsage {
			t.Fatalf("arguments=%q code=%d stderr=%q", arguments, code, stderr.String())
		}
		if !strings.Contains(stderr.String(), "Usage: agentctl") {
			t.Fatalf("arguments=%q stderr=%q", arguments, stderr.String())
		}
	}
}

func TestRunStatusUsesExplicitSelectionAndBareStatusEnumeratesAll(t *testing.T) {
	t.Parallel()

	collector := &statusCollectorStub{
		report: statuspkg.ShimReport{Schema: 1, Session: "fleet", Presentation: statuspkg.PresentationGone, Agents: []statuspkg.ShimAgent{}},
		all:    statuspkg.ShimSessionsReport{Schema: 1, Sessions: []statuspkg.ShimReport{{Schema: 1, Session: "alpha", Presentation: statuspkg.PresentationGone, Agents: []statuspkg.ShimAgent{}}}},
	}
	resolver := &resolverStub{selected: "fleet"}
	var selectedOut, selectedErr bytes.Buffer
	if code := runWithDependencies(context.Background(), []string{"status", "--session", "fleet", "--json"}, &selectedOut, &selectedErr, dependencies{resolver: resolver, collector: collector}); code != exitOK {
		t.Fatalf("selected status code=%d stderr=%q", code, selectedErr.String())
	}
	if resolver.explicit == nil || *resolver.explicit != "fleet" || collector.collected != "fleet" {
		t.Fatalf("explicit=%v collected=%q", resolver.explicit, collector.collected)
	}
	var allOut, allErr bytes.Buffer
	if code := runWithDependencies(context.Background(), []string{"status", "--json"}, &allOut, &allErr, dependencies{resolver: &resolverStub{err: errors.New("must not resolve")}, collector: collector}); code != exitOK {
		t.Fatalf("bare status code=%d stderr=%q", code, allErr.String())
	}
	if !strings.Contains(allOut.String(), `"sessions":[{"schema":1,"session":"alpha"`) {
		t.Fatalf("bare JSON=%q", allOut.String())
	}
}

func TestRunKillUsesResolvedNameAndReportsObservedCompletion(t *testing.T) {
	t.Parallel()

	resolver := &resolverStub{selected: "fleet"}
	killer := &killerStub{result: kill.ShimKillResult{Session: "fleet", StoppedRoles: 2}}
	var stdout, stderr bytes.Buffer
	if code := runWithDependencies(context.Background(), []string{"kill"}, &stdout, &stderr, dependencies{resolver: resolver, killer: killer}); code != exitOK {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if killer.session != "fleet" || stderr.String() != "agentctl: killed session \"fleet\"; every recorded child was observed absent\n" {
		t.Fatalf("session=%q stderr=%q", killer.session, stderr.String())
	}
}

func TestRunKillMapsAlreadyStoppingWithoutClaimingSignalAndRetainsPresentation(t *testing.T) {
	t.Parallel()

	state := "stopping"
	observation := fleet.ShimRoleObservation{Outcome: shim.OutcomeStopAlreadyStopping, State: state, ShimPID: 41, ChildPID: 73}
	refusal := &kill.ShimKillRefusalError{Session: "fleet", Role: "planner", Outcome: observation.Outcome, Observation: observation}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &bytes.Buffer{}, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, killer: &killerStub{err: refusal},
	})
	want := "agentctl: stop for role \"planner\" in session \"fleet\" found shim PID 41 state stopping for child PID 73; no second signal was attempted and no PTY input was written (stop-already-stopping)\n"
	if code != exitUnsafe || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitUnsafe, want)
	}
}

func TestRunKillPreservesPresentationRetentionFacts(t *testing.T) {
	t.Parallel()

	err := &kill.ShimKillPresentationRetainedError{
		Session: "fleet", PresentationID: "$4", RemoveErr: errors.New("remove failed"), ObservedID: "$5",
	}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &bytes.Buffer{}, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, killer: &killerStub{err: err},
	})
	want := "agentctl: kill failed for session \"fleet\": \"shim kill retained fleet record for session \\\"fleet\\\": exact-ID presentation removal of \\\"$4\\\" failed: \\\"remove failed\\\"; post-removal presentation \\\"$5\\\" remained present\" (unclassified)\n"
	if code != exitUnclassified || stderr.String() != want {
		t.Fatalf("code=%d stderr=%q, want %d %q", code, stderr.String(), exitUnclassified, want)
	}
}

func TestRunKillReportsPostExitCleanupRetentionWithoutDenyingObservedExit(t *testing.T) {
	t.Parallel()

	err := &kill.ShimKillCleanupRetainedError{
		Session: "fleet", Role: "planner", ChildPID: 73,
		LastOutcome: shim.OutcomeInvalidRecord, Cause: errors.New("role cleanup was not observed within 5s"),
	}
	var stderr bytes.Buffer
	code := runWithDependencies(context.Background(), []string{"kill", "--session", "fleet"}, &bytes.Buffer{}, &stderr, dependencies{
		resolver: &resolverStub{selected: "fleet"}, killer: &killerStub{err: err},
	})
	want := "agentctl: stop for role \"planner\" in session \"fleet\" observed child PID 73 exit, but role cleanup was not observed complete; last outcome was invalid-record: \"role cleanup was not observed within 5s\"; presentation and fleet record were retained (post-exit-cleanup-retained)\n"
	if code != exitLaunchUnproven || stderr.String() != want || strings.Contains(stderr.String(), "did not observe child") {
		t.Fatalf("code=%d stderr=%q, want %d truthful post-exit retention %q", code, stderr.String(), exitLaunchUnproven, want)
	}
}

type resolverStub struct {
	selected string
	err      error
	explicit *string
}

func (r *resolverStub) Select(_ context.Context, explicit *string) (string, error) {
	r.explicit = explicit
	return r.selected, r.err
}

type killerStub struct {
	result  kill.ShimKillResult
	err     error
	session string
}

func (k *killerStub) Execute(_ context.Context, sessionName string) (kill.ShimKillResult, error) {
	k.session = sessionName
	return k.result, k.err
}

type statusCollectorStub struct {
	report    statuspkg.ShimReport
	all       statuspkg.ShimSessionsReport
	err       error
	collected string
}

func (c *statusCollectorStub) Collect(_ context.Context, sessionName string) (statuspkg.ShimReport, error) {
	c.collected = sessionName
	return c.report, c.err
}

func (c *statusCollectorStub) CollectAll(context.Context) (statuspkg.ShimSessionsReport, error) {
	return c.all, c.err
}

func lookupValues(values map[string]string) session.LookupEnv {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func restoreBuildStamp(t *testing.T, stamp string) {
	t.Helper()
	previous := buildinfo.Stamp
	buildinfo.Stamp = stamp
	t.Cleanup(func() { buildinfo.Stamp = previous })
}
