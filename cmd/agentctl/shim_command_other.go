//go:build !darwin

package main

import (
	"context"
	"fmt"
	"io"
)

// The resident PTY shim is Darwin-only. Keeping the hidden dispatch seam on
// other platforms preserves cross-platform analysis/builds without exposing a
// partial lifecycle implementation.
func newProductionHiddenShimCommand() hiddenShimCommand {
	return hiddenShimCommandFunc(func(_ context.Context, _ []string, _, stderr io.Writer) int {
		fmt.Fprintln(stderr, "agentctl: hidden shim is unavailable on this platform")
		return exitUnclassified
	})
}
