package shim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mnbf9rca/agentctl/internal/config"
)

func TestProtocolPublicRequestCannotRepresentCallerPayloadText(t *testing.T) {
	want := []string{"Version", "Session", "Role", "Operation"}
	typeOfRequest := reflect.TypeOf(Request{})
	if typeOfRequest.NumField() != len(want) {
		t.Fatalf("Request has %d fields, want exactly %d", typeOfRequest.NumField(), len(want))
	}
	for index, fieldName := range want {
		if got := typeOfRequest.Field(index).Name; got != fieldName {
			t.Fatalf("Request field %d = %q, want %q", index, got, fieldName)
		}
	}
	for _, forbidden := range []string{"Payload", "Text", "Keys", "Arguments", "Model", "Environment"} {
		if _, present := typeOfRequest.FieldByName(forbidden); present {
			t.Fatalf("Request can represent forbidden %s input", forbidden)
		}
	}
}

func TestProtocolAdmitsClosedControlOperationsWithoutPayloadFields(t *testing.T) {
	for _, operation := range []string{"observe", "stop"} {
		encoded, err := EncodeRequest(Request{Version: ShimProtocolVersion, Session: "fleet", Role: "planner", Operation: operation})
		if err != nil {
			t.Fatalf("EncodeRequest(%q) error = %v", operation, err)
		}
		if bytes.Contains(encoded, []byte("payload")) {
			t.Fatalf("EncodeRequest(%q) = %s, unexpectedly contains a payload field", operation, encoded)
		}
		decoded, err := DecodeRequest(encoded)
		if err != nil {
			t.Fatalf("DecodeRequest(%q) error = %v", operation, err)
		}
		if decoded.Operation != operation {
			t.Fatalf("DecodeRequest(%q).Operation = %q", operation, decoded.Operation)
		}
	}
}

func TestProtocolStopResponsesKeepSignalAttemptAndObservedExitSeparate(t *testing.T) {
	tests := []Response{
		{
			Version: ShimProtocolVersion, Outcome: OutcomeStopChildExited,
			ChildPID: intPointer(123), SignalAttempted: boolPointer(true), Signal: stringPointer("SIGHUP"),
			ChildExitObserved: boolPointer(true),
		},
		{
			Version: ShimProtocolVersion, Outcome: OutcomeStopChildRetained,
			ChildPID: intPointer(123), SignalAttempted: boolPointer(true), Signal: stringPointer("SIGHUP"),
			ChildExitObserved: boolPointer(false), State: stringPointer("present-match"),
		},
		{
			Version: ShimProtocolVersion, Outcome: OutcomeStopAlreadyStopping,
			State: stringPointer("stopping"), ShimPID: intPointer(122), ChildPID: intPointer(123),
			SignalAttempted: boolPointer(false),
		},
	}
	for _, response := range tests {
		encoded, err := EncodeResponse(response)
		if err != nil {
			t.Fatalf("EncodeResponse(%q) error = %v", response.Outcome, err)
		}
		decoded, err := DecodeResponse(encoded)
		if err != nil {
			t.Fatalf("DecodeResponse(%q) error = %v", response.Outcome, err)
		}
		if decoded.SignalAttempted == nil || (decoded.Outcome != OutcomeStopAlreadyStopping && decoded.ChildExitObserved == nil) {
			t.Fatalf("DecodeResponse(%q) omitted signal/exit facts: %#v", response.Outcome, decoded)
		}
	}
}

func TestProtocolRequestVersionPrepassWinsBeforeSchemaAndOperation(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		observed string
	}{
		{name: "absent", payload: `{"payload":"arbitrary"}`, observed: "absent"},
		{name: "duplicate", payload: `{"version":1,"version":2,"operation":"rename"}`, observed: "duplicate"},
		{name: "foreign", payload: `{"version":2,"payload":"arbitrary","operation":"rename"}`, observed: "2"},
		{name: "non-integer", payload: `{"version":"one","payload":"arbitrary"}`, observed: strconv.Quote(`"one"`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(tt.payload))
			var skew *ProtocolSkewError
			if !errors.As(err, &skew) {
				t.Fatalf("error = %T %v, want *ProtocolSkewError", err, err)
			}
			if skew.CanonicalObserved() != tt.observed {
				t.Fatalf("CanonicalObserved = %q, want %q", skew.CanonicalObserved(), tt.observed)
			}
		})
	}
}

