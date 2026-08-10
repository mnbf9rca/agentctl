package shim

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/control"
)

const (
	ShimFrameHeaderBytes     = 4
	ShimFrameMaxPayloadBytes = 4096
	ShimProtocolIOTimeout    = 2 * time.Second
)

// Request is deliberately incapable of carrying PTY bytes or arguments.
type Request struct {
	Version   int    `json:"version"`
	Session   string `json:"session"`
	Role      string `json:"role"`
	Operation string `json:"operation"`
}

// Outcome is a closed response fact.
type Outcome string

const (
	OutcomeDeliverySubmitted            Outcome = "delivery-submitted"
	OutcomeDeliveryCancelledClean       Outcome = "delivery-cancelled-clean"
	OutcomeDeliveryCancelledWithResidue Outcome = "delivery-cancelled-with-residue"
	OutcomeInvalidRecord                Outcome = "invalid-record"
	OutcomeStateRootDisagreement        Outcome = "state-root-disagreement"
	OutcomeProtocolSkew                 Outcome = "protocol-skew"
	OutcomeAnswererDisagreement         Outcome = "answerer-disagreement"
	OutcomeStarting                     Outcome = "starting"
	OutcomeIndeterminateChildStarting   Outcome = "indeterminate-child-starting"
	OutcomeRunning                      Outcome = "running"
	OutcomeOrphan                       Outcome = "orphan"
	OutcomePresentTokenDisagreement     Outcome = "present-token-disagreement"
	OutcomePresentNotOurs               Outcome = "present-not-ours"
	OutcomeCouldNotObserve              Outcome = "could-not-observe"
	OutcomeStaleRecord                  Outcome = "stale-record"
	OutcomeMissing                      Outcome = "missing"
	OutcomeCleanupFailed                Outcome = "cleanup-failed"
	OutcomeConcurrentContender          Outcome = "concurrent-contender"
	OutcomeObservedSelfTarget           Outcome = "observed-self-target"
	OutcomeAncestryUndetermined         Outcome = "ancestry-undetermined"
	OutcomeReadinessTimeout             Outcome = "readiness-timeout"
	OutcomeReadinessObservationFailed   Outcome = "readiness-observation-failed"
	OutcomeChildExitedBeforeReady       Outcome = "child-exited-before-ready"
)

// Response contains only the typed objective fields ratified by protocol v1.
// Pointer fields preserve the distinction between omitted and observed zero.
type Response struct {
	Version        int         `json:"version"`
	Outcome        Outcome     `json:"outcome"`
	State          *string     `json:"state,omitempty"`
	ShimPID        *int        `json:"shim_pid,omitempty"`
	ChildPID       *int        `json:"child_pid,omitempty"`
	BytesWritten   *uint64     `json:"bytes_written,omitempty"`
	SubmitObserved *bool       `json:"submit_observed,omitempty"`
	Cause          *string     `json:"cause,omitempty"`
	Cleanup        *string     `json:"cleanup,omitempty"`
	RecordPath     *string     `json:"record_path,omitempty"`
	LocalRoot      *string     `json:"local_root,omitempty"`
	RecordedRoot   *string     `json:"recorded_root,omitempty"`
	RecordedToken  *StartToken `json:"recorded_token,omitempty"`
	ObservedToken  *StartToken `json:"observed_token,omitempty"`
	CallerPID      *int        `json:"caller_pid,omitempty"`
	TargetPID      *int        `json:"target_pid,omitempty"`
	FinalICANON    *bool       `json:"final_icanon,omitempty"`
	FinalECHO      *bool       `json:"final_echo,omitempty"`
}

type hello struct {
	Version int `json:"version"`
}

// ProtocolSkewKind is the closed version pre-pass result.
type ProtocolSkewKind string

const (
	ProtocolSkewAbsent     ProtocolSkewKind = "absent"
	ProtocolSkewDuplicate  ProtocolSkewKind = "duplicate"
	ProtocolSkewNonInteger ProtocolSkewKind = "non-integer"
	ProtocolSkewForeign    ProtocolSkewKind = "foreign"
)

