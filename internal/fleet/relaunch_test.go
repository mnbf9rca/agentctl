package fleet

import (
	"context"
	"errors"
	"io/fs"
	"reflect"
	"strings"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/target"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

const windowListFormat = "#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}"

func relaunchSession() tmuxx.Session { return tmuxx.Session{ID: "$4", Name: "epic123"} }

func stringPtr(value string) *string { return &value }

func relaunchLauncher(runner *tmuxx.FakeRunner, lookPath preflight.LookPathFunc) Launcher {
	if lookPath == nil {
		lookPath = presentExecutable
	}
	return New(runner, Dependencies{
		LookPath: lookPath,
		Getwd:    func() (string, error) { return "", errors.New("relaunch must never consult the invocation directory") },
		Stat: func(string) (fs.FileInfo, error) {
			return testFileInfo{mode: fs.ModeDir | 0o755}, nil
		},
	})
}

// storedMetadataResponses scripts the five session option reads plus the window
// listing that every relaunch performs before it creates anything.
func storedMetadataResponses(roster, fleetValue, directory, windows string) []tmuxx.Response {
	return []tmuxx.Response{
		{Stdout: []byte("1\n")},
		{Stdout: []byte("1\n")},
		{Stdout: []byte(roster + "\n")},
		{Stdout: []byte(fleetValue + "\n")},
		{Stdout: []byte(directory + "\n")},
		{Stdout: []byte(windows)},
	}
}

func metadataReadCalls() []tmuxx.Call {
	return []tmuxx.Call{
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_roles"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_fleet"}},
		{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_dir"}},
		{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", windowListFormat}},
	}
}

func createdPlannerWindowResponse() tmuxx.Response {
	return tmuxx.Response{Stdout: []byte("@71\tplanner\t\t\t\t\t\n")}
}

func TestRelaunchStoredConfigurationRecreatesWindowAndStampsInOrder(t *testing.T) {
	responses := storedMetadataResponses(
		"planner,reviewer",
		"planner:claude:fable:max,reviewer:codex::",
		"/fleet workspace",
		"@65\treviewer\treviewer\tcodex\t\t\tcodex\n",
	)
	responses = append(responses,
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)

	result, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}

	want := RelaunchResult{
		Role:          "planner",
		Session:       "epic123",
		Harness:       "claude",
		Model:         "fable",
		Effort:        "max",
		Directory:     "/fleet workspace",
		WindowID:      "@71",
		PaneID:        "%88",
		HarnessFrom:   ProvenanceStored,
		ModelFrom:     ProvenanceStored,
		EffortFrom:    ProvenanceStored,
		DirectoryFrom: ProvenanceStored,
	}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("Relaunch() result = %#v, want %#v", result, want)
	}
	assertCalls(t, runner, append(metadataReadCalls(),
		tmuxx.Call{Executable: "tmux", Args: []string{
			"new-window", "-d", "-t", "$4", "-n", "planner", "-c", "/fleet workspace",
			"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
			"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
			"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude' '--' '--model' 'fable' '--effort' 'max'",
		}},
		tmuxx.Call{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", windowListFormat}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_managed", "1"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_role", "planner"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_harness", "claude"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_model", "fable"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_effort", "max"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "5150"}},
		tmuxx.Call{Executable: "ps", Args: []string{"-o", "comm=", "-p", "5150"}},
		tmuxx.Call{Executable: "tmux", Args: []string{"set-option", "-w", "-t", "@71", "@agentctl_process", "2.1.220"}},
	)...)
}

func TestRelaunchCreatesWithoutAnIndexArgument(t *testing.T) {
	responses := storedMetadataResponses("planner", "planner:claude::", "/repo", "")
	responses = append(responses,
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)

	if _, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"}); err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	creation := runner.Calls[6]
	want := tmuxx.Call{Executable: "tmux", Args: []string{
		"new-window", "-d", "-t", "$4", "-n", "planner", "-c", "/repo",
		"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
		"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
		"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude'",
	}}
	if !reflect.DeepEqual(creation, want) {
		t.Fatalf("creation call = %#v, want the canonical row-3 argv %#v", creation, want)
	}
	// The window lands at the next free index: the target is the bare session ID
	// with no window index and no -a/-b placement flag.
	for _, argument := range creation.Args {
		if argument == "-a" || argument == "-b" || strings.HasPrefix(argument, "$4:") {
			t.Fatalf("creation argv = %#v, want no index or placement argument", creation.Args)
		}
	}
}

