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
		{name: "single quote", input: "don't", want: `'don'"'"'t'`},
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

func FuzzQuoteRoundTrip(f *testing.F) {
	for _, seed := range []string{
		"",
		"plain",
		"don't",
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
		if strings.IndexByte(input, 0) >= 0 {
			t.Skip("POSIX command arguments cannot contain NUL")
		}

		cmd := exec.Command("sh", "-c", "printf %s "+Quote(input))
		got, err := cmd.Output()
		if err != nil {
			t.Fatalf("round-trip command failed for %q: %v", input, err)
		}
		if !bytes.Equal(got, []byte(input)) {
			t.Fatalf("round trip = %q, want %q", got, []byte(input))
		}
	})
}