// ProtocolSkewError is selected by the version-only pre-pass before any
// schema or operation field is interpreted. Token is retained only for the
// closed non-integer/foreign renderers.
type ProtocolSkewError struct {
	Kind  ProtocolSkewKind
	Token string
}

func (e *ProtocolSkewError) Error() string {
	return fmt.Sprintf("protocol version was %s; expected %d", e.CanonicalObserved(), ShimProtocolVersion)
}

// CanonicalObserved returns only a §15.8 permitted version substitution.
func (e *ProtocolSkewError) CanonicalObserved() string {
	switch e.Kind {
	case ProtocolSkewAbsent:
		return "absent"
	case ProtocolSkewDuplicate:
		return "duplicate"
	case ProtocolSkewNonInteger:
		return strconv.Quote(e.Token)
	case ProtocolSkewForeign:
		return e.Token
	default:
		return "absent"
	}
}

// ProtocolSchemaKind is the closed §15.8 version-1 schema cause set.
type ProtocolSchemaKind string

const (
	ProtocolSchemaDuplicateField         ProtocolSchemaKind = "duplicate-field"
	ProtocolSchemaUnknownField           ProtocolSchemaKind = "unknown-field"
	ProtocolSchemaMissingRequiredField   ProtocolSchemaKind = "missing-required-field"
	ProtocolSchemaWrongJSONType          ProtocolSchemaKind = "wrong-json-type"
	ProtocolSchemaOperationNotRegistered ProtocolSchemaKind = "operation-not-registered"
	ProtocolSchemaResponseFieldInvalid   ProtocolSchemaKind = "response-field-invalid"
)

// ProtocolSchemaError reports one structured strict version-1 schema refusal.
type ProtocolSchemaError struct {
	Kind         ProtocolSchemaKind
	Field        string
	ObservedType string
	ExpectedType string
	Operation    string
	Outcome      Outcome
}

func (e *ProtocolSchemaError) Error() string { return e.CanonicalCause() }

// CanonicalCause renders only one exact §15.8 schema cause.
func (e *ProtocolSchemaError) CanonicalCause() string {
	switch e.Kind {
	case ProtocolSchemaDuplicateField:
		return fmt.Sprintf("duplicate field %q", e.Field)
	case ProtocolSchemaUnknownField:
		return fmt.Sprintf("unknown field %q", e.Field)
	case ProtocolSchemaMissingRequiredField:
		return fmt.Sprintf("missing required field %q", e.Field)
	case ProtocolSchemaWrongJSONType:
		return fmt.Sprintf("field %q has JSON type %s; expected %s", e.Field, e.ObservedType, e.ExpectedType)
	case ProtocolSchemaOperationNotRegistered:
		return fmt.Sprintf("operation %q is not registered", e.Operation)
	case ProtocolSchemaResponseFieldInvalid:
		return fmt.Sprintf("response field %q is not valid for outcome %q", e.Field, e.Outcome)
	default:
		return "missing required field \"version\""
	}
}

// ProtocolValueError is a typed version-1 value refusal that is deliberately
// not rendered as one of the closed schema causes.
type ProtocolValueError struct {
	Field  string
	Reason string
}

func (e *ProtocolValueError) Error() string { return fmt.Sprintf("field %q %s", e.Field, e.Reason) }

// JSONErrorKind is the closed lexical/object error set accepted by §15.8.
type JSONErrorKind string

const (
	JSONInvalidUTF8       JSONErrorKind = "invalid-utf8"
	JSONTrailingBytes     JSONErrorKind = "trailing-bytes"
	JSONTopLevelNotObject JSONErrorKind = "top-level-not-object"
	JSONSyntax            JSONErrorKind = "syntax"
)

// JSONError keeps syntax failures single-line and separate from schema facts.
type JSONError struct {
	Kind   JSONErrorKind
	Detail string
}

func (e *JSONError) Error() string { return e.CanonicalCause() }

func (e *JSONError) CanonicalCause() string {
	switch e.Kind {
	case JSONInvalidUTF8:
		return "payload is not valid UTF-8"
	case JSONTrailingBytes:
		return "payload has trailing bytes after its JSON value"
	case JSONTopLevelNotObject:
		return "payload top level is not an object"
	case JSONSyntax:
		if e.Detail == "" {
			return strconv.Quote("unexpected EOF")
		}
		return strconv.Quote(e.Detail)
	default:
		return strconv.Quote("unexpected EOF")
	}
}

