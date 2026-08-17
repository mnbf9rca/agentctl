package control

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestOperationsExposeOnlyRatifiedArgumentFreePayloads(t *testing.T) {
	want := []Command{
		{Operation: "clear", Kind: OperationPayload, Payload: "/clear"},
		{Operation: "compact", Kind: OperationPayload, Payload: "/compact"},
		{Operation: "observe", Kind: OperationControl},
		{Operation: "stop", Kind: OperationControl},
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

func TestControlOperationsCannotRepresentPTYInput(t *testing.T) {
	for _, operation := range []string{"observe", "stop"} {
		command, err := Lookup(operation)
		if err != nil {
			t.Fatalf("Lookup(%q) error = %v", operation, err)
		}
		if command.Kind != OperationControl || command.Payload != "" {
			t.Fatalf("Lookup(%q) = %#v, want control operation with no payload", operation, command)
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
