package status

import (
	"bytes"
	"testing"
)

func TestWriteTableRendersDefaultModelOnlyForHumans(t *testing.T) {
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
				Window:  "planner",
				PaneID:  "%12",
				Process: "claude",
				State:   StateRunning,
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
		"SESSION  ROLE     HARNESS  MODEL    PANE  PROCESS   STATE\n" +
		"epic123  planner  claude   fable    %12   claude    running\n" +
		"epic123  codex1   codex    default  %13   /bin/zsh  unexpected-process\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteTable() output =\n%q\nwant:\n%q", got, want)
	}
}

func TestWriteJSONMatchesVersionedSchemaAndPreservesEmptyModel(t *testing.T) {
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
	want := "{\"schema\":1,\"session\":\"epic123\",\"managed\":true,\"agents\":[{\"role\":\"codex1\",\"harness\":\"codex\",\"model\":\"\",\"window\":\"codex1\",\"pane_id\":\"%13\",\"process\":\"codex\",\"state\":\"running\"}]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteJSON() output = %q, want %q", got, want)
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
