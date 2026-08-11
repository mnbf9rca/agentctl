//go:build darwin

package status

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/mnbf9rca/agentctl/internal/shim"
	"github.com/mnbf9rca/agentctl/internal/tmuxx"
)

// ShimFleetReader returns one already-validated durable roster view.
type ShimFleetReader interface {
	Read(string) (ShimFleetRecord, error)
}

// ShimRoleObservation carries all concurrently applicable state candidates;
// ShimCollector, not the source, applies the normative first-match order.
type ShimRoleObservation struct {
	Candidates    []RuntimeState
	Confidence    Confidence
	ShimPID       int
	AnswererPID   int
	RecordShimPID int
	AdvisoryNonce string
	RecordNonce   string
	LocalRoot     string
	RecordedRoot  string
	ChildPID      int
	Cleanup       *ShimCleanup
}

// ShimRoleSource observes runtime claims before durable role state.
type ShimRoleSource interface {
	ObserveRole(context.Context, string, string) (ShimRoleObservation, error)
}

// ShimPresentationObservation is optional UI evidence and the interim
// objective aggregate note.
type ShimPresentationObservation struct {
	State PresentationState
	Note  string
}

// ShimPresentationSource enriches a complete runtime report without owning
// identity or state selection.
type ShimPresentationSource interface {
	ObservePresentation(context.Context, string, []string, []ShimAgent) (ShimPresentationObservation, error)
}

// ShimCollector gathers runtime-authoritative compatibility status.
type ShimCollector struct {
	fleets       ShimFleetReader
	roles        ShimRoleSource
	presentation ShimPresentationSource
}

// NewShimCollector constructs the compatibility collector kept beside the
// legacy tmux collector until PR 7.
func NewShimCollector(fleets ShimFleetReader, roles ShimRoleSource, presentation ShimPresentationSource) ShimCollector {
	return ShimCollector{fleets: fleets, roles: roles, presentation: presentation}
}

// Collect enumerates the durable roster in order and lets runtime evidence
// select state. A nil presentation source factually means no tmux presentation.
func (c ShimCollector) Collect(ctx context.Context, session string) (ShimReport, error) {
	if c.fleets == nil || c.roles == nil {
		return ShimReport{}, errors.New("shim status collector requires fleet and role sources")
	}
	record, err := c.fleets.Read(session)
	if err != nil {
		return ShimReport{}, err
	}
	report := ShimReport{
		Schema: 1, Session: session, Presentation: PresentationGone,
		Agents: make([]ShimAgent, 0, len(record.Roster)),
	}
	for _, role := range record.Roster {
		configured, ok := record.Roles[role]
		if !ok {
			return ShimReport{}, fmt.Errorf("durable roster role %q has no configuration", role)
		}
		observation, err := c.roles.ObserveRole(ctx, session, role)
		if err != nil {
			return ShimReport{}, err
		}
		state, err := selectRuntimeState(observation.Candidates)
		if err != nil {
			return ShimReport{}, fmt.Errorf("observe role %q: %w", role, err)
		}
		if observation.Confidence != ConfidenceAnchored && observation.Confidence != ConfidenceUnanchored {
			return ShimReport{}, fmt.Errorf("observe role %q: invalid confidence %q", role, observation.Confidence)
		}
		report.Agents = append(report.Agents, ShimAgent{
			Role: role, Harness: configured.Harness, Model: configured.Model, Effort: configured.Effort,
			Directory: record.Directory, Confidence: observation.Confidence,
			ShimPID: observation.ShimPID, AnswererPID: observation.AnswererPID,
			RecordShimPID: observation.RecordShimPID, LocalRoot: observation.LocalRoot,
			AdvisoryNonce: observation.AdvisoryNonce, RecordNonce: observation.RecordNonce,
			RecordedRoot: observation.RecordedRoot, ChildPID: observation.ChildPID,
			Cleanup: observation.Cleanup, State: state,
		})
	}
	if c.presentation != nil {
		observation, err := c.presentation.ObservePresentation(ctx, session, append([]string(nil), record.Roster...), append([]ShimAgent(nil), report.Agents...))
		if err != nil {
			report.Presentation = PresentationUnavailable
			return report, nil
		}
		switch observation.State {
		case PresentationPresent, PresentationGone, PresentationUnavailable:
			report.Presentation = observation.State
		default:
			report.Presentation = PresentationUnavailable
		}
		report.Note = observation.Note
	}
	return report, nil
}