func TestRelaunchFlagOverridesRewriteFleetMetadataAfterTheBaseline(t *testing.T) {
	responses := storedMetadataResponses(
		"planner,reviewer",
		"planner:claude:fable:max,reviewer:codex::",
		"/repo",
		"@65\treviewer\treviewer\tcodex\t\t\tcodex\n",
	)
	responses = append(responses,
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("codex\n")},
		tmuxx.Response{Stdout: []byte("codex\n")},
		tmuxx.Response{},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)

	result, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{
		Role:    "planner",
		Harness: stringPtr("codex"),
		Model:   stringPtr("gpt-5.6"),
		Effort:  stringPtr("high"),
	})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.Harness != "codex" || result.Model != "gpt-5.6" || result.Effort != "high" {
		t.Fatalf("result configuration = %q/%q/%q, want codex/gpt-5.6/high", result.Harness, result.Model, result.Effort)
	}
	if result.HarnessFrom != ProvenanceOverride || result.ModelFrom != ProvenanceOverride || result.EffortFrom != ProvenanceOverride || result.DirectoryFrom != ProvenanceStored {
		t.Fatalf("result provenance = %#v, want harness, model, and effort overridden with a stored directory", result)
	}
	last := runner.Calls[len(runner.Calls)-1]
	want := tmuxx.Call{Executable: "tmux", Args: []string{
		"set-option", "-t", "$4", "@agentctl_fleet", "planner:codex:gpt-5.6:high,reviewer:codex::",
	}}
	if !reflect.DeepEqual(last, want) {
		t.Fatalf("last call = %#v, want re-encoded fleet metadata %#v", last, want)
	}
	baseline := runner.Calls[len(runner.Calls)-2]
	if got := baseline.Args[len(baseline.Args)-2]; got != "@agentctl_process" {
		t.Fatalf("call before the fleet rewrite = %#v, want the process baseline", baseline)
	}
}

func TestRelaunchDirectoryOverrideLeavesRecordedFleetDirectoryUnchanged(t *testing.T) {
	responses := storedMetadataResponses("planner", "planner:claude::", "/repo", "")
	responses = append(responses,
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)

	result, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{
		Role:      "planner",
		Directory: stringPtr("/elsewhere"),
	})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.Directory != "/elsewhere" || result.DirectoryFrom != ProvenanceOverride {
		t.Fatalf("result directory = %q (%s), want /elsewhere as a flag override", result.Directory, result.DirectoryFrom)
	}
	if result.StoredDirectory != "/repo" {
		t.Fatalf("result.StoredDirectory = %q, want the diverging recorded directory /repo", result.StoredDirectory)
	}
	for _, call := range runner.Calls {
		if len(call.Args) >= 2 && call.Args[0] == "set-option" && call.Args[1] == "-t" {
			t.Fatalf("unexpected session metadata write: %#v", call)
		}
	}
	if got := runner.Calls[6].Args[7]; got != "/elsewhere" {
		t.Fatalf("creation -c argument = %q, want /elsewhere", got)
	}
}

func TestRelaunchRefusesSessionsFailingTheOwnershipGate(t *testing.T) {
	for _, tt := range []struct {
		name      string
		responses []tmuxx.Response
		option    string
		value     string
		message   string
	}{
		{
			name:      "unmanaged",
			responses: []tmuxx.Response{{Stdout: []byte("\n")}},
			option:    "@agentctl_managed",
			message:   `session "epic123" is not managed by agentctl`,
		},
		{
			name:      "absent version",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("\n")}},
			option:    "@agentctl_version",
			message:   "managed session carries no @agentctl_version marker",
		},
		{
			name:      "other version",
			responses: []tmuxx.Response{{Stdout: []byte("1\n")}, {Stdout: []byte("2\n")}},
			option:    "@agentctl_version",
			value:     "2",
			message:   `session "epic123" has @agentctl_version="2"; expected "1"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(tt.responses...)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

			var gate *SessionGateError
			if !errors.As(err, &gate) {
				t.Fatalf("Relaunch() error = %v, want *SessionGateError", err)
			}
			if gate.Option != tt.option || gate.Value != tt.value {
				t.Fatalf("SessionGateError = %#v, want option %q value %q", gate, tt.option, tt.value)
			}
			if got := err.Error(); got != tt.message {
				t.Fatalf("error = %q, want %q", got, tt.message)
			}
			assertNoCreation(t, runner)
		})
	}
}

func TestRelaunchRefusesDefectiveRoster(t *testing.T) {
	for _, tt := range []struct {
		name    string
		roster  string
		message string
	}{
		{name: "absent", roster: "", message: "managed session has no @agentctl_roles roster"},
		{name: "empty entry", roster: "planner,,reviewer", message: `managed session has invalid @agentctl_roles roster "planner,,reviewer"`},
		{name: "trailing comma", roster: "planner,", message: `managed session has invalid @agentctl_roles roster "planner,"`},
		{name: "duplicate role", roster: "planner,planner", message: `managed session has invalid @agentctl_roles roster "planner,planner": duplicate role "planner"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte(tt.roster + "\n")},
			)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

			var roster *RosterError
			if !errors.As(err, &roster) {
				t.Fatalf("Relaunch() error = %v, want *RosterError", err)
			}
			if got := err.Error(); got != tt.message {
				t.Fatalf("error = %q, want %q", got, tt.message)
			}
			assertNoCreation(t, runner)
		})
	}
}

