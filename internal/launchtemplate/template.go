// Package launchtemplate reads the structural values in a launch template.
// It deliberately does not validate fleet values; internal/config owns those
// rules after template and flag values have been merged.
package launchtemplate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"reflect"
	"strconv"
	"strings"
	"syscall"

	"github.com/mnbf9rca/agentctl/skills"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

// MaxBytes is the largest launch template agentctl will read.
const MaxBytes = 1 << 20

const fleetTemplateSchemaPath = skills.Root + "/references/fleet-template.schema.json"

var fleetTemplateSchema = mustCompileFleetTemplateSchema()

func mustCompileFleetTemplateSchema() *jsonschema.Schema {
	contents, err := skills.Tree.ReadFile(fleetTemplateSchemaPath)
	if err != nil {
		panic(fmt.Sprintf("read embedded launch template schema: %v", err))
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(contents))
	if err != nil {
		panic(fmt.Sprintf("decode embedded launch template schema: %v", err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(fleetTemplateSchemaPath, document); err != nil {
		panic(fmt.Sprintf("register embedded launch template schema: %v", err))
	}
	schema, err := compiler.Compile(fleetTemplateSchemaPath)
	if err != nil {
		panic(fmt.Sprintf("compile embedded launch template schema: %v", err))
	}
	return schema
}

// File is the opened descriptor the decoder verifies and then reads.
type File interface {
	io.Reader
	Stat() (fs.FileInfo, error)
	Close() error
}

// OpenFunc opens one caller-supplied template path.
type OpenFunc func(string) (File, error)

// Decoder supplies the template file seam. A nil Open uses a non-blocking,
// read-only os.OpenFile so a writerless FIFO can be opened and refused.
type Decoder struct {
	Open OpenFunc
}

// Role is one structurally decoded role. Optional values remain nil when the
// field was absent; defaults and value semantics are applied only after union.
type Role struct {
	Name    string
	Harness *string
	Model   *string
	Effort  *string
}

// Document is one decoded template source.
type Document struct {
	Path      string
	Directory *string
	Roles     []Role
}

// Error gives every template failure file and optional field context.
type Error struct {
	Path     string
	Location string
	Reason   string
	Cause    error
}

func (e *Error) Error() string {
	prefix := fmt.Sprintf("template %s: ", e.Path)
	if e.Location != "" {
		prefix += e.Location + ": "
	}
	return prefix + e.Reason
}

// Unwrap retains the file or decoder failure underneath the stable message.
func (e *Error) Unwrap() error { return e.Cause }

// Decode reads a template with production filesystem dependencies.
func Decode(path string) (Document, error) {
	return (Decoder{}).Decode(path)
}

// Decode opens, verifies, bounds, and structurally decodes one template.
func (d Decoder) Decode(path string) (Document, error) {
	open := d.Open
	if open == nil {
		open = func(path string) (File, error) {
			return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		}
	}
	file, err := open(path)
	if err != nil {
		return Document{}, templateError(path, "", "cannot open: "+err.Error(), err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return Document{}, templateError(path, "", "cannot inspect opened file: "+err.Error(), err)
	}
	if !info.Mode().IsRegular() {
		return Document{}, templateError(path, "", "must be a regular file; opened object is "+fileKind(info.Mode()), nil)
	}

	contents, err := io.ReadAll(io.LimitReader(file, MaxBytes+1))
	if err != nil {
		return Document{}, templateError(path, "", "cannot read: "+err.Error(), err)
	}
	if len(contents) > MaxBytes {
		return Document{}, templateError(path, "", fmt.Sprintf("exceeds %d-byte limit", MaxBytes), nil)
	}
	return decodeDocument(path, contents)
}

func fileKind(mode fs.FileMode) string {
	switch {
	case mode.IsDir():
		return "directory"
	case mode&fs.ModeNamedPipe != 0:
		return "named pipe"
	case mode&fs.ModeSocket != 0:
		return "socket"
	case mode&fs.ModeDevice != 0 && mode&fs.ModeCharDevice != 0:
		return "character device"
	case mode&fs.ModeDevice != 0:
		return "device"
	default:
		return "non-regular file"
	}
}

type versionState struct {
	present bool
	raw     json.RawMessage
}

type tokenState struct {
	version      versionState
	trailing     bool
	schema       bool
	session      bool
	objectFields map[string][]string
}

func decodeDocument(path string, contents []byte) (Document, error) {
	tokens, err := inspectTokens(path, contents)
	if err != nil {
		return Document{}, err
	}
	if err := requireVersion(path, tokens.version); err != nil {
		return Document{}, err
	}
	instance, err := decodeFirstJSONValue(contents)
	if err != nil {
		return Document{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	if err := selectSchemaFailure(path, instance, tokens); err != nil {
		return Document{}, err
	}

	var wire documentWire
	if err := json.Unmarshal(contents, &wire); err != nil {
		return Document{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	return decodeWire(path, wire), nil
}

func inspectTokens(path string, contents []byte) (tokenState, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	state := tokenState{objectFields: make(map[string][]string)}
	if err := scanValue(decoder, "", true, &state, contents); err != nil {
		var duplicate *duplicateFieldError
		if errors.As(err, &duplicate) {
			return tokenState{}, templateError(path, duplicate.Location, fmt.Sprintf("duplicate field %q", duplicate.Field), err)
		}
		return tokenState{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		state.trailing = true
	}
	return state, nil
}

func scanValue(decoder *json.Decoder, location string, root bool, state *tokenState, contents []byte) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key is %T, want string", keyToken)
			}
			if _, duplicate := seen[key]; duplicate {
				return &duplicateFieldError{Location: location, Field: key}
			}
			seen[key] = struct{}{}

			valueStart := decoder.InputOffset()
			child := fieldLocation(location, key)
			if root {
				switch key {
				case "version":
					state.version.present = true
				case "$schema":
					state.schema = true
				case "session":
					state.session = true
				}
			}
			state.objectFields[location] = append(state.objectFields[location], key)

			if err := scanValue(decoder, child, false, state, contents); err != nil {
				return err
			}
			if root && key == "version" {
				state.version.raw = rawValue(contents[valueStart:decoder.InputOffset()])
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			child := fmt.Sprintf("%s[%d]", location, index)
			if err := scanValue(decoder, child, false, state, contents); err != nil {
				return err
			}
			index++
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
}

func rawValue(value []byte) json.RawMessage {
	value = bytes.TrimSpace(value)
	value = bytes.TrimLeft(value, ": \t\r\n")
	return append(json.RawMessage(nil), bytes.TrimSpace(value)...)
}

func isRoleItemLocation(location string) bool {
	if !strings.HasPrefix(location, "roles[") || !strings.HasSuffix(location, "]") {
		return false
	}
	_, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(location, "roles["), "]"))
	return err == nil
}

type duplicateFieldError struct {
	Location string
	Field    string
}

func (e *duplicateFieldError) Error() string {
	if e.Location == "" {
		return fmt.Sprintf("duplicate field %q", e.Field)
	}
	return fmt.Sprintf("%s: duplicate field %q", e.Location, e.Field)
}

func requireVersion(path string, version versionState) error {
	if !version.present {
		return templateError(path, "version", "is required", nil)
	}
	if string(version.raw) == "1" {
		return nil
	}

	value, err := decodeRawJSON(version.raw)
	if err != nil {
		return templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	number, ok := value.(json.Number)
	if !ok || string(number) != "1" {
		if ok {
			if integer, err := number.Int64(); err == nil {
				return templateError(path, "", fmt.Sprintf("version %d is not supported by this agentctl (supports 1)", integer), nil)
			}
		}
		return templateError(path, "version", "must be exactly 1, got "+renderJSONValue(value), nil)
	}
	return nil
}

func decodeRawJSON(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func renderJSONValue(value any) string {
	if value == nil {
		return "null"
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}

type documentWire struct {
	Version   int        `json:"version"`
	Directory *string    `json:"dir"`
	Roles     []roleWire `json:"roles"`
}

type roleWire struct {
	Name    *string `json:"role"`
	Harness *string `json:"harness"`
	Model   *string `json:"model"`
	Effort  *string `json:"effort"`
}

var roleWireFields = jsonFieldNames(reflect.TypeOf(roleWire{}))

func jsonFieldNames(typ reflect.Type) []string {
	fields := make([]string, 0, typ.NumField())
	for index := range typ.NumField() {
		name, _, _ := strings.Cut(typ.Field(index).Tag.Get("json"), ",")
		fields = append(fields, name)
	}
	return fields
}

func decodeWire(path string, wire documentWire) Document {
	roles := make([]Role, 0, len(wire.Roles))
	for _, role := range wire.Roles {
		roles = append(roles, Role{Name: *role.Name, Harness: role.Harness, Model: role.Model, Effort: role.Effort})
	}
	return Document{Path: path, Directory: wire.Directory, Roles: roles}
}

func decodeFirstJSONValue(contents []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func selectSchemaFailure(path string, instance any, tokens tokenState) error {
	err := fleetTemplateSchema.Validate(instance)
	var leaves []*jsonschema.ValidationError
	if err != nil {
		var validation *jsonschema.ValidationError
		if !errors.As(err, &validation) {
			return templateError(path, "", "does not match the embedded template schema", err)
		}
		leaves = schemaFailures(validation)
	}
	rootUnknown := firstAdditionalProperty(leaves, tokens.objectFields[""], nil)
	if tokens.schema && rootUnknown != "" {
		return schemaKeywordError(path)
	}
	if rootUnknown != "" {
		return templateError(path, "", fmt.Sprintf("unknown field %q", rootUnknown), err)
	}
	if tokens.trailing {
		return templateError(path, "", "must contain exactly one JSON document", nil)
	}
	if tokens.schema {
		return schemaKeywordError(path)
	}
	if tokens.session {
		return sessionFieldError(path)
	}

	if failure := schemaFailureAt(leaves, []string{"dir"}); failure != nil {
		return translateSchemaFailure(path, failure, err)
	}

	root, _ := instance.(map[string]any)
	roles, present := root["roles"].([]any)
	if !present {
		if failure := schemaFailureAt(leaves, []string{"roles"}); failure != nil {
			return translateSchemaFailure(path, failure, err)
		}
		if err == nil {
			return nil
		}
		return templateError(path, "", "does not match the embedded template schema", err)
	}

	seen := make(map[string]struct{}, len(roles))
	for index, role := range roles {
		parts := []string{"roles", strconv.Itoa(index)}
		if failure := schemaFailureAt(leaves, parts); failure != nil {
			return translateSchemaFailure(path, failure, err)
		}

		location := schemaLocation(parts)
		if name := firstAdditionalProperty(leaves, tokens.objectFields[location], parts); name != "" {
			return templateError(path, location, fmt.Sprintf("unknown field %q", name), err)
		}
		if failure := requiredSchemaFailureAt(leaves, parts); failure != nil {
			return translateSchemaFailure(path, failure, err)
		}
		if failure := schemaFailureAt(leaves, append(parts, roleWireFields[0])); failure != nil {
			return translateSchemaFailure(path, failure, err)
		}

		name := role.(map[string]any)[roleWireFields[0]].(string)
		if _, duplicate := seen[name]; duplicate {
			return templateError(path, location, fmt.Sprintf("duplicate role %q", name), nil)
		}
		seen[name] = struct{}{}
		for _, field := range roleWireFields[1:] {
			if failure := schemaFailureAt(leaves, append(parts, field)); failure != nil {
				return translateSchemaFailure(path, failure, err)
			}
		}
	}
	if err == nil {
		return nil
	}
	return templateError(path, "", "does not match the embedded template schema", err)
}

func schemaKeywordError(path string) *Error {
	return templateError(path, "", `"$schema" is not a template field; the schema is applied automatically; see references/fleet-template.schema.json`, nil)
}

func sessionFieldError(path string) *Error {
	return templateError(path, "", `"session" is not a template field; session identity is supplied per invocation with --session`, nil)
}

func schemaFailures(validation *jsonschema.ValidationError) []*jsonschema.ValidationError {
	var failures []*jsonschema.ValidationError
	collectSchemaFailures(validation, &failures)
	return failures
}

func collectSchemaFailures(validation *jsonschema.ValidationError, failures *[]*jsonschema.ValidationError) {
	for _, cause := range validation.Causes {
		collectSchemaFailures(cause, failures)
	}
	if len(validation.Causes) != 0 {
		return
	}
	if _, root := validation.ErrorKind.(*kind.Schema); !root {
		*failures = append(*failures, validation)
	}
}

func schemaFailureAt(failures []*jsonschema.ValidationError, parts []string) *jsonschema.ValidationError {
	for _, failure := range failures {
		if sameSchemaLocation(failure.InstanceLocation, parts) {
			if _, additional := failure.ErrorKind.(*kind.AdditionalProperties); !additional {
				return failure
			}
		}
	}
	return nil
}

func requiredSchemaFailureAt(failures []*jsonschema.ValidationError, parts []string) *jsonschema.ValidationError {
	for _, failure := range failures {
		if sameSchemaLocation(failure.InstanceLocation, parts) {
			if _, required := failure.ErrorKind.(*kind.Required); required {
				return failure
			}
		}
	}
	return nil
}

func firstAdditionalProperty(failures []*jsonschema.ValidationError, fields []string, parts []string) string {
	additional := make(map[string]struct{})
	for _, failure := range failures {
		if !sameSchemaLocation(failure.InstanceLocation, parts) {
			continue
		}
		if issue, ok := failure.ErrorKind.(*kind.AdditionalProperties); ok {
			for _, field := range issue.Properties {
				if field != "$schema" && field != "session" {
					additional[field] = struct{}{}
				}
			}
		}
	}
	for _, field := range fields {
		if _, found := additional[field]; found {
			return field
		}
	}
	return ""
}

func sameSchemaLocation(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func translateSchemaFailure(path string, failure *jsonschema.ValidationError, cause error) error {
	location := schemaLocation(failure.InstanceLocation)
	switch issue := failure.ErrorKind.(type) {
	case *kind.Required:
		return templateError(path, fieldLocation(location, issue.Missing[0]), "is required", cause)
	case *kind.Type:
		return schemaTypeError(path, location, issue.Got, cause)
	case *kind.MinLength:
		if strings.HasSuffix(location, ".role") {
			return templateError(path, location, "must not be empty", cause)
		}
		return templateError(path, location, "must not be empty; omit the field instead", cause)
	case *kind.Pattern:
		if location == "dir" {
			if issue.Got == "" {
				return templateError(path, location, "must not be empty; omit the field instead", cause)
			}
			return templateError(path, location, fmt.Sprintf("path %q must be absolute; omit dir and supply --dir at invocation", issue.Got), cause)
		}
		if issue.Got == "" {
			return templateError(path, location, "must not be empty", cause)
		}
		return templateError(path, location, fmt.Sprintf("value %q must match %s", issue.Got, issue.Want), cause)
	default:
		return templateError(path, location, "does not match the embedded template schema", cause)
	}
}

func schemaTypeError(path, location, got string, cause error) error {
	switch {
	case location == "dir":
		if got == "null" {
			return templateError(path, location, "must not be null; omit the field instead", cause)
		}
		return templateError(path, location, "must be a string", cause)
	case location == "roles":
		if got == "null" {
			return templateError(path, location, "must not be null; omit the field instead", cause)
		}
		return templateError(path, location, "must be an array", cause)
	case isRoleItemLocation(location):
		return templateError(path, location, "must be an object", cause)
	case strings.HasSuffix(location, ".role"):
		if got == "null" {
			return templateError(path, location, "must not be null", cause)
		}
		return templateError(path, location, "must be a string", cause)
	default:
		if got == "null" {
			return templateError(path, location, "must not be null; omit the field instead", cause)
		}
		return templateError(path, location, "must be a string", cause)
	}
}

func schemaLocation(parts []string) string {
	var location string
	for index, part := range parts {
		if index == 1 && parts[0] == "roles" {
			location += "[" + part + "]"
			continue
		}
		location = fieldLocation(location, part)
	}
	return location
}

func templateError(path, location, reason string, cause error) *Error {
	return &Error{Path: path, Location: location, Reason: reason, Cause: cause}
}

func fieldLocation(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}
