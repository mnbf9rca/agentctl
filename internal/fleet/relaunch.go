package fleet

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/mnbf9rca/agentctl/internal/config"
	"github.com/mnbf9rca/agentctl/internal/preflight"
	"github.com/mnbf9rca/agentctl/internal/status"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// Provenance records where one effective relaunch field came from.
type Provenance string

const (
	// ProvenanceStored is a value read from the session's own metadata.
	ProvenanceStored Provenance = "stored"
	// ProvenanceOverride is a stored value replaced by an explicit flag.
	ProvenanceOverride Provenance = "flag override"
	// ProvenanceFlags is a value supplied by flags because the session records none.
	ProvenanceFlags Provenance = "flags"
)

// RelaunchRequest is one relaunch invocation. A nil field means its option was
// omitted; an omitted option is never the same as an empty one.
type RelaunchRequest struct {
	Role      string
	Harness   *string
	Model     *string
	Directory *string
}

// RelaunchResult states exactly what a successful relaunch created, including
// where each element of the configuration came from.
type RelaunchResult struct {
	Role      string
	Session   string
	Harness   string
	Model     string
	Directory string
	WindowID  tmuxx.WindowID
	PaneID    tmuxx.PaneID

	HarnessFrom   Provenance
	ModelFrom     Provenance
	DirectoryFrom Provenance

	// StoredDirectory is the session's recorded launch directory, set only when
	// an explicit --dir diverges from it. The recorded value is left unchanged.
	StoredDirectory string
}

// SessionGateError reports a session failing the managed/version ownership gate.
type SessionGateError struct {
	Session tmuxx.Session
	Option  string
	Value   string
}

func (e *SessionGateError) Error() string {
	if e.Option == optionManaged {
		return fmt.Sprintf("session %q is not managed by agentctl", e.Session.Name)
	}
	if e.Value == "" {
		return "managed session carries no @agentctl_version marker"
	}
	return fmt.Sprintf("session %q has @agentctl_version=%q; expected %q", e.Session.Name, e.Value, "1")
}

// RosterError reports absent or structurally invalid role roster metadata.
type RosterError struct {
	Session   tmuxx.Session
	Roster    string
	Duplicate string
}

func (e *RosterError) Error() string {
	if e.Roster != "" {
		if e.Duplicate != "" {
			return fmt.Sprintf("managed session has invalid @agentctl_roles roster %q: duplicate role %q", e.Roster, e.Duplicate)
		}
		return fmt.Sprintf("managed session has invalid @agentctl_roles roster %q", e.Roster)
	}
	return "managed session has no @agentctl_roles roster"
}

// UnknownRoleError reports a role the session's roster does not declare.
type UnknownRoleError struct {
	Session tmuxx.Session
	Role    string
	Roster  string
}

func (e *UnknownRoleError) Error() string {
	return fmt.Sprintf("role %q is not in @agentctl_roles %q", e.Role, e.Roster)
}

// MetadataError reports per-role configuration metadata that cannot be acted on.
// Its reason renders the values observed rather than naming a cause.
type MetadataError struct {
	Session tmuxx.Session
	Reason  string
}

func (e *MetadataError) Error() string {
	return fmt.Sprintf("managed session %q %s", e.Session.Name, e.Reason)
}

// LegacySessionError reports a session launched before agentctl recorded
// per-role configuration, with no configuration supplied on the command line.
type LegacySessionError struct {
	Session tmuxx.Session
	Role    string
}

func (e *LegacySessionError) Error() string {
	return "session records no per-role configuration; it was launched before agentctl recorded " +
		optionFleet + " and " + optionDirectory + "; supply --harness [--model] --dir"
}

// ObservedWindow is one existing window blocking a relaunch, with the state
// status would report for it.
type ObservedWindow struct {
	ID    tmuxx.WindowID
	State status.State
}

// WindowPresentError reports that the role's window has not gone away. Relaunch
// recreates what is absent; it never removes or restarts what exists.
type WindowPresentError struct {
	Session tmuxx.Session
	Role    string
	Windows []ObservedWindow
}

