package server

import "fmt"

// SessionProfile is the session's TOOL-SURFACE profile, enum-as-string on the
// wire (CreateSessionRequest.profile — the Event.type/Result.stop idiom: a
// string field, additive values, no proto enum). It is a NEUTRAL value object
// owned by the server adapter, like ProviderSelector: the composition root
// (internal/app) interprets it when building the per-session engine; the
// adapter only validates and routes it. The profile is FIXED for the session
// lifetime.
type SessionProfile string

const (
	// ProfileDefault is the full filesystem profile. Composition binds the
	// server-owned deployment default before constructing this tool surface.
	ProfileDefault SessionProfile = ""
	// ProfileNoFS is the NO-FILESYSTEM profile (issue #55): no workspace, no
	// file tools (Read/ListDir/Edit/Write/Copy/Move/Remove/Grep/Glob), no Shell, no Parallel, no
	// SkillDraft — the agent works through MCP tools, memory, web fetch, Skill
	// bodies, and file-less delegation. It binds an exact no-FS EnvironmentRef and
	// requires a per-session engine because the shared engine has FS tools.
	ProfileNoFS SessionProfile = "no-fs"
)

// ParseSessionProfile validates a wire profile string. "" is the default
// profile; "no-fs" is the no-filesystem profile; anything else is a loud
// ErrInvalidArgument — never a silent fallback to the default profile (an
// operator asking for a constrained surface must not silently get the full one).
func ParseSessionProfile(s string) (SessionProfile, error) {
	switch p := SessionProfile(s); p {
	case ProfileDefault, ProfileNoFS:
		return p, nil
	default:
		return "", fmt.Errorf("%w: unknown session profile %q (supported: \"\" (default) and %q)", ErrInvalidArgument, s, ProfileNoFS)
	}
}