type objectSchemaError struct {
	Kind         ProtocolSchemaKind
	Field        string
	ObservedType string
	ExpectedType string
}

func (e *objectSchemaError) Error() string {
	return (&ProtocolSchemaError{
		Kind: e.Kind, Field: e.Field, ObservedType: e.ObservedType, ExpectedType: e.ExpectedType,
	}).CanonicalCause()
}

type integerRangeError struct {
	Field string
	Value string
}

func (e *integerRangeError) Error() string {
	return fmt.Sprintf("field %q integer %s is outside its permitted range", e.Field, e.Value)
}

func asProtocolError(err error) error {
	var schema *objectSchemaError
	if errors.As(err, &schema) {
		return &ProtocolSchemaError{
			Kind: schema.Kind, Field: schema.Field,
			ObservedType: schema.ObservedType, ExpectedType: schema.ExpectedType,
		}
	}
	var integerRange *integerRangeError
	if errors.As(err, &integerRange) {
		return &ProtocolValueError{Field: integerRange.Field, Reason: "is outside its permitted integer range"}
	}
	return err
}

func EncodeHello() ([]byte, error) {
	return json.Marshal(hello{Version: ShimProtocolVersion})
}

func DecodeHello(payload []byte) error {
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return err
	}
	if err := requireCurrentVersion(fields); err != nil {
		return err
	}
	return asProtocolError(requireFields(fields, map[string]bool{"version": true}, []string{"version"}))
}

func EncodeRequest(request Request) ([]byte, error) {
	if err := validateRequest(request); err != nil {
		return nil, err
	}
	return json.Marshal(request)
}

func DecodeRequest(payload []byte) (Request, error) {
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return Request{}, err
	}
	if err := requireCurrentVersion(fields); err != nil {
		return Request{}, err
	}
	allowed := map[string]bool{"version": true, "session": true, "role": true, "operation": true}
	if err := requireFields(fields, allowed, []string{"version", "session", "role", "operation"}); err != nil {
		return Request{}, asProtocolError(err)
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "session": "string", "role": "string", "operation": "string",
	}); err != nil {
		return Request{}, asProtocolError(err)
	}
	var request Request
	if err := unmarshalStrictFields(payload, &request); err != nil {
		return Request{}, err
	}
	if err := validateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func validateRequest(request Request) error {
	if request.Version != ShimProtocolVersion {
		return &ProtocolSkewError{Kind: ProtocolSkewForeign, Token: strconv.Itoa(request.Version)}
	}
	if err := config.ValidateSessionName(request.Session); err != nil {
		return err
	}
	if err := config.ValidateRoleName(request.Role); err != nil {
		return err
	}
	if _, err := control.Lookup(request.Operation); err != nil {
		return &ProtocolSchemaError{Kind: ProtocolSchemaOperationNotRegistered, Operation: request.Operation}
	}
	return nil
}

