package client

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// The session-refetch surface: unary session wrappers, the messages the ui's
// footer context-meter heal and mode switcher consume, narrow interfaces, and the
// tea.Cmd constructors. As with the rest of this package, NO proto type leaks past
// this file — the ui consumes only proto-free value objects / msgs.

// SessionSnapshot is the proto-free subset of a server session snapshot mecatui needs.
type SessionSnapshot struct {
	Mode          string
	State         string
	Placement     Placement
	CreatedAt     int64
	ResolvedModel ResolvedModel
	// Usage is the canonical cumulative main-session ledger. It is distinct from
	// ContextOccupancy, which is only the latest context-meter display state.
	Usage Usage
	// ContextOccupancy is nil when a legacy or pre-turn snapshot has no known
	// context-meter numerator.
	ContextOccupancy *ContextOccupancy
	Title            string
	TitleProvenance  string
	TitleRevision    uint64
	// Capabilities is the server's feature-advertisement snapshot from the Session
	// proto (the SAME global value CreateSessionResponse carries), with per-session
	// media overlaid when SessionCapabilities is present. A client that reloads or
	// switches to a persisted session (continue, /effort fork, /clear successor)
	// reads this to re-derive its affordances. An older server (nil fields) yields
	// the zero value, which the consumer treats as "keep current caps".
	Capabilities Capabilities
}

// ContextOccupancy is the optional latest context-meter display value from a
// session snapshot. It is neither lifetime usage nor a model budget.
type ContextOccupancy struct {
	InputTokens int64
	Estimated   bool
}

func contextOccupancyFrom(occupancy *mecatlv1.ContextOccupancy) *ContextOccupancy {
	if occupancy == nil {
		return nil
	}
	return &ContextOccupancy{InputTokens: occupancy.GetInputTokens(), Estimated: occupancy.GetEstimated()}
}

func snapshotFrom(s *mecatlv1.Session) SessionSnapshot {
	return snapshotFromWithGlobalCapabilities(s, Capabilities{})
}

func snapshotFromWithGlobalCapabilities(s *mecatlv1.Session, global Capabilities) SessionSnapshot {
	if s == nil {
		return SessionSnapshot{Mode: ModeDefaultString}
	}
	return SessionSnapshot{
		Mode:             ModeString(s.GetMode()),
		State:            s.GetState(),
		Placement:        placementFrom(s.GetPlacement()),
		CreatedAt:        s.GetCreatedAtUnix(),
		ResolvedModel:    resolvedModelFrom(s.GetResolvedModel()),
		Usage:            usageFrom(s.GetTokenUsage()["main"].GetTotal()),
		ContextOccupancy: contextOccupancyFrom(s.GetLatestContextOccupancy()),
		Title:            titleFromProto(s),
		TitleProvenance:  titleProvenanceFromProto(s),
		TitleRevision:    s.GetTitleMetadata().GetRevision(),
		Capabilities:     capabilitiesWithSessionMediaFrom(global, s.GetSessionCapabilities()),
	}
}

func titleFromProto(s *mecatlv1.Session) string {
	return s.GetTitleMetadata().GetTitle()
}

func titleProvenanceFromProto(s *mecatlv1.Session) string {
	return s.GetTitleMetadata().GetProvenance()
}

// GetSession looks up an existing session by id and returns the server-authored
// session snapshot subset mecatui needs: the current permission mode and the
// EFFECTIVE provider+model the server has resolved it to (echoed verbatim,
// including the context window). This is the self-healing refetch the footer
// context meter uses:
// for a session on a LIVE-ONLY model (present in the live /models listing but not
// the curated catalog — e.g. an OpenRouter openai/gpt-5.5) the create-time echo can
// carry a 0 / curated-floor window when the async live model-list swap had not yet
// landed; once it has, the server's ResolvedModel resolves the real live window
// (live-first via the same windowResolver the engine compacts at) and GetSession
// returns it. A nil Session/ResolvedModel (older server) yields zero values (see
// snapshotFrom / resolvedModelFrom).
func (c *Client) GetSession(ctx context.Context, id string) (SessionSnapshot, error) {
	resp, err := c.svc.GetSession(withSessionAffinity(ctx, id), &mecatlv1.GetSessionRequest{SessionId: id})
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("get session: %w", err)
	}
	global, err := c.compatibilityCapabilities(ctx)
	if err != nil {
		// Servers predating GetCompatibilityInfo still support session resume. Their
		// session media snapshot remains authoritative; absent global bits degrade
		// safely to false rather than making resume fail.
		global = Capabilities{}
	}
	return snapshotFromWithGlobalCapabilities(resp.GetSession(), global), nil
}

// SetMode asks the server to change the session's permission posture and returns
// the updated authoritative mode. Mid-turn changes are rejected by the server;
// callers that want next-prompt semantics should defer and retry once idle.
func (c *Client) SetMode(ctx context.Context, id, mode string) (string, error) {
	resp, err := c.svc.SetMode(withSessionAffinity(ctx, id), &mecatlv1.SetModeRequest{SessionId: id, Mode: ModeFromString(mode)})
	if err != nil {
		return "", fmt.Errorf("set mode: %w", err)
	}
	return snapshotFrom(resp.GetSession()).Mode, nil
}