func TestRelaunchRefusesRoleOutsideTheRoster(t *testing.T) {
	runner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner,reviewer\n")},
	)

	_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "worker"})

	var unknown *UnknownRoleError
	if !errors.As(err, &unknown) {
		t.Fatalf("Relaunch() error = %v, want *UnknownRoleError", err)
	}
	want := `role "worker" is not in @agentctl_roles "planner,reviewer"`
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	assertNoCreation(t, runner)
}

func TestRelaunchRefusesDefectiveFleetMetadata(t *testing.T) {
	for _, tt := range []struct {
		name       string
		roster     string
		fleetValue string
		directory  string
		message    string
	}{
		{
			name: "fleet without directory", roster: "planner", fleetValue: "planner:claude::", directory: "",
			message: `managed session "epic123" has @agentctl_fleet "planner:claude::" but no @agentctl_dir`,
		},
		{
			name: "directory without fleet", roster: "planner", fleetValue: "", directory: "/repo",
			message: `managed session "epic123" has @agentctl_dir "/repo" but no @agentctl_fleet`,
		},
		{
			name: "roles disagree with roster", roster: "planner,reviewer", fleetValue: "planner:claude::,worker:codex::", directory: "/repo",
			message: `managed session "epic123" has @agentctl_fleet "planner:claude::,worker:codex::" whose roles do not match @agentctl_roles "planner,reviewer"`,
		},
		{
			name: "roster order differs", roster: "planner,reviewer", fleetValue: "reviewer:codex::,planner:claude::", directory: "/repo",
			message: `managed session "epic123" has @agentctl_fleet "reviewer:codex::,planner:claude::" whose roles do not match @agentctl_roles "planner,reviewer"`,
		},
		{
			name: "duplicate role", roster: "planner,reviewer", fleetValue: "planner:claude::,planner:codex::", directory: "/repo",
			message: `managed session "epic123" has invalid @agentctl_fleet "planner:claude::,planner:codex::": entry 2 "planner:codex::" repeats role "planner"`,
		},
		{
			name: "entry is not a quad", roster: "planner", fleetValue: "planner:claude", directory: "/repo",
			message: `managed session "epic123" has invalid @agentctl_fleet "planner:claude": entry 1 "planner:claude" is not role:harness:model:effort`,
		},
		{
			name: "unknown harness", roster: "planner", fleetValue: "planner:bash::", directory: "/repo",
			message: `managed session "epic123" has invalid @agentctl_fleet "planner:bash::": entry 1 "planner:bash::" names unknown harness "bash"`,
		},
		{
			name: "smuggled model", roster: "planner", fleetValue: "planner:claude:--dangerously:", directory: "/repo",
			message: `managed session "epic123" has invalid @agentctl_fleet "planner:claude:--dangerously:": entry 1 "planner:claude:--dangerously:" has invalid model "--dangerously"`,
		},
		{
			name: "unknown effort", roster: "planner", fleetValue: "planner:claude::turbo", directory: "/repo",
			message: `managed session "epic123" has invalid @agentctl_fleet "planner:claude::turbo": entry 1 "planner:claude::turbo" has invalid effort "turbo"`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte(tt.roster + "\n")},
				tmuxx.Response{Stdout: []byte(tt.fleetValue + "\n")},
				tmuxx.Response{Stdout: []byte(tt.directory + "\n")},
			)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

			var metadata *MetadataError
			if !errors.As(err, &metadata) {
				t.Fatalf("Relaunch() error = %v, want *MetadataError", err)
			}
			if got := err.Error(); got != tt.message {
				t.Fatalf("error = %q, want %q", got, tt.message)
			}
			assertNoCreation(t, runner)
		})
	}
}

func TestRelaunchRefusesStoredRelativeDirectoryWithoutOverride(t *testing.T) {
	runner := tmuxx.NewFakeRunner(storedMetadataResponses("planner", "planner:claude::", "payload", "")...)

	_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

	var stored *StoredDirectoryError
	if !errors.As(err, &stored) {
		t.Fatalf("Relaunch() error = %v, want *StoredDirectoryError", err)
	}
	if stored.Path != "payload" {
		t.Fatalf("StoredDirectoryError.Path = %q, want payload", stored.Path)
	}
	want := `managed session "epic123" records launch directory "payload": path is not absolute; supply --dir to relaunch planner elsewhere`
	if got := err.Error(); got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
	assertNoCreation(t, runner)
}

