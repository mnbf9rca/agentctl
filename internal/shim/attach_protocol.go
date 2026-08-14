package shim

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"

	"github.com/mnbf9rca/agentctl/internal/config"
)

const (
	attachFrameHeaderBytes       = 5
	attachControlMaxPayloadBytes = 4096
	attachDataMaxPayloadBytes    = 65536
	attachCounterMax             = uint64(1<<53 - 1)
)

// AttachFrameKind is the closed one-connection attach framing vocabulary.
type AttachFrameKind uint8

const (
	AttachFrameControl AttachFrameKind = iota
	AttachFrameViewerInput
	AttachFrameRoleOutput
)

// AttachFrame owns one copied payload at the attach wire boundary.
type AttachFrame struct {
	Kind AttachFrameKind
	Data []byte
}

// AttachDirection identifies which endpoint produced a frame. It lets the
// protocol sequence validator enforce the two independently ordered streams.
type AttachDirection uint8

const (
	AttachFromClient AttachDirection = iota
	AttachFromShim
)

// AttachSequence validates the attach handshake and the independently ordered
// post-admission streams without owning a connection or lifecycle action. Its
// zero value is ready to observe a new conversation.
type AttachSequence struct {
	state        attachSequenceState
	shimTerminal bool
}

type attachSequenceState uint8

const (
	attachSequenceStart attachSequenceState = iota
	attachSequenceShimHello
	attachSequenceClientHello
	attachSequenceAdmitted
	attachSequenceRefused
)

// AttachControlKind is the closed selector for JSON control frames.
type AttachControlKind string

const (
	AttachControlShimHello AttachControlKind = "attach-shim-hello"
	AttachControlHello     AttachControlKind = "attach-hello"
	AttachControlAdmitted  AttachControlKind = "attach-admitted"
	AttachControlRefused   AttachControlKind = "attach-refused"
	AttachControlResize    AttachControlKind = "attach-resize"
	AttachControlFinal     AttachControlKind = "attach-final"
)

// AttachRefusal is the closed pre-admission refusal vocabulary.
type AttachRefusal string

const (
	AttachRefusalViewerPresent     AttachRefusal = "viewer-present"
	AttachRefusalPeerUnverified    AttachRefusal = "peer-unverified"
	AttachRefusalPeerUnobservable  AttachRefusal = "peer-unobservable"
	AttachRefusalInitialSizeFailed AttachRefusal = "initial-size-failed"
)

// AttachDisposition is the closed post-admission terminal vocabulary.
type AttachDisposition string

const (
	AttachDispositionChildExited      AttachDisposition = "child-exited"
	AttachDispositionViewerEvicted    AttachDisposition = "viewer-evicted"
	AttachDispositionCleanupRetained  AttachDisposition = "cleanup-retained"
	AttachDispositionServerClosing    AttachDisposition = "server-closing"
	AttachDispositionResizeFailed     AttachDisposition = "resize-failed"
	AttachDispositionTailUndelivered  AttachDisposition = "tail-undelivered"
	AttachDispositionTailUnconfirmed  AttachDisposition = "tail-unconfirmed"
	AttachDispositionCounterExhausted AttachDisposition = "counter-exhausted"
)

// AttachControl is a closed union selected by Kind and, where applicable,
// Outcome or Disposition. Zero values represent fields absent from a variant;
// required zero-valued counters are emitted according to their selector.
type AttachControl struct {
	Version          int
	Kind             AttachControlKind
	Session          string
	Role             string
	Rows             uint32
	Cols             uint32
	Outcome          AttachRefusal
	ViewerPID        int
	PeerPID          int
	PeerUID          uint32
	ShimUID          uint32
	Cause            string
	Disposition      AttachDisposition
	Bytes            uint64
	Undelivered      uint64
	KnownUndelivered uint64
}