func selectRuntimeState(candidates []RuntimeState) (RuntimeState, error) {
	best := len(runtimeStates)
	for _, candidate := range candidates {
		found := false
		for rank, state := range runtimeStates {
			if candidate == state {
				found = true
				if rank < best {
					best = rank
				}
				break
			}
		}
		if !found {
			return "", fmt.Errorf("unknown runtime state %q", candidate)
		}
	}
	if best == len(runtimeStates) {
		return "", errors.New("runtime observation supplied no state")
	}
	return runtimeStates[best], nil
}

type shimRuntimeAccess interface {
	LocalStateRoot() string
	ReadAdvisory(string, string) (shim.Advisory, error)
	ObserveTopology(string, string) (runtimeTopology, error)
	ReadRecord(string, string) (shim.Record, error)
	Observe(context.Context, string, string) (shim.Response, error)
}

type runtimeTopology struct {
	SocketPresent bool
	Claim         shim.ClaimObservation
}

type shimObserver interface {
	Observe(context.Context, string, string) (shim.Response, error)
}

// RuntimeShimRoleSource composes descriptor-anchored runtime/durable reads,
// the versioned shim observation, and the sole child process oracle.
type RuntimeShimRoleSource struct {
	access         shimRuntimeAccess
	observeProcess func(int, shim.StartToken) shim.ProcessResult
}

// NewRuntimeShimRoleSource constructs the production runtime role source.
func NewRuntimeShimRoleSource(namespace *shim.Namespace, client shimObserver) RuntimeShimRoleSource {
	return newRuntimeShimRoleSource(&namespaceRuntimeAccess{namespace: namespace, client: client}, shim.ObserveProcess)
}

func newRuntimeShimRoleSource(access shimRuntimeAccess, observeProcess func(int, shim.StartToken) shim.ProcessResult) RuntimeShimRoleSource {
	return RuntimeShimRoleSource{access: access, observeProcess: observeProcess}
}

