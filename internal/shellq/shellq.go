// Package shellq quotes tokens for POSIX shell command strings.
// Quote is total: it accepts arbitrary bytes and imposes no precondition on callers.
// NUL is the one documented exception — it cannot exist in a POSIX shell word or an
// argv element, so the round-trip property is undefined for inputs containing it.
package shellq

import "strings"

// Quote returns s as one POSIX shell word using single-quote escaping.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// Join quotes each token and joins the resulting shell words with spaces.
func Join(tokens []string) string {
	quoted := make([]string, len(tokens))
	for i, token := range tokens {
		quoted[i] = Quote(token)
	}
	return strings.Join(quoted, " ")
}
