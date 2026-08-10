package shim

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// ProtocolSkewError is selected by the version-only pre-pass before any
// schema or operation field is interpreted.
type ProtocolSkewError struct {
	Observed string
}

func (e *ProtocolSkewError) Error() string {
	return fmt.Sprintf("protocol version was %s; expected %d", e.Observed, ShimProtocolVersion)
}

// ProtocolSchemaError reports a strict version-1 schema refusal.
type ProtocolSchemaError struct {
	Reason string
}

func (e *ProtocolSchemaError) Error() string { return e.Reason }

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
	return requireFields(fields, map[string]bool{"version": true}, []string{"version"})
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
		return Request{}, err
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "session": "string", "role": "string", "operation": "string",
	}); err != nil {
		return Request{}, err
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
		return &ProtocolSkewError{Observed: strconv.Itoa(request.Version)}
	}
	if err := config.ValidateSessionName(request.Session); err != nil {
		return err
	}
	if err := config.ValidateRoleName(request.Role); err != nil {
		return err
	}
	if _, err := control.Lookup(request.Operation); err != nil {
		return &ProtocolSchemaError{Reason: fmt.Sprintf("operation %q is not registered", request.Operation)}
	}
	return nil
}

func EncodeResponse(response Response) ([]byte, error) {
	if response.Version != ShimProtocolVersion {
		return nil, &ProtocolSkewError{Observed: strconv.Itoa(response.Version)}
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
		return Response{}, err
	}
	if err := requireJSONTypes(fields, map[string]string{
		"version": "integer", "outcome": "string", "state": "string", "shim_pid": "integer",
		"child_pid": "integer", "bytes_written": "integer", "submit_observed": "boolean",
		"cause": "string", "cleanup": "string", "record_path": "string", "local_root": "string",
		"recorded_root": "string", "recorded_token": "object", "observed_token": "object",
		"caller_pid": "integer", "target_pid": "integer", "final_icanon": "boolean", "final_echo": "boolean",
	}); err != nil {
		return Response{}, err
	}
	for _, name := range []string{"recorded_token", "observed_token"} {
		if len(fields[name]) == 1 {
			if err := validateStartTokenJSON(fields[name][0]); err != nil {
				return Response{}, err
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

var responseFieldSets = map[Outcome]map[string]bool{
	OutcomeDeliverySubmitted:            {"bytes_written": true, "submit_observed": true},
	OutcomeDeliveryCancelledClean:       {},
	OutcomeDeliveryCancelledWithResidue: {"bytes_written": true},
	OutcomeInvalidRecord:                {"cause": true, "record_path": true},
	OutcomeStateRootDisagreement:        {"local_root": true, "recorded_root": true},
	OutcomeProtocolSkew:                 {"cause": true},
	OutcomeAnswererDisagreement:         {"shim_pid": true, "target_pid": true, "cause": true},
	OutcomeStarting:                     {"state": true, "shim_pid": true, "child_pid": true},
	OutcomeIndeterminateChildStarting:   {"state": true, "shim_pid": true, "record_path": true},
	OutcomeRunning:                      {"state": true, "shim_pid": true, "child_pid": true},
	OutcomeOrphan:                       {"shim_pid": true, "child_pid": true, "recorded_token": true, "observed_token": true},
	OutcomePresentTokenDisagreement:     {"child_pid": true, "recorded_token": true, "observed_token": true},
	OutcomePresentNotOurs:               {"child_pid": true},
	OutcomeCouldNotObserve:              {"child_pid": true, "cause": true},
	OutcomeStaleRecord:                  {"child_pid": true},
	OutcomeMissing:                      {},
	OutcomeCleanupFailed:                {"cause": true, "cleanup": true, "child_pid": true},
	OutcomeConcurrentContender:          {"shim_pid": true, "cause": true},
	OutcomeObservedSelfTarget:           {"caller_pid": true, "target_pid": true},
	OutcomeAncestryUndetermined:         {"caller_pid": true, "target_pid": true, "cause": true},
	OutcomeReadinessTimeout:             {"child_pid": true, "final_icanon": true, "final_echo": true, "cleanup": true},
	OutcomeReadinessObservationFailed:   {"child_pid": true, "cause": true, "cleanup": true},
	OutcomeChildExitedBeforeReady:       {"child_pid": true, "cleanup": true},
}

var responseRequiredFields = map[Outcome][]string{
	OutcomeDeliverySubmitted:            {"bytes_written", "submit_observed"},
	OutcomeDeliveryCancelledClean:       {},
	OutcomeDeliveryCancelledWithResidue: {"bytes_written"},
	OutcomeInvalidRecord:                {"cause", "record_path"},
	OutcomeStateRootDisagreement:        {"local_root", "recorded_root"},
	OutcomeProtocolSkew:                 {"cause"},
	OutcomeAnswererDisagreement:         {"shim_pid", "target_pid", "cause"},
	OutcomeStarting:                     {"state", "shim_pid"},
	OutcomeIndeterminateChildStarting:   {"state", "shim_pid", "record_path"},
	OutcomeRunning:                      {"state", "shim_pid", "child_pid"},
	OutcomeOrphan:                       {"shim_pid", "child_pid", "recorded_token", "observed_token"},
	OutcomePresentTokenDisagreement:     {"child_pid", "recorded_token", "observed_token"},
	OutcomePresentNotOurs:               {"child_pid"},
	OutcomeCouldNotObserve:              {"child_pid", "cause"},
	OutcomeStaleRecord:                  {"child_pid"},
	OutcomeMissing:                      {},
	OutcomeCleanupFailed:                {"cause", "cleanup", "child_pid"},
	OutcomeConcurrentContender:          {"shim_pid", "cause"},
	OutcomeObservedSelfTarget:           {"caller_pid", "target_pid"},
	OutcomeAncestryUndetermined:         {"caller_pid", "target_pid", "cause"},
	OutcomeReadinessTimeout:             {"child_pid", "final_icanon", "final_echo", "cleanup"},
	OutcomeReadinessObservationFailed:   {"child_pid", "cause", "cleanup"},
	OutcomeChildExitedBeforeReady:       {"child_pid", "cleanup"},
}

func validateResponse(response Response) error {
	allowed, ok := responseFieldSets[response.Outcome]
	if !ok {
		return &ProtocolSchemaError{Reason: fmt.Sprintf("unknown outcome %q", response.Outcome)}
	}
	present := responsePresentFields(response)
	for field := range present {
		if !allowed[field] {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("response field %q is not valid for outcome %q", field, response.Outcome)}
		}
	}
	for _, required := range responseRequiredFields[response.Outcome] {
		if !present[required] {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("missing required field %q", required)}
		}
	}
	for name, pid := range map[string]*int{
		"shim_pid": response.ShimPID, "child_pid": response.ChildPID,
		"caller_pid": response.CallerPID, "target_pid": response.TargetPID,
	} {
		if pid != nil && *pid <= 0 {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("field %q must be a positive integer", name)}
		}
	}
	for name, token := range map[string]*StartToken{
		"recorded_token": response.RecordedToken, "observed_token": response.ObservedToken,
	} {
		if token != nil && (token.Sec <= 0 || token.Usec < 0 || token.Usec >= 1_000_000) {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("field %q must be a raw timeval", name)}
		}
	}
	switch response.Outcome {
	case OutcomeDeliverySubmitted:
		if *response.BytesWritten == 0 {
			return &ProtocolSchemaError{Reason: `field "bytes_written" must be positive for outcome "delivery-submitted"`}
		}
		if !*response.SubmitObserved {
			return &ProtocolSchemaError{Reason: `field "submit_observed" must be true for outcome "delivery-submitted"`}
		}
	case OutcomeDeliveryCancelledWithResidue:
		if *response.BytesWritten == 0 {
			return &ProtocolSchemaError{Reason: `field "bytes_written" must be positive for outcome "delivery-cancelled-with-residue"`}
		}
	case OutcomeStarting:
		if *response.State != string(RecordStateChildStarting) && *response.State != string(RecordStateChildRecorded) {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("field %q has invalid state %q for outcome %q", "state", *response.State, response.Outcome)}
		}
	case OutcomeIndeterminateChildStarting:
		if *response.State != string(RecordStateChildStarting) {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("field %q has invalid state %q for outcome %q", "state", *response.State, response.Outcome)}
		}
	case OutcomeRunning:
		if *response.State != string(OutcomeRunning) {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("field %q has invalid state %q for outcome %q", "state", *response.State, response.Outcome)}
		}
	case OutcomeStateRootDisagreement:
		if *response.LocalRoot == *response.RecordedRoot {
			return &ProtocolSchemaError{Reason: "state-root-disagreement requires different local_root and recorded_root values"}
		}
	case OutcomeOrphan:
		if !response.RecordedToken.Equal(*response.ObservedToken) {
			return &ProtocolSchemaError{Reason: "orphan requires equal recorded_token and observed_token values"}
		}
	case OutcomePresentTokenDisagreement:
		if response.RecordedToken.Equal(*response.ObservedToken) {
			return &ProtocolSchemaError{Reason: "present-token-disagreement requires different recorded_token and observed_token values"}
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
		return nil, errors.New("payload is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, ok := first.(json.Delim)
	if !ok || delimiter != '{' {
		return nil, errors.New("payload top level is not an object")
	}
	fields := make(map[string][]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("object field name is not a string")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, err
		}
		fields[name] = append(fields[name], append(json.RawMessage(nil), raw...))
	}
	if _, err := decoder.Token(); err != nil {
		return nil, err
	}
	if trailing := bytes.TrimSpace(payload[decoder.InputOffset():]); len(trailing) != 0 {
		return nil, errors.New("payload has trailing bytes after its JSON value")
	}
	return fields, nil
}