func TestRelaunchStoredRelativeDirectoryAllowsExplicitOverride(t *testing.T) {
	responses := storedMetadataResponses("planner", "planner:claude::", "payload", "")
	responses = append(responses,
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{Stdout: []byte("claude\n")},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)

	result, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{
		Role:      "planner",
		Directory: stringPtr("/elsewhere"),
	})

	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.Directory != "/elsewhere" || result.DirectoryFrom != ProvenanceOverride || result.StoredDirectory != "payload" {
		t.Fatalf("Relaunch() result = %#v, want /elsewhere override with stored payload preserved", result)
	}
	if got := runner.Calls[6].Args[7]; got != "/elsewhere" {
		t.Fatalf("creation -c argument = %q, want /elsewhere", got)
	}
	for _, call := range runner.Calls {
		if len(call.Args) >= 5 && call.Args[0] == "set-option" && call.Args[1] == "-t" && call.Args[3] == "@agentctl_dir" {
			t.Fatalf("unexpected stored directory rewrite: %#v", call)
		}
	}
}

func TestRelaunchRefusesLegacySessionsWithoutSuppliedConfiguration(t *testing.T) {
	wantMessage := "session records no per-role configuration; it was launched before agentctl recorded @agentctl_fleet and @agentctl_dir; supply --harness [--model] [--effort] --dir"
	for _, tt := range []struct {
		name    string
		request RelaunchRequest
	}{
		{name: "no flags", request: RelaunchRequest{Role: "planner"}},
		{name: "harness only", request: RelaunchRequest{Role: "planner", Harness: stringPtr("claude")}},
		{name: "directory only", request: RelaunchRequest{Role: "planner", Directory: stringPtr("/repo")}},
		{name: "model without harness", request: RelaunchRequest{Role: "planner", Model: stringPtr("fable"), Directory: stringPtr("/repo")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("1\n")},
				tmuxx.Response{Stdout: []byte("planner\n")},
				tmuxx.Response{Stdout: []byte("\n")},
				tmuxx.Response{Stdout: []byte("\n")},
			)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), tt.request)

			var legacy *LegacySessionError
			if !errors.As(err, &legacy) {
				t.Fatalf("Relaunch() error = %v, want *LegacySessionError", err)
			}
			if got := err.Error(); got != wantMessage {
				t.Fatalf("error = %q, want %q", got, wantMessage)
			}
			assertNoCreation(t, runner)
		})
	}
}

func TestRelaunchAcceptsLegacySessionWithSuppliedConfigurationAndNeverDefaultsDirectory(t *testing.T) {
	responses := []tmuxx.Response{
		{Stdout: []byte("1\n")},
		{Stdout: []byte("1\n")},
		{Stdout: []byte("planner\n")},
		{Stdout: []byte("\n")},
		{Stdout: []byte("\n")},
		{Stdout: []byte("")},
		{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		{}, {}, {}, {}, {},
		{Stdout: []byte("codex\n")},
		{Stdout: []byte("codex\n")},
		{},
	}
	runner := tmuxx.NewFakeRunner(responses...)

	result, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{
		Role:      "planner",
		Harness:   stringPtr("codex"),
		Directory: stringPtr("/legacy repo"),
	})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}
	if result.HarnessFrom != ProvenanceFlags || result.ModelFrom != ProvenanceFlags || result.EffortFrom != ProvenanceFlags || result.DirectoryFrom != ProvenanceFlags {
		t.Fatalf("result provenance = %#v, want every field reported as supplied by flags", result)
	}
	if result.Model != "" || result.StoredDirectory != "" {
		t.Fatalf("result = %#v, want a defaulted model and no recorded directory", result)
	}
	if got := runner.Calls[6].Args[7]; got != "/legacy repo" {
		t.Fatalf("creation -c argument = %q, want /legacy repo", got)
	}
	for _, call := range runner.Calls {
		if len(call.Args) >= 5 && call.Args[0] == "set-option" && call.Args[1] == "-t" {
			t.Fatalf("unexpected session option write on a legacy session: %#v", call)
		}
	}
}

