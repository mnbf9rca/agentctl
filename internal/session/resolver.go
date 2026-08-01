// Package session resolves an agentctl session name to an exact tmux session ID.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// LookupEnv is compatible with os.LookupEnv.
type LookupEnv func(string) (string, bool)

// Client is the tmux surface required for session resolution.
type Client interface {
	ListSessions(context.Context) ([]tmuxx.Session, error)
	DisplayMessage(context.Context, tmuxx.PaneID) (string, error)
}

// Source identifies a permitted session-name source.
type Source string

const (
	SourceExplicit    Source = "explicit --session"
	SourceEnvironment Source = "AGENTCTL_SESSION"
	SourceCurrent     Source = "current tmux session"
)

// UsageError reports an invalid explicitly supplied --session value.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string {
	return e.Err.Error()
}

func (e *UsageError) Unwrap() error {
	return e.Err
}

// ResolutionError reports a validly executed resolution that did not produce
// exactly one session.
type ResolutionError struct {
	Source  Source
	Name    string
	Matches []tmuxx.Session
	Err     error
}

func (e *ResolutionError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Source, e.Err)
	}
	if e.Name == "" {
		return "session could not be resolved: provide --session, set AGENTCTL_SESSION, or run inside tmux"
	}
	if len(e.Matches) == 0 {
		return fmt.Sprintf("session %q not found", e.Name)
	}
	ids := make([]string, 0, len(e.Matches))
	for _, match := range e.Matches {
		ids = append(ids, string(match.ID))
	}
	return fmt.Sprintf("session %q is ambiguous: matched %s", e.Name, strings.Join(ids, ", "))
}

func (e *ResolutionError) Unwrap() error {
	return e.Err
}

// Resolver applies agentctl's fixed session-source precedence.
type Resolver struct {
	client    Client
	lookupEnv LookupEnv
}

// New constructs a session resolver.
func New(client Client, lookupEnv LookupEnv) Resolver {
	if lookupEnv == nil {
		lookupEnv = os.LookupEnv
	}
	return Resolver{client: client, lookupEnv: lookupEnv}
}

// Resolve returns the one exact tmux session selected by the permitted sources.
// A nil explicit value means --session was omitted; a non-nil empty value
// preserves an explicitly supplied --session= usage error.
func (r Resolver) Resolve(ctx context.Context, explicit *string) (tmuxx.Session, error) {
	if explicit != nil {
		if err := config.ValidateSessionName(*explicit); err != nil {
			return tmuxx.Session{}, &UsageError{Err: err}
		}
		return r.resolveName(ctx, *explicit)
	}

	if value, set := r.lookupEnv("AGENTCTL_SESSION"); set && value != "" {
		if err := config.ValidateSessionName(value); err != nil {
			return tmuxx.Session{}, &ResolutionError{Source: SourceEnvironment, Name: value, Err: err}
		}
		return r.resolveName(ctx, value)
	}

	paneValue, insideTmux := r.lookupEnv("TMUX_PANE")
	if !insideTmux {
		return tmuxx.Session{}, &ResolutionError{}
	}
	name, err := r.client.DisplayMessage(ctx, tmuxx.PaneID(paneValue))
	if err != nil {
		var invalidID *tmuxx.InvalidIDError
		if errors.As(err, &invalidID) {
			return tmuxx.Session{}, &ResolutionError{Source: SourceCurrent, Err: err}
		}
		return tmuxx.Session{}, tmuxx.ClassifyError(err)
	}
	if err := config.ValidateSessionName(name); err != nil {
		return tmuxx.Session{}, &ResolutionError{Source: SourceCurrent, Name: name, Err: err}
	}
	return r.resolveName(ctx, name)
}

func (r Resolver) resolveName(ctx context.Context, name string) (tmuxx.Session, error) {
	sessions, err := r.client.ListSessions(ctx)
	if err != nil {
		return tmuxx.Session{}, tmuxx.ClassifyError(err)
	}
	matches := make([]tmuxx.Session, 0, 1)
	for _, candidate := range sessions {
		if candidate.Name == name {
			matches = append(matches, candidate)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return tmuxx.Session{}, &ResolutionError{Name: name, Matches: matches}
}
