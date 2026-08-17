//go:build darwin

package fleet

import (
	"reflect"
	"testing"

	"github.com/mnbf9rca/agentctl/internal/status"
)

func TestShimStatusFleetReaderAdaptsDurableRosterWithoutChangingProvenance(t *testing.T) {
	t.Parallel()

	record := ShimFleetRecord{
		Version: 1, Session: "fleet", Directory: "/work", Presentation: PresentationTmux, Roster: []string{"planner"},
		Roles: map[string]ShimFleetRoleRecord{"planner": {Harness: "claude", Model: "fable", Effort: "max"}},
	}
	reader := NewShimStatusFleetReader(shimStatusFleetRecordsFake{record: record})
	got, err := reader.Read("fleet")
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	want := status.ShimFleetRecord{
		Version: 1, Session: "fleet", Directory: "/work", Roster: []string{"planner"},
		Roles: map[string]status.ShimFleetRole{"planner": {Harness: "claude", Model: "fable", Effort: "max"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() = %#v, want %#v", got, want)
	}
	got.Roster[0] = "mutated"
	got.Roles["planner"] = status.ShimFleetRole{}
	if record.Roster[0] != "planner" || record.Roles["planner"].Harness != "claude" {
		t.Fatalf("adapter exposed mutable fleet record storage: %#v", record)
	}
}

type shimStatusFleetRecordsFake struct{ record ShimFleetRecord }

func (f shimStatusFleetRecordsFake) Create(ShimFleetRecord) error                        { return nil }
func (f shimStatusFleetRecordsFake) Read(string) (ShimFleetRecord, error)                { return f.record, nil }
func (f shimStatusFleetRecordsFake) ReplaceOwned(ShimFleetRecord, ShimFleetRecord) error { return nil }
func (f shimStatusFleetRecordsFake) RemoveOwned(ShimFleetRecord) error                   { return nil }
