package shim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestAttachFrameRoundTripsEveryKindWithExactFraming(t *testing.T) {
	control := []byte(`{"version":1,"kind":"attach-shim-hello"}`)
	tests := []struct {
		name  string
		frame AttachFrame
		want  []byte
	}{
		{
			name:  "control",
			frame: AttachFrame{Kind: AttachFrameControl, Data: control},
			want:  append([]byte{0, 0, 0, 40, 0}, control...),
		},
		{
			name:  "viewer input remains raw",
			frame: AttachFrame{Kind: AttachFrameViewerInput, Data: []byte{0x00, 0xff, '\r', '\n'}},
			want:  []byte{0, 0, 0, 4, 1, 0x00, 0xff, '\r', '\n'},
		},
		{
			name:  "role output remains raw",
			frame: AttachFrame{Kind: AttachFrameRoleOutput, Data: []byte{0xfe, 0x00, 'x'}},
			want:  []byte{0, 0, 0, 3, 2, 0xfe, 0x00, 'x'},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wire bytes.Buffer
			if err := WriteAttachFrame(&wire, tt.frame); err != nil {
				t.Fatalf("WriteAttachFrame() error = %v", err)
			}
			if got := wire.Bytes(); !bytes.Equal(got, tt.want) {
				t.Fatalf("wire = %v, want %v", got, tt.want)
			}
			decoded, err := ReadAttachFrame(bytes.NewReader(wire.Bytes()))
			if err != nil {
				t.Fatalf("ReadAttachFrame() error = %v", err)
			}
			if !reflect.DeepEqual(decoded, tt.frame) {
				t.Fatalf("decoded = %#v, want %#v", decoded, tt.frame)
			}
			tt.frame.Data[0] ^= 0xff
			if bytes.Equal(decoded.Data, tt.frame.Data) {
				t.Fatal("decoded payload aliases caller-owned frame data")
			}
		})
	}
}

func TestAttachFrameEnforcesKindSpecificBoundsAndCompleteFrames(t *testing.T) {
	controlAtCap := []byte(`{"x":"` + strings.Repeat("a", 4088) + `"}`)
	if len(controlAtCap) != 4096 {
		t.Fatalf("control fixture length = %d, want 4096", len(controlAtCap))
	}
	for _, frame := range []AttachFrame{
		{Kind: AttachFrameControl, Data: controlAtCap},
		{Kind: AttachFrameViewerInput, Data: bytes.Repeat([]byte{'i'}, 65536)},
		{Kind: AttachFrameRoleOutput, Data: bytes.Repeat([]byte{'o'}, 65536)},
	} {
		var wire bytes.Buffer
		if err := WriteAttachFrame(&wire, frame); err != nil {
			t.Fatalf("WriteAttachFrame(%d bytes, kind %d) error = %v", len(frame.Data), frame.Kind, err)
		}
	}

	for _, frame := range []AttachFrame{
		{Kind: AttachFrameControl, Data: nil},
		{Kind: AttachFrameViewerInput, Data: nil},
		{Kind: AttachFrameRoleOutput, Data: nil},
		{Kind: AttachFrameControl, Data: []byte(`{"x":"` + strings.Repeat("a", 4089) + `"}`)},
		{Kind: AttachFrameViewerInput, Data: bytes.Repeat([]byte{'i'}, 65537)},
		{Kind: AttachFrameRoleOutput, Data: bytes.Repeat([]byte{'o'}, 65537)},
		{Kind: AttachFrameKind(3), Data: []byte{'x'}},
	} {
		if err := WriteAttachFrame(io.Discard, frame); err == nil {
			t.Fatalf("WriteAttachFrame accepted kind %d length %d", frame.Kind, len(frame.Data))
		}
	}

	invalidWire := [][]byte{
		{0, 0, 0, 0, byte(AttachFrameViewerInput)},
		{0, 0, 16, 1, byte(AttachFrameControl)},
		{0, 1, 0, 1, byte(AttachFrameViewerInput)},
		{0, 0, 0, 1, 3, 'x'},
	}
	for _, wire := range invalidWire {
		if _, err := ReadAttachFrame(bytes.NewReader(wire)); err == nil {
			t.Fatalf("ReadAttachFrame accepted invalid header %v", wire[:5])
		}
	}

	complete := append([]byte{0, 0, 0, 4, 1}, []byte("data")...)
	for cut := 1; cut < len(complete); cut++ {
		if _, err := ReadAttachFrame(bytes.NewReader(complete[:cut])); err == nil {
			t.Fatalf("ReadAttachFrame accepted frame truncated at byte %d", cut)
		}
	}
	if _, err := ReadAttachFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("empty between-frame read error = %T %v, want io.EOF", err, err)
	}
}

