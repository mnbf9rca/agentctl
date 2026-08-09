package status

import (
	"bytes"
	"testing"
)

func TestWriteTableRendersDefaultModelAndEffortOnlyForHumans(t *testing.T) {
	t.Parallel()

	report := Report{
		Schema:  1,
		Session: "epic123",
		Managed: true,
		Agents: []Agent{
			{
				Role:    "planner",
				Harness: "claude",
				Model:   "fable",
				Effort:  "max",
				Window:  "planner",
				PaneID:  "%12",
				Process: "claude",
				State:   StateRunning,
			},
			{
				Role:    "worker",
				Harness: "codex",
				Window:  "worker",
				PaneID:  "%14",
				State:   StateNoBaseline,
			},
			{
				Role:    "codex1",
				Harness: "codex",
				Window:  "codex1",
				PaneID:  "%13",
				Process: "/bin/zsh",
				State:   StateUnexpectedProcess,
			},
		},
	}

	var output bytes.Buffer
	if err := WriteTable(&output, report); err != nil {
		t.Fatalf("WriteTable() error = %v", err)
	}
	want := "" +
		"SESSION  ROLE     HARNESS  MODEL    EFFORT   PANE  PROCESS   STATE\n" +
		"epic123  planner  claude   fable    max      %12   claude    running\n" +
		"epic123  worker   codex    default  default  %14             no-baseline\n" +
		"epic123  codex1   codex    default  default  %13   /bin/zsh  unexpected-process\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteTable() output =\n%q\nwant:\n%q", got, want)
	}
}

func TestWriteJSONMatchesVersionedSchemaAndPreservesEmptyModelAndEffort(t *testing.T) {
	t.Parallel()

	report := Report{
		Schema:  1,
		Session: "epic123",
		Managed: true,
		Agents: []Agent{{
			Role:    "codex1",
			Harness: "codex",
			Model:   "",
			Window:  "codex1",
			PaneID:  "%13",
			Process: "codex",
			State:   StateRunning,
		}},
	}

	var output bytes.Buffer
	if err := WriteJSON(&output, report); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	want := "{\"schema\":1,\"session\":\"epic123\",\"managed\":true,\"agents\":[{\"role\":\"codex1\",\"harness\":\"codex\",\"model\":\"\",\"effort\":\"\",\"window\":\"codex1\",\"pane_id\":\"%13\",\"process\":\"codex\",\"state\":\"running\"}]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteJSON() output = %q, want %q", got, want)
	}
}

func TestWriteSessionsTableMatchesInsideSessionSpecFixture(t *testing.T) {
	t.Parallel()

	report := listingSpecFixture(true)

	var output bytes.Buffer
	if err := WriteSessionsTable(&output, report); err != nil {
		t.Fatalf("WriteSessionsTable() error = %v", err)
	}
	want := "" +
		"   SESSION  ROLE     HARNESS  MODEL    EFFORT  PANE  PROCESS  STATE\n" +
		"*  epic123  planner  claude   fable    max     %12   claude   running\n" +
		"*  epic123  codex1   codex    default  high    %13   codex    running\n" +
		"   shell                                                      unmanaged\n" +
		"   future                                                     session \"future\" was created by a different agentctl version \"2\"\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsTable() output =\n%q\nwant:\n%q", got, want)
	}
}

func TestWriteSessionsTableMatchesOutsideSessionSpecFixture(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteSessionsTable(&output, listingSpecFixture(false)); err != nil {
		t.Fatalf("WriteSessionsTable() error = %v", err)
	}
	want := "" +
		"  SESSION  ROLE     HARNESS  MODEL    EFFORT  PANE  PROCESS  STATE\n" +
		"  epic123  planner  claude   fable    max     %12   claude   running\n" +
		"  epic123  codex1   codex    default  high    %13   codex    running\n" +
		"  shell                                                      unmanaged\n" +
		"  future                                                     session \"future\" was created by a different agentctl version \"2\"\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsTable() output =\n%q\nwant:\n%q", got, want)
	}
}

func TestWriteSessionsTableDoesNotCallManagedAgentlessSessionUnmanaged(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	report := SessionsReport{Schema: 1, Sessions: []Report{{Schema: 1, Session: "empty", Managed: true, Agents: []Agent{}}}}
	if err := WriteSessionsTable(&output, report); err != nil {
		t.Fatalf("WriteSessionsTable() error = %v", err)
	}
	want := "  SESSION  ROLE  HARNESS  MODEL  EFFORT  PANE  PROCESS  STATE\n" +
		"  empty                                                 managed\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsTable() output = %q, want %q", got, want)
	}
}