// ObserveRole examines the volatile anchor before any durable path. Missing
// anchor observations remain unanchored through every derived state.
func (s RuntimeShimRoleSource) ObserveRole(ctx context.Context, session, role string) (ShimRoleObservation, error) {
	if s.access == nil || s.observeProcess == nil {
		return ShimRoleObservation{}, errors.New("runtime shim role source requires runtime and process observers")
	}
	advisory, advisoryErr := s.access.ReadAdvisory(session, role)
	if advisoryErr != nil {
		var malformed *shim.AdvisoryParseError
		if errors.As(advisoryErr, &malformed) {
			return oneRuntimeState(RuntimeStateInvalidRecord, ConfidenceUnanchored, 0, 0), nil
		}
		if !errors.Is(advisoryErr, os.ErrNotExist) {
			return oneRuntimeState(RuntimeStateCouldNotObserve, ConfidenceUnanchored, 0, 0), nil
		}
		topology, topologyErr := s.access.ObserveTopology(session, role)
		var invalidSocket *shim.SocketTopologyError
		if errors.As(topologyErr, &invalidSocket) || (topologyErr == nil && topology.SocketPresent && !topology.Claim.Held) {
			return oneRuntimeState(RuntimeStateAnswererDisagreement, ConfidenceUnanchored, 0, 0), nil
		}
		if topologyErr != nil && !errors.Is(topologyErr, os.ErrNotExist) {
			return oneRuntimeState(RuntimeStateCouldNotObserve, ConfidenceUnanchored, 0, 0), nil
		}
		return s.observeDurable(session, role, ConfidenceUnanchored, shim.Advisory{}, topology.Claim.Held)
	}
	if err := advisory.CompareStateRoot(s.access.LocalStateRoot()); err != nil {
		observation := oneRuntimeState(RuntimeStateRootDisagreement, ConfidenceAnchored, advisory.ShimPID, 0)
		observation.LocalRoot = s.access.LocalStateRoot()
		observation.RecordedRoot = advisory.StateRoot
		return observation, nil
	}
	topology, topologyErr := s.access.ObserveTopology(session, role)
	var invalidSocket *shim.SocketTopologyError
	if errors.As(topologyErr, &invalidSocket) || (topologyErr == nil && topology.SocketPresent && !topology.Claim.Held) {
		return oneRuntimeState(RuntimeStateAnswererDisagreement, ConfidenceAnchored, advisory.ShimPID, 0), nil
	}
	if topologyErr != nil {
		return oneRuntimeState(RuntimeStateCouldNotObserve, ConfidenceAnchored, advisory.ShimPID, 0), nil
	}
	if !topology.SocketPresent {
		return s.observeDurable(session, role, ConfidenceAnchored, advisory, topology.Claim.Held)
	}

	response, observeErr := s.access.Observe(ctx, session, role)
	if observeErr == nil {
		return observationFromRuntimeResponse(response, advisory.ShimPID), nil
	}
	var rootDisagreement *shim.StateRootDisagreementError
	if errors.As(observeErr, &rootDisagreement) {
		observation := oneRuntimeState(RuntimeStateRootDisagreement, ConfidenceAnchored, advisory.ShimPID, 0)
		observation.LocalRoot, observation.RecordedRoot = rootDisagreement.LocalRoot, rootDisagreement.RecordedRoot
		return observation, nil
	}
	var skew *shim.ProtocolSkewError
	if errors.As(observeErr, &skew) {
		return oneRuntimeState(RuntimeStateProtocolSkew, ConfidenceAnchored, advisory.ShimPID, 0), nil
	}
	var answerer *shim.AnswererDisagreementError
	if errors.As(observeErr, &answerer) {
		observation := oneRuntimeState(RuntimeStateAnswererDisagreement, ConfidenceAnchored, advisory.ShimPID, 0)
		observation.AnswererPID = answerer.AnswererPID
		return observation, nil
	}
	var networkError *net.OpError
	if (errors.As(observeErr, &networkError) && networkError.Op == "dial") || errors.Is(observeErr, os.ErrNotExist) {
		return s.observeDurable(session, role, ConfidenceAnchored, advisory, topology.Claim.Held)
	}
	return oneRuntimeState(RuntimeStateCouldNotObserve, ConfidenceAnchored, advisory.ShimPID, 0), nil
}

func (s RuntimeShimRoleSource) observeDurable(
	session string,
	role string,
	confidence Confidence,
	advisory shim.Advisory,
	claimHeld bool,
) (ShimRoleObservation, error) {
	record, err := s.access.ReadRecord(session, role)
	if errors.Is(err, os.ErrNotExist) {
		if claimHeld {
			return oneRuntimeState(RuntimeStateStarting, confidence, advisory.ShimPID, 0), nil
		}
		return oneRuntimeState(RuntimeStateMissing, confidence, 0, 0), nil
	}
	if err != nil {
		var malformed *shim.RecordParseError
		if errors.As(err, &malformed) {
			return oneRuntimeState(RuntimeStateInvalidRecord, confidence, advisory.ShimPID, 0), nil
		}
		return oneRuntimeState(RuntimeStateCouldNotObserve, confidence, advisory.ShimPID, 0), nil
	}
	shimPID := record.ShimPID
	if advisory.ShimPID != 0 {
		shimPID = advisory.ShimPID
		if record.ShimPID != advisory.ShimPID || record.Nonce != advisory.Nonce {
			observation := oneRuntimeState(RuntimeStateAnswererDisagreement, confidence, advisory.ShimPID, record.ChildPID)
			observation.RecordShimPID = record.ShimPID
			observation.AdvisoryNonce = advisory.Nonce
			observation.RecordNonce = record.Nonce
			return observation, nil
		}
	}
	if record.State == shim.RecordStateCleanupFailed {
		observation := oneRuntimeState(RuntimeStateCleanupFailed, confidence, shimPID, record.ChildPID)
		observation.Cleanup = &ShimCleanup{
			Cause: record.Cleanup.Cause, Observation: string(record.Cleanup.Observation),
			Remaining: append([]string(nil), record.Cleanup.Remaining...),
		}
		return observation, nil
	}
	if claimHeld {
		return oneRuntimeState(RuntimeStateStarting, confidence, shimPID, record.ChildPID), nil
	}
	if record.State == shim.RecordStateChildStarting {
		return oneRuntimeState(RuntimeStateIndeterminateChildStarting, confidence, shimPID, 0), nil
	}
	if record.State != shim.RecordStateChildRecorded || record.ChildStartToken == nil {
		return oneRuntimeState(RuntimeStateInvalidRecord, confidence, shimPID, record.ChildPID), nil
	}
	result := s.observeProcess(record.ChildPID, *record.ChildStartToken)
	state := runtimeStateFromProcess(result.Observation)
	return oneRuntimeState(state, confidence, shimPID, record.ChildPID), nil
}