func TestRelaunchRefusesAnyExistingRoleWindowRenderingObservedState(t *testing.T) {
	for _, tt := range []struct {
		name      string
		windows   string
		extra     []tmuxx.Response
		wantState status.State
		message   string
		wantCalls []tmuxx.Call
	}{
		{
			name:      "running",
			windows:   "@23\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n",
			extra:     []tmuxx.Response{{Stdout: []byte("%42\t4242\t0\t1\n")}, {Stdout: []byte("2.1.220\n")}},
			wantState: status.StateRunning,
			message:   "role planner already has 1 window in epic123 (@23 running); relaunch creates only absent role windows",
		},
		{
			name:      "dead",
			windows:   "@23\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n",
			extra:     []tmuxx.Response{{Stdout: []byte("%42\t4242\t1\t1\n")}},
			wantState: status.StateDead,
			message:   "role planner already has 1 window in epic123 (@23 dead); relaunch creates only absent role windows",
		},
		{
			name:      "unmanaged without stored role",
			windows:   "@23\tplanner\t\tclaude\tfable\t\t2.1.220\n",
			wantState: status.StateUnmanaged,
			message:   "role planner already has 1 window in epic123 (@23 unmanaged); relaunch creates only absent role windows",
			wantCalls: []tmuxx.Call{
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_managed"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_version"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_roles"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_fleet"}},
				{Executable: "tmux", Args: []string{"show-options", "-qv", "-t", "$4", "@agentctl_dir"}},
				{Executable: "tmux", Args: []string{
					"list-windows", "-t", "$4", "-F",
					"#{window_id}\t#{window_name}\t#{@agentctl_role}\t#{@agentctl_harness}\t#{@agentctl_model}\t#{@agentctl_effort}\t#{@agentctl_process}",
				}},
			},
		},
		{
			name:      "unmanaged pane count",
			windows:   "@23\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n",
			extra:     []tmuxx.Response{{Stdout: []byte("%42\t4242\t0\t2\n%43\t4243\t0\t2\n")}},
			wantState: status.StateUnmanaged,
			message:   "role planner already has 1 window in epic123 (@23 unmanaged); relaunch creates only absent role windows",
		},
		{
			name:      "zero panes",
			windows:   "@23\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n",
			extra:     []tmuxx.Response{{Stdout: []byte("")}},
			wantState: status.StateMissing,
			message:   "role planner already has 1 window in epic123 (@23 missing); relaunch creates only absent role windows",
		},
		{
			name:      "unexpected process",
			windows:   "@23\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n",
			extra:     []tmuxx.Response{{Stdout: []byte("%42\t4242\t0\t1\n")}, {Stdout: []byte("bash\n")}},
			wantState: status.StateUnexpectedProcess,
			message:   "role planner already has 1 window in epic123 (@23 unexpected-process); relaunch creates only absent role windows",
		},
		{
			name:      "ambiguous",
			windows:   "@23\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n@31\tplanner\tplanner\tclaude\tfable\t\t2.1.220\n",
			wantState: status.StateAmbiguous,
			message:   "role planner already has 2 windows in epic123 (@23 ambiguous, @31 ambiguous); relaunch creates only absent role windows",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			responses := storedMetadataResponses("planner", "planner:claude:fable:", "/repo", tt.windows)
			responses = append(responses, tt.extra...)
			runner := tmuxx.NewFakeRunner(responses...)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

			var present *WindowPresentError
			if !errors.As(err, &present) {
				t.Fatalf("Relaunch() error = %v, want *WindowPresentError", err)
			}
			if len(present.Windows) == 0 || present.Windows[0].State != tt.wantState {
				t.Fatalf("observed windows = %#v, want first state %q", present.Windows, tt.wantState)
			}
			if got := err.Error(); got != tt.message {
				t.Fatalf("error = %q, want %q", got, tt.message)
			}
			if tt.wantCalls != nil {
				assertCalls(t, runner, tt.wantCalls...)
			}
			assertNoCreation(t, runner)
		})
	}
}