// Observe accepts one complete frame only when it is legal at the current
// attach protocol boundary. After admission it imposes no ordering between the
// two directions beyond each direction's own allowed vocabulary.
func (sequence *AttachSequence) Observe(direction AttachDirection, frame AttachFrame) error {
	if direction != AttachFromClient && direction != AttachFromShim {
		return fmt.Errorf("unknown attach frame direction %d", direction)
	}
	if err := validateAttachFrame(frame); err != nil {
		return err
	}
	var control AttachControl
	if frame.Kind == AttachFrameControl {
		decoded, err := DecodeAttachControl(frame.Data)
		if err != nil {
			return err
		}
		control = decoded
	}

	switch sequence.state {
	case attachSequenceStart:
		if direction != AttachFromShim || control.Kind != AttachControlShimHello {
			return errors.New("attach sequence must begin with shim hello")
		}
		sequence.state = attachSequenceShimHello
	case attachSequenceShimHello:
		if direction != AttachFromClient || control.Kind != AttachControlHello {
			return errors.New("attach client hello must follow shim hello")
		}
		sequence.state = attachSequenceClientHello
	case attachSequenceClientHello:
		if direction != AttachFromShim {
			return errors.New("attach admission decision must follow client hello")
		}
		switch control.Kind {
		case AttachControlAdmitted:
			sequence.state = attachSequenceAdmitted
		case AttachControlRefused:
			sequence.state = attachSequenceRefused
		default:
			return errors.New("attach admission decision must be admitted or refused")
		}
	case attachSequenceAdmitted:
		if direction == AttachFromClient {
			if frame.Kind == AttachFrameViewerInput {
				return nil
			}
			if frame.Kind != AttachFrameControl || control.Kind != AttachControlResize {
				return errors.New("admitted client stream accepts only input or resize frames")
			}
			return nil
		}
		if sequence.shimTerminal {
			return errors.New("attach shim stream is already terminal")
		}
		if frame.Kind == AttachFrameRoleOutput {
			return nil
		}
		if frame.Kind != AttachFrameControl || control.Kind != AttachControlFinal {
			return errors.New("admitted shim stream accepts only output or final frames")
		}
		sequence.shimTerminal = true
	case attachSequenceRefused:
		return errors.New("attach sequence ended with refusal")
	default:
		return errors.New("attach sequence has invalid internal state")
	}
	return nil
}

// WriteAttachFrame writes one complete bounded attach frame. It copies the
// payload before the first write so caller mutation cannot change an in-flight
// frame.
func WriteAttachFrame(writer io.Writer, frame AttachFrame) error {
	if err := validateAttachFrame(frame); err != nil {
		return err
	}
	payload := append([]byte(nil), frame.Data...)
	var header [attachFrameHeaderBytes]byte
	binary.BigEndian.PutUint32(header[:4], uint32(len(payload)))
	header[4] = byte(frame.Kind)
	if err := writeAttachBytes(writer, header[:]); err != nil {
		return err
	}
	return writeAttachBytes(writer, payload)
}

func validateAttachFrame(frame AttachFrame) error {
	maximum, err := attachFrameMaximum(frame.Kind)
	if err != nil {
		return err
	}
	if len(frame.Data) == 0 {
		return errors.New("attach frame payload must not be empty")
	}
	if len(frame.Data) > maximum {
		return fmt.Errorf("attach frame kind %d payload length %d exceeds %d", frame.Kind, len(frame.Data), maximum)
	}
	if frame.Kind == AttachFrameControl {
		if _, err := decodeJSONObject(frame.Data); err != nil {
			return err
		}
	}
	return nil
}

// ReadAttachFrame reads exactly one attach frame and owns the returned copy.
// A zero-byte EOF is the between-frame close; any partial header or payload is
// a malformed frame.
func ReadAttachFrame(reader io.Reader) (AttachFrame, error) {
	var header [attachFrameHeaderBytes]byte
	read, err := io.ReadFull(reader, header[:])
	if err != nil {
		if read == 0 && errors.Is(err, io.EOF) {
			return AttachFrame{}, io.EOF
		}
		return AttachFrame{}, &FrameReadError{Phase: "attach header", Read: read, Total: len(header), Err: err}
	}
	kind := AttachFrameKind(header[4])
	maximum, err := attachFrameMaximum(kind)
	if err != nil {
		return AttachFrame{}, err
	}
	length := int(binary.BigEndian.Uint32(header[:4]))
	if length == 0 {
		return AttachFrame{}, errors.New("attach frame payload length is zero")
	}
	if length > maximum {
		return AttachFrame{}, fmt.Errorf("attach frame kind %d payload length %d exceeds %d", kind, length, maximum)
	}
	payload := make([]byte, length)
	read, err = io.ReadFull(reader, payload)
	if err != nil {
		return AttachFrame{}, &FrameReadError{Phase: "attach payload", Read: read, Total: length, Err: err}
	}
	if kind == AttachFrameControl {
		if _, err := decodeJSONObject(payload); err != nil {
			return AttachFrame{}, err
		}
	}
	return AttachFrame{Kind: kind, Data: payload}, nil
}

