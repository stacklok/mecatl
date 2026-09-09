package tool

import (
	"context"
	"errors"
)

// ErrSearchUnavailable is the sentinel a SearchProvider returns when web search
// is DISABLED on the deployment — the operator-set kill switch (--websearch=off),
// for which no backend is wired at all. The WebSearch tool surfaces it to the
// model as an honest "disabled on this deployment" message rather than aborting
// the harness — mirroring the ErrNoShell precedent the Shell tool uses for a
// shell-less CommandRunner. It is DISTINCT from ErrSearchBackendDown: this means
// "intentionally off", that means "a configured/default backend tried and failed".
var ErrSearchUnavailable = errors.New("tool: no search provider configured")

// ErrSearchBackendDown is the sentinel a SearchProvider returns when a real,
// configured (or default) backend was attempted but is unreachable, rate-limited,
// or returned a fault/malformed result. It is the MANDATORY-degradation signal
// (issue #26): the WebSearch tool surfaces it as a model-visible message naming
// the upgrade path (set BRAVE_API_KEY or SEARXNG_URL) — never a silent empty
// result or a hang. It is DISTINCT from ErrSearchUnavailable (operator-disabled):
// a backend-down condition is environmental/transient, not a deployment choice.
var ErrSearchBackendDown = errors.New("tool: search backend unavailable")

// SearchProvider is the outbound web-search seam the WebSearch tool depends on,
// the way the Shell tool depends on CommandRunner. It lives here in engine/tool,
// NOT engine/port, for the same layering reason FileSystem/Workspace/CommandRunner
// do: it is a TOOL collaborator injected at execution, never a loop port the
// agent.Engine references. The agent loop never names this type — only the
// WebSearch tool does, which is what keeps web search optional in the catalog.
//
// Implementations must be safe for concurrent use: the read-parallel dispatcher
// fans out N concurrent WebSearch calls per turn, so an adapter that performs
// network I/O must carry its OWN per-call timeout AND a concurrency/rate limit
// internally (never relying on the dispatcher or the tool to bound egress).
type SearchProvider interface {
	// Search runs q and returns ranked results, best-first. The Limit on q is
	// already clamped by the tool before the call (the tool is the bounding choke
	// point); an implementation MAY further cap but must never EXCEED it. A
	// not-configured backend returns ErrSearchUnavailable; any other non-nil error
	// is a backend fault the tool renders as a model-facing tool error.
	Search(ctx context.Context, q SearchQuery) ([]SearchResult, error)
}

// SearchQuery is one web-search request. Query is the verbatim user/model query
// — implementations MUST pass it through unchanged (secret-scanning is the
// guardrails layer's job, not the search adapter's). Limit is the clamped result
// cap. Site and Freshness are optional refinements an adapter MAY map onto its
// backend's parameters (or ignore if unsupported).
type SearchQuery struct {
	// Query is the verbatim search string. Never mutated by an adapter.
	Query string
	// Limit is the maximum number of results to return, already clamped by the
	// tool to its default-when-absent and hard-max bounds.
	Limit int
	// Site, when non-empty, restricts results to a single site/domain (e.g.
	// "go.dev"). Adapter maps it onto the backend's site filter if supported.
	Site string
	// Freshness, when non-empty, is an opaque recency hint (e.g. "day", "week",
	// "month") an adapter MAY map onto its backend's recency filter.
	Freshness string
}

// SearchResult is one discovered source. URL is the candidate the model can then
// pass to WebFetch to retrieve the full page; the other fields are compact,
// attribution-oriented metadata. Every field is UNTRUSTED external content — the
// WebSearch tool fences it before it reaches the model.
type SearchResult struct {
	// Title is the result's title/headline.
	Title string
	// URL is the candidate source URL (the handle for a follow-up WebFetch).
	URL string
	// Snippet is a short excerpt/summary of the result.
	Snippet string
	// Date is an optional publication/last-modified date string, as the backend
	// reported it (no parsing/normalisation in the domain).
	Date string
	// Source is an optional source/publisher name (the attribution axis).
	Source string
}
