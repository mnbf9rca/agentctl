package shellq

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

func TestQuote(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: `''`},
		{name: "plain", input: "agent123", want: `'agent123'`},
		{name: "lone single quote", input: "'", want: `''"'"''`},
		{name: "single quote", input: "don't", want: `'don'"'"'t'`},
		{name: "backslash", input: `\`, want: `'\'`},
		{name: "double quote", input: `say "hi"`, want: `'say "hi"'`},
		{name: "dollar", input: "$HOME", want: `'$HOME'`},
		{name: "backticks", input: "`whoami`", want: "'`whoami`'"},
		{name: "spaces", input: "two words", want: `'two words'`},
		{name: "newline", input: "line1\nline2", want: "'line1\nline2'"},
		{name: "shell operators", input: ";|&<>()", want: `';|&<>()'`},
		{name: "leading dash", input: "--flag", want: `'--flag'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Quote(tt.input); got != tt.want {
				t.Fatalf("Quote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestQuoteRoundTrip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "plain", input: "agent123"},
		{name: "lone single quote", input: "'"},
		{name: "single quote", input: "don't"},
		{name: "backslash", input: `\`},
		{name: "spaces", input: "two words"},
		{name: "newline", input: "line1\nline2"},
		{name: "shell operators", input: ";|&<>()"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertQuoteRoundTrip(t, tt.input)
		})
	}
}

func TestJoin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		inputs []string
		want   string
	}{
		{name: "nil", inputs: nil, want: ""},
		{name: "empty", inputs: []string{}, want: ""},
		{name: "one", inputs: []string{"one"}, want: `'one'`},
		{
			name:   "mixed tokens",
			inputs: []string{"a b", "", "don't"},
			want:   `'a b' '' 'don'"'"'t'`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Join(tt.inputs); got != tt.want {
				t.Fatalf("Join(%q) = %q, want %q", tt.inputs, got, tt.want)
			}
		})
	}
}

func TestJoinPreservesHiddenShimArgvBoundaries(t *testing.T) {
	t.Parallel()

	argv := []string{
		"/Applications/Agent Ctl/agentctl", "__shim",
		"--session", "fleet one", "--role", "planner",
		"--harness", "claude", "--model", "model'quoted", "--effort", "max",
	}
	joined := Join(argv)
	wantJoined := `'/Applications/Agent Ctl/agentctl' '__shim' '--session' 'fleet one' '--role' 'planner' '--harness' 'claude' '--model' 'model'"'"'quoted' '--effort' 'max'`
	if joined != wantJoined {
		t.Fatalf("Join(hidden shim argv) = %q, want %q", joined, wantJoined)
	}

	script := "set -- " + joined + `; printf '%s\000' "$@"`
	got, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("hidden shim argv round-trip command failed: %v", err)
	}
	want := []byte(strings.Join(argv, "\x00") + "\x00")
	if !bytes.Equal(got, want) {
		t.Fatalf("hidden shim argv round trip = %q, want %q", got, want)
	}
}

func FuzzQuoteRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain",
		"'",
		"don't",
		`\`,
		`"double"`,
		"$HOME",
		"`whoami`",
		"two words",
		"line1\nline2",
		";|&<>()",
		"--flag",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input string) {
		assertQuoteRoundTrip(t, input)
	})
}

func assertQuoteRoundTrip(t testing.TB, input string) {
	t.Helper()
	if strings.IndexByte(input, 0) >= 0 {
		t.Skip("POSIX shell words cannot contain NUL")
	}

	script := "set -- " + Quote(input) + `; printf '%s:' "$#"; printf %s "$1"`
	got, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("round-trip command failed for %q: %v", input, err)
	}
	want := append([]byte("1:"), []byte(input)...)
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}
