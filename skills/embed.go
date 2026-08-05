// Package skills carries the agent-facing skill tree that documents this
// binary, embedded so distribution can never drift from the release.
package skills

import "embed"

// Root is the skill directory name inside Tree.
const Root = "agentctl"

//go:embed agentctl
var Tree embed.FS
