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
	Mode            string
	State           string
	Placement       Placement
	CreatedAt       int64
	ResolvedModel   ResolvedModel
	Title           string
	TitleProvenance string
	// Capabilities is the server's feature-advertisement snapshot from the Session
	// proto (the SAME value CreateSessionResponse carries). A client that reloads
	// or switches to a persisted session (continue, /effort fork, /clear successor)
	// reads this to re-derive its affordances. An older server (nil field) yields
	// the zero value, which the consumer treats as "keep current caps".
	Capabilities Capabilities
}

func snapshotFrom(s *mecatlv1.Session) SessionSnapshot {
	if s == nil {
		return SessionSnapshot{Mode: ModeDefaultString}
	}
	return SessionSnapshot{
		Mode:            ModeString(s.GetMode()),
		State:           s.GetState(),
		Placement:       placementFrom(s.GetPlacement()),
		CreatedAt:       s.GetCreatedAtUnix(),
		ResolvedModel:   resolvedModelFrom(s.GetResolvedModel()),
		Title:           s.GetTitle(),
		TitleProvenance: s.GetTitleProvenance(),
		Capabilities:    capabilitiesFrom(s.GetCapabilities()),
	}
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
	resp, err := c.svc.GetSession(ctx, &mecatlv1.GetSessionRequest{SessionId: id})
	if err != nil {
		return SessionSnapshot{}, fmt.Errorf("get session: %w", err)
	}
	return snapshotFrom(resp.GetSession()), nil
}

// SetMode asks the server to change the session's permission posture and returns
// the updated authoritative mode. Mid-turn changes are rejected by the server;
// callers that want next-prompt semantics should defer and retry once idle.
func (c *Client) SetMode(ctx context.Context, id, mode string) (string, error) {
	resp, err := c.svc.SetMode(ctx, &mecatlv1.SetModeRequest{SessionId: id, Mode: ModeFromString(mode)})
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
// denominator (benign — the heal simply retries on the next turn boundary).
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
// Capabilities carries the server's feature-advertisement snapshot from the
// Session proto (issue #348). It arrives on the SAME GetSession refetch so the
// caps-heal path (/sessions continue, /effort fork) can re-derive affordances in
// one round-trip. A zero value means an older server (field absent) — the reducer
// keeps the current caps untouched (fail-conservative).
type ResolvedModelMsg struct {
	SessionID string
	Resolved  ResolvedModel
	Mode      string
	State     string
	Placement Placement
	CreatedAt int64
	// Title is the session's stored title from the snapshot (self-heal channel for
	// the window title). See the struct doc.
	Title string
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
// with SessionID stamped so the reducer can correlate/drop it. It backs two paths:
//
//  1. The footer context-meter heal: the ui fires it on a turn boundary while the
//     meter's denominator is still unknown, and the ResolvedModelMsg arm raises the
//     window once the server's live-first resolution heals it.
//  2. The plan-approval mode+model refresh (issue #206): after a plan_approved
//     terminal, the ui fires it to refetch the server's flipped mode (plan→default/
//     acceptEdits) and the execute model, so the header updates from the session
//     snapshot.
//
// Mode is carried alongside ResolvedModel so the reducer can update both the mode
// echo and the effective model in one refetch (reusing the ResolvedModelMsg arm).
// Capabilities is carried so the caps-heal path (/sessions continue, /effort fork,
// issue #348) can re-derive affordances in the same round-trip.
func RefreshResolvedModelCmd(ctx context.Context, g SessionGetter, id string) tea.Cmd {
	return func() tea.Msg {
		snap, err := g.GetSession(ctx, id)
		return ResolvedModelMsg{
			SessionID: id, Resolved: snap.ResolvedModel, Mode: snap.Mode,
			State: snap.State, Placement: snap.Placement, CreatedAt: snap.CreatedAt,
			Title: snap.Title, Capabilities: snap.Capabilities, Err: err,
		}
	}
}
