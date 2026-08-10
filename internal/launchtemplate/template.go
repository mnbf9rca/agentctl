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
	version     versionState
	trailing    bool
	schema      bool
	session     bool
	rootUnknown *unknownFieldError
	roleUnknown *unknownFieldError
}

type unknownFieldError struct {
	Location string
	Field    string
}

func decodeDocument(path string, contents []byte) (Document, error) {
	tokens, err := inspectTokens(path, contents)
	if err != nil {
		return Document{}, err
	}
	if err := requireVersion(path, tokens.version); err != nil {
		return Document{}, err
	}
	if tokens.trailing {
		return Document{}, templateError(path, "", "must contain exactly one JSON document", nil)
	}
	if tokens.schema {
		return Document{}, templateError(path, "", `"$schema" is not a template field; the schema is applied automatically; see references/fleet-template.schema.json`, nil)
	}
	if tokens.session {
		return Document{}, templateError(path, "", `"session" is not a template field; session identity is supplied per invocation with --session`, nil)
	}
	if tokens.rootUnknown != nil {
		return Document{}, templateError(path, tokens.rootUnknown.Location, fmt.Sprintf("unknown field %q", tokens.rootUnknown.Field), nil)
	}
	if tokens.roleUnknown != nil {
		return Document{}, templateError(path, tokens.roleUnknown.Location, fmt.Sprintf("unknown field %q", tokens.roleUnknown.Field), nil)
	}

	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(contents))
	if err != nil {
		return Document{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	if err := fleetTemplateSchema.Validate(instance); err != nil {
		return Document{}, translateSchemaError(path, err)
	}

	var wire documentWire
	if err := json.Unmarshal(contents, &wire); err != nil {
		return Document{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	return decodeWire(path, wire)
}

func inspectTokens(path string, contents []byte) (tokenState, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var state tokenState
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
				default:
					if !isDocumentField(key) && state.rootUnknown == nil {
						state.rootUnknown = &unknownFieldError{Location: location, Field: key}
					}
				}
			} else if isRoleObjectLocation(location) && !isRoleField(key) && state.roleUnknown == nil {
				state.roleUnknown = &unknownFieldError{Location: location, Field: key}
			}

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

func isDocumentField(field string) bool {
	switch field {
	case "version", "dir", "roles", "session", "$schema":
		return true
	default:
		return false
	}
}

func isRoleField(field string) bool {
	switch field {
	case "role", "harness", "model", "effort":
		return true
	default:
		return false
	}
}

func isRoleObjectLocation(location string) bool {
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

func decodeWire(path string, wire documentWire) (Document, error) {
	roles := make([]Role, 0, len(wire.Roles))
	seen := make(map[string]struct{}, len(wire.Roles))
	for index, role := range wire.Roles {
		location := fmt.Sprintf("roles[%d]", index)
		if role.Name == nil {
			return Document{}, templateError(path, location+".role", "is required", nil)
		}
		if _, duplicate := seen[*role.Name]; duplicate {
			return Document{}, templateError(path, location, fmt.Sprintf("duplicate role %q", *role.Name), nil)
		}
		seen[*role.Name] = struct{}{}
		roles = append(roles, Role{Name: *role.Name, Harness: role.Harness, Model: role.Model, Effort: role.Effort})
	}
	return Document{Path: path, Directory: wire.Directory, Roles: roles}, nil
}

func translateSchemaError(path string, err error) error {
	var validation *jsonschema.ValidationError
	if !errors.As(err, &validation) {
		return templateError(path, "", "does not match the embedded template schema", err)
	}
	failure := firstSchemaFailure(validation)
	if failure == nil {
		return templateError(path, "", "does not match the embedded template schema", err)
	}
	location := schemaLocation(failure.InstanceLocation)
	switch issue := failure.ErrorKind.(type) {
	case *kind.Required:
		return templateError(path, fieldLocation(location, issue.Missing[0]), "is required", err)
	case *kind.Type:
		return schemaTypeError(path, location, issue.Got, err)
	case *kind.MinLength:
		if strings.HasSuffix(location, ".role") {
			return templateError(path, location, "must not be empty", err)
		}
		return templateError(path, location, "must not be empty; omit the field instead", err)
	case *kind.Pattern:
		if location == "dir" {
			if issue.Got == "" {
				return templateError(path, location, "must not be empty; omit the field instead", err)
			}
			return templateError(path, location, fmt.Sprintf("path %q must be absolute; omit dir and supply --dir at invocation", issue.Got), err)
		}
		if issue.Got == "" {
			return templateError(path, location, "must not be empty", err)
		}
		return templateError(path, location, fmt.Sprintf("value %q must match ^[a-z0-9][a-z0-9_-]*$", issue.Got), err)
	case *kind.AdditionalProperties:
		return templateError(path, location, fmt.Sprintf("unknown field %q", issue.Properties[0]), err)
	default:
		return templateError(path, location, "does not match the embedded template schema", err)
	}
}

func firstSchemaFailure(validation *jsonschema.ValidationError) *jsonschema.ValidationError {
	for _, cause := range validation.Causes {
		if failure := firstSchemaFailure(cause); failure != nil {
			return failure
		}
	}
	if _, root := validation.ErrorKind.(*kind.Schema); root {
		return nil
	}
	return validation
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
	case isRoleObjectLocation(location):
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