func (e *WindowPresentError) Error() string {
	rendered := make([]string, len(e.Windows))
	for index, window := range e.Windows {
		rendered[index] = fmt.Sprintf("%s %s", window.ID, window.State)
	}
	noun := "windows"
	if len(e.Windows) == 1 {
		noun = "window"
	}
	return fmt.Sprintf("role %s already has %d %s in %s (%s); relaunch creates only absent role windows",
		e.Role, len(e.Windows), noun, e.Session.Name, strings.Join(rendered, ", "))
}

// StoredDirectoryError reports a recorded launch directory that cannot be used.
type StoredDirectoryError struct {
	Session tmuxx.Session
	Role    string
	Path    string
	Err     error
}

func (e *StoredDirectoryError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("managed session %q records launch directory %q: %v; supply --dir to relaunch %s elsewhere",
			e.Session.Name, e.Path, e.Err, e.Role)
	}
	return fmt.Sprintf("managed session %q records launch directory %q: not a directory; supply --dir to relaunch %s elsewhere",
		e.Session.Name, e.Path, e.Role)
}

func (e *StoredDirectoryError) Unwrap() error { return e.Err }

// WindowCreationError reports malformed successful new-window output. It is a
// pre-ownership error because no typed window ID was obtained, so nothing is
// removed.
type WindowCreationError struct {
	Role  string
	Cause error
}

func (e *WindowCreationError) Error() string {
	return fmt.Sprintf("%v; a window named %s may exist; inspect with tmux list-windows", e.Cause, e.Role)
}

func (e *WindowCreationError) Unwrap() error { return e.Cause }

// RelaunchError reports a post-ownership relaunch failure and its one cleanup
// attempt. Only the window this invocation created is ever removed.
type RelaunchError struct {
	Role       string
	WindowID   tmuxx.WindowID
	Cause      error
	CleanupErr error
}

func (e *RelaunchError) Error() string {
	if e.CleanupErr != nil {
		return fmt.Sprintf("failed to relaunch %s; failed to remove window %s: %v (relaunch failure: %v)",
			e.Role, e.WindowID, e.CleanupErr, e.Cause)
	}
	return fmt.Sprintf("failed to relaunch %s; removed window %s: %v", e.Role, e.WindowID, e.Cause)
}

func (e *RelaunchError) Unwrap() error { return e.Cause }

// Relaunch recreates one absent role window inside an existing managed session.
// Stored per-role configuration is authoritative; explicit options override it
// per field and every field's provenance is reported.
func (l Launcher) Relaunch(ctx context.Context, session tmuxx.Session, request RelaunchRequest) (RelaunchResult, error) {
	if err := config.ValidateRoleName(request.Role); err != nil {
		return RelaunchResult{}, err
	}
	if request.Harness != nil {
		if _, err := config.ParseHarness(*request.Harness); err != nil {
			return RelaunchResult{}, err
		}
	}
	if request.Model != nil {
		if err := config.ValidateModelName(*request.Model); err != nil {
			return RelaunchResult{}, err
		}
	}

	if err := l.checkOwnership(ctx, session); err != nil {
		return RelaunchResult{}, err
	}
	role, directory, provenance, err := l.resolveConfiguration(ctx, session, request)
	if err != nil {
		return RelaunchResult{}, err
	}
	if err := l.requireAbsentWindow(ctx, session, request.Role); err != nil {
		return RelaunchResult{}, err
	}
	if err := preflight.CheckExecutables(config.FleetConfig{Roles: []config.RoleConfig{role}}, l.lookPath); err != nil {
		return RelaunchResult{}, err
	}
	if err := l.checkRelaunchDirectory(session, request, role.Name, directory); err != nil {
		return RelaunchResult{}, err
	}

	created, err := l.newWindow(ctx, session.ID, session.Name, role, directory)
	if err != nil {
		if errors.Is(err, tmuxx.ErrCreationOutput) {
			return RelaunchResult{}, &WindowCreationError{Role: role.Name, Cause: err}
		}
		return RelaunchResult{}, tmuxx.ClassifyError(err)
	}
	if err := l.stampWindow(ctx, created.WindowID, created.PanePID, role); err != nil {
		return RelaunchResult{}, l.rollbackWindow(ctx, created.WindowID, role.Name, err)
	}
	if provenance.rewritesFleet() {
		updated := replaceRole(provenance.storedRoles, role)
		if err := l.tmux.SetSessionOption(ctx, session.ID, optionFleet, EncodeFleet(updated)); err != nil {
			return RelaunchResult{}, l.rollbackWindow(ctx, created.WindowID, role.Name, err)
		}
	}

	result := RelaunchResult{
		Role:          role.Name,
		Session:       session.Name,
		Harness:       string(role.Harness),
		Model:         role.Model,
		Directory:     directory,
		WindowID:      created.WindowID,
		PaneID:        created.PaneID,
		HarnessFrom:   provenance.harness,
		ModelFrom:     provenance.model,
		DirectoryFrom: provenance.directory,
	}
	if provenance.directory == ProvenanceOverride && provenance.storedDirectory != directory {
		result.StoredDirectory = provenance.storedDirectory
	}
	return result, nil
}