func EncodeResponse(response Response) ([]byte, error) {
	if response.Version != ShimProtocolVersion {
		return nil, &ProtocolSkewError{Kind: ProtocolSkewForeign, Token: strconv.Itoa(response.Version)}
	}
	if err := validateResponse(response); err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func DecodeResponse(payload []byte) (Response, error) {
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return Response{}, err
	}
	if err := requireCurrentVersion(fields); err != nil {
		return Response{}, err
	}
	allowed := map[string]bool{
		"version": true, "outcome": true, "state": true, "shim_pid": true, "child_pid": true,
		"bytes_written": true, "submit_observed": true, "cause": true, "cleanup": true,
		"record_path": true, "local_root": true, "recorded_root": true, "recorded_token": true,
		"observed_token": true, "caller_pid": true, "target_pid": true, "final_icanon": true, "final_echo": true,
	}
	if err := requireFields(fields, allowed, []string{"version", "outcome"}); err != nil {
		return Response{}, asProtocolError(err)
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "outcome": "string", "state": "string", "shim_pid": "integer",
		"child_pid": "integer", "bytes_written": "integer", "submit_observed": "boolean",
		"cause": "string", "cleanup": "string", "record_path": "string", "local_root": "string",
		"recorded_root": "string", "recorded_token": "object", "observed_token": "object",
		"caller_pid": "integer", "target_pid": "integer", "final_icanon": "boolean", "final_echo": "boolean",
	}); err != nil {
		return Response{}, asProtocolError(err)
	}
	for _, name := range []string{"shim_pid", "child_pid", "caller_pid", "target_pid"} {
		if err := requireJSONIntegerRange(fields, name, big.NewInt(1), big.NewInt(darwinPIDMax)); err != nil {
			return Response{}, asProtocolError(err)
		}
	}
	maxUint64 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	if err := requireJSONIntegerRange(fields, "bytes_written", big.NewInt(0), maxUint64); err != nil {
		return Response{}, asProtocolError(err)
	}
	for _, name := range []string{"recorded_token", "observed_token"} {
		if len(fields[name]) == 1 {
			if err := validateStartTokenJSON(fields[name][0]); err != nil {
				return Response{}, asProtocolError(err)
			}
		}
	}
	var response Response
	if err := unmarshalStrictFields(payload, &response); err != nil {
		return Response{}, err
	}
	if err := validateResponse(response); err != nil {
		return Response{}, err
	}
	return response, nil
}

type responseSchema struct {
	allowed  map[string]bool
	required []string
}

func newResponseSchema(required []string, optional ...string) responseSchema {
	allowed := make(map[string]bool, len(required)+len(optional))
	for _, field := range append(append([]string(nil), required...), optional...) {
		allowed[field] = true
	}
	return responseSchema{allowed: allowed, required: required}
}

var responseSchemas = map[Outcome]responseSchema{
	OutcomeDeliverySubmitted:            newResponseSchema([]string{"bytes_written", "submit_observed"}),
	OutcomeDeliveryCancelledClean:       newResponseSchema(nil),
	OutcomeDeliveryCancelledWithResidue: newResponseSchema([]string{"bytes_written"}),
	OutcomeInvalidRecord:                newResponseSchema([]string{"cause", "record_path"}),
	OutcomeStateRootDisagreement:        newResponseSchema([]string{"local_root", "recorded_root"}),
	OutcomeProtocolSkew:                 newResponseSchema([]string{"cause"}),
	OutcomeAnswererDisagreement:         newResponseSchema([]string{"shim_pid", "target_pid", "cause"}),
	OutcomeStarting:                     newResponseSchema([]string{"state", "shim_pid"}, "child_pid"),
	OutcomeIndeterminateChildStarting:   newResponseSchema([]string{"state", "shim_pid", "record_path"}),
	OutcomeRunning:                      newResponseSchema([]string{"state", "shim_pid", "child_pid"}),
	OutcomeOrphan:                       newResponseSchema([]string{"shim_pid", "child_pid", "recorded_token", "observed_token"}),
	OutcomePresentTokenDisagreement:     newResponseSchema([]string{"child_pid", "recorded_token", "observed_token"}),
	OutcomePresentNotOurs:               newResponseSchema([]string{"child_pid"}),
	OutcomeCouldNotObserve:              newResponseSchema([]string{"child_pid", "cause"}),
	OutcomeStaleRecord:                  newResponseSchema([]string{"child_pid"}),
	OutcomeMissing:                      newResponseSchema(nil),
	OutcomeCleanupFailed:                newResponseSchema([]string{"cause", "cleanup", "child_pid"}),
	OutcomeConcurrentContender:          newResponseSchema([]string{"shim_pid", "cause"}),
	OutcomeObservedSelfTarget:           newResponseSchema([]string{"caller_pid", "target_pid"}),
	OutcomeAncestryUndetermined:         newResponseSchema([]string{"caller_pid", "target_pid", "cause"}),
	OutcomeReadinessTimeout:             newResponseSchema([]string{"child_pid", "final_icanon", "final_echo", "cleanup"}),
	OutcomeReadinessObservationFailed:   newResponseSchema([]string{"child_pid", "cause", "cleanup"}),
	OutcomeChildExitedBeforeReady:       newResponseSchema([]string{"child_pid", "cleanup"}),
}