func TestRelaunchReportsMissingExecutablesBeforeCreating(t *testing.T) {
	runner := tmuxx.NewFakeRunner(storedMetadataResponses("planner", "planner:codex::", "/repo", "")...)
	var lookedUp []string
	lookPath := func(name string) (string, error) {
		lookedUp = append(lookedUp, name)
		if name == "codex" {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}

	_, err := relaunchLauncher(runner, lookPath).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

	var missing *preflight.MissingExecutableError
	if !errors.As(err, &missing) {
		t.Fatalf("Relaunch() error = %v, want *preflight.MissingExecutableError", err)
	}
	if missing.Name != "codex" {
		t.Fatalf("missing executable = %q, want codex", missing.Name)
	}
	if want := []string{"tmux", "amq", "codex"}; !reflect.DeepEqual(lookedUp, want) {
		t.Fatalf("LookPath calls = %#v, want only the relaunched role's harness %#v", lookedUp, want)
	}
	assertNoCreation(t, runner)
}

func TestRelaunchClassifiesAnUnusableDirectoryByProvenance(t *testing.T) {
	for _, tt := range []struct {
		name      string
		request   RelaunchRequest
		stat      func(string) (fs.FileInfo, error)
		wantFlag  bool
		wantPath  string
		wantError string
	}{
		{
			name:      "stored directory is gone",
			request:   RelaunchRequest{Role: "planner"},
			stat:      func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
			wantPath:  "/repo",
			wantError: `managed session "epic123" records launch directory "/repo": file does not exist; supply --dir to relaunch planner elsewhere`,
		},
		{
			name:      "stored directory is a file",
			request:   RelaunchRequest{Role: "planner"},
			stat:      func(string) (fs.FileInfo, error) { return testFileInfo{mode: 0o644}, nil },
			wantPath:  "/repo",
			wantError: `managed session "epic123" records launch directory "/repo": not a directory; supply --dir to relaunch planner elsewhere`,
		},
		{
			name:      "supplied directory is gone",
			request:   RelaunchRequest{Role: "planner", Directory: stringPtr("/elsewhere")},
			stat:      func(string) (fs.FileInfo, error) { return nil, fs.ErrNotExist },
			wantFlag:  true,
			wantPath:  "/elsewhere",
			wantError: `invalid launch directory "/elsewhere": file does not exist`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner(storedMetadataResponses("planner", "planner:claude::", "/repo", "")...)
			launcher := New(runner, Dependencies{LookPath: presentExecutable, Stat: tt.stat})

			_, err := launcher.Relaunch(context.Background(), relaunchSession(), tt.request)

			if tt.wantFlag {
				var directory *DirectoryError
				if !errors.As(err, &directory) {
					t.Fatalf("Relaunch() error = %v, want *DirectoryError", err)
				}
				if directory.Path != tt.wantPath {
					t.Fatalf("DirectoryError.Path = %q, want %q", directory.Path, tt.wantPath)
				}
			} else {
				var stored *StoredDirectoryError
				if !errors.As(err, &stored) {
					t.Fatalf("Relaunch() error = %v, want *StoredDirectoryError", err)
				}
				if stored.Path != tt.wantPath {
					t.Fatalf("StoredDirectoryError.Path = %q, want %q", stored.Path, tt.wantPath)
				}
			}
			if got := err.Error(); got != tt.wantError {
				t.Fatalf("error = %q, want %q", got, tt.wantError)
			}
			assertNoCreation(t, runner)
		})
	}
}

func TestRelaunchMalformedCreationOutputRemovesNothing(t *testing.T) {
	responses := storedMetadataResponses("planner", "planner:claude::", "/repo", "")
	responses = append(responses, tmuxx.Response{Stdout: []byte("not a record\n")})
	runner := tmuxx.NewFakeRunner(responses...)

	_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

	var creation *WindowCreationError
	if !errors.As(err, &creation) {
		t.Fatalf("Relaunch() error = %v, want *WindowCreationError", err)
	}
	if !errors.Is(err, tmuxx.ErrCreationOutput) {
		t.Fatalf("Relaunch() error = %v, want wrapped tmuxx.ErrCreationOutput", err)
	}
	wantSuffix := "; a window named planner may exist; inspect with tmux list-windows"
	if got := err.Error(); !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("error = %q, want suffix %q", got, wantSuffix)
	}
	assertNoWindowKill(t, runner)
}

func TestRelaunchRollsBackItsCreatedWindowWhenPostCreateVerificationFindsAConflict(t *testing.T) {
	for _, tt := range []struct {
		name        string
		windows     string
		wantMessage string
	}{
		{
			name:        "created window disappeared",
			wantMessage: "failed to relaunch planner; removed window @71: post-create verification observed role planner in 0 windows in epic123; expected only created window @71",
		},
		{
			name:        "sole match is not the created window",
			windows:     "@70\tplanner\tplanner\tclaude\t\t\tclaude\n",
			wantMessage: "failed to relaunch planner; removed window @71: post-create verification observed role planner in 1 window in epic123 (@70); expected only created window @71",
		},
		{
			name: "concurrent duplicate",
			windows: "@70\tplanner\tplanner\tclaude\t\t\tclaude\n" +
				"@71\tplanner\t\t\t\t\t\n",
			wantMessage: "failed to relaunch planner; removed window @71: post-create verification observed role planner in 2 windows in epic123 (@70, @71); expected only created window @71",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			responses := storedMetadataResponses("planner", "planner:claude::", "/repo", "")
			responses = append(responses,
				tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
				tmuxx.Response{Stdout: []byte(tt.windows)},
				tmuxx.Response{},
			)
			runner := tmuxx.NewFakeRunner(responses...)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

			if err == nil || err.Error() != tt.wantMessage {
				t.Fatalf("Relaunch() error = %v, want %q", err, tt.wantMessage)
			}
			wantCalls := append(metadataReadCalls(),
				tmuxx.Call{Executable: "tmux", Args: []string{
					"new-window", "-d", "-t", "$4", "-n", "planner", "-c", "/repo",
					"-e", "AGENTCTL_SESSION=epic123", "-e", "AGENTCTL_ROLE=planner", "-e", "AGENTCTL_MANAGED=1",
					"-P", "-F", "#{window_id}\t#{pane_id}\t#{pane_pid}", "--",
					"exec 'amq' 'coop' 'exec' '--session' 'epic123' '--me' 'planner' 'claude'",
				}},
				tmuxx.Call{Executable: "tmux", Args: []string{"list-windows", "-t", "$4", "-F", windowListFormat}},
				tmuxx.Call{Executable: "tmux", Args: []string{"kill-window", "-t", "@71"}},
			)
			assertCalls(t, runner, wantCalls...)
		})
	}
}