// ResolvedModelMsg carries the result of a GetSession refetch (the footer
// context-meter heal, issue #66, the plan-approval mode+model refresh,
// issue #206, and the caps-heal path for /sessions continue + /effort fork,
// issue #348). SessionID is STAMPED on every result — success AND error — so
// the reducer can drop a result that landed AFTER a /models switch rebound the
// ui to a new session (a stale window must never clobber the new session's
// denominator). Err set ⇒ the refetch failed; the reducer keeps the current
// snapshot-derived display state (benign — the heal simply retries on the next
// turn boundary).
//
// ContextOccupancy carries the persisted latest context-meter numerator. It
// lets an authoritative startup-resume refetch restore the display state when
// the initial snapshot was incomplete, without treating cumulative usage as
// current context occupancy. It is adopted only when
// AdoptContextOccupancy identifies that startup-resume handoff.
//
// Mode carries the server-confirmed permission posture from the session snapshot.
// When non-empty (the plan-approval refresh path) the reducer applies it to the
// header mode echo; the footer-heal path may leave it empty (the mode is unchanged).
//
// Title carries the session's stored title from the same GetSession refetch — the
// self-heal channel for the terminal window title on the carryover/fork/adopt
// paths where the server already set a title this client never saw (the on-sent
// set-once in submitPrompt only seeds from a prompt the user typed HERE). The
// reducer adopts it only when the local sessionTitle is still empty (set-once).
//
// Capabilities carries the server's global feature-advertisement snapshot with
// per-session media overlaid when SessionCapabilities was present (issue #348).
// It arrives on the SAME GetSession refetch so the caps-heal path (/sessions
// continue, /effort fork) can re-derive affordances in one round-trip. A zero
// value means an older server omitted both fields; the reducer keeps current caps
// untouched. SessionMediaPresent distinguishes an explicit text-only media
// snapshot from that older-server absence.
type ResolvedModelMsg struct {
	SessionID string
	Resolved  ResolvedModel
	// ContextOccupancy is the persisted latest context-meter display state from
	// the same authoritative snapshot as Resolved.
	ContextOccupancy *ContextOccupancy
	// AdoptContextOccupancy scopes occupancy adoption to startup resume so a
	// delayed ordinary model refresh cannot overwrite newer live turn metrics.
	AdoptContextOccupancy bool
	// StartupResumeRefreshAttempt orders the bounded startup-resume denominator
	// retries. It is meaningful only with AdoptContextOccupancy.
	StartupResumeRefreshAttempt int
	Mode                        string
	State                       string
	Placement                   Placement
	CreatedAt                   int64
	// Title is the session's stored title from the snapshot (self-heal channel for
	// the window title). See the struct doc.
	Title string
	// TitleProvenance carries the title's source alongside Title.
	TitleProvenance string
	// TitleRevision orders authoritative title metadata updates; zero is legacy.
	TitleRevision uint64
	// Capabilities is the server's feature-advertisement snapshot. See the struct doc.
	Capabilities Capabilities
	Err          error
}

// SessionGetter is the narrow subset of *Client that RefreshResolvedModelCmd needs.
// Splitting it out keeps the ui injectable with a fake for offline tests; *Client
// (and the ui's wider SessionCreator, via the sessionAdapter) satisfies it.
type SessionGetter interface {
	GetSession(ctx context.Context, id string) (SessionSnapshot, error)
}

// ModeSetter changes a server-side session's permission mode.
type ModeSetter interface {
	SetMode(ctx context.Context, id, mode string) (string, error)
}

// ModeChangedMsg carries the async result of SetModeCmd.
type ModeChangedMsg struct {
	SessionID string
	Requested string
	Mode      string
	Err       error
}

// SetModeCmd asks the server to change a session's permission mode off the update goroutine.
func SetModeCmd(ctx context.Context, s ModeSetter, id, mode string) tea.Cmd {
	return func() tea.Msg {
		actual, err := s.SetMode(ctx, id, mode)
		return ModeChangedMsg{SessionID: id, Requested: mode, Mode: actual, Err: err}
	}
}

// RefreshResolvedModelCmd refetches the resolved model for session id off the
// update goroutine; the result (success or error) arrives as a ResolvedModelMsg
// with SessionID stamped so the reducer can correlate/drop it. It backs the
// footer context-meter denominator heal, plan-approval model/mode refresh, and
// caps-heal path for /sessions continue + /effort fork.
func RefreshResolvedModelCmd(ctx context.Context, g SessionGetter, id string) tea.Cmd {
	return refreshResolvedModelCmd(ctx, g, id, false, 0)
}

// RefreshStartupResumeCmd additionally carries persisted context occupancy for
// the startup-resume handoff. Its result is marked so the UI can restore this
// display-only snapshot without allowing ordinary delayed refreshes to replace
// newer live turn metrics.
func RefreshStartupResumeCmd(ctx context.Context, g SessionGetter, id string) tea.Cmd {
	return refreshResolvedModelCmd(ctx, g, id, true, 0)
}

// RefreshStartupResumeRetryCmd retries startup-resume model metadata after an
// explicitly provisional zero context window. attempt is diagnostic ordering;
// the UI owns the bounded retry policy and stale-session guard.
func RefreshStartupResumeRetryCmd(ctx context.Context, g SessionGetter, id string, attempt int) tea.Cmd {
	return refreshResolvedModelCmd(ctx, g, id, true, attempt)
}

func refreshResolvedModelCmd(ctx context.Context, g SessionGetter, id string, adoptContextOccupancy bool, startupResumeRefreshAttempt int) tea.Cmd {
	return func() tea.Msg {
		snap, err := g.GetSession(ctx, id)
		return ResolvedModelMsg{
			SessionID: id, Resolved: snap.ResolvedModel, ContextOccupancy: snap.ContextOccupancy, AdoptContextOccupancy: adoptContextOccupancy, StartupResumeRefreshAttempt: startupResumeRefreshAttempt, Mode: snap.Mode,
			State: snap.State, Placement: snap.Placement, CreatedAt: snap.CreatedAt,
			Title: snap.Title, TitleProvenance: snap.TitleProvenance, TitleRevision: snap.TitleRevision, Capabilities: snap.Capabilities, Err: err,
		}
	}
}