func validateResponse(response Response) error {
	schema, ok := responseSchemas[response.Outcome]
	if !ok {
		return &ProtocolValueError{Field: "outcome", Reason: fmt.Sprintf("has unknown value %q", response.Outcome)}
	}
	present := responsePresentFields(response)
	for field := range present {
		if !schema.allowed[field] {
			return &ProtocolSchemaError{Kind: ProtocolSchemaResponseFieldInvalid, Field: field, Outcome: response.Outcome}
		}
	}
	for _, required := range schema.required {
		if !present[required] {
			return &ProtocolSchemaError{Kind: ProtocolSchemaMissingRequiredField, Field: required}
		}
	}
	for name, pid := range map[string]*int{
		"shim_pid": response.ShimPID, "child_pid": response.ChildPID,
		"caller_pid": response.CallerPID, "target_pid": response.TargetPID,
	} {
		if pid != nil && !validDarwinPID(*pid) {
			return &ProtocolValueError{Field: name, Reason: "must be a positive signed Darwin pid_t"}
		}
	}
	for name, token := range map[string]*StartToken{
		"recorded_token": response.RecordedToken, "observed_token": response.ObservedToken,
	} {
		if token != nil && (token.Sec <= 0 || token.Usec < 0 || token.Usec >= 1_000_000) {
			return &ProtocolValueError{Field: name, Reason: "must be a raw timeval"}
		}
	}
	switch response.Outcome {
	case OutcomeDeliverySubmitted:
		if *response.BytesWritten == 0 {
			return &ProtocolValueError{Field: "bytes_written", Reason: `must be positive for outcome "delivery-submitted"`}
		}
		if !*response.SubmitObserved {
			return &ProtocolValueError{Field: "submit_observed", Reason: `must be true for outcome "delivery-submitted"`}
		}
	case OutcomeDeliveryCancelledWithResidue:
		if *response.BytesWritten == 0 {
			return &ProtocolValueError{Field: "bytes_written", Reason: `must be positive for outcome "delivery-cancelled-with-residue"`}
		}
	case OutcomeStarting:
		if *response.State != string(RecordStateChildStarting) && *response.State != string(RecordStateChildRecorded) {
			return &ProtocolValueError{Field: "state", Reason: fmt.Sprintf("has invalid value %q for outcome %q", *response.State, response.Outcome)}
		}
	case OutcomeIndeterminateChildStarting:
		if *response.State != string(RecordStateChildStarting) {
			return &ProtocolValueError{Field: "state", Reason: fmt.Sprintf("has invalid value %q for outcome %q", *response.State, response.Outcome)}
		}
	case OutcomeRunning:
		if *response.State != string(OutcomeRunning) {
			return &ProtocolValueError{Field: "state", Reason: fmt.Sprintf("has invalid value %q for outcome %q", *response.State, response.Outcome)}
		}
	case OutcomeStateRootDisagreement:
		if *response.LocalRoot == *response.RecordedRoot {
			return &ProtocolValueError{Field: "local_root", Reason: "must differ from recorded_root for state-root-disagreement"}
		}
	case OutcomeOrphan:
		if !response.RecordedToken.Equal(*response.ObservedToken) {
			return &ProtocolValueError{Field: "observed_token", Reason: "must equal recorded_token for orphan"}
		}
	case OutcomePresentTokenDisagreement:
		if response.RecordedToken.Equal(*response.ObservedToken) {
			return &ProtocolValueError{Field: "observed_token", Reason: "must differ from recorded_token for present-token-disagreement"}
		}
	}
	return nil
}

