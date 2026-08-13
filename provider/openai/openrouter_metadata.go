package openai

import "encoding/json"

// openrouter_metadata is the opt-in routing block OpenRouter adds to a response
// when the request carries `X-OpenRouter-Metadata: enabled` (issue #480). On the
// streaming Responses path it arrives on the FINAL `response.completed` event's
// raw JSON, as an UNMODELLed field the openai-go SDK does not type — we read it
// off Response.RawJSON(). It names the DOWNSTREAM inference provider OpenRouter
// actually routed to (mecatl's "provider" stays the wire adapter).
//
// Shape (only the fields we consume; everything else is ignored):
//
//	"openrouter_metadata": {
//	  "endpoints": { "available": [ {"provider": "Anthropic", "selected": true}, ... ] },
//	  "attempts":  [ {"provider": "Anthropic", "status": 200}, ... ],
//	  "summary":   "..."
//	}
//
// The parse is TOLERANT and fail-empty: any absent/malformed field yields "",
// which the caller turns into "no ChunkProviderRoute emitted" — never a
// fabricated value. A cache hit strips openrouter_metadata entirely, so "" is
// the honest cache-hit result.
type openrouterMetadata struct {
	Endpoints struct {
		Available []struct {
			Provider string `json:"provider"`
			Selected bool   `json:"selected"`
		} `json:"available"`
	} `json:"endpoints"`
	Attempts []struct {
		Provider string `json:"provider"`
	} `json:"attempts"`
}

// selectedDownstreamProvider extracts the downstream provider from a
// response.completed event's raw JSON. It prefers the endpoint marked
// selected:true; failing that it falls back to the LAST attempt's provider (the
// one that succeeded after any fallbacks). It returns "" when the metadata is
// absent, malformed, or names nothing — the caller emits no chunk in that case.
//
// NAMING NOTE (issue #480): the metadata's `provider` is OpenRouter's DISPLAY name
// for the downstream (e.g. "Anthropic", "Amazon Bedrock"), which differs in casing/
// spacing from the lowercase-kebab slug the steering `order` list uses (e.g.
// "anthropic", "amazon-bedrock"). They come from two different OpenRouter surfaces
// (the routing request's provider object vs the response's routing metadata) and
// are NOT the same grammar — we relay the display name verbatim for the status
// echo and never try to map it back to a config slug.
func selectedDownstreamProvider(raw string) string {
	if raw == "" {
		return ""
	}
	// Cheap gate: the field is absent on a cache hit and on every non-openrouter
	// response — avoid the unmarshal cost (and any chance of a false positive).
	var probe struct {
		Meta json.RawMessage `json:"openrouter_metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &probe); err != nil || len(probe.Meta) == 0 {
		return ""
	}
	var meta openrouterMetadata
	if err := json.Unmarshal(probe.Meta, &meta); err != nil {
		return ""
	}
	for _, ep := range meta.Endpoints.Available {
		if ep.Selected && ep.Provider != "" {
			return ep.Provider
		}
	}
	for i := len(meta.Attempts) - 1; i >= 0; i-- {
		if meta.Attempts[i].Provider != "" {
			return meta.Attempts[i].Provider
		}
	}
	return ""
}