func attachFrameMaximum(kind AttachFrameKind) (int, error) {
	switch kind {
	case AttachFrameControl:
		return attachControlMaxPayloadBytes, nil
	case AttachFrameViewerInput, AttachFrameRoleOutput:
		return attachDataMaxPayloadBytes, nil
	default:
		return 0, fmt.Errorf("unknown attach frame kind %d", kind)
	}
}

func writeAttachBytes(writer io.Writer, payload []byte) error {
	for len(payload) != 0 {
		written, err := writer.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

// EncodeAttachControl validates and encodes exactly one closed control union.
func EncodeAttachControl(control AttachControl) ([]byte, error) {
	if control.Version != ShimProtocolVersion {
		return nil, fmt.Errorf("attach protocol version is %d; expected %d", control.Version, ShimProtocolVersion)
	}
	var payload []byte
	var err error
	switch control.Kind {
	case AttachControlShimHello, AttachControlAdmitted:
		if err := rejectAttachControlFields(control, nil); err != nil {
			return nil, err
		}
		payload, err = json.Marshal(struct {
			Version int               `json:"version"`
			Kind    AttachControlKind `json:"kind"`
		}{control.Version, control.Kind})
	case AttachControlHello:
		if err := validateAttachHello(control); err != nil {
			return nil, err
		}
		payload, err = json.Marshal(struct {
			Version int               `json:"version"`
			Kind    AttachControlKind `json:"kind"`
			Session string            `json:"session"`
			Role    string            `json:"role"`
			Rows    uint32            `json:"rows"`
			Cols    uint32            `json:"cols"`
		}{control.Version, control.Kind, control.Session, control.Role, control.Rows, control.Cols})
	case AttachControlRefused:
		payload, err = encodeAttachRefusal(control)
	case AttachControlResize:
		if err := validateAttachSize(control.Rows, control.Cols); err != nil {
			return nil, err
		}
		if err := rejectAttachControlFields(control, map[string]bool{"rows": true, "cols": true}); err != nil {
			return nil, err
		}
		payload, err = json.Marshal(struct {
			Version int               `json:"version"`
			Kind    AttachControlKind `json:"kind"`
			Rows    uint32            `json:"rows"`
			Cols    uint32            `json:"cols"`
		}{control.Version, control.Kind, control.Rows, control.Cols})
	case AttachControlFinal:
		payload, err = encodeAttachFinal(control)
	default:
		return nil, &ProtocolValueError{Field: "kind", Reason: fmt.Sprintf("has unknown attach control value %q", control.Kind)}
	}
	if err != nil {
		return nil, err
	}
	if len(payload) > attachControlMaxPayloadBytes {
		return nil, fmt.Errorf("attach control payload length %d exceeds %d", len(payload), attachControlMaxPayloadBytes)
	}
	return payload, nil
}

func validateAttachHello(control AttachControl) error {
	if err := config.ValidateSessionName(control.Session); err != nil {
		return err
	}
	if err := config.ValidateRoleName(control.Role); err != nil {
		return err
	}
	if err := validateAttachSize(control.Rows, control.Cols); err != nil {
		return err
	}
	return rejectAttachControlFields(control, map[string]bool{
		"session": true, "role": true, "rows": true, "cols": true,
	})
}

func encodeAttachRefusal(control AttachControl) ([]byte, error) {
	switch control.Outcome {
	case AttachRefusalViewerPresent:
		if !validDarwinPID(control.ViewerPID) {
			return nil, &ProtocolValueError{Field: "viewer_pid", Reason: "must be a positive signed Darwin pid_t"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{"outcome": true, "viewer_pid": true}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version   int               `json:"version"`
			Kind      AttachControlKind `json:"kind"`
			Outcome   AttachRefusal     `json:"outcome"`
			ViewerPID int               `json:"viewer_pid"`
		}{control.Version, control.Kind, control.Outcome, control.ViewerPID})
	case AttachRefusalPeerUnverified:
		if !validDarwinPID(control.PeerPID) {
			return nil, &ProtocolValueError{Field: "peer_pid", Reason: "must be a positive signed Darwin pid_t"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{
			"outcome": true, "peer_pid": true, "peer_uid": true, "shim_uid": true,
		}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version int               `json:"version"`
			Kind    AttachControlKind `json:"kind"`
			Outcome AttachRefusal     `json:"outcome"`
			PeerPID int               `json:"peer_pid"`
			PeerUID uint32            `json:"peer_uid"`
			ShimUID uint32            `json:"shim_uid"`
		}{control.Version, control.Kind, control.Outcome, control.PeerPID, control.PeerUID, control.ShimUID})
	case AttachRefusalPeerUnobservable:
		if control.Cause == "" {
			return nil, &ProtocolValueError{Field: "cause", Reason: "must not be empty"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{"outcome": true, "cause": true}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version int               `json:"version"`
			Kind    AttachControlKind `json:"kind"`
			Outcome AttachRefusal     `json:"outcome"`
			Cause   string            `json:"cause"`
		}{control.Version, control.Kind, control.Outcome, control.Cause})
	case AttachRefusalInitialSizeFailed:
		if err := validateAttachSize(control.Rows, control.Cols); err != nil {
			return nil, err
		}
		if control.Cause == "" {
			return nil, &ProtocolValueError{Field: "cause", Reason: "must not be empty"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{
			"outcome": true, "rows": true, "cols": true, "cause": true,
		}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version int               `json:"version"`
			Kind    AttachControlKind `json:"kind"`
			Outcome AttachRefusal     `json:"outcome"`
			Rows    uint32            `json:"rows"`
			Cols    uint32            `json:"cols"`
			Cause   string            `json:"cause"`
		}{control.Version, control.Kind, control.Outcome, control.Rows, control.Cols, control.Cause})
	default:
		return nil, &ProtocolValueError{Field: "outcome", Reason: fmt.Sprintf("has unknown attach refusal value %q", control.Outcome)}
	}
}

func encodeAttachFinal(control AttachControl) ([]byte, error) {
	if control.Bytes > attachCounterMax {
		return nil, &ProtocolValueError{Field: "bytes", Reason: "is outside its permitted integer range"}
	}
	switch control.Disposition {
	case AttachDispositionChildExited,
		AttachDispositionViewerEvicted,
		AttachDispositionCleanupRetained,
		AttachDispositionServerClosing,
		AttachDispositionCounterExhausted:
		if err := rejectAttachControlFields(control, map[string]bool{"disposition": true, "bytes": true}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version     int               `json:"version"`
			Kind        AttachControlKind `json:"kind"`
			Disposition AttachDisposition `json:"disposition"`
			Bytes       uint64            `json:"bytes"`
		}{control.Version, control.Kind, control.Disposition, control.Bytes})
	case AttachDispositionResizeFailed:
		if err := validateAttachSize(control.Rows, control.Cols); err != nil {
			return nil, err
		}
		if control.Cause == "" {
			return nil, &ProtocolValueError{Field: "cause", Reason: "must not be empty"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{
			"disposition": true, "bytes": true, "rows": true, "cols": true, "cause": true,
		}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version     int               `json:"version"`
			Kind        AttachControlKind `json:"kind"`
			Disposition AttachDisposition `json:"disposition"`
			Bytes       uint64            `json:"bytes"`
			Rows        uint32            `json:"rows"`
			Cols        uint32            `json:"cols"`
			Cause       string            `json:"cause"`
		}{control.Version, control.Kind, control.Disposition, control.Bytes, control.Rows, control.Cols, control.Cause})
	case AttachDispositionTailUndelivered:
		if control.Undelivered > attachCounterMax {
			return nil, &ProtocolValueError{Field: "undelivered", Reason: "is outside its permitted integer range"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{
			"disposition": true, "bytes": true, "undelivered": true,
		}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version     int               `json:"version"`
			Kind        AttachControlKind `json:"kind"`
			Disposition AttachDisposition `json:"disposition"`
			Bytes       uint64            `json:"bytes"`
			Undelivered uint64            `json:"undelivered"`
		}{control.Version, control.Kind, control.Disposition, control.Bytes, control.Undelivered})
	case AttachDispositionTailUnconfirmed:
		if control.KnownUndelivered > attachCounterMax {
			return nil, &ProtocolValueError{Field: "known_undelivered", Reason: "is outside its permitted integer range"}
		}
		if err := rejectAttachControlFields(control, map[string]bool{
			"disposition": true, "bytes": true, "known_undelivered": true,
		}); err != nil {
			return nil, err
		}
		return json.Marshal(struct {
			Version          int               `json:"version"`
			Kind             AttachControlKind `json:"kind"`
			Disposition      AttachDisposition `json:"disposition"`
			Bytes            uint64            `json:"bytes"`
			KnownUndelivered uint64            `json:"known_undelivered"`
		}{control.Version, control.Kind, control.Disposition, control.Bytes, control.KnownUndelivered})
	default:
		return nil, &ProtocolValueError{Field: "disposition", Reason: fmt.Sprintf("has unknown attach final value %q", control.Disposition)}
	}
}

func rejectAttachControlFields(control AttachControl, allowed map[string]bool) error {
	present := map[string]bool{
		"session":           control.Session != "",
		"role":              control.Role != "",
		"rows":              control.Rows != 0,
		"cols":              control.Cols != 0,
		"outcome":           control.Outcome != "",
		"viewer_pid":        control.ViewerPID != 0,
		"peer_pid":          control.PeerPID != 0,
		"peer_uid":          control.PeerUID != 0,
		"shim_uid":          control.ShimUID != 0,
		"cause":             control.Cause != "",
		"disposition":       control.Disposition != "",
		"bytes":             control.Bytes != 0,
		"undelivered":       control.Undelivered != 0,
		"known_undelivered": control.KnownUndelivered != 0,
	}
	for field, isPresent := range present {
		if isPresent && !allowed[field] {
			return &ProtocolSchemaError{Kind: ProtocolSchemaUnknownField, Field: field}
		}
	}
	return nil
}

func validateAttachSize(rows, cols uint32) error {
	if rows < 1 || rows > 65535 {
		return &ProtocolValueError{Field: "rows", Reason: "is outside its permitted integer range"}
	}
	if cols < 1 || cols > 65535 {
		return &ProtocolValueError{Field: "cols", Reason: "is outside its permitted integer range"}
	}
	return nil
}

// DecodeAttachControl performs the version-only pre-pass before selecting and
// validating exactly one closed control union variant.
func DecodeAttachControl(payload []byte) (AttachControl, error) {
	fields, err := decodeJSONObject(payload)
	if err != nil {
		return AttachControl{}, err
	}
	if err := requireCurrentVersion(fields); err != nil {
		return AttachControl{}, err
	}
	kind, err := attachStringSelector(fields, "kind")
	if err != nil {
		return AttachControl{}, err
	}
	control := AttachControl{Version: ShimProtocolVersion, Kind: AttachControlKind(kind)}
	switch control.Kind {
	case AttachControlShimHello, AttachControlAdmitted:
		err = requireAttachFields(fields, []string{"version", "kind"}, nil, nil)
	case AttachControlHello:
		err = requireAttachFields(fields,
			[]string{"version", "kind", "session", "role", "rows", "cols"},
			[]string{"session", "role", "rows", "cols"},
			map[string]string{"session": "string", "role": "string", "rows": "integer", "cols": "integer"})
		if err == nil {
			err = requireAttachRanges(fields, []attachIntegerRange{
				{name: "rows", minimum: 1, maximum: 65535},
				{name: "cols", minimum: 1, maximum: 65535},
			})
		}
	case AttachControlRefused:
		err = decodeAttachRefusalFields(fields)
	case AttachControlResize:
		err = requireAttachFields(fields,
			[]string{"version", "kind", "rows", "cols"},
			[]string{"rows", "cols"},
			map[string]string{"rows": "integer", "cols": "integer"})
		if err == nil {
			err = requireAttachRanges(fields, []attachIntegerRange{
				{name: "rows", minimum: 1, maximum: 65535},
				{name: "cols", minimum: 1, maximum: 65535},
			})
		}
	case AttachControlFinal:
		err = decodeAttachFinalFields(fields)
	default:
		return AttachControl{}, &ProtocolValueError{Field: "kind", Reason: fmt.Sprintf("has unknown attach control value %q", control.Kind)}
	}
	if err != nil {
		return AttachControl{}, err
	}
	var wire attachControlWire
	if err := unmarshalStrictFields(payload, &wire); err != nil {
		return AttachControl{}, err
	}
	control.Session = wire.Session
	control.Role = wire.Role
	control.Rows = wire.Rows
	control.Cols = wire.Cols
	control.Outcome = wire.Outcome
	control.ViewerPID = wire.ViewerPID
	control.PeerPID = wire.PeerPID
	control.PeerUID = wire.PeerUID
	control.ShimUID = wire.ShimUID
	control.Cause = wire.Cause
	control.Disposition = wire.Disposition
	control.Bytes = wire.Bytes
	control.Undelivered = wire.Undelivered
	control.KnownUndelivered = wire.KnownUndelivered
	if control.Kind == AttachControlHello {
		if err := config.ValidateSessionName(control.Session); err != nil {
			return AttachControl{}, err
		}
		if err := config.ValidateRoleName(control.Role); err != nil {
			return AttachControl{}, err
		}
	}
	if (control.Kind == AttachControlRefused && (control.Outcome == AttachRefusalPeerUnobservable || control.Outcome == AttachRefusalInitialSizeFailed)) ||
		(control.Kind == AttachControlFinal && control.Disposition == AttachDispositionResizeFailed) {
		if control.Cause == "" {
			return AttachControl{}, &ProtocolValueError{Field: "cause", Reason: "must not be empty"}
		}
	}
	return control, nil
}

type attachControlWire struct {
	Version          int               `json:"version"`
	Kind             AttachControlKind `json:"kind"`
	Session          string            `json:"session,omitempty"`
	Role             string            `json:"role,omitempty"`
	Rows             uint32            `json:"rows,omitempty"`
	Cols             uint32            `json:"cols,omitempty"`
	Outcome          AttachRefusal     `json:"outcome,omitempty"`
	ViewerPID        int               `json:"viewer_pid,omitempty"`
	PeerPID          int               `json:"peer_pid,omitempty"`
	PeerUID          uint32            `json:"peer_uid,omitempty"`
	ShimUID          uint32            `json:"shim_uid,omitempty"`
	Cause            string            `json:"cause,omitempty"`
	Disposition      AttachDisposition `json:"disposition,omitempty"`
	Bytes            uint64            `json:"bytes,omitempty"`
	Undelivered      uint64            `json:"undelivered,omitempty"`
	KnownUndelivered uint64            `json:"known_undelivered,omitempty"`
}

func decodeAttachRefusalFields(fields map[string][]json.RawMessage) error {
	outcome, err := attachStringSelector(fields, "outcome")
	if err != nil {
		return err
	}
	switch AttachRefusal(outcome) {
	case AttachRefusalViewerPresent:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "outcome", "viewer_pid"}, []string{"outcome", "viewer_pid"},
			map[string]string{"outcome": "string", "viewer_pid": "integer"}); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{{name: "viewer_pid", minimum: 1, maximum: darwinPIDMax}})
	case AttachRefusalPeerUnverified:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "outcome", "peer_pid", "peer_uid", "shim_uid"},
			[]string{"outcome", "peer_pid", "peer_uid", "shim_uid"},
			map[string]string{"outcome": "string", "peer_pid": "integer", "peer_uid": "integer", "shim_uid": "integer"}); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{
			{name: "peer_pid", minimum: 1, maximum: darwinPIDMax},
			{name: "peer_uid", minimum: 0, maximum: 1<<32 - 1},
			{name: "shim_uid", minimum: 0, maximum: 1<<32 - 1},
		})
	case AttachRefusalPeerUnobservable:
		return requireAttachFields(fields,
			[]string{"version", "kind", "outcome", "cause"}, []string{"outcome", "cause"},
			map[string]string{"outcome": "string", "cause": "string"})
	case AttachRefusalInitialSizeFailed:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "outcome", "rows", "cols", "cause"},
			[]string{"outcome", "rows", "cols", "cause"},
			map[string]string{"outcome": "string", "rows": "integer", "cols": "integer", "cause": "string"}); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{
			{name: "rows", minimum: 1, maximum: 65535},
			{name: "cols", minimum: 1, maximum: 65535},
		})
	default:
		return &ProtocolValueError{Field: "outcome", Reason: fmt.Sprintf("has unknown attach refusal value %q", outcome)}
	}
}