func observationFromRuntimeResponse(response shim.Response, advisoryShimPID int) ShimRoleObservation {
	shimPID := advisoryShimPID
	if response.ShimPID != nil {
		shimPID = *response.ShimPID
	}
	childPID := 0
	if response.ChildPID != nil {
		childPID = *response.ChildPID
	}
	return oneRuntimeState(runtimeStateFromOutcome(response.Outcome), ConfidenceAnchored, shimPID, childPID)
}

func oneRuntimeState(state RuntimeState, confidence Confidence, shimPID, childPID int) ShimRoleObservation {
	return ShimRoleObservation{Candidates: []RuntimeState{state}, Confidence: confidence, ShimPID: shimPID, ChildPID: childPID}
}

func runtimeStateFromOutcome(outcome shim.Outcome) RuntimeState {
	switch outcome {
	case shim.OutcomeInvalidRecord:
		return RuntimeStateInvalidRecord
	case shim.OutcomeStateRootDisagreement:
		return RuntimeStateRootDisagreement
	case shim.OutcomeProtocolSkew:
		return RuntimeStateProtocolSkew
	case shim.OutcomeAnswererDisagreement:
		return RuntimeStateAnswererDisagreement
	case shim.OutcomeCleanupFailed:
		return RuntimeStateCleanupFailed
	case shim.OutcomeConcurrentContender:
		return RuntimeStateConcurrentContender
	case shim.OutcomeStarting:
		return RuntimeStateStarting
	case shim.OutcomeStopping, shim.OutcomeShimStopping:
		return RuntimeStateStopping
	case shim.OutcomeStopped:
		return RuntimeStateStopped
	case shim.OutcomeIndeterminateChildStarting:
		return RuntimeStateIndeterminateChildStarting
	case shim.OutcomeRunning:
		return RuntimeStateRunning
	case shim.OutcomeOrphan:
		return RuntimeStateOrphan
	case shim.OutcomePresentTokenDisagreement:
		return RuntimeStatePresentTokenDisagreement
	case shim.OutcomePresentNotOurs:
		return RuntimeStatePresentNotOurs
	case shim.OutcomeStaleRecord:
		return RuntimeStateStaleRecord
	case shim.OutcomeMissing:
		return RuntimeStateMissing
	default:
		return RuntimeStateCouldNotObserve
	}
}

func runtimeStateFromProcess(observation shim.ProcessObservation) RuntimeState {
	switch observation {
	case shim.ProcessAbsent:
		return RuntimeStateStaleRecord
	case shim.ProcessPresentMatch:
		return RuntimeStateOrphan
	case shim.ProcessPresentTokenDisagreement:
		return RuntimeStatePresentTokenDisagreement
	case shim.ProcessPresentNotOurs:
		return RuntimeStatePresentNotOurs
	default:
		return RuntimeStateCouldNotObserve
	}
}

type namespaceRuntimeAccess struct {
	namespace *shim.Namespace
	client    shimObserver
}

type runtimePresentationClient interface {
	FindPresentationSession(context.Context, string) (tmuxx.Session, bool, error)
	ListWindows(context.Context, tmuxx.SessionID) ([]tmuxx.Window, error)
	ListPanes(context.Context, tmuxx.WindowID) ([]tmuxx.Pane, error)
}

// RuntimePresentationSource observes optional tmux UI without participating
// in runtime identity.
type RuntimePresentationSource struct{ client runtimePresentationClient }

