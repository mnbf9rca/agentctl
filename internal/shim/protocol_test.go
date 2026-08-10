package shim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"reflect"
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

func TestProtocolRequestVersionPrepassWinsBeforeSchemaAndOperation(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		observed string
	}{
		{name: "absent", payload: `{"payload":"arbitrary"}`, observed: "absent"},
		{name: "duplicate", payload: `{"version":1,"version":2,"operation":"rename"}`, observed: "duplicate"},
		{name: "foreign", payload: `{"version":2,"payload":"arbitrary","operation":"rename"}`, observed: "2"},
		{name: "non-integer", payload: `{"version":"one","payload":"arbitrary"}`, observed: `"one"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := DecodeRequest([]byte(tt.payload))
			var skew *ProtocolSkewError
			if !errors.As(err, &skew) {
				t.Fatalf("error = %T %v, want *ProtocolSkewError", err, err)
			}
			if skew.Observed != tt.observed {
				t.Fatalf("Observed = %q, want %q", skew.Observed, tt.observed)
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
	if got, want := schema.Reason, `field "session" has JSON type number; expected string`; got != want {
		t.Fatalf("request reason = %q, want %q", got, want)
	}

	_, err = DecodeResponse([]byte(`{"version":1,"outcome":"delivery-submitted","bytes_written":"seven","submit_observed":true}`))
	if !errors.As(err, &schema) {
		t.Fatalf("response error = %T %v, want *ProtocolSchemaError", err, err)
	}
	if got, want := schema.Reason, `field "bytes_written" has JSON type string; expected integer`; got != want {
		t.Fatalf("response reason = %q, want %q", got, want)
	}
}

func TestProtocolResponseVersionPrepassWinsBeforeUnknownFields(t *testing.T) {
	_, err := DecodeResponse([]byte(`{"version":2,"payload":"text"}`))
	var skew *ProtocolSkewError
	if !errors.As(err, &skew) || skew.Observed != "2" {
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

func uint64Pointer(value uint64) *uint64 { return &value }
func boolPointer(value bool) *bool       { return &value }