func TestProtocolSkewUsesClosedStructuredKindsAndCanonicalObservedValues(t *testing.T) {
	hugeVersion := "99999999999999999999999999999999999999999999999999"
	tests := []struct {
		name    string
		payload string
		kind    ProtocolSkewKind
		want    string
	}{
		{name: "absent", payload: `{}`, kind: ProtocolSkewAbsent, want: "absent"},
		{name: "duplicate", payload: `{"version":1,"version":2}`, kind: ProtocolSkewDuplicate, want: "duplicate"},
		{name: "string", payload: `{"version":"one"}`, kind: ProtocolSkewNonInteger, want: strconv.Quote(`"one"`)},
		{name: "object multiline", payload: "{\"version\":{\n\"x\":1}}", kind: ProtocolSkewNonInteger, want: strconv.Quote("{\n\"x\":1}")},
		{name: "array", payload: `{"version":[1,2]}`, kind: ProtocolSkewNonInteger, want: strconv.Quote(`[1,2]`)},
		{name: "oversized integer", payload: `{"version":` + hugeVersion + `}`, kind: ProtocolSkewForeign, want: hugeVersion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(tt.payload))
			var skew *ProtocolSkewError
			if !errors.As(err, &skew) {
				t.Fatalf("error = %T %v, want *ProtocolSkewError", err, err)
			}
			if skew.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q", skew.Kind, tt.kind)
			}
			if got := skew.CanonicalObserved(); got != tt.want {
				t.Fatalf("CanonicalObserved = %q, want %q", got, tt.want)
			}
			if strings.ContainsRune(skew.CanonicalObserved(), '\n') {
				t.Fatalf("canonical skew contains literal newline: %q", skew.CanonicalObserved())
			}
		})
	}
}

