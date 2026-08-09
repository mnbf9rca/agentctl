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
	"strings"
	"syscall"
)

// MaxBytes is the largest launch template agentctl will read.
const MaxBytes = 1 << 20

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
	value   any
}

func decodeDocument(path string, contents []byte) (Document, error) {
	version, err := inspectTokens(path, contents)
	if err != nil {
		return Document{}, err
	}
	if err := requireVersion(path, version); err != nil {
		return Document{}, err
	}

	var wire documentWire
	if err := strictDecode(contents, &wire); err != nil {
		if isTrailingDocument(err) {
			return Document{}, templateError(path, "", "must contain exactly one JSON document", err)
		}
		return Document{}, decoderError(path, "", err)
	}
	if wire.Session != nil {
		return Document{}, templateError(path, "", `"session" is not a template field; session identity is supplied per invocation with --session`, nil)
	}

	directory, err := optionalString(path, "dir", wire.Directory)
	if err != nil {
		return Document{}, err
	}
	roles, err := decodeRoles(path, wire.Roles)
	if err != nil {
		return Document{}, err
	}
	return Document{Path: path, Directory: directory, Roles: roles}, nil
}

func inspectTokens(path string, contents []byte) (versionState, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var version versionState
	if err := scanValue(decoder, "", true, &version); err != nil {
		var duplicate *duplicateFieldError
		if errors.As(err, &duplicate) {
			return versionState{}, templateError(path, duplicate.Location, fmt.Sprintf("duplicate field %q", duplicate.Field), err)
		}
		return versionState{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
	}
	if version.present {
		value, err := decodeVersionValue(contents)
		if err != nil {
			return versionState{}, templateError(path, "", "invalid JSON: "+err.Error(), err)
		}
		version.value = value
	}
	return version, nil
}

func decodeVersionValue(contents []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var root map[string]json.RawMessage
	if err := decoder.Decode(&root); err != nil {
		return nil, err
	}
	decoder = json.NewDecoder(bytes.NewReader(root["version"]))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}

func scanValue(decoder *json.Decoder, location string, root bool, version *versionState) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		if location == "version" {
			version.value = token
		}
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
			child := fieldLocation(location, key)
			if root && key == "version" {
				version.present = true
			}
			if err := scanValue(decoder, child, false, version); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		index := 0
		for decoder.More() {
			child := fmt.Sprintf("%s[%d]", location, index)
			if err := scanValue(decoder, child, false, version); err != nil {
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
	number, ok := version.value.(json.Number)
	if !ok || string(number) != "1" {
		if ok {
			if integer, err := number.Int64(); err == nil {
				return templateError(path, "", fmt.Sprintf("version %d is not supported by this agentctl (supports 1)", integer), nil)
			}
		}
		return templateError(path, "version", "must be exactly 1, got "+renderJSONValue(version.value), nil)
	}
	return nil
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
	Version   json.RawMessage `json:"version"`
	Directory json.RawMessage `json:"dir"`
	Roles     json.RawMessage `json:"roles"`
	Session   json.RawMessage `json:"session"`
}

type roleWire struct {
	Name    json.RawMessage `json:"role"`
	Harness json.RawMessage `json:"harness"`
	Model   json.RawMessage `json:"model"`
	Effort  json.RawMessage `json:"effort"`
}

type trailingDocumentError struct{ Cause error }

func (e *trailingDocumentError) Error() string { return e.Cause.Error() }
func (e *trailingDocumentError) Unwrap() error { return e.Cause }

func strictDecode(contents []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("found another JSON value")
		}
		return &trailingDocumentError{Cause: err}
	}
	return nil
}

func isTrailingDocument(err error) bool {
	_, ok := err.(*trailingDocumentError)
	return ok
}

func decodeRoles(path string, raw json.RawMessage) ([]Role, error) {
	if raw == nil {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, templateError(path, "roles", "must not be null; omit the field instead", nil)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, templateError(path, "roles", "must be an array", err)
	}

	roles := make([]Role, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		location := fmt.Sprintf("roles[%d]", index)
		trimmed := bytes.TrimSpace(entry)
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return nil, templateError(path, location, "must be an object", nil)
		}
		var wire roleWire
		if err := strictDecode(entry, &wire); err != nil {
			return nil, decoderError(path, location, err)
		}

		name, err := requiredString(path, location+".role", wire.Name)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, templateError(path, location, fmt.Sprintf("duplicate role %q", name), nil)
		}
		seen[name] = struct{}{}

		harness, err := optionalString(path, location+".harness", wire.Harness)
		if err != nil {
			return nil, err
		}
		model, err := optionalString(path, location+".model", wire.Model)
		if err != nil {
			return nil, err
		}
		effort, err := optionalString(path, location+".effort", wire.Effort)
		if err != nil {
			return nil, err
		}
		roles = append(roles, Role{Name: name, Harness: harness, Model: model, Effort: effort})
	}
	return roles, nil
}

func requiredString(path, location string, raw json.RawMessage) (string, error) {
	if raw == nil {
		return "", templateError(path, location, "is required", nil)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", templateError(path, location, "must not be null", nil)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", templateError(path, location, "must be a string", err)
	}
	if value == "" {
		return "", templateError(path, location, "must not be empty", nil)
	}
	return value, nil
}

func optionalString(path, location string, raw json.RawMessage) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, templateError(path, location, "must not be null; omit the field instead", nil)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, templateError(path, location, "must be a string", err)
	}
	if value == "" {
		return nil, templateError(path, location, "must not be empty; omit the field instead", nil)
	}
	return &value, nil
}

func decoderError(path, location string, err error) error {
	reason := strings.TrimPrefix(err.Error(), "json: ")
	return templateError(path, location, reason, err)
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
