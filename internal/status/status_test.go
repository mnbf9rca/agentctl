package status

import (
	"reflect"
	"testing"
)

func TestRuntimeStatesMatchApprovedPrecedenceVocabulary(t *testing.T) {
	t.Parallel()

	want := []RuntimeState{
		RuntimeStateInvalidRecord,
		RuntimeStateRootDisagreement,
		RuntimeStateProtocolSkew,
		RuntimeStateAnswererDisagreement,
		RuntimeStateCleanupFailed,
		RuntimeStateConcurrentContender,
		RuntimeStateStarting,
		RuntimeStateStopping,
		RuntimeStateStopped,
		RuntimeStateIndeterminateChildStarting,
		RuntimeStateRunning,
		RuntimeStateOrphan,
		RuntimeStatePresentTokenDisagreement,
		RuntimeStatePresentNotOurs,
		RuntimeStateCouldNotObserve,
		RuntimeStateStaleRecord,
		RuntimeStateMissing,
	}
	if got := RuntimeStates(); !reflect.DeepEqual(got, want) {
		t.Fatalf("RuntimeStates() = %#v, want precedence order %#v", got, want)
	}
	got := RuntimeStates()
	got[0] = "mutated"
	if fresh := RuntimeStates(); fresh[0] != RuntimeStateInvalidRecord {
		t.Fatalf("RuntimeStates() returned shared storage: %#v", fresh)
	}
}