func TestProtocolStrictRequestRejectsUnknownFieldsAndOperations(t *testing.T) {
	tests := []string{
		`{"version":1,"session":"s","role":"r","operation":"clear","payload":"text"}`,
		`{"version":1,"session":"s","role":"r","operation":"rename"}`,
		`{"version":1,"session":"s","role":"r","role":"other","operation":"clear"}`,
	}
	for _, payload := range tests {
		if _, err := DecodeRequest([]byte(payload)); err == nil {
			t.Fatalf("DecodeRequest accepted %s", payload)
		}
	}
	if _, err := EncodeRequest(Request{Version: 1, Session: "s", Role: "r", Operation: "rename"}); err == nil {
		t.Fatal("EncodeRequest accepted unknown operation")
	}
	encoded, err := EncodeRequest(Request{Version: 1, Session: "s", Role: "r", Operation: "clear"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"version":1,"session":"s","role":"r","operation":"clear"}`; got != want {
		t.Fatalf("encoded request = %s, want %s", got, want)
	}
}

func TestProtocolInvalidSessionAndRoleRemainTypedInvalidRequests(t *testing.T) {
	tests := []string{
		`{"version":1,"session":"bad/name","role":"r","operation":"clear"}`,
		`{"version":1,"session":"s","role":"bad/name","operation":"clear"}`,
	}
	for _, payload := range tests {
		_, err := DecodeRequest([]byte(payload))
		var validation *config.ValidationError
		if !errors.As(err, &validation) {
			t.Fatalf("DecodeRequest(%s) error = %T %v, want *config.ValidationError", payload, err, err)
		}
		var schema *ProtocolSchemaError
		if errors.As(err, &schema) {
			t.Fatalf("DecodeRequest(%s) classified name validation as protocol schema: %v", payload, err)
		}
	}
}

func TestProtocolStrictSchemaReportsWrongFieldTypes(t *testing.T) {
	_, err := DecodeRequest([]byte(`{"version":1,"session":7,"role":"r","operation":"clear"}`))
	var schema *ProtocolSchemaError
	if !errors.As(err, &schema) {
		t.Fatalf("request error = %T %v, want *ProtocolSchemaError", err, err)
	}
	if got, want := schema.CanonicalCause(), `field "session" has JSON type number; expected string`; got != want {
		t.Fatalf("request reason = %q, want %q", got, want)
	}

	_, err = DecodeResponse([]byte(`{"version":1,"outcome":"delivery-submitted","bytes_written":"seven","submit_observed":true}`))
	if !errors.As(err, &schema) {
		t.Fatalf("response error = %T %v, want *ProtocolSchemaError", err, err)
	}
	if got, want := schema.CanonicalCause(), `field "bytes_written" has JSON type string; expected integer`; got != want {
		t.Fatalf("response reason = %q, want %q", got, want)
	}
}

func TestProtocolSchemaCanonicalRendererCoversEveryClosedCause(t *testing.T) {
	tests := []struct {
		name   string
		decode func() error
		kind   ProtocolSchemaKind
		want   string
	}{
		{
			name: "duplicate field",
			decode: func() error {
				_, err := DecodeRequest([]byte(`{"version":1,"session":"s","role":"r","role":"x","operation":"clear"}`))
				return err
			},
			kind: ProtocolSchemaDuplicateField,
			want: `duplicate field "role"`,
		},
		{
			name: "unknown field",
			decode: func() error {
				_, err := DecodeRequest([]byte(`{"version":1,"session":"s","role":"r","operation":"clear","extra":true}`))
				return err
			},
			kind: ProtocolSchemaUnknownField,
			want: `unknown field "extra"`,
		},
		{
			name: "missing required field",
			decode: func() error {
				_, err := DecodeRequest([]byte(`{"version":1,"session":"s","role":"r"}`))
				return err
			},
			kind: ProtocolSchemaMissingRequiredField,
			want: `missing required field "operation"`,
		},
		{
			name: "wrong object type",
			decode: func() error {
				_, err := DecodeRequest([]byte(`{"version":1,"session":{},"role":"r","operation":"clear"}`))
				return err
			},
			kind: ProtocolSchemaWrongJSONType,
			want: `field "session" has JSON type object; expected string`,
		},
		{
			name: "wrong array type",
			decode: func() error {
				_, err := DecodeRequest([]byte(`{"version":1,"session":"s","role":"r","operation":[]}`))
				return err
			},
			kind: ProtocolSchemaWrongJSONType,
			want: `field "operation" has JSON type array; expected string`,
		},
		{
			name: "unknown operation",
			decode: func() error {
				_, err := DecodeRequest([]byte(`{"version":1,"session":"s","role":"r","operation":"rename"}`))
				return err
			},
			kind: ProtocolSchemaOperationNotRegistered,
			want: `operation "rename" is not registered`,
		},
		{
			name: "irrelevant response field",
			decode: func() error {
				_, err := DecodeResponse([]byte(`{"version":1,"outcome":"delivery-submitted","bytes_written":1,"submit_observed":true,"cause":"bad"}`))
				return err
			},
			kind: ProtocolSchemaResponseFieldInvalid,
			want: `response field "cause" is not valid for outcome "delivery-submitted"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.decode()
			var schema *ProtocolSchemaError
			if !errors.As(err, &schema) {
				t.Fatalf("error = %T %v, want *ProtocolSchemaError", err, err)
			}
			if schema.Kind != tt.kind {
				t.Fatalf("Kind = %q, want %q", schema.Kind, tt.kind)
			}
			if got := schema.CanonicalCause(); got != tt.want {
				t.Fatalf("CanonicalCause = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProtocolJSONErrorsUseClosedCanonicalCauses(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		kind    JSONErrorKind
		want    string
	}{
		{name: "invalid UTF-8", payload: []byte{0xff}, kind: JSONInvalidUTF8, want: "payload is not valid UTF-8"},
		{name: "trailing", payload: []byte(`{} trailing`), kind: JSONTrailingBytes, want: "payload has trailing bytes after its JSON value"},
		{name: "non-object", payload: []byte(`[]`), kind: JSONTopLevelNotObject, want: "payload top level is not an object"},
		{name: "syntax", payload: []byte(`{"version":`), kind: JSONSyntax, want: strconv.Quote("unexpected EOF")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeJSONObject(tt.payload)
			var jsonError *JSONError
			if !errors.As(err, &jsonError) {
				t.Fatalf("error = %T %v, want *JSONError", err, err)
			}
			if jsonError.Kind != tt.kind || jsonError.CanonicalCause() != tt.want {
				t.Fatalf("JSON error = %#v cause %q, want kind %q cause %q", jsonError, jsonError.CanonicalCause(), tt.kind, tt.want)
			}
			if strings.ContainsRune(jsonError.CanonicalCause(), '\n') {
				t.Fatalf("canonical JSON cause contains newline: %q", jsonError.CanonicalCause())
			}
		})
	}
}

func TestProtocolResponseVersionPrepassWinsBeforeUnknownFields(t *testing.T) {
	_, err := DecodeResponse([]byte(`{"version":2,"payload":"text"}`))
	var skew *ProtocolSkewError
	if !errors.As(err, &skew) || skew.CanonicalObserved() != "2" {
		t.Fatalf("error = %T %v, want foreign-version skew", err, err)
	}
}

func TestProtocolHelloUsesTheSameVersionFirstGate(t *testing.T) {
	if got, err := EncodeHello(); err != nil || string(got) != `{"version":1}` {
		t.Fatalf("EncodeHello = %s, %v", got, err)
	}
	for _, payload := range []string{`{}`, `{"version":2,"unknown":true}`} {
		if err := DecodeHello([]byte(payload)); err == nil {
			t.Fatalf("DecodeHello accepted %s", payload)
		}
	}
}

func TestProtocolResponseRejectsUnknownAndOutcomeIrrelevantFields(t *testing.T) {
	valid := Response{Version: 1, Outcome: OutcomeDeliverySubmitted, BytesWritten: uint64Pointer(7), SubmitObserved: boolPointer(true)}
	payload, err := EncodeResponse(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(payload); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"version":1,"outcome":"delivery-submitted","bytes_written":7,"submit_observed":true,"payload":"text"}`,
		`{"version":1,"outcome":"delivery-submitted","bytes_written":7,"submit_observed":true,"cause":"irrelevant"}`,
	} {
		if _, err := DecodeResponse([]byte(invalid)); err == nil {
			t.Fatalf("DecodeResponse accepted %s", invalid)
		}
	}
}

func TestProtocolResponseAdmitsOnlyTypedStateRootDisagreementFacts(t *testing.T) {
	local := "/tmp/local"
	recorded := "/tmp/recorded"
	valid := Response{
		Version: 1, Outcome: OutcomeStateRootDisagreement,
		LocalRoot: &local, RecordedRoot: &recorded,
	}
	payload, err := EncodeResponse(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeResponse(payload); err != nil {
		t.Fatal(err)
	}
	child := 12
	valid.ChildPID = &child
	if _, err := EncodeResponse(valid); err == nil {
		t.Fatal("state-root-disagreement encoded irrelevant child_pid")
	}
}

func TestProtocolResponseRejectsMalformedObjectiveNumbers(t *testing.T) {
	state := "running"
	negative := -1
	response := Response{Version: 1, Outcome: OutcomeRunning, State: &state, ShimPID: &negative}
	if _, err := EncodeResponse(response); err == nil {
		t.Fatal("EncodeResponse accepted negative shim PID")
	}
	if _, err := DecodeResponse([]byte(`{"version":1,"outcome":"running","state":"running","shim_pid":-1}`)); err == nil {
		t.Fatal("DecodeResponse accepted negative shim PID")
	}
}

func TestProtocolResponseRejectsPIDOutsideDarwinPIDT(t *testing.T) {
	tooLarge := int(math.MaxInt32) + 1
	response := Response{Version: 1, Outcome: OutcomeRunning, State: stringPointer("running"), ShimPID: intPointer(10), ChildPID: &tooLarge}
	if _, err := EncodeResponse(response); err == nil {
		t.Fatal("EncodeResponse accepted PID above signed Darwin pid_t")
	}
	payload := `{"version":1,"outcome":"running","state":"running","shim_pid":10,"child_pid":` + strconv.Itoa(tooLarge) + `}`
	if _, err := DecodeResponse([]byte(payload)); err == nil {
		t.Fatal("DecodeResponse accepted PID above signed Darwin pid_t")
	}
}

func TestProtocolResponseRejectsFactuallyImpossibleValues(t *testing.T) {
	token := StartToken{Sec: 1, Usec: 2}
	otherToken := StartToken{Sec: 1, Usec: 3}
	tests := []Response{
		{Version: 1, Outcome: OutcomeDeliverySubmitted, BytesWritten: uint64Pointer(1), SubmitObserved: boolPointer(false)},
		{Version: 1, Outcome: OutcomeDeliverySubmitted, BytesWritten: uint64Pointer(0), SubmitObserved: boolPointer(true)},
		{Version: 1, Outcome: OutcomeDeliveryCancelledWithResidue, BytesWritten: uint64Pointer(0)},
		{Version: 1, Outcome: OutcomeRunning, State: stringPointer("child-starting"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeStarting, State: stringPointer("arbitrary"), ShimPID: intPointer(10)},
		{Version: 1, Outcome: OutcomeStopping, State: stringPointer("stopped"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeStopped, State: stringPointer("stopping"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeShimStopping, State: stringPointer("running"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeIndeterminateChildStarting, State: stringPointer("child-recorded"), ShimPID: intPointer(10), RecordPath: stringPointer("/record")},
		{Version: 1, Outcome: OutcomeStateRootDisagreement, LocalRoot: stringPointer("/same"), RecordedRoot: stringPointer("/same")},
		{Version: 1, Outcome: OutcomeOrphan, ShimPID: intPointer(10), ChildPID: intPointer(11), RecordedToken: &token, ObservedToken: &otherToken},
		{Version: 1, Outcome: OutcomePresentTokenDisagreement, ChildPID: intPointer(11), RecordedToken: &token, ObservedToken: &token},
		{Version: 1, Outcome: OutcomeStopChildExited, ChildPID: intPointer(11), SignalAttempted: boolPointer(false), Signal: stringPointer("SIGHUP"), ChildExitObserved: boolPointer(true)},
		{Version: 1, Outcome: OutcomeStopChildRetained, ChildPID: intPointer(11), SignalAttempted: boolPointer(true), Signal: stringPointer("SIGHUP"), ChildExitObserved: boolPointer(true), State: stringPointer("present-match")},
		{Version: 1, Outcome: OutcomeStopAlreadyStopping, State: stringPointer("stopping"), ShimPID: intPointer(10), ChildPID: intPointer(11), SignalAttempted: boolPointer(true)},
	}
	for _, response := range tests {
		if _, err := EncodeResponse(response); err == nil {
			t.Fatalf("EncodeResponse accepted impossible %#v", response)
		}
	}
}

func TestProtocolResponseAcceptsClosedStateValues(t *testing.T) {
	for _, response := range []Response{
		{Version: 1, Outcome: OutcomeStarting, State: stringPointer("child-starting"), ShimPID: intPointer(10)},
		{Version: 1, Outcome: OutcomeStarting, State: stringPointer("child-recorded"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeIndeterminateChildStarting, State: stringPointer("child-starting"), ShimPID: intPointer(10), RecordPath: stringPointer("/record")},
		{Version: 1, Outcome: OutcomeRunning, State: stringPointer("running"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeStopping, State: stringPointer("stopping"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeStopped, State: stringPointer("stopped"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeShimStopping, State: stringPointer("stopping"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeShimStopping, State: stringPointer("stopped"), ShimPID: intPointer(10), ChildPID: intPointer(11)},
		{Version: 1, Outcome: OutcomeStopAlreadyStopping, State: stringPointer("stopping"), ShimPID: intPointer(10), ChildPID: intPointer(11), SignalAttempted: boolPointer(false)},
	} {
		if _, err := EncodeResponse(response); err != nil {
			t.Fatalf("EncodeResponse rejected valid state response %#v: %v", response, err)
		}
	}
}

func TestProtocolStopChildExitedCanReportSignalErrorSeparatelyFromObservedExit(t *testing.T) {
	response := Response{
		Version: ShimProtocolVersion, Outcome: OutcomeStopChildExited, ChildPID: intPointer(11),
		SignalAttempted: boolPointer(true), Signal: stringPointer("SIGHUP"), ChildExitObserved: boolPointer(true),
		Cause: stringPointer("signal process group: operation not permitted"),
	}
	if _, err := EncodeResponse(response); err != nil {
		t.Fatalf("EncodeResponse() error = %v, want separate signal error and child-exit facts", err)
	}
}

func TestProtocolResponseRequiresEveryOutcomeFact(t *testing.T) {
	tests := []string{
		`{"version":1,"outcome":"delivery-cancelled-with-residue"}`,
		`{"version":1,"outcome":"invalid-record","record_path":"/record"}`,
		`{"version":1,"outcome":"present-token-disagreement","child_pid":12,"recorded_token":{"sec":1,"usec":2}}`,
	}
	for _, payload := range tests {
		if _, err := DecodeResponse([]byte(payload)); err == nil || !strings.Contains(err.Error(), "missing required field") {
			t.Fatalf("DecodeResponse(%s) error = %v, want missing required field", payload, err)
		}
	}
}

func TestProtocolResponseSchemaSingleSourceCoversEveryOutcome(t *testing.T) {
	outcomes := []Outcome{
		OutcomeDeliverySubmitted, OutcomeDeliveryCancelledClean, OutcomeDeliveryCancelledWithResidue,
		OutcomeInvalidRequest, OutcomeProtocolSchemaInvalid,
		OutcomeInvalidRecord, OutcomeStateRootDisagreement, OutcomeProtocolSkew, OutcomeAnswererDisagreement,
		OutcomeStarting, OutcomeIndeterminateChildStarting, OutcomeRunning, OutcomeOrphan,
		OutcomePresentTokenDisagreement, OutcomePresentNotOurs, OutcomeCouldNotObserve, OutcomeStaleRecord,
		OutcomeMissing, OutcomeCleanupFailed, OutcomeConcurrentContender, OutcomeObservedSelfTarget,
		OutcomeAncestryUndetermined, OutcomeReadinessTimeout, OutcomeReadinessObservationFailed,
		OutcomeChildExitedBeforeReady, OutcomeStopChildExited, OutcomeStopChildRetained,
		OutcomeStopping, OutcomeStopped, OutcomeShimStopping, OutcomeStopAlreadyStopping,
	}
	if len(responseSchemas) != len(outcomes) {
		t.Fatalf("responseSchemas has %d outcomes, want %d", len(responseSchemas), len(outcomes))
	}
	seen := make(map[Outcome]bool, len(outcomes))
	for _, outcome := range outcomes {
		if seen[outcome] {
			t.Fatalf("duplicate outcome %q in closed test inventory", outcome)
		}
		seen[outcome] = true
		schema, ok := responseSchemas[outcome]
		if !ok {
			t.Fatalf("responseSchemas is missing %q", outcome)
		}
		for _, required := range schema.required {
			if !schema.allowed[required] {
				t.Fatalf("outcome %q requires field %q but does not allow it", outcome, required)
			}
		}
	}
}

func TestProtocolNestedStartTokensRejectMissingAndDuplicateFields(t *testing.T) {
	tests := []string{
		`{"version":1,"outcome":"present-token-disagreement","child_pid":12,"recorded_token":{"sec":1,"sec":2,"usec":3},"observed_token":{"sec":1,"usec":3}}`,
		`{"version":1,"outcome":"present-token-disagreement","child_pid":12,"recorded_token":{"sec":1},"observed_token":{"sec":1,"usec":3}}`,
	}
	for _, payload := range tests {
		if _, err := DecodeResponse([]byte(payload)); err == nil {
			t.Fatalf("DecodeResponse accepted malformed nested token: %s", payload)
		}
	}
}

func TestProtocolFrameUsesBigEndianLengthAndRejectsInvalidBounds(t *testing.T) {
	left, right := net.Pipe()
	done := make(chan error, 1)
	go func() {
		_, err := WriteFrame(left, []byte(`{"version":1}`))
		done <- err
	}()
	header := make([]byte, ShimFrameHeaderBytes)
	if _, err := right.Read(header); err != nil {
		t.Fatal(err)
	}
	if got, want := binary.BigEndian.Uint32(header), uint32(len(`{"version":1}`)); got != want {
		t.Fatalf("frame length = %d, want %d", got, want)
	}
	payload := make([]byte, int(binary.BigEndian.Uint32(header)))
	if _, err := right.Read(payload); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_ = left.Close()
	_ = right.Close()

	for _, length := range []uint32{0, ShimFrameMaxPayloadBytes + 1} {
		server, client := net.Pipe()
		go func(length uint32) {
			var raw [4]byte
			binary.BigEndian.PutUint32(raw[:], length)
			_, _ = client.Write(raw[:])
			_ = client.Close()
		}(length)
		if _, err := ReadFrame(server); err == nil {
			t.Fatalf("ReadFrame accepted length %d", length)
		}
		_ = server.Close()
	}

	server, client := net.Pipe()
	if _, err := WriteFrame(client, bytes.Repeat([]byte{'x'}, ShimFrameMaxPayloadBytes+1)); err == nil {
		t.Fatal("WriteFrame accepted oversized payload")
	}
	_ = server.Close()
	_ = client.Close()
}

func TestProtocolFrameAcceptsExact4096ByteObject(t *testing.T) {
	payload := []byte(`{"x":"` + strings.Repeat("a", ShimFrameMaxPayloadBytes-len(`{"x":""}`)) + `"}`)
	if len(payload) != ShimFrameMaxPayloadBytes {
		t.Fatalf("fixture length = %d", len(payload))
	}
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	done := make(chan error, 1)
	go func() {
		_, err := WriteFrame(client, payload)
		done <- err
	}()
	got, err := ReadFrame(server)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("4096-byte payload changed across frame")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestProtocolFrameReportsPartialEOFCounts(t *testing.T) {
	tests := []struct {
		name  string
		write func(net.Conn)
		want  string
	}{
		{
			name: "header",
			write: func(connection net.Conn) {
				_, _ = connection.Write([]byte{0, 0})
			},
			want: "EOF after 2 of 4 header bytes",
		},
		{
			name: "payload",
			write: func(connection net.Conn) {
				var header [4]byte
				binary.BigEndian.PutUint32(header[:], 10)
				_, _ = connection.Write(header[:])
				_, _ = connection.Write([]byte("abc"))
			},
			want: "EOF after 3 of 10 payload bytes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, client := net.Pipe()
			go func() {
				tt.write(client)
				_ = client.Close()
			}()
			_, err := ReadFrame(server)
			_ = server.Close()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("ReadFrame error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestProtocolFrameReportsPartialWriteTimeoutCount(t *testing.T) {
	payload := []byte(`{"version":1}`)
	connection := &partialWriteTimeoutConn{}
	written, err := WriteFrame(connection, payload)
	if written != 3 {
		t.Fatalf("written = %d, want 3", written)
	}
	if got, want := err.Error(), "frame write exceeded 2s after 3 of 17 bytes"; got != want {
		t.Fatalf("WriteFrame error = %q, want %q", got, want)
	}
}

func TestProtocolFrameRejectsMalformedPayloads(t *testing.T) {
	for _, payload := range [][]byte{
		{0xff},
		[]byte(`{"version":1} trailing`),
		[]byte(`[]`),
	} {
		if _, err := decodeJSONObject(payload); err == nil {
			t.Fatalf("decodeJSONObject accepted %q", payload)
		}
	}
}

func TestProtocolFrameDeadlineCoversWholeFrame(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	go func() {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], 10)
		_, _ = client.Write(header[:])
		_, _ = client.Write([]byte("short"))
	}()
	started := time.Now()
	_, err := ReadFrame(server)
	if err == nil || !strings.Contains(err.Error(), "exceeded 2s during payload") {
		t.Fatalf("ReadFrame error = %v, want payload deadline", err)
	}
	if elapsed := time.Since(started); elapsed < ShimProtocolIOTimeout || elapsed > ShimProtocolIOTimeout+time.Second {
		t.Fatalf("deadline elapsed = %s, want approximately %s", elapsed, ShimProtocolIOTimeout)
	}
}

func TestProtocolFrameHeaderTimeReducesPayloadDeadline(t *testing.T) {
	server, client := net.Pipe()
	defer func() { _ = server.Close() }()
	defer func() { _ = client.Close() }()
	go func() {
		time.Sleep(1200 * time.Millisecond)
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], 10)
		_, _ = client.Write(header[:])
	}()
	started := time.Now()
	_, err := ReadFrame(server)
	elapsed := time.Since(started)
	if err == nil || !strings.Contains(err.Error(), "exceeded 2s during payload") {
		t.Fatalf("ReadFrame error = %v, want payload deadline", err)
	}
	if elapsed < ShimProtocolIOTimeout || elapsed > ShimProtocolIOTimeout+700*time.Millisecond {
		t.Fatalf("elapsed = %s, want one absolute %s deadline", elapsed, ShimProtocolIOTimeout)
	}
}

func TestProtocolDeadlineSetupFailuresUseClosedFrameCauses(t *testing.T) {
	wantErr := errors.New("injected deadline failure")
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})

	_, err := ReadFrame(&deadlineFailureConn{Conn: server, readErr: wantErr})
	if got, want := err.Error(), "frame read failed during header after 0 of 4 bytes: injected deadline failure"; got != want {
		t.Fatalf("ReadFrame deadline error = %q, want %q", got, want)
	}
	payload := []byte(`{"version":1}`)
	_, err = WriteFrame(&deadlineFailureConn{Conn: client, writeErr: wantErr}, payload)
	if got, want := err.Error(), "frame write failed after 0 of 17 bytes: injected deadline failure"; got != want {
		t.Fatalf("WriteFrame deadline error = %q, want %q", got, want)
	}
}

type deadlineFailureConn struct {
	net.Conn
	readErr  error
	writeErr error
}

func (c *deadlineFailureConn) SetReadDeadline(time.Time) error  { return c.readErr }
func (c *deadlineFailureConn) SetWriteDeadline(time.Time) error { return c.writeErr }

type partialWriteTimeoutConn struct {
	net.Conn
	writes int
}

func (c *partialWriteTimeoutConn) SetWriteDeadline(time.Time) error { return nil }

func (c *partialWriteTimeoutConn) Write([]byte) (int, error) {
	c.writes++
	if c.writes == 1 {
		return 3, nil
	}
	return 0, timeoutError{}
}

type timeoutError struct{}

func (timeoutError) Error() string   { return "timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func uint64Pointer(value uint64) *uint64 { return &value }
func boolPointer(value bool) *bool       { return &value }
func intPointer(value int) *int          { return &value }
func stringPointer(value string) *string { return &value }