// sources records where each effective field came from and what the session
// stored, so the result can report provenance and the fleet re-encode can run.
type sources struct {
	harness         Provenance
	model           Provenance
	directory       Provenance
	storedRoles     []config.RoleConfig
	storedDirectory string
}

func (s sources) rewritesFleet() bool {
	return s.storedRoles != nil && (s.harness == ProvenanceOverride || s.model == ProvenanceOverride)
}

func (l Launcher) checkOwnership(ctx context.Context, session tmuxx.Session) error {
	managed, err := l.tmux.ShowSessionOption(ctx, session.ID, optionManaged)
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	if managed != "1" {
		return &SessionGateError{Session: session, Option: optionManaged, Value: managed}
	}
	version, err := l.tmux.ShowSessionOption(ctx, session.ID, optionVersion)
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	if version != "1" {
		return &SessionGateError{Session: session, Option: optionVersion, Value: version}
	}
	return nil
}

func (l Launcher) resolveConfiguration(ctx context.Context, session tmuxx.Session, request RelaunchRequest) (config.RoleConfig, string, sources, error) {
	roster, err := l.tmux.ShowSessionOption(ctx, session.ID, optionRoles)
	if err != nil {
		return config.RoleConfig{}, "", sources{}, tmuxx.ClassifyError(err)
	}
	rosterRoles, err := parseRoster(session, roster)
	if err != nil {
		return config.RoleConfig{}, "", sources{}, err
	}
	if !containsRole(rosterRoles, request.Role) {
		return config.RoleConfig{}, "", sources{}, &UnknownRoleError{Session: session, Role: request.Role, Roster: roster}
	}

	fleetValue, err := l.tmux.ShowSessionOption(ctx, session.ID, optionFleet)
	if err != nil {
		return config.RoleConfig{}, "", sources{}, tmuxx.ClassifyError(err)
	}
	directoryValue, err := l.tmux.ShowSessionOption(ctx, session.ID, optionDirectory)
	if err != nil {
		return config.RoleConfig{}, "", sources{}, tmuxx.ClassifyError(err)
	}

	switch {
	case fleetValue == "" && directoryValue == "":
		return legacyConfiguration(session, request)
	case fleetValue == "":
		return config.RoleConfig{}, "", sources{}, &MetadataError{
			Session: session,
			Reason:  fmt.Sprintf("has %s %q but no %s", optionDirectory, directoryValue, optionFleet),
		}
	case directoryValue == "":
		return config.RoleConfig{}, "", sources{}, &MetadataError{
			Session: session,
			Reason:  fmt.Sprintf("has %s %q but no %s", optionFleet, fleetValue, optionDirectory),
		}
	}

	storedRoles, err := decodeFleet(fleetValue)
	if err != nil {
		return config.RoleConfig{}, "", sources{}, &MetadataError{
			Session: session,
			Reason:  fmt.Sprintf("has invalid %s %q: %v", optionFleet, fleetValue, err),
		}
	}
	if !sameRoles(storedRoles, rosterRoles) {
		return config.RoleConfig{}, "", sources{}, &MetadataError{
			Session: session,
			Reason:  fmt.Sprintf("has %s %q whose roles do not match %s %q", optionFleet, fleetValue, optionRoles, roster),
		}
	}

	provenance := sources{
		harness:         ProvenanceStored,
		model:           ProvenanceStored,
		directory:       ProvenanceStored,
		storedRoles:     storedRoles,
		storedDirectory: directoryValue,
	}
	role := storedRoles[roleIndex(storedRoles, request.Role)]
	directory := directoryValue
	if request.Harness != nil {
		harness, _ := config.ParseHarness(*request.Harness)
		role.Harness = harness
		provenance.harness = ProvenanceOverride
	}
	if request.Model != nil {
		role.Model = *request.Model
		provenance.model = ProvenanceOverride
	}
	if request.Directory != nil {
		directory = *request.Directory
		provenance.directory = ProvenanceOverride
	}
	return role, directory, provenance, nil
}

