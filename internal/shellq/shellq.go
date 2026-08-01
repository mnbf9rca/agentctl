// Package shellq quotes tokens for POSIX shell command strings.
// POSIX shell words cannot contain NUL bytes, so callers must not pass them.
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