func TestRelaunchRollsBackOnlyTheWindowThisInvocationCreated(t *testing.T) {
	cause := errors.New("injected failure")
	for _, tt := range []struct {
		name  string
		index int
	}{
		{name: "post-create verification", index: 7},
		{name: "window managed option", index: 8},
		{name: "window role option", index: 9},
		{name: "window harness option", index: 10},
		{name: "window model option", index: 11},
		{name: "window effort option", index: 12},
		{name: "process baseline", index: 13},
		{name: "window process option", index: 14},
		{name: "fleet metadata rewrite", index: 15},
	} {
		t.Run(tt.name, func(t *testing.T) {
			responses := relaunchPrefixResponses(tt.index)
			responses = append(responses, tmuxx.Response{Err: cause}, tmuxx.Response{})
			runner := tmuxx.NewFakeRunner(responses...)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{
				Role:    "planner",
				Harness: stringPtr("codex"),
			})

			var relaunchErr *RelaunchError
			if !errors.As(err, &relaunchErr) {
				t.Fatalf("Relaunch() error = %v, want *RelaunchError", err)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("Relaunch() error = %v, want wrapped injected cause", err)
			}
			if got, want := len(runner.Calls), tt.index+2; got != want {
				t.Fatalf("runner calls = %d, want %d (failure, one rollback, no later calls)", got, want)
			}
			last := runner.Calls[len(runner.Calls)-1]
			want := tmuxx.Call{Executable: "tmux", Args: []string{"kill-window", "-t", "@71"}}
			if !reflect.DeepEqual(last, want) {
				t.Fatalf("rollback call = %#v, want %#v", last, want)
			}
			for _, call := range runner.Calls {
				if len(call.Args) > 0 && call.Args[0] == "kill-session" {
					t.Fatalf("unexpected kill-session during relaunch rollback: %#v", call)
				}
			}
		})
	}
}

func TestRelaunchErrorReportsBothCleanupOutcomesWithTheirCause(t *testing.T) {
	cause := errors.New("stamp failed")
	cleanupCause := errors.New("kill failed")
	for _, tt := range []struct {
		name        string
		cleanup     tmuxx.Response
		wantMessage string
		wantCleanup bool
	}{
		{
			name:        "removed",
			cleanup:     tmuxx.Response{},
			wantMessage: "failed to relaunch planner; removed window @71: tmux set window option: stamp failed",
		},
		{
			name:        "cleanup failed",
			cleanup:     tmuxx.Response{Err: cleanupCause},
			wantMessage: "failed to relaunch planner; failed to remove window @71: tmux kill window: kill failed (relaunch failure: tmux set window option: stamp failed)",
			wantCleanup: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			responses := append(relaunchPrefixResponses(8), tmuxx.Response{Err: cause}, tt.cleanup)
			runner := tmuxx.NewFakeRunner(responses...)

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})

			var relaunchErr *RelaunchError
			if !errors.As(err, &relaunchErr) {
				t.Fatalf("Relaunch() error = %v, want *RelaunchError", err)
			}
			if got := err.Error(); got != tt.wantMessage {
				t.Fatalf("error = %q, want %q", got, tt.wantMessage)
			}
			if (relaunchErr.CleanupErr != nil) != tt.wantCleanup {
				t.Fatalf("RelaunchError.CleanupErr = %v, want cleanup present %t", relaunchErr.CleanupErr, tt.wantCleanup)
			}
		})
	}
}

