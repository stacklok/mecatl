package anthropicsub

// Claude first-party client fingerprint.
//
// A subscription grant is only honoured on requests that reproduce the
// first-party client's identity, so every value here is a wire contract copied
// from that client, not a mecatl choice. ClientVersion in particular is
// hand-maintained: when Anthropic ships a new client, these values drift and
// subscription requests may begin to fail until it is bumped.
//
// None of this is sent on API-key requests, which continue to identify mecatl
// honestly through the provider adapter's own transport.
const (
	// ClientVersion is the Claude client release these values mirror.
	ClientVersion = "2.1.220"

	// userAgent is the inference User-Agent of the client's desktop entrypoint.
	userAgent = "claude-cli/" + ClientVersion + " (external, claude-desktop)"
	// bootstrapUserAgent is sent on the identity bootstrap request, which uses
	// a different form to the inference path.
	bootstrapUserAgent = "claude-code/" + ClientVersion
	// refreshUserAgent is what the client's SDK sends on a token refresh.
	refreshUserAgent = "anthropic-sdk-typescript/0.94.0 userOAuthProvider"

	// bootstrapModel is a required query parameter of the bootstrap endpoint.
	bootstrapModel = "claude-opus-4-8"

	// oauthBeta gates OAuth token handling. It is sent on refresh and
	// bootstrap, and deliberately not on inference, where the beta list below
	// applies instead.
	oauthBeta = "oauth-2025-04-20"

	apiVersion = "2023-06-01"

	// identityInstruction is the identity block the first-party client
	// prepends to every conversation.
	identityInstruction = "You are a Claude agent, built on Anthropic's Claude Agent SDK."

	// MaxOutputTokens is the client's per-request output ceiling. Subscription
	// requests are clamped to it regardless of the model's own maximum.
	MaxOutputTokens = 64000

	// toolPrefix namespaces caller tools away from the client's built-ins.
	toolPrefix = "_"
)

// builtinToolNames are the provider-side tools that must not be namespaced.
var builtinToolNames = map[string]struct{}{
	"web_search":     {},
	"code_execution": {},
	"text_editor":    {},
	"computer":       {},
}

// agentBetas is the beta list the client advertises on an agent request (one
// carrying tools or extended thinking).
//
// Two are deliberately absent. The long-context beta is never advertised
// because subscription credentials have no long-context credit balance and the
// provider hard-refuses beta-gated million-token models regardless of prompt
// size. The extended-cache-TTL beta is likewise not offered on subscription
// auth.
var agentBetas = []string{
	"claude-code-20250219",
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"mid-conversation-system-2026-04-07",
	"advanced-tool-use-2025-11-20",
}

// utilityBetas is the list advertised on a request with neither tools nor
// thinking.
var utilityBetas = []string{
	"interleaved-thinking-2025-05-14",
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
	"structured-outputs-2025-12-15",
}

const (
	effortBeta         = "effort-2025-11-24"
	fallbackCreditBeta = "fallback-credit-2026-06-01"
)

// betaHeader builds the anthropic-beta value for one request. Ordering is
// preserved because the value is part of the fingerprint.
func betaHeader(agentRequest, thinkingRequest bool) string {
	source := utilityBetas
	if agentRequest {
		source = agentBetas
	}
	betas := make([]string, 0, len(source)+2)
	betas = append(betas, source...)
	if agentRequest {
		if thinkingRequest {
			betas = append(betas, effortBeta)
		}
		betas = append(betas, fallbackCreditBeta)
	}
	return joinBetas(betas)
}

func joinBetas(betas []string) string {
	// No space after the comma: the provider's client emits it packed.
	out := ""
	for i, beta := range betas {
		if i > 0 {
			out += ","
		}
		out += beta
	}
	return out
}

// stainlessHeaders are the SDK telemetry headers the client emits. The
// operating system is reported as the client's own build target rather than
// this host's, because the value identifies the client, not the machine.
var stainlessHeaders = map[string]string{
	"X-Stainless-Arch":            "arm64",
	"X-Stainless-Lang":            "js",
	"X-Stainless-OS":              "Linux",
	"X-Stainless-Package-Version": "0.94.0",
	"X-Stainless-Retry-Count":     "0",
	"X-Stainless-Runtime":         "node",
	"X-Stainless-Runtime-Version": "v26.3.0",
	"X-Stainless-Timeout":         "600",
}
