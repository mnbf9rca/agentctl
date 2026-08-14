package attach

import "github.com/mnbf9rca/agentctl/internal/shim"

// RoleResult is the shim-observed terminal disposition and byte accounting for
// one admitted direct-role attachment.
type RoleResult struct {
	Disposition      shim.AttachDisposition
	Bytes            uint64
	Raw              uint64
	Written          uint64
	Undelivered      uint64
	KnownUndelivered uint64
	Rows             uint32
	Cols             uint32
	Cause            string
}