// NewRuntimePresentationSource constructs additive presentation enrichment.
func NewRuntimePresentationSource(client runtimePresentationClient) RuntimePresentationSource {
	return RuntimePresentationSource{client: client}
}

// ObservePresentation reports present/gone and the exact interim aggregate
// note only at its objective threshold.
func (s RuntimePresentationSource) ObservePresentation(
	ctx context.Context,
	session string,
	roster []string,
	agents []ShimAgent,
) (ShimPresentationObservation, error) {
	if s.client == nil {
		return ShimPresentationObservation{}, errors.New("presentation client is unavailable")
	}
	presentation, present, err := s.client.FindPresentationSession(ctx, session)
	if err != nil {
		return ShimPresentationObservation{}, err
	}
	if !present {
		return ShimPresentationObservation{State: PresentationGone}, nil
	}
	observation := ShimPresentationObservation{State: PresentationPresent}
	if len(agents) != len(roster) {
		return observation, nil
	}
	for _, agent := range agents {
		if agent.State != RuntimeStateMissing {
			return observation, nil
		}
	}
	windows, err := s.client.ListWindows(ctx, presentation.ID)
	if err != nil {
		return ShimPresentationObservation{}, err
	}
	rosterSet := make(map[string]bool, len(roster))
	for _, role := range roster {
		rosterSet[role] = true
	}
	var roleless []tmuxx.Window
	for _, window := range windows {
		if window.Role == "" {
			roleless = append(roleless, window)
		}
		if rosterSet[window.Name] {
			return observation, nil
		}
	}
	if len(roleless) != 1 {
		return observation, nil
	}
	panes, err := s.client.ListPanes(ctx, roleless[0].ID)
	if err != nil {
		return ShimPresentationObservation{}, err
	}
	if len(panes) == len(roster) {
		observation.Note = fmt.Sprintf("all %d roster roles are missing; unmanaged window %q has %d panes", len(roster), roleless[0].Name, len(panes))
	}
	return observation, nil
}

func (a *namespaceRuntimeAccess) LocalStateRoot() string {
	if a == nil || a.namespace == nil {
		return ""
	}
	return a.namespace.StateRoot
}

func (a *namespaceRuntimeAccess) ReadAdvisory(session, role string) (shim.Advisory, error) {
	if a == nil || a.namespace == nil {
		return shim.Advisory{}, errors.New("runtime namespace is unavailable")
	}
	path, err := a.namespace.ExistingRuntimeRolePath(session, role)
	if err != nil {
		return shim.Advisory{}, err
	}
	defer func() { _ = path.Close() }()
	return shim.ReadAdvisory(path)
}

func (a *namespaceRuntimeAccess) ObserveTopology(session, role string) (runtimeTopology, error) {
	if a == nil || a.namespace == nil {
		return runtimeTopology{}, errors.New("runtime namespace is unavailable")
	}
	path, err := a.namespace.ExistingRuntimeRolePath(session, role)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeTopology{}, nil
	}
	if err != nil {
		return runtimeTopology{}, err
	}
	defer func() { _ = path.Close() }()
	present, err := shim.SocketPresent(path)
	if err != nil {
		return runtimeTopology{}, err
	}
	claim, err := shim.ObserveClaim(path)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeTopology{SocketPresent: present}, nil
	}
	if err != nil {
		return runtimeTopology{}, err
	}
	return runtimeTopology{SocketPresent: present, Claim: claim}, nil
}

func (a *namespaceRuntimeAccess) ReadRecord(session, role string) (shim.Record, error) {
	if a == nil || a.namespace == nil {
		return shim.Record{}, errors.New("runtime namespace is unavailable")
	}
	path, err := a.namespace.ExistingDurableRolePath(session, role)
	if err != nil {
		return shim.Record{}, err
	}
	defer func() { _ = path.Close() }()
	return shim.ReadRecord(path)
}

func (a *namespaceRuntimeAccess) Observe(ctx context.Context, session, role string) (shim.Response, error) {
	if a == nil || a.client == nil {
		return shim.Response{}, errors.New("shim observer is unavailable")
	}
	return a.client.Observe(ctx, session, role)
}