func TestRelaunchRejectsInvalidValuesBeforeAnyCommand(t *testing.T) {
	for _, tt := range []struct {
		name    string
		request RelaunchRequest
	}{
		{name: "uppercase role", request: RelaunchRequest{Role: "Planner"}},
		{name: "empty role", request: RelaunchRequest{Role: ""}},
		{name: "flag-shaped role", request: RelaunchRequest{Role: "-planner"}},
		{name: "unknown harness", request: RelaunchRequest{Role: "planner", Harness: stringPtr("bash")}},
		{name: "smuggled model", request: RelaunchRequest{Role: "planner", Model: stringPtr("--dangerously")}},
		{name: "unknown effort", request: RelaunchRequest{Role: "planner", Effort: stringPtr("turbo")}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := tmuxx.NewFakeRunner()

			_, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), tt.request)

			var validation *config.ValidationError
			if !errors.As(err, &validation) {
				t.Fatalf("Relaunch() error = %v, want *config.ValidationError", err)
			}
			if len(runner.Calls) != 0 {
				t.Fatalf("runner calls = %#v, want none", runner.Calls)
			}
		})
	}
}

func TestRelaunchedRoleSatisfiesStatusAndControlAgainstTheFreshBaseline(t *testing.T) {
	responses := storedMetadataResponses("planner", "planner:claude:fable:", "/repo", "")
	responses = append(responses,
		tmuxx.Response{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{}, tmuxx.Response{},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
		tmuxx.Response{},
	)
	runner := tmuxx.NewFakeRunner(responses...)

	result, err := relaunchLauncher(runner, nil).Relaunch(context.Background(), relaunchSession(), RelaunchRequest{Role: "planner"})
	if err != nil {
		t.Fatalf("Relaunch() error = %v", err)
	}

	// Build the post-relaunch window record from the options relaunch actually
	// stamped, so status and control are checked against its own baseline.
	stamped := map[string]string{}
	for _, call := range runner.Calls {
		if len(call.Args) == 6 && call.Args[0] == "set-option" && call.Args[1] == "-w" {
			stamped[call.Args[4]] = call.Args[5]
		}
	}
	record := strings.Join([]string{
		string(result.WindowID), result.Role,
		stamped["@agentctl_role"],
		stamped["@agentctl_harness"], stamped["@agentctl_model"], stamped["@agentctl_effort"], stamped["@agentctl_process"],
	}, "\t") + "\n"
	paneRecord := string(result.PaneID) + "\t5150\t0\t1\n"

	statusRunner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("planner\n")},
		tmuxx.Response{Stdout: []byte(record)},
		tmuxx.Response{Stdout: []byte(paneRecord)},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
	)
	report, err := status.NewCollector(tmuxx.New(statusRunner)).Collect(context.Background(), "epic123", "$4")
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if len(report.Agents) != 1 || report.Agents[0].State != status.StateRunning {
		t.Fatalf("status report = %#v, want one running role", report.Agents)
	}

	controlRunner := tmuxx.NewFakeRunner(
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte("1\n")},
		tmuxx.Response{Stdout: []byte(record)},
		tmuxx.Response{Stdout: []byte(paneRecord)},
		tmuxx.Response{Stdout: []byte("2.1.220\n")},
	)
	resolver := target.New(tmuxx.New(controlRunner), func(string) (string, bool) { return "", false })
	pane, err := resolver.Resolve(context.Background(), relaunchSession(), "planner")
	if err != nil {
		t.Fatalf("target Resolve() error = %v", err)
	}
	if pane != result.PaneID {
		t.Fatalf("resolved pane = %q, want the relaunched pane %q", pane, result.PaneID)
	}
}

// relaunchPrefixResponses returns the successful scripted calls before zero-based
// call index end for a stored-mode relaunch of planner in a one-role fleet.
func relaunchPrefixResponses(end int) []tmuxx.Response {
	all := []tmuxx.Response{
		{Stdout: []byte("1\n")},
		{Stdout: []byte("1\n")},
		{Stdout: []byte("planner\n")},
		{Stdout: []byte("planner:claude:fable:\n")},
		{Stdout: []byte("/repo\n")},
		{Stdout: []byte("")},
		{Stdout: []byte("@71\t%88\t5150\n")},
		createdPlannerWindowResponse(),
		{}, {}, {}, {}, {},
		{Stdout: []byte("2.1.220\n")},
		{Stdout: []byte("2.1.220\n")},
		{},
		{},
	}
	return append([]tmuxx.Response(nil), all[:end]...)
}

func assertNoCreation(t *testing.T, runner *tmuxx.FakeRunner) {
	t.Helper()
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) > 0 && (call.Args[0] == "new-window" || call.Args[0] == "new-session") {
			t.Fatalf("unexpected creation call: %#v", call)
		}
	}
	assertNoWindowKill(t, runner)
}

func assertNoWindowKill(t *testing.T, runner *tmuxx.FakeRunner) {
	t.Helper()
	for _, call := range runner.Calls {
		if call.Executable == "tmux" && len(call.Args) > 0 && (call.Args[0] == "kill-window" || call.Args[0] == "kill-session") {
			t.Fatalf("unexpected kill call: %#v", call)
		}
	}
}
