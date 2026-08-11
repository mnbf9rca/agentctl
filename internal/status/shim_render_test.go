package status

import (
	"bytes"
	"testing"
)

func TestWriteShimTableRendersConfidenceAndPresentationWithoutChangingState(t *testing.T) {
	t.Parallel()

	report := ShimReport{
		Schema: 1, Session: "fleet", Presentation: PresentationGone,
		Agents: []ShimAgent{
			{Role: "planner", Harness: "claude", Model: "fable", Effort: "max", Confidence: ConfidenceAnchored, ShimPID: 11, ChildPID: 12, State: RuntimeStateRunning},
			{Role: "worker", Harness: "codex", Confidence: ConfidenceUnanchored, ChildPID: 22, State: RuntimeStateOrphan},
		},
		Note: `all 2 roster roles are missing; unmanaged window "joined" has 2 panes`,
	}
	var output bytes.Buffer
	if err := WriteShimTable(&output, report); err != nil {
		t.Fatalf("WriteShimTable() error = %v", err)
	}
	want := "SESSION  ROLE     HARNESS  MODEL    EFFORT   CONFIDENCE  SHIM  CHILD  PRESENTATION  STATE    FACTS\n" +
		"fleet    planner  claude   fable    max      anchored    11    12     gone          running  -\n" +
		"fleet    worker   codex    default  default  unanchored        22     gone          orphan   -\n" +
		"note: all 2 roster roles are missing; unmanaged window \"joined\" has 2 panes\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteShimTable() =\n%q\nwant:\n%q", got, want)
	}
}

func TestWriteShimTableRendersBothSidesOfRuntimeDisagreement(t *testing.T) {
	t.Parallel()

	report := ShimReport{Schema: 1, Session: "fleet", Presentation: PresentationGone, Agents: []ShimAgent{{
		Role: "planner", Harness: "claude", Confidence: ConfidenceAnchored, ShimPID: 10,
		AnswererPID: 11, RecordShimPID: 12, LocalRoot: "/local", RecordedRoot: "/recorded",
		State: RuntimeStateAnswererDisagreement,
	}}}
	var output bytes.Buffer
	if err := WriteShimTable(&output, report); err != nil {
		t.Fatal(err)
	}
	wantFacts := "answerer_pid=11,record_shim_pid=12,local_root=/local,recorded_root=/recorded"
	if got := output.String(); !bytes.Contains([]byte(got), []byte(wantFacts)) {
		t.Fatalf("WriteShimTable() = %q, want both-side facts %q", got, wantFacts)
	}
}

func TestWriteShimJSONCarriesExplicitUnanchoredMissing(t *testing.T) {
	t.Parallel()

	report := ShimReport{
		Schema: 1, Session: "fleet", Presentation: PresentationUnavailable,
		Agents: []ShimAgent{{Role: "planner", Harness: "claude", Confidence: ConfidenceUnanchored, State: RuntimeStateMissing}},
	}
	var output bytes.Buffer
	if err := WriteShimJSON(&output, report); err != nil {
		t.Fatalf("WriteShimJSON() error = %v", err)
	}
	want := "{\"schema\":1,\"session\":\"fleet\",\"presentation\":\"unavailable\",\"agents\":[{\"role\":\"planner\",\"harness\":\"claude\",\"model\":\"\",\"effort\":\"\",\"directory\":\"\",\"confidence\":\"unanchored\",\"state\":\"missing\"}]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteShimJSON() = %q, want %q", got, want)
	}
}

func TestWriteShimSessionsTableMarksCurrentFleetAndKeepsDefectiveEntryVisible(t *testing.T) {
	t.Parallel()

	report := ShimSessionsReport{Schema: 1, Sessions: []ShimReport{
		{Schema: 1, Session: "alpha", Current: true, Presentation: PresentationGone, Agents: []ShimAgent{{Role: "planner", Harness: "claude", Confidence: ConfidenceUnanchored, State: RuntimeStateMissing}}},
		{Schema: 1, Session: "broken", Presentation: PresentationUnavailable, Agents: []ShimAgent{}, Defect: "fleet.json is not a regular file"},
	}}
	var output bytes.Buffer
	if err := WriteShimSessionsTable(&output, report); err != nil {
		t.Fatalf("WriteShimSessionsTable() error = %v", err)
	}
	want := "   SESSION  ROLE     HARNESS  MODEL    EFFORT   CONFIDENCE  SHIM  CHILD  PRESENTATION  STATE           FACTS\n" +
		"*  alpha    planner  claude   default  default  unanchored               gone          missing         -\n" +
		"   broken                                                                unavailable   invalid-record  fleet.json is not a regular file\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteShimSessionsTable() =\n%q\nwant:\n%q", got, want)
	}
}

func TestWriteShimSessionsTableKeepsEachNoteWithItsFleet(t *testing.T) {
	t.Parallel()

	report := ShimSessionsReport{Schema: 1, Sessions: []ShimReport{
		{Schema: 1, Session: "alpha", Presentation: PresentationGone, Agents: []ShimAgent{{Role: "planner", Harness: "claude", Confidence: ConfidenceUnanchored, State: RuntimeStateMissing}}, Note: "alpha note"},
		{Schema: 1, Session: "beta", Presentation: PresentationGone, Agents: []ShimAgent{{Role: "worker", Harness: "codex", Confidence: ConfidenceUnanchored, State: RuntimeStateMissing}}, Note: "beta note"},
	}}
	var output bytes.Buffer
	if err := WriteShimSessionsTable(&output, report); err != nil {
		t.Fatalf("WriteShimSessionsTable() error = %v", err)
	}
	wantOrder := []string{
		"alpha    planner",
		"note: alpha note",
		"beta     worker",
		"note: beta note",
	}
	remaining := output.String()
	for _, fragment := range wantOrder {
		index := bytes.Index([]byte(remaining), []byte(fragment))
		if index < 0 {
			t.Fatalf("WriteShimSessionsTable() = %q, want %q after prior fragments", output.String(), fragment)
		}
		remaining = remaining[index+len(fragment):]
	}
}

func TestWriteShimSessionsJSONUsesEmptyArraysForDefectiveFleet(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	report := ShimSessionsReport{Schema: 1, Sessions: []ShimReport{{
		Schema: 1, Session: "broken", Presentation: PresentationUnavailable,
		Defect: "bad record",
	}}}
	if err := WriteShimSessionsJSON(&output, report); err != nil {
		t.Fatal(err)
	}
	want := "{\"schema\":1,\"sessions\":[{\"schema\":1,\"session\":\"broken\",\"presentation\":\"unavailable\",\"agents\":[],\"defect\":\"bad record\"}]}\n"
	if got := output.String(); got != want {
		t.Fatalf("WriteShimSessionsJSON() = %q, want %q", got, want)
	}
}