func decodeAttachFinalFields(fields map[string][]json.RawMessage) error {
	disposition, err := attachStringSelector(fields, "disposition")
	if err != nil {
		return err
	}
	baseTypes := map[string]string{"disposition": "string", "bytes": "integer"}
	switch AttachDisposition(disposition) {
	case AttachDispositionChildExited,
		AttachDispositionViewerEvicted,
		AttachDispositionCleanupRetained,
		AttachDispositionServerClosing,
		AttachDispositionCounterExhausted:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "disposition", "bytes"}, []string{"disposition", "bytes"}, baseTypes); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{{name: "bytes", minimum: 0, maximum: int64(attachCounterMax)}})
	case AttachDispositionResizeFailed:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "disposition", "bytes", "rows", "cols", "cause"},
			[]string{"disposition", "bytes", "rows", "cols", "cause"},
			map[string]string{"disposition": "string", "bytes": "integer", "rows": "integer", "cols": "integer", "cause": "string"}); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{
			{name: "bytes", minimum: 0, maximum: int64(attachCounterMax)},
			{name: "rows", minimum: 1, maximum: 65535},
			{name: "cols", minimum: 1, maximum: 65535},
		})
	case AttachDispositionTailUndelivered:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "disposition", "bytes", "undelivered"},
			[]string{"disposition", "bytes", "undelivered"},
			map[string]string{"disposition": "string", "bytes": "integer", "undelivered": "integer"}); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{
			{name: "bytes", minimum: 0, maximum: int64(attachCounterMax)},
			{name: "undelivered", minimum: 0, maximum: int64(attachCounterMax)},
		})
	case AttachDispositionTailUnconfirmed:
		if err := requireAttachFields(fields,
			[]string{"version", "kind", "disposition", "bytes", "known_undelivered"},
			[]string{"disposition", "bytes", "known_undelivered"},
			map[string]string{"disposition": "string", "bytes": "integer", "known_undelivered": "integer"}); err != nil {
			return err
		}
		return requireAttachRanges(fields, []attachIntegerRange{
			{name: "bytes", minimum: 0, maximum: int64(attachCounterMax)},
			{name: "known_undelivered", minimum: 0, maximum: int64(attachCounterMax)},
		})
	default:
		return &ProtocolValueError{Field: "disposition", Reason: fmt.Sprintf("has unknown attach final value %q", disposition)}
	}
}

