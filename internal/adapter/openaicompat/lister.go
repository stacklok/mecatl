// Package openaicompat is a stdlib-only LEAF adapter that fetches a LIVE model
// catalog from any endpoint speaking the OpenAI-shaped GET /v1/models protocol —
// it names the PROTOCOL, not a specific vendor. It carries no behaviour beyond
// the HTTP GET + JSON→neutral mapping; nothing here knows about the agent,
// providers, ports, the composition layer, ToolHive, or any other adapter.
//
// # Provenance and wire shape
//
// The endpoint is:
//
//	GET <baseURL>/models
//
// where baseURL already ends in "/v1" (e.g. "http://127.0.0.1:14000/v1"). An
// optional bearer token is sent as `Authorization: Bearer <token>` when
// non-empty (many OpenAI-compatible gateways — including the ToolHive LLM
// gateway proxy — accept a placeholder credential rather than none at all).
// The response envelope is
// {"object":"list","data":[{"id","object","created","owned_by","display_name","context_window"}]}
// — decode only `id`, `display_name`, and `context_window`; everything else is
// ignored by encoding/json. Wire shape pinned to
// stacklok-enterprise-platform#2270 ("New data-plane GET /v1/models intercept...
// returns an OpenAI-shaped response
// (`{object:"list", data:[{id, object, created, owned_by, display_name}]}`)"),
// and extended by stacklok-enterprise-platform#3288; the fixture in testdata/
// mirrors that shape.
//
// # Layering
//
// LEAF adapter: net/http + encoding/json + stdlib ONLY. It imports NO domain
// (session/prompt/tool/governance), NO port, NO internal/app, NO
// providercatalog, and NO other adapter — and, critically, NO ToolHive Go
// package: this lister only ever speaks the generic OpenAI-shaped protocol.
// Its Model type is package-own and must never leak into the domain or port —
// only the composition layer (internal/app) reads it and maps it to its own
// neutral modelEntry type (so there is no import cycle: the adapter does not
// import internal/app).
package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/modeltext"
)

const (
	// maxResponseBytes caps the response body read so a hostile or pathological
	// body cannot OOM the process (CWE-770). 1 MiB comfortably holds a
	// per-credential gateway catalog (a handful to a few dozen models).
	maxResponseBytes = 1 << 20

	// defaultTimeout bounds the whole fetch when no client timeout is otherwise
	// configured (the injected client may carry its own; the ctx deadline also
	// applies). A live catalog fetch that hangs must not stall the caller.
	defaultTimeout = 5 * time.Second

	// maxIDRunes / maxNameRunes bound a SINGLE model's id and display name. The
	// 1 MiB whole-response cap above does not stop ONE hostile entry with a
	// multi-megabyte id/name from surviving into a picker row, so each field is
	// control-stripped then rune-truncated in the map loop (a picker label is
	// short by nature; these are generous). Rune-aware truncation never splits
	// a multi-byte boundary.
	maxIDRunes   = 256
	maxNameRunes = 512
)

// Model is the adapter's OWN neutral result type. The composition layer maps it
// to its composition-local modelEntry; it never leaves this package's caller
// as-is and carries nothing provider-private (no key, no URL).
type Model struct {
	ID           string
	DisplayName  string
	ContextLimit int
}

// StatusError is returned when the endpoint answers with a non-2xx status. The
// composition layer classifies it via errors.As (401/403 ⇒ "unauthorized";
// every other status, or a non-StatusError failure, ⇒ "unreachable").
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("openaicompat: unexpected status %d", e.Code)
}

// StatusCode exposes the response code for provider-neutral classification.
func (e *StatusError) StatusCode() int { return e.Code }

// Lister fetches a live OpenAI-shaped model catalog over an INJECTED
// *http.Client (tests pass a mock transport; production gets a default client
// with a sane timeout).
type Lister struct {
	baseURL     string
	bearerToken string
	httpClient  *http.Client
}

// RefuseRedirects is the shared CheckRedirect policy (CWE-918): a loopback
// gateway endpoint must never be allowed to bounce a request off-loopback via
// a redirect response. It is exported so the INFERENCE path (the openai
// adapter's WithHTTPClient, wired for the ToolHive gateway registry entry
// only — see internal/app/registry.go's newGatewayEntry) and this LISTING
// path share the exact same policy and cannot drift on wording/behaviour.
func RefuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// NewLister constructs a Lister against baseURL (already ending in "/v1")
// with an optional bearerToken (sent as `Authorization: Bearer <token>` when
// non-empty). NOTE the argument order — (baseURL, bearerToken, client) —
// deliberately differs from the sibling provider/anthropic.NewLister's
// (key, baseURL, client): don't copy-paste call sites between the two without
// checking. A nil client yields a default client with defaultTimeout AND
// RefuseRedirects (CWE-918): baseURL is a loopback address the CALLER already
// validated (composition never lets an operator point this at a remote host
// in v1), so a hostile/misconfigured listener on that port answering with a
// redirect must never be allowed to bounce the request off-loopback —
// production wiring may pass nil and tests inject a mock transport (which
// bypasses CheckRedirect entirely, so tests exercising the redirect gate use
// a real httptest server).
func NewLister(baseURL, bearerToken string, client *http.Client) *Lister {
	if client == nil {
		client = &http.Client{
			Timeout:       defaultTimeout,
			CheckRedirect: RefuseRedirects,
		}
	}
	return &Lister{baseURL: baseURL, bearerToken: bearerToken, httpClient: client}
}

// wireResponse mirrors the subset of the OpenAI-shaped /v1/models envelope
// this adapter consumes. Unmapped fields (object, created, owned_by, …) are
// ignored by encoding/json.
type wireResponse struct {
	Data []wireModel `json:"data"`
}

type wireModel struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name"`
	ContextWindow int    `json:"context_window"`
}

// ListModels GETs the live catalog and maps it to []Model. It is read-only and
// fail-safe to the caller: any transport, status, size, or parse error returns
// a non-nil error (the composition layer then classifies it and falls back to
// the embedded catalog / last-known-good). It NEVER panics.
func (l *Lister) ListModels(ctx context.Context) ([]Model, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, l.baseURL+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if l.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+l.bearerToken)
	}

	resp, err := l.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openaicompat: fetch models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, &StatusError{Code: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("openaicompat: read body: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("openaicompat: response exceeds %d-byte cap", maxResponseBytes)
	}

	var wire wireResponse
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("openaicompat: decode models: %w", err)
	}

	out := make([]Model, 0, len(wire.Data))
	for _, w := range wire.Data {
		id := modeltext.StripControls(w.ID)
		if id == "" {
			continue // defensive: skip a malformed/hostile entry with no id
		}
		out = append(out, Model{
			ID:           modeltext.TruncateRunes(id, maxIDRunes),
			DisplayName:  modeltext.TruncateRunes(modeltext.StripControls(w.DisplayName), maxNameRunes),
			ContextLimit: w.ContextWindow,
		})
	}
	return out, nil
}