func TestWriteSessionsTableRendersHeaderOnlyWithoutManagedSessions(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteSessionsTable(&output, SessionsReport{Schema: 1, Sessions: []Report{}}); err != nil {
		t.Fatalf("WriteSessionsTable() error = %v", err)
	}
	want := "  SESSION  ROLE  HARNESS  MODEL  EFFORT  PANE  PROCESS  STATE\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsTable() output = %q, want %q", got, want)
	}
}

func TestWriteSessionsJSONMatchesInsideSessionSpecFixture(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteSessionsJSON(&output, listingSpecFixture(true)); err != nil {
		t.Fatalf("WriteSessionsJSON() error = %v", err)
	}
	want := "{\"schema\":1,\"sessions\":[{\"schema\":1,\"session\":\"epic123\",\"managed\":true,\"agents\":[{\"role\":\"planner\",\"harness\":\"claude\",\"model\":\"fable\",\"effort\":\"max\",\"window\":\"planner\",\"pane_id\":\"%12\",\"process\":\"claude\",\"state\":\"running\"},{\"role\":\"codex1\",\"harness\":\"codex\",\"model\":\"\",\"effort\":\"high\",\"window\":\"codex1\",\"pane_id\":\"%13\",\"process\":\"codex\",\"state\":\"running\"}],\"current\":true},{\"schema\":1,\"session\":\"shell\",\"managed\":false,\"agents\":[]},{\"schema\":1,\"session\":\"future\",\"managed\":true,\"agents\":[],\"defect\":\"session \\\"future\\\" was created by a different agentctl version \\\"2\\\"\"}]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsJSON() output = %q, want %q", got, want)
	}
}

func TestWriteSessionsJSONMatchesOutsideSessionSpecFixture(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteSessionsJSON(&output, listingSpecFixture(false)); err != nil {
		t.Fatalf("WriteSessionsJSON() error = %v", err)
	}
	want := "{\"schema\":1,\"sessions\":[{\"schema\":1,\"session\":\"epic123\",\"managed\":true,\"agents\":[{\"role\":\"planner\",\"harness\":\"claude\",\"model\":\"fable\",\"effort\":\"max\",\"window\":\"planner\",\"pane_id\":\"%12\",\"process\":\"claude\",\"state\":\"running\"},{\"role\":\"codex1\",\"harness\":\"codex\",\"model\":\"\",\"effort\":\"high\",\"window\":\"codex1\",\"pane_id\":\"%13\",\"process\":\"codex\",\"state\":\"running\"}]},{\"schema\":1,\"session\":\"shell\",\"managed\":false,\"agents\":[]},{\"schema\":1,\"session\":\"future\",\"managed\":true,\"agents\":[],\"defect\":\"session \\\"future\\\" was created by a different agentctl version \\\"2\\\"\"}]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsJSON() output = %q, want %q", got, want)
	}
}

func listingSpecFixture(current bool) SessionsReport {
	return SessionsReport{Schema: 1, Sessions: []Report{
		{Schema: 1, Session: "epic123", Managed: true, Current: current, Agents: []Agent{
			{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Window: "planner", PaneID: "%12", Process: "claude", State: StateRunning},
			{Role: "codex1", Harness: "codex", Effort: "high", Window: "codex1", PaneID: "%13", Process: "codex", State: StateRunning},
		}},
		{Schema: 1, Session: "shell", Managed: false, Agents: []Agent{}},
		{Schema: 1, Session: "future", Managed: true, Agents: []Agent{}, Defect: "session \"future\" was created by a different agentctl version \"2\""},
	}}
}

func TestWriteSessionsJSONUsesEmptyArraysWithoutManagedSessions(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteSessionsJSON(&output, SessionsReport{Schema: 1}); err != nil {
		t.Fatalf("WriteSessionsJSON() error = %v", err)
	}
	want := "{\"schema\":1,\"sessions\":[]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteSessionsJSON() output = %q, want %q", got, want)
	}
}

func TestWriteJSONUsesEmptyArrayForUnmanagedSession(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	if err := WriteJSON(&output, Report{Schema: 1, Session: "other", Managed: false}); err != nil {
		t.Fatalf("WriteJSON() error = %v", err)
	}
	want := "{\"schema\":1,\"session\":\"other\",\"managed\":false,\"agents\":[]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteJSON() output = %q, want %q", got, want)
	}
}
