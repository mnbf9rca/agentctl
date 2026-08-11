//go:build darwin

package fleet

import (
	"errors"

	"github.com/mnbf9rca/agentctl/internal/status"
)

// ShimStatusFleetReader adapts the durable fleet store to status's cycle-free
// operator-provenance view.
type ShimStatusFleetReader struct{ records ShimFleetRecords }

// NewShimStatusFleetReader constructs the durable-fleet status adapter.
func NewShimStatusFleetReader(records ShimFleetRecords) ShimStatusFleetReader {
	return ShimStatusFleetReader{records: records}
}

// Read copies the roster and role configuration without reclassifying either
// as runtime evidence.
func (r ShimStatusFleetReader) Read(session string) (status.ShimFleetRecord, error) {
	if r.records == nil {
		return status.ShimFleetRecord{}, errors.New("shim status fleet reader requires durable fleet records")
	}
	record, err := r.records.Read(session)
	if err != nil {
		return status.ShimFleetRecord{}, fleetMissing(session, err)
	}
	converted := status.ShimFleetRecord{
		Version: record.Version, Session: record.Session, Directory: record.Directory,
		Roster: append([]string(nil), record.Roster...),
		Roles:  make(map[string]status.ShimFleetRole, len(record.Roles)),
	}
	for role, configured := range record.Roles {
		converted.Roles[role] = status.ShimFleetRole{
			Harness: configured.Harness, Model: configured.Model, Effort: configured.Effort,
		}
	}
	return converted, nil
}

// List preserves every durable entry name for per-fleet status collection.
func (r ShimStatusFleetReader) List() ([]string, error) {
	if r.records == nil {
		return nil, errors.New("shim status fleet reader requires durable fleet records")
	}
	lister, ok := r.records.(interface{ List() ([]string, error) })
	if !ok {
		return nil, errors.New("durable fleet records do not support session enumeration")
	}
	return lister.List()
}
