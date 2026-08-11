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
