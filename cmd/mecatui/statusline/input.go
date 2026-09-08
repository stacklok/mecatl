package statusline

import "time"

// Input is the canonical, raw snapshot supplied to status handlers. It is
// intentionally a small allowlist: it carries display-safe session facts, never
// prompts, transcript/tool content, credentials, authentication metadata,
// diagnostics, or raw command output.
//
// Commands receive this exact data as JSON. Template rendering receives a
// private, StatusML-escaped projection so template interpolation cannot create
// markup or terminal controls. The raw input remains available to commands for
// ordinary display-safe status data.
//
// Renderer policy—clipping, safety/activity lanes, and context-bar glyph/style
// selection—does not belong here. Header and footer available widths are facts
// computed after those renderer reservations.
type Input struct {
	Version    uint8
	Server     ServerTarget
	Session    Session
	Model      Model
	Usage      Usage
	Context    Context
	Workspace  Workspace
	Terminal   Terminal
	MainAgent  MainAgent
	Delegation Delegation
	Clock      Clock
}

// Clock is the raw source-owned time fact. A submitted value is preserved until
// the source refreshes it; templates and future commands observe the same value.
type Clock struct {
	Now time.Time
}

// ServerTarget identifies the display target and connection mode, without any
// authentication or endpoint credential data. ConnectionMode is one of "embedded"
// (the local in-process server) or "connect" (an explicitly dialed server);
// an empty value means the mode is not yet known.
type ServerTarget struct {
	// DisplayTarget is the credential-free connection target shown in chrome.
	DisplayTarget  string
	ConnectionMode string
}

// Session carries optional user-facing session facts. Title is empty when no
// custom or generated title exists. Handle is the fixed, terminal-safe escaped
// session prefix used by ordinary mecatui presentation. Mode is the active
// permission mode. ReasoningEffort is either empty (unset or unsupported) or one
// of "low", "medium", "high", "xhigh", or "max".
type Session struct {
	Title           string
	Handle          string
	Mode            string
	ReasoningEffort string
}

// Model carries the provider-independent model facts selected for the session.
// ProviderID and ID are opaque routing identifiers; DisplayName is the operator-
// facing label. ContextWindow is the resolved model context capacity, not current
// session consumption.
type Model struct {
	ProviderID    string
	ID            string
	DisplayName   string
	Route         string
	ContextWindow ContextAtom
}

// UsageAtom carries an exact non-negative token count and the same count rendered
// with the standard compact display notation (for example, "1.1M"). Raw is for
// arithmetic or command handlers; Human is a display-ready atom.
type UsageAtom struct {
	Raw   int64
	Human string
}

// Usage is the cumulative session usage summary. CacheRead is input served from
// the prompt cache (a subset of Input); CacheWrite is input written to the prompt
// cache. CacheReadPercent is the integer percentage CacheRead.Raw/Input.Raw, or
// zero when the input count is zero.
type Usage struct {
	Input            UsageAtom
	Output           UsageAtom
	CacheRead        UsageAtom
	CacheWrite       UsageAtom
	CacheReadPercent int
}

// ContextAtom carries an exact token count and its standard compact display
// notation. It is separate from UsageAtom to make the context-window contract
// explicit at call sites.
type ContextAtom struct {
	Raw   int64
	Human string
}

// Context describes current context-window consumption. Percent is Used.Raw as
// an integer percentage of Window.Raw, or zero when the window is unknown. The
// visual bar is renderer-owned because its glyphs and semantic style depend on
// the active theme and available surface width.
type Context struct {
	Used    ContextAtom
	Window  ContextAtom
	Percent int
}

// Workspace is the active session workspace's display-safe provenance. Location
// is "local", "remote", or "unknown". Name is provider-supplied display metadata,
// not a filesystem basename. Path is supplied to status templates through their
// StatusML-escaped projection and to a direct local status command after the
// privileged local-context RPC has returned an eligible local root. It is otherwise
// empty and is not part of universal client projections.
type Workspace struct {
	Location string
	Name     string
	Path     string
}

// Terminal provides measured dimensions and independently reserved columns for
// status surfaces. Cols is the full terminal width. HeaderAvailCols and
// FooterAvailCols are the widths remaining after the renderer reserves mandatory
// header safety/navigation and footer activity lanes, respectively.
type Terminal struct {
	Rows            int
	Cols            int
	HeaderAvailCols int
	FooterAvailCols int
}

// MainAgent carries the main agent's display-safe activity. State is one of
// "connecting", "idle", "thinking", "running_tool", "awaiting_approval",
// "completed", "failed", or "cancelled". Activity is a bounded display label.
// Approval is "none" or "awaiting" and reflects whether a human verdict is pending.
type MainAgent struct {
	State    string
	Activity string
	Approval string
}

// DelegationStateCounts classifies delegated leaf work into mutually exclusive
// terminal and live states.
type DelegationStateCounts struct {
	Running          int
	AwaitingApproval int
	Completed        int
	Failed           int
	Cancelled        int
	Stopped          int
}

func (c DelegationStateCounts) add(other DelegationStateCounts) DelegationStateCounts {
	return DelegationStateCounts{
		Running:          c.Running + other.Running,
		AwaitingApproval: c.AwaitingApproval + other.AwaitingApproval,
		Completed:        c.Completed + other.Completed,
		Failed:           c.Failed + other.Failed,
		Cancelled:        c.Cancelled + other.Cancelled,
		Stopped:          c.Stopped + other.Stopped,
	}
}

// DelegationSummary is the display-oriented running/finished projection of one
// leaf family. Finished deliberately collapses the detailed terminal states;
// raw DelegationStateCounts remains available for command handlers.
type DelegationSummary struct {
	Running  int
	Finished int
}

// LiveTeam is present only while a team is active. Completed teams intentionally
// disappear from the footer, unlike parallel and direct-subagent summaries.
type LiveTeam struct {
	ID      string
	Working int
	Total   int
}

// Delegation groups the uniform state counts by flat leaf unit. Total is the
// component-wise sum of direct subagent, team member, and parallel branch.
type Delegation struct {
	Total          DelegationStateCounts
	DirectSubagent DelegationStateCounts
	TeamMember     DelegationStateCounts
	ParallelBranch DelegationStateCounts
	Subagents      DelegationSummary
	Parallel       DelegationSummary
	Team           LiveTeam
}

// Valid reports whether Total is exactly the sum of the leaf units.
func (d Delegation) Valid() bool {
	return d.Total == d.DirectSubagent.add(d.TeamMember).add(d.ParallelBranch)
}