// legacyConfiguration handles a session predating per-role metadata. The launch
// directory is never defaulted to the invocation directory: silently relaunching
// a role somewhere the rest of the fleet does not live is the hazard this
// refusal exists to prevent.
func legacyConfiguration(session tmuxx.Session, request RelaunchRequest) (config.RoleConfig, string, sources, error) {
	if request.Harness == nil || request.Directory == nil {
		return config.RoleConfig{}, "", sources{}, &LegacySessionError{Session: session, Role: request.Role}
	}
	harness, err := config.ParseHarness(*request.Harness)
	if err != nil {
		return config.RoleConfig{}, "", sources{}, err
	}
	role := config.RoleConfig{Name: request.Role, Harness: harness}
	if request.Model != nil {
		role.Model = *request.Model
	}
	return role, *request.Directory, sources{
		harness:   ProvenanceFlags,
		model:     ProvenanceFlags,
		directory: ProvenanceFlags,
	}, nil
}

// requireAbsentWindow refuses unless the role matches exactly zero windows. A
// window with no panes refuses too: recreating beside it would manufacture the
// ambiguity §13.5 fails closed on.
func (l Launcher) requireAbsentWindow(ctx context.Context, session tmuxx.Session, role string) error {
	windows, err := l.tmux.ListWindows(ctx, session.ID)
	if err != nil {
		return tmuxx.ClassifyError(err)
	}
	matches := make([]tmuxx.Window, 0, 1)
	for _, window := range windows {
		if window.Name == role {
			matches = append(matches, window)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	observed, err := l.observeWindows(ctx, matches, role)
	if err != nil {
		return err
	}
	return &WindowPresentError{Session: session, Role: role, Windows: observed}
}

func (l Launcher) observeWindows(ctx context.Context, matches []tmuxx.Window, role string) ([]ObservedWindow, error) {
	if len(matches) > 1 {
		observed := make([]ObservedWindow, len(matches))
		for index, window := range matches {
			observed[index] = ObservedWindow{ID: window.ID, State: status.StateAmbiguous}
		}
		return observed, nil
	}
	state, err := l.observeWindow(ctx, matches[0], role)
	if err != nil {
		return nil, err
	}
	return []ObservedWindow{{ID: matches[0].ID, State: state}}, nil
}

// observeWindow reports the state status would report for one window, in the
// same precedence order, so a refusal never asserts more than was verified.
func (l Launcher) observeWindow(ctx context.Context, window tmuxx.Window, role string) (status.State, error) {
	if window.Managed != "1" || window.Role != role {
		return status.StateUnmanaged, nil
	}
	panes, err := l.tmux.ListPanes(ctx, window.ID)
	if err != nil {
		return "", tmuxx.ClassifyError(err)
	}
	if len(panes) == 0 {
		return status.StateMissing, nil
	}
	if len(panes) > 1 || panes[0].WindowPanes != 1 {
		return status.StateUnmanaged, nil
	}
	pane := panes[0]
	if pane.Dead {
		return status.StateDead, nil
	}
	if window.Process == "" {
		return status.StateUnexpectedProcess, nil
	}
	process, err := l.tmux.ProcessName(ctx, pane.PID)
	if err != nil {
		if errors.Is(err, tmuxx.ErrProcessUnavailable) {
			return status.StateUnexpectedProcess, nil
		}
		return "", tmuxx.ClassifyError(err)
	}
	if process != window.Process {
		return status.StateUnexpectedProcess, nil
	}
	return status.StateRunning, nil
}

func (l Launcher) checkRelaunchDirectory(session tmuxx.Session, request RelaunchRequest, role, directory string) error {
	info, err := l.stat(directory)
	if err == nil && info.IsDir() {
		return nil
	}
	if request.Directory != nil {
		return &DirectoryError{Path: directory, Err: err}
	}
	return &StoredDirectoryError{Session: session, Role: role, Path: directory, Err: err}
}

func (l Launcher) rollbackWindow(ctx context.Context, windowID tmuxx.WindowID, role string, cause error) error {
	return &RelaunchError{
		Role:       role,
		WindowID:   windowID,
		Cause:      cause,
		CleanupErr: l.tmux.KillWindow(ctx, windowID),
	}
}

func parseRoster(session tmuxx.Session, roster string) ([]string, error) {
	if roster == "" {
		return nil, &RosterError{Session: session}
	}
	roles := strings.Split(roster, ",")
	seen := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		if role == "" {
			return nil, &RosterError{Session: session, Roster: roster}
		}
		if _, exists := seen[role]; exists {
			return nil, &RosterError{Session: session, Roster: roster, Duplicate: role}
		}
		seen[role] = struct{}{}
	}
	return roles, nil
}

// decodeFleet parses @agentctl_fleet. Stored values are re-validated because
// tmux options are advisory: any same-user process can rewrite them.
func decodeFleet(value string) ([]config.RoleConfig, error) {
	entries := strings.Split(value, ",")
	roles := make([]config.RoleConfig, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		fields := strings.SplitN(entry, ":", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("entry %d %q is not role:harness:model", index+1, entry)
		}
		if err := config.ValidateRoleName(fields[0]); err != nil {
			return nil, fmt.Errorf("entry %d %q names invalid role %q", index+1, entry, fields[0])
		}
		if _, exists := seen[fields[0]]; exists {
			return nil, fmt.Errorf("entry %d %q repeats role %q", index+1, entry, fields[0])
		}
		seen[fields[0]] = struct{}{}
		harness, err := config.ParseHarness(fields[1])
		if err != nil {
			return nil, fmt.Errorf("entry %d %q names unknown harness %q", index+1, entry, fields[1])
		}
		if fields[2] != "" {
			if err := config.ValidateModelName(fields[2]); err != nil {
				return nil, fmt.Errorf("entry %d %q has invalid model %q", index+1, entry, fields[2])
			}
		}
		roles = append(roles, config.RoleConfig{Name: fields[0], Harness: harness, Model: fields[2]})
	}
	return roles, nil
}

func sameRoles(stored []config.RoleConfig, roster []string) bool {
	if len(stored) != len(roster) {
		return false
	}
	for index, role := range stored {
		if role.Name != roster[index] {
			return false
		}
	}
	return true
}

func containsRole(roster []string, role string) bool {
	for _, candidate := range roster {
		if candidate == role {
			return true
		}
	}
	return false
}

func roleIndex(roles []config.RoleConfig, role string) int {
	for index, candidate := range roles {
		if candidate.Name == role {
			return index
		}
	}
	return -1
}

func replaceRole(roles []config.RoleConfig, role config.RoleConfig) []config.RoleConfig {
	updated := append([]config.RoleConfig(nil), roles...)
	if index := roleIndex(updated, role.Name); index >= 0 {
		updated[index] = role
	}
	return updated
}