func responsePresentFields(response Response) map[string]bool {
	present := make(map[string]bool)
	values := []struct {
		name    string
		present bool
	}{
		{"state", response.State != nil}, {"shim_pid", response.ShimPID != nil}, {"child_pid", response.ChildPID != nil},
		{"bytes_written", response.BytesWritten != nil}, {"submit_observed", response.SubmitObserved != nil},
		{"cause", response.Cause != nil}, {"cleanup", response.Cleanup != nil}, {"record_path", response.RecordPath != nil},
		{"local_root", response.LocalRoot != nil}, {"recorded_root", response.RecordedRoot != nil},
		{"recorded_token", response.RecordedToken != nil}, {"observed_token", response.ObservedToken != nil},
		{"caller_pid", response.CallerPID != nil}, {"target_pid", response.TargetPID != nil},
		{"final_icanon", response.FinalICANON != nil}, {"final_echo", response.FinalECHO != nil},
	}
	for _, value := range values {
		if value.present {
			present[value.name] = true
		}
	}
	return present
}

func decodeJSONObject(payload []byte) (map[string][]json.RawMessage, error) {
	if !utf8.Valid(payload) {
		return nil, &JSONError{Kind: JSONInvalidUTF8}
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return nil, canonicalJSONError(err)
	}
	delimiter, ok := first.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, &JSONError{Kind: JSONTopLevelNotObject}
	}
	fields := make(map[string][]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, canonicalJSONError(err)
		}
		name, ok := token.(string)
		if !ok {
			return nil, &JSONError{Kind: JSONSyntax, Detail: "object field name is not a string"}
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, canonicalJSONError(err)
		}
		fields[name] = append(fields[name], append(json.RawMessage(nil), raw...))
	}
	if _, err := decoder.Token(); err != nil {
		return nil, canonicalJSONError(err)
	}
	if trailing := bytes.TrimSpace(payload[decoder.InputOffset():]); len(trailing) != 0 {
		return nil, &JSONError{Kind: JSONTrailingBytes}
	}
	return fields, nil
}

func canonicalJSONError(err error) error {
	detail := err.Error()
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		detail = "unexpected EOF"
	}
	return &JSONError{Kind: JSONSyntax, Detail: detail}
}

func requireCurrentVersion(fields map[string][]json.RawMessage) error {
	versions := fields["version"]
	if len(versions) == 0 {
		return &ProtocolSkewError{Kind: ProtocolSkewAbsent}
	}
	if len(versions) != 1 {
		return &ProtocolSkewError{Kind: ProtocolSkewDuplicate}
	}
	raw := strings.TrimSpace(string(versions[0]))
	if raw == "" || strings.ContainsAny(raw, ".eE") || !isJSONInteger(versions[0]) {
		return &ProtocolSkewError{Kind: ProtocolSkewNonInteger, Token: raw}
	}
	version, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return &ProtocolSkewError{Kind: ProtocolSkewNonInteger, Token: raw}
	}
	if version.Cmp(big.NewInt(ShimProtocolVersion)) != 0 {
		return &ProtocolSkewError{Kind: ProtocolSkewForeign, Token: version.String()}
	}
	return nil
}

func requireFields(fields map[string][]json.RawMessage, allowed map[string]bool, required []string) error {
	for name, values := range fields {
		if len(values) > 1 {
			return &objectSchemaError{Kind: ProtocolSchemaDuplicateField, Field: name}
		}
		if !allowed[name] {
			return &objectSchemaError{Kind: ProtocolSchemaUnknownField, Field: name}
		}
	}
	for _, name := range required {
		if len(fields[name]) == 0 {
			return &objectSchemaError{Kind: ProtocolSchemaMissingRequiredField, Field: name}
		}
	}
	return nil
}

func requireJSONTypes(fields map[string][]json.RawMessage, expected map[string]string) error {
	for name, expectedType := range expected {
		values := fields[name]
		if len(values) != 1 {
			continue
		}
		observed := jsonType(values[0])
		if expectedType == "integer" && observed == "number" && isJSONInteger(values[0]) {
			continue
		}
		if observed != expectedType {
			return &objectSchemaError{
				Kind: ProtocolSchemaWrongJSONType, Field: name,
				ObservedType: observed, ExpectedType: expectedType,
			}
		}
	}
	return nil
}

