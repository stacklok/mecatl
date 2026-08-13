package openai

import (
	"encoding/json"
	"strings"
	"unicode"
)

const maxDownstreamProviderLabelRunes = 128

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
	var envelope struct {
		Meta openrouterMetadata `json:"openrouter_metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return ""
	}
	for _, ep := range envelope.Meta.Endpoints.Available {
		if ep.Selected {
			if label := normaliseDownstreamProviderLabel(ep.Provider); label != "" {
				return label
			}
		}
	}
	for i := len(envelope.Meta.Attempts) - 1; i >= 0; i-- {
		if label := normaliseDownstreamProviderLabel(envelope.Meta.Attempts[i].Provider); label != "" {
			return label
		}
	}
	return ""
}

// normaliseDownstreamProviderLabel bounds and flattens the upstream-controlled
// display label before it crosses into a client-visible engine event. Terminal
// clients still escape for their own medium; this keeps the neutral wire value
// valid UTF-8, single-line, and cheap to retain or render.
func normaliseDownstreamProviderLabel(label string) string {
	label = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(label, "\uFFFD"))
	label = strings.Join(strings.Fields(label), " ")
	runes := []rune(label)
	if len(runes) > maxDownstreamProviderLabelRunes {
		runes = runes[:maxDownstreamProviderLabelRunes]
	}
	return string(runes)
}