func TestAttachFrameRejectsMalformedControlPayloadsOnRead(t *testing.T) {
	for _, payload := range [][]byte{
		{0xff},
		[]byte(`[]`),
		[]byte(`{} trailing`),
		[]byte(`{"version":`),
	} {
		wire := rawAttachFrame(AttachFrameControl, payload)
		if _, err := ReadAttachFrame(bytes.NewReader(wire)); err == nil {
			t.Fatalf("ReadAttachFrame accepted malformed control payload %q", payload)
		}
	}
	for _, payload := range [][]byte{{0xff}, []byte(`[]`)} {
		wire := rawAttachFrame(AttachFrameViewerInput, payload)
		if _, err := ReadAttachFrame(bytes.NewReader(wire)); err != nil {
			t.Fatalf("ReadAttachFrame rejected raw data payload %v: %v", payload, err)
		}
	}
}

func TestAttachControlRoundTripsEveryClosedVariantWithExactJSON(t *testing.T) {
	tests := []struct {
		name    string
		control AttachControl
		want    string
	}{
		{name: "shim hello", control: AttachControl{Version: 1, Kind: AttachControlShimHello}, want: `{"version":1,"kind":"attach-shim-hello"}`},
		{name: "client hello", control: AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 60, Cols: 200}, want: `{"version":1,"kind":"attach-hello","session":"fleet","role":"planner","rows":60,"cols":200}`},
		{name: "admitted", control: AttachControl{Version: 1, Kind: AttachControlAdmitted}, want: `{"version":1,"kind":"attach-admitted"}`},
		{name: "viewer present", control: AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: 101}, want: `{"version":1,"kind":"attach-refused","outcome":"viewer-present","viewer_pid":101}`},
		{name: "peer unverified", control: AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalPeerUnverified, PeerPID: 102, PeerUID: 501, ShimUID: 502}, want: `{"version":1,"kind":"attach-refused","outcome":"peer-unverified","peer_pid":102,"peer_uid":501,"shim_uid":502}`},
		{name: "peer unobservable", control: AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalPeerUnobservable, Cause: "LOCAL_PEERPID failed"}, want: `{"version":1,"kind":"attach-refused","outcome":"peer-unobservable","cause":"LOCAL_PEERPID failed"}`},
		{name: "initial size failed", control: AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalInitialSizeFailed, Rows: 60, Cols: 200, Cause: "TIOCSWINSZ failed"}, want: `{"version":1,"kind":"attach-refused","outcome":"initial-size-failed","rows":60,"cols":200,"cause":"TIOCSWINSZ failed"}`},
		{name: "resize", control: AttachControl{Version: 1, Kind: AttachControlResize, Rows: 61, Cols: 201}, want: `{"version":1,"kind":"attach-resize","rows":61,"cols":201}`},
		{name: "child exited", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionChildExited, Bytes: 0}, want: `{"version":1,"kind":"attach-final","disposition":"child-exited","bytes":0}`},
		{name: "viewer evicted", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionViewerEvicted, Bytes: 1}, want: `{"version":1,"kind":"attach-final","disposition":"viewer-evicted","bytes":1}`},
		{name: "cleanup retained", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionCleanupRetained, Bytes: 2}, want: `{"version":1,"kind":"attach-final","disposition":"cleanup-retained","bytes":2}`},
		{name: "server closing", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing, Bytes: 3}, want: `{"version":1,"kind":"attach-final","disposition":"server-closing","bytes":3}`},
		{name: "resize failed", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionResizeFailed, Bytes: 4, Rows: 62, Cols: 202, Cause: "TIOCSWINSZ failed"}, want: `{"version":1,"kind":"attach-final","disposition":"resize-failed","bytes":4,"rows":62,"cols":202,"cause":"TIOCSWINSZ failed"}`},
		{name: "tail undelivered", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionTailUndelivered, Bytes: 5, Undelivered: 6}, want: `{"version":1,"kind":"attach-final","disposition":"tail-undelivered","bytes":5,"undelivered":6}`},
		{name: "tail unconfirmed", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionTailUnconfirmed, Bytes: 7, KnownUndelivered: 8}, want: `{"version":1,"kind":"attach-final","disposition":"tail-unconfirmed","bytes":7,"known_undelivered":8}`},
		{name: "counter exhausted", control: AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionCounterExhausted, Bytes: 9007199254740991}, want: `{"version":1,"kind":"attach-final","disposition":"counter-exhausted","bytes":9007199254740991}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := EncodeAttachControl(tt.control)
			if err != nil {
				t.Fatalf("EncodeAttachControl() error = %v", err)
			}
			if got := string(encoded); got != tt.want {
				t.Fatalf("encoded = %s, want %s", got, tt.want)
			}
			decoded, err := DecodeAttachControl(encoded)
			if err != nil {
				t.Fatalf("DecodeAttachControl() error = %v", err)
			}
			if !reflect.DeepEqual(decoded, tt.control) {
				t.Fatalf("decoded = %#v, want %#v", decoded, tt.control)
			}
		})
	}
}

func TestAttachControlVersionPrepassWinsBeforeUnionValidation(t *testing.T) {
	for _, payload := range []string{
		`{"kind":"unknown","extra":true}`,
		`{"version":1,"version":2,"kind":"unknown"}`,
		`{"version":"one","kind":"unknown"}`,
		`{"version":2,"kind":"unknown","extra":true}`,
	} {
		_, err := DecodeAttachControl([]byte(payload))
		var skew *ProtocolSkewError
		if !errors.As(err, &skew) {
			t.Fatalf("DecodeAttachControl(%s) error = %T %v, want ProtocolSkewError", payload, err, err)
		}
	}
}

func TestAttachControlRejectsMalformedAndCrossVariantFields(t *testing.T) {
	tests := []string{
		`{"version":1,"kind":"attach-shim-hello","extra":true}`,
		`{"version":1,"kind":"attach-shim-hello","kind":"attach-admitted"}`,
		`{"version":1,"kind":"unknown"}`,
		`{"version":1,"kind":"attach-hello","session":"fleet","role":"planner","rows":60}`,
		`{"version":1,"kind":"attach-hello","session":"fleet","role":"planner","rows":0,"cols":1}`,
		`{"version":1,"kind":"attach-hello","session":"fleet","role":"planner","rows":65536,"cols":1}`,
		`{"version":1,"kind":"attach-hello","session":"bad/name","role":"planner","rows":1,"cols":1}`,
		`{"version":1,"kind":"attach-resize","rows":1,"cols":1,"session":"fleet"}`,
		`{"version":1,"kind":"attach-refused","outcome":"unknown","cause":"x"}`,
		`{"version":1,"kind":"attach-refused","outcome":"viewer-present","viewer_pid":0}`,
		`{"version":1,"kind":"attach-refused","outcome":"viewer-present","viewer_pid":1,"cause":"x"}`,
		`{"version":1,"kind":"attach-refused","outcome":"peer-unverified","peer_pid":1,"peer_uid":-1,"shim_uid":1}`,
		`{"version":1,"kind":"attach-refused","outcome":"peer-unobservable","cause":""}`,
		`{"version":1,"kind":"attach-final","disposition":"unknown","bytes":0}`,
		`{"version":1,"kind":"attach-final","disposition":"child-exited"}`,
		`{"version":1,"kind":"attach-final","disposition":"child-exited","bytes":9007199254740992}`,
		`{"version":1,"kind":"attach-final","disposition":"resize-failed","bytes":0,"rows":1,"cols":1}`,
		`{"version":1,"kind":"attach-final","disposition":"tail-undelivered","bytes":0,"known_undelivered":1}`,
		`{"version":1,"kind":"attach-final","disposition":"tail-unconfirmed","bytes":0,"undelivered":1}`,
		`{"version":1,"kind":"attach-final","disposition":"counter-exhausted","bytes":"0"}`,
		`{"version":1,"kind":"attach-admitted"} trailing`,
		`[]`,
	}
	for _, payload := range tests {
		if _, err := DecodeAttachControl([]byte(payload)); err == nil {
			t.Fatalf("DecodeAttachControl accepted malformed union %s", payload)
		}
	}

	invalidValues := []AttachControl{
		{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 0, Cols: 1},
		{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 65536, Cols: 1},
		{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: 1, Cause: "cross-variant"},
		{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionChildExited, Bytes: 9007199254740992},
		{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionResizeFailed, Bytes: 0, Rows: 1, Cols: 1},
	}
	for _, control := range invalidValues {
		if _, err := EncodeAttachControl(control); err == nil {
			t.Fatalf("EncodeAttachControl accepted invalid union %#v", control)
		}
	}
}

func TestAttachFramesPreserveBothDirectionalSequencesWithoutCrossDirectionClaims(t *testing.T) {
	clientFrames := []AttachFrame{
		controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 60, Cols: 200}),
		{Kind: AttachFrameViewerInput, Data: []byte("first")},
		controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlResize, Rows: 61, Cols: 201}),
		{Kind: AttachFrameViewerInput, Data: []byte("second")},
	}
	shimFrames := []AttachFrame{
		controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlShimHello}),
		controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlAdmitted}),
		{Kind: AttachFrameRoleOutput, Data: []byte("paint")},
		controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionServerClosing, Bytes: 5}),
	}
	for _, frames := range [][]AttachFrame{clientFrames, shimFrames} {
		var wire bytes.Buffer
		for _, frame := range frames {
			if err := WriteAttachFrame(&wire, frame); err != nil {
				t.Fatal(err)
			}
		}
		for index, want := range frames {
			got, err := ReadAttachFrame(&wire)
			if err != nil {
				t.Fatalf("ReadAttachFrame(%d) error = %v", index, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("frame %d = %#v, want %#v", index, got, want)
			}
		}
	}
}

func TestAttachSequenceAcceptsHandshakeTrafficAndOneTerminalDecision(t *testing.T) {
	var sequence AttachSequence
	frames := []attachSequenceItem{
		{AttachFromShim, controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlShimHello})},
		{AttachFromClient, controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 60, Cols: 200})},
		{AttachFromShim, controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlAdmitted})},
		{AttachFromClient, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("input")}},
		{AttachFromShim, AttachFrame{Kind: AttachFrameRoleOutput, Data: []byte("output")}},
		{AttachFromClient, controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlResize, Rows: 61, Cols: 201})},
		{AttachFromShim, controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlFinal, Disposition: AttachDispositionChildExited, Bytes: 6})},
	}
	for index, item := range frames {
		if err := sequence.Observe(item.direction, item.frame); err != nil {
			t.Fatalf("Observe(%d) error = %v", index, err)
		}
	}
	if err := sequence.Observe(AttachFromShim, AttachFrame{Kind: AttachFrameRoleOutput, Data: []byte("late")}); err == nil {
		t.Fatal("Observe accepted output after the terminal frame")
	}

	var refused AttachSequence
	for index, item := range frames[:2] {
		if err := refused.Observe(item.direction, item.frame); err != nil {
			t.Fatalf("refusal prefix Observe(%d) error = %v", index, err)
		}
	}
	refusal := controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlRefused, Outcome: AttachRefusalViewerPresent, ViewerPID: 101})
	if err := refused.Observe(AttachFromShim, refusal); err != nil {
		t.Fatalf("Observe(refusal) error = %v", err)
	}
	if err := refused.Observe(AttachFromClient, AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("late")}); err == nil {
		t.Fatal("Observe accepted input after refusal")
	}
}

func TestAttachSequenceRejectsWrongDirectionOrderAndControl(t *testing.T) {
	shimHello := controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlShimHello})
	clientHello := controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlHello, Session: "fleet", Role: "planner", Rows: 60, Cols: 200})
	admitted := controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlAdmitted})
	resize := controlAttachFrame(t, AttachControl{Version: 1, Kind: AttachControlResize, Rows: 61, Cols: 201})

	tests := []struct {
		name      string
		prefix    []attachSequenceItem
		direction AttachDirection
		frame     AttachFrame
	}{
		{name: "client hello before shim hello", direction: AttachFromClient, frame: clientHello},
		{name: "admission before client hello", prefix: attachSequenceItems(AttachFromShim, shimHello), direction: AttachFromShim, frame: admitted},
		{name: "second shim hello", prefix: attachSequenceItems(AttachFromShim, shimHello), direction: AttachFromShim, frame: shimHello},
		{name: "input before admission", prefix: attachSequenceItems(AttachFromShim, shimHello, AttachFromClient, clientHello), direction: AttachFromClient, frame: AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("early")}},
		{name: "resize before admission", prefix: attachSequenceItems(AttachFromShim, shimHello, AttachFromClient, clientHello), direction: AttachFromClient, frame: resize},
		{name: "output before admission", prefix: attachSequenceItems(AttachFromShim, shimHello, AttachFromClient, clientHello), direction: AttachFromShim, frame: AttachFrame{Kind: AttachFrameRoleOutput, Data: []byte("early")}},
		{name: "client sends role output", prefix: attachSequenceItems(AttachFromShim, shimHello, AttachFromClient, clientHello, AttachFromShim, admitted), direction: AttachFromClient, frame: AttachFrame{Kind: AttachFrameRoleOutput, Data: []byte("wrong")}},
		{name: "shim sends viewer input", prefix: attachSequenceItems(AttachFromShim, shimHello, AttachFromClient, clientHello, AttachFromShim, admitted), direction: AttachFromShim, frame: AttachFrame{Kind: AttachFrameViewerInput, Data: []byte("wrong")}},
		{name: "unknown direction", direction: AttachDirection(2), frame: shimHello},
		{name: "malformed control", direction: AttachFromShim, frame: AttachFrame{Kind: AttachFrameControl, Data: []byte(`{"version":1}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sequence AttachSequence
			for index, item := range tt.prefix {
				if err := sequence.Observe(item.direction, item.frame); err != nil {
					t.Fatalf("prefix Observe(%d) error = %v", index, err)
				}
			}
			if err := sequence.Observe(tt.direction, tt.frame); err == nil {
				t.Fatalf("Observe accepted invalid frame %#v", tt.frame)
			}
		})
	}
}

type attachSequenceItem struct {
	direction AttachDirection
	frame     AttachFrame
}

func attachSequenceItems(items ...any) []attachSequenceItem {
	result := make([]attachSequenceItem, 0, len(items)/2)
	for index := 0; index < len(items); index += 2 {
		result = append(result, attachSequenceItem{items[index].(AttachDirection), items[index+1].(AttachFrame)})
	}
	return result
}

func rawAttachFrame(kind AttachFrameKind, payload []byte) []byte {
	header := make([]byte, 5)
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	header[4] = byte(kind)
	return append(header, payload...)
}

func controlAttachFrame(t *testing.T, control AttachControl) AttachFrame {
	t.Helper()
	payload, err := EncodeAttachControl(control)
	if err != nil {
		t.Fatal(err)
	}
	return AttachFrame{Kind: AttachFrameControl, Data: payload}
}