func requireJSONIntegerRange(fields map[string][]json.RawMessage, name string, minimum, maximum *big.Int) error {
	values := fields[name]
	if len(values) != 1 {
		return nil
	}
	raw := strings.TrimSpace(string(values[0]))
	value, ok := new(big.Int).SetString(raw, 10)
	if !ok || value.Cmp(minimum) < 0 || value.Cmp(maximum) > 0 {
		return &integerRangeError{Field: name, Value: raw}
	}
	return nil
}

func validateStartTokenJSON(raw json.RawMessage) error {
	fields, err := decodeJSONObject(raw)
	if err != nil {
		return err
	}
	allowed := map[string]bool{"sec": true, "usec": true}
	if err := requireFields(fields, allowed, []string{"sec", "usec"}); err != nil {
		return err
	}
	if err := requireJSONTypes(fields, map[string]string{"sec": "integer", "usec": "integer"}); err != nil {
		return err
	}
	if err := requireJSONIntegerRange(fields, "sec", big.NewInt(1), big.NewInt(1<<63-1)); err != nil {
		return err
	}
	return requireJSONIntegerRange(fields, "usec", big.NewInt(0), big.NewInt(999_999))
}

func jsonType(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "invalid"
	}
	switch trimmed[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

func isJSONInteger(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	if value == "" || strings.ContainsAny(value, ".eE") {
		return false
	}
	_, ok := new(big.Int).SetString(value, 10)
	return ok
}

func unmarshalStrictFields(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return &ProtocolValueError{Field: "payload", Reason: "could not be decoded after strict validation"}
	}
	return nil
}

// WriteFrame writes one bounded big-endian length-prefixed JSON object under
// one absolute deadline covering header and payload.
func WriteFrame(connection net.Conn, payload []byte) (int, error) {
	if len(payload) == 0 {
		return 0, errors.New("zero payload length")
	}
	if len(payload) > ShimFrameMaxPayloadBytes {
		return 0, fmt.Errorf("payload length %d exceeds %d", len(payload), ShimFrameMaxPayloadBytes)
	}
	if _, err := decodeJSONObject(payload); err != nil {
		return 0, err
	}
	var header [ShimFrameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	frame := append(header[:], payload...)
	deadline := time.Now().Add(ShimProtocolIOTimeout)
	if err := connection.SetWriteDeadline(deadline); err != nil {
		return 0, fmt.Errorf("frame write failed after 0 of %d bytes: %w", len(frame), err)
	}
	written := 0
	for written < len(frame) {
		n, err := connection.Write(frame[written:])
		written += n
		if err != nil {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				return written, fmt.Errorf("frame write exceeded 2s after %d of %d bytes", written, len(frame))
			}
			return written, fmt.Errorf("frame write failed after %d of %d bytes: %w", written, len(frame), err)
		}
	}
	return written, nil
}

// ReadFrame reads one bounded big-endian length-prefixed JSON object under one
// absolute deadline covering header and payload.
func ReadFrame(connection net.Conn) ([]byte, error) {
	deadline := time.Now().Add(ShimProtocolIOTimeout)
	if err := connection.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("frame read failed during header after 0 of %d bytes: %w", ShimFrameHeaderBytes, err)
	}
	var header [ShimFrameHeaderBytes]byte
	if err := readFramePart(connection, header[:], "header", ShimFrameHeaderBytes); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header[:])
	if length == 0 {
		return nil, errors.New("zero payload length")
	}
	if length > ShimFrameMaxPayloadBytes {
		return nil, fmt.Errorf("payload length %d exceeds %d", length, ShimFrameMaxPayloadBytes)
	}
	payload := make([]byte, int(length))
	if err := readFramePart(connection, payload, "payload", int(length)); err != nil {
		return nil, err
	}
	if _, err := decodeJSONObject(payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func readFramePart(connection net.Conn, target []byte, phase string, total int) error {
	read := 0
	for read < len(target) {
		n, err := connection.Read(target[read:])
		read += n
		if err == nil {
			continue
		}
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return fmt.Errorf("frame read exceeded 2s during %s after %d of %d bytes", phase, read, total)
		}
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("EOF after %d of %d %s bytes", read, total, phase)
		}
		return fmt.Errorf("frame read failed during %s after %d of %d bytes: %w", phase, read, total, err)
	}
	return nil
}
