// Package session selects a validated durable-fleet session name. Tmux is only
// the final ambient-name fallback when no explicit or advisory name is set.
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

// Client is the optional tmux surface used by the ambient-name fallback.
type Client interface {
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

// ResolutionError reports that the permitted source chain did not produce a
// validated session name.
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

// Unresolved reports that no permitted source named a session at all, as
// distinct from a named session that was invalid, missing, or ambiguous.
func (e *ResolutionError) Unresolved() bool {
	return e.Source == "" && e.Name == "" && e.Err == nil && len(e.Matches) == 0
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

// Select returns the validated session name chosen by the permitted source
// chain. Explicit and AGENTCTL_SESSION inputs are selection facts only and do
// not require a tmux presentation. The current-tmux fallback observes only the
// displayed session name for the caller's validated pane ID.
func (r Resolver) Select(ctx context.Context, explicit *string) (string, error) {
	if explicit != nil {
		if err := config.ValidateSessionName(*explicit); err != nil {
			return "", &UsageError{Err: err}
		}
		return *explicit, nil
	}

	if value, set := r.lookupEnv("AGENTCTL_SESSION"); set && value != "" {
		if err := config.ValidateSessionName(value); err != nil {
			return "", &ResolutionError{Source: SourceEnvironment, Name: value, Err: err}
		}
		return value, nil
	}

	paneValue, insideTmux := r.lookupEnv("TMUX_PANE")
	if !insideTmux {
		return "", &ResolutionError{}
	}
	name, err := r.client.DisplayMessage(ctx, tmuxx.PaneID(paneValue))
	if err != nil {
		var invalidID *tmuxx.InvalidIDError
		if errors.As(err, &invalidID) {
			return "", &ResolutionError{Source: SourceCurrent, Err: err}
		}
		return "", tmuxx.ClassifyError(err)
	}
	if err := config.ValidateSessionName(name); err != nil {
		return "", &ResolutionError{Source: SourceCurrent, Name: name, Err: err}
	}
	return name, nil
}
