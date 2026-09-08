package anthropic

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// listerPageLimit is the per-page size for the paginated /v1/models fetch. The
// SDK's ListAutoPaging follows the cursor across pages; a generous page keeps the
// round-trips low (Anthropic's model count is small — tens, not thousands).
const listerPageLimit int64 = 1000

const maxModelsResponseBytes int64 = 1 << 20

var errModelsResponseTooLarge = errors.New("anthropic: models response exceeds byte cap")

type cappedReadCloser struct {
	io.ReadCloser
	remaining int64
}

func (r *cappedReadCloser) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errModelsResponseTooLarge
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.remaining -= int64(n)
	return n, err
}

type cappedTransport struct{ base http.RoundTripper }

func (t cappedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &cappedReadCloser{ReadCloser: resp.Body, remaining: maxModelsResponseBytes}
	return resp, nil
}

// ThinkingDescriptor is the adapter's OWN neutral projection of a model's
// extended-thinking capability (Capabilities.Thinking.Types). It carries nothing
// SDK-private; the composition layer maps it to its own neutral descriptor. The
// zero value (none) means the model supports no extended thinking.
type ThinkingDescriptor struct {
	Adaptive bool // Capabilities.Thinking.Types.adaptive.supported
	Enabled  bool // Capabilities.Thinking.Types.enabled.supported (manual)
}

// Model is the lister's OWN neutral result type — the rich, self-described
// per-model metadata Anthropic's /v1/models returns. It carries nothing
// provider-private (no key, no URL); the composition layer maps it to its
// composition-local modelEntry. It is the AUTHORITATIVE source for the output
// ceiling, context window, image input, and the thinking matrix — replacing the
// adapter's hardcoded id-prefix guesswork for models the live API describes.
type Model struct {
	ID           string
	DisplayName  string
	ContextLimit int // MaxInputTokens (the input/context window)
	OutputLimit  int // MaxTokens (the max_tokens output ceiling)
	Image        bool
	Thinking     ThinkingDescriptor
}

// Lister fetches the LIVE Anthropic model catalog from the KEYED /v1/models
// endpoint (SDK client.Models.ListAutoPaging) and maps the rich ModelInfo into the
// neutral Model above. Unlike the openrouter lister (KEYLESS), this endpoint is
// AUTHENTICATED: the Lister carries the API key for a READ-ONLY metadata GET. The
// key is used ONLY to read and is NEVER logged (CWE-200) — it mirrors the same key
// the provider uses, so the metadata fetch shares the request key's blast radius and
// introduces no new secret. The Lister runs only for an AVAILABLE (keyed) anthropic
// provider, so the key is present by construction.
type Lister struct {
	models sdk.ModelService
}

// NewLister constructs a Lister authenticated with key (and an optional baseURL for
// compatible/proxy endpoints). httpClient is the test seam: a nil client uses the
// SDK default; tests inject a mock transport (option.WithHTTPClient) so NO test ever
// contacts api.anthropic.com. It suppresses the SDK's ambient environment defaults
// (mirroring the provider) so only the harness-resolved key/base URL apply.
func NewLister(key, baseURL string, httpClient *http.Client) *Lister {
	reqOpts := []option.RequestOption{option.WithoutEnvironmentDefaults()}
	if key != "" {
		reqOpts = append(reqOpts, option.WithAPIKey(key))
	}
	if baseURL != "" {
		reqOpts = append(reqOpts, option.WithBaseURL(baseURL))
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	clientCopy := *httpClient
	base := clientCopy.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clientCopy.Transport = cappedTransport{base: base}
	reqOpts = append(reqOpts, option.WithHTTPClient(&clientCopy))
	client := sdk.NewClient(reqOpts...)
	return &Lister{models: client.Models}
}

type listerStatusError struct {
	err        error
	statusCode int
}

func (e *listerStatusError) Error() string   { return "anthropic: list models: " + e.err.Error() }
func (e *listerStatusError) Unwrap() error   { return e.err }
func (e *listerStatusError) StatusCode() int { return e.statusCode }

// ListModels GETs the live Anthropic catalog (auto-paging) and maps each ModelInfo
// into a neutral Model. It is read-only and fail-safe to the caller: a transport or
// pagination error returns a non-nil error (the composition layer then falls back to
// the embedded catalog). It NEVER panics and never logs the key.
func (l *Lister) ListModels(ctx context.Context) ([]Model, error) {
	if l == nil {
		return nil, fmt.Errorf("anthropic: lister not configured")
	}
	pager := l.models.ListAutoPaging(ctx, sdk.ModelListParams{
		Limit: sdk.Int(listerPageLimit),
	})
	var out []Model
	for pager.Next() {
		info := pager.Current()
		if info.ID == "" {
			continue // defensive: skip a malformed entry with no id
		}
		out = append(out, mapModelInfo(info))
	}
	if err := pager.Err(); err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			return nil, &listerStatusError{err: err, statusCode: apiErr.StatusCode}
		}
		return nil, fmt.Errorf("anthropic: list models: %w", err)
	}
	return out, nil
}

// mapModelInfo projects the SDK's rich ModelInfo into the neutral Model. The
// thinking descriptor reads Capabilities.Thinking.Types directly (the live
// replacement for the prefix matrix). int64 ceilings are narrowed to int (model
// limits are well within int range on every supported platform).
func mapModelInfo(info sdk.ModelInfo) Model {
	caps := info.Capabilities
	return Model{
		ID:           info.ID,
		DisplayName:  info.DisplayName,
		ContextLimit: int(info.MaxInputTokens),
		OutputLimit:  int(info.MaxTokens),
		Image:        caps.ImageInput.Supported,
		Thinking: ThinkingDescriptor{
			Adaptive: caps.Thinking.Types.Adaptive.Supported,
			Enabled:  caps.Thinking.Types.Enabled.Supported,
		},
	}
}
