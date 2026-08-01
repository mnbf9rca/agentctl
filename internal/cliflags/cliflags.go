// Package cliflags provides stdlib flag parsing with duplicate-option rejection.
package cliflags

import (
	"flag"
	"fmt"
	"io"
	"strconv"
)

// Set is a command-specific flag set.
type Set struct {
	set    *flag.FlagSet
	values map[string]*onceValue
}

// New constructs a flag set that returns parse errors without printing them.
func New(name string) *Set {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	set.SetOutput(io.Discard)
	return &Set{set: set, values: make(map[string]*onceValue)}
}

// String defines a string option and returns its destination.
func (s *Set) String(name, value, usage string) *string {
	destination := new(string)
	*destination = value
	tracked := &onceValue{name: name, value: &stringValue{destination: destination}}
	s.values[name] = tracked
	s.set.Var(tracked, name, usage)
	return destination
}

// Bool defines a boolean option and returns its destination.
func (s *Set) Bool(name string, value bool, usage string) *bool {
	destination := new(bool)
	*destination = value
	tracked := &onceValue{name: name, value: &boolValue{destination: destination}}
	s.values[name] = tracked
	s.set.Var(tracked, name, usage)
	return destination
}

// Parse parses command arguments.
func (s *Set) Parse(arguments []string) error {
	return s.set.Parse(arguments)
}

// Args returns positional arguments remaining after option parsing.
func (s *Set) Args() []string {
	return s.set.Args()
}

// WasSet reports whether an option appeared in the parsed arguments.
func (s *Set) WasSet(name string) bool {
	value, ok := s.values[name]
	return ok && value.seen
}

type onceValue struct {
	name  string
	value flag.Value
	seen  bool
}

func (v *onceValue) Set(value string) error {
	if v.seen {
		return fmt.Errorf("--%s provided more than once", v.name)
	}
	v.seen = true
	return v.value.Set(value)
}

func (v *onceValue) String() string {
	return v.value.String()
}

func (v *onceValue) IsBoolFlag() bool {
	boolean, ok := v.value.(interface{ IsBoolFlag() bool })
	return ok && boolean.IsBoolFlag()
}

type stringValue struct {
	destination *string
}

func (v *stringValue) Set(value string) error {
	*v.destination = value
	return nil
}

func (v *stringValue) String() string {
	return *v.destination
}

type boolValue struct {
	destination *bool
}

func (v *boolValue) Set(value string) error {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return err
	}
	*v.destination = parsed
	return nil
}

func (v *boolValue) String() string {
	return strconv.FormatBool(*v.destination)
}

func (v *boolValue) IsBoolFlag() bool {
	return true
}
