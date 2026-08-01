package control

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOperationsExposeOnlyRatifiedArgumentFreePayloads(t *testing.T) {
	want := []Command{
		{Operation: "clear", Payload: "/clear"},
		{Operation: "compact", Payload: "/compact"},
	}

	if got := Operations(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Operations() = %#v, want %#v", got, want)
	}
	for _, command := range Operations() {
		if strings.Contains(command.Operation, "%") || strings.Contains(command.Payload, "%") {
			t.Errorf("registry entry = %#v, want no formatting verbs in any field", command)
		}
		if strings.ContainsAny(command.Payload, " \t\r\n") {
			t.Errorf("payload for %q = %q, want one argument-free literal with no formatting verb", command.Operation, command.Payload)
		}
	}
}

func TestLookupRejectsUnknownOperationAsDefenseInDepth(t *testing.T) {
	_, err := Lookup("rename")
	var unknown *UnknownOperationError
	if !errors.As(err, &unknown) {
		t.Fatalf("Lookup() error = %T %v, want *UnknownOperationError", err, err)
	}
	if unknown.Operation != "rename" {
		t.Fatalf("UnknownOperationError.Operation = %q, want %q", unknown.Operation, "rename")
	}
}