func requireCurrentVersion(fields map[string][]json.RawMessage) error {
	versions := fields["version"]
	if len(versions) == 0 {
		return &ProtocolSkewError{Observed: "absent"}
	}
	if len(versions) != 1 {
		return &ProtocolSkewError{Observed: "duplicate"}
	}
	raw := strings.TrimSpace(string(versions[0]))
	if raw == "" || strings.ContainsAny(raw, ".eE") {
		return &ProtocolSkewError{Observed: raw}
	}
	version, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return &ProtocolSkewError{Observed: raw}
	}
	if version != ShimProtocolVersion {
		return &ProtocolSkewError{Observed: strconv.FormatInt(version, 10)}
	}
	return nil
}

func requireFields(fields map[string][]json.RawMessage, allowed map[string]bool, required []string) error {
	for name, values := range fields {
		if len(values) > 1 {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("duplicate field %q", name)}
		}
		if !allowed[name] {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("unknown field %q", name)}
		}
	}
	for _, name := range required {
		if len(fields[name]) == 0 {
			return &ProtocolSchemaError{Reason: fmt.Sprintf("missing required field %q", name)}
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
			return &ProtocolSchemaError{Reason: fmt.Sprintf("field %q has JSON type %s; expected %s", name, observed, expectedType)}
		}
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
	return requireJSONTypes(fields, map[string]string{"sec": "integer", "usec": "integer"})
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
	_, err := strconv.ParseInt(value, 10, 64)
	if err == nil {
		return true
	}
	_, err = strconv.ParseUint(value, 10, 64)
	return err == nil
}

func unmarshalStrictFields(payload []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return &ProtocolSchemaError{Reason: err.Error()}
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
