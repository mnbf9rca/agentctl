// Package control dispatches predefined terminal-control operations.
package control

import "fmt"

// Command pairs one public operation name with its complete literal payload.
// Registry entries are permanently argument-free: operations that interpolate
// or append caller-supplied text are not admissible.
type Command struct {
	Operation string
	Kind      OperationKind
	Payload   string
}

// OperationKind separates payload-bearing terminal operations from lifecycle
// control operations that are structurally incapable of producing PTY input.
type OperationKind string

const (
	OperationPayload OperationKind = "payload"
	OperationControl OperationKind = "control"
)

const (
	clearPayload   = "/clear"
	compactPayload = "/compact"
)

var commands = [...]Command{
	{Operation: "clear", Kind: OperationPayload, Payload: clearPayload},
	{Operation: "compact", Kind: OperationPayload, Payload: compactPayload},
	{Operation: "observe", Kind: OperationControl},
	{Operation: "stop", Kind: OperationControl},
}

// UnknownOperationError reports an operation outside the closed registry.
type UnknownOperationError struct {
	Operation string
}

func (e *UnknownOperationError) Error() string {
	return fmt.Sprintf("unknown control operation %q", e.Operation)
}

// Operations returns a copy of the closed command registry.
func Operations() []Command {
	operations := make([]Command, len(commands))
	copy(operations, commands[:])
	return operations
}

// Lookup returns the registered command for operation.
func Lookup(operation string) (Command, error) {
	for _, command := range commands {
		if command.Operation == operation {
			return command, nil
		}
	}
	return Command{}, &UnknownOperationError{Operation: operation}
}