func attachStringSelector(fields map[string][]json.RawMessage, name string) (string, error) {
	values := fields[name]
	if len(values) == 0 {
		return "", &ProtocolSchemaError{Kind: ProtocolSchemaMissingRequiredField, Field: name}
	}
	if len(values) != 1 {
		return "", &ProtocolSchemaError{Kind: ProtocolSchemaDuplicateField, Field: name}
	}
	if got := jsonType(values[0]); got != "string" {
		return "", &ProtocolSchemaError{Kind: ProtocolSchemaWrongJSONType, Field: name, ObservedType: got, ExpectedType: "string"}
	}
	var value string
	if err := json.Unmarshal(values[0], &value); err != nil {
		return "", err
	}
	return value, nil
}

func requireAttachFields(fields map[string][]json.RawMessage, allowedNames, requiredNames []string, expectedTypes map[string]string) error {
	allowed := make(map[string]bool, len(allowedNames))
	for _, name := range allowedNames {
		allowed[name] = true
	}
	required := append([]string{"version", "kind"}, requiredNames...)
	if err := requireFields(fields, allowed, required); err != nil {
		return asProtocolError(err)
	}
	types := map[string]string{"version": "integer", "kind": "string"}
	for name, expected := range expectedTypes {
		types[name] = expected
	}
	return asProtocolError(requireJSONTypes(fields, types))
}

type attachIntegerRange struct {
	name             string
	minimum, maximum int64
}

func requireAttachRanges(fields map[string][]json.RawMessage, ranges []attachIntegerRange) error {
	for _, integerRange := range ranges {
		if err := requireJSONIntegerRange(fields, integerRange.name, big.NewInt(integerRange.minimum), big.NewInt(integerRange.maximum)); err != nil {
			return asProtocolError(err)
		}
	}
	return nil
}
