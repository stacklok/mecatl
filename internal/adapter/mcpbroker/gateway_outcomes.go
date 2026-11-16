package mcpbroker

import (
	"context"
	"errors"

	"github.com/stacklok/mecatl/engine/session"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// gatewayDeclinedNote is appended to a gateway call's result when the
// gateway sent a request during the call, which was declined.
const gatewayDeclinedNote = "Note: the gateway asked for input during the call; it was declined."

// gatewayOutcome is a gateway call that ended without the gateway's answer to
// the tools/call: code names it in diagnostics and text is the model-visible
// tool error. Whether the call counts as sent follows from its attempts, not
// from the row (see releaseUnlessSent).
type gatewayOutcome struct{ code, text string }

func (o gatewayOutcome) result(id session.ToolCallID) session.ToolResult {
	return session.NewToolError(id, o.text)
}

var (
	outcomeIdentityUnavailable = gatewayOutcome{code: "identity_unavailable", text: "MCP gateway call refused: this agent has no identity for gateway calls (code identity_unavailable)."}
	outcomeNoGrant             = gatewayOutcome{code: "no_grant", text: "MCP gateway call refused: the session holds no sign-in to act on; a new request from the user reopens it with their sign-in (code no_grant)."}
	outcomeSessionClosed       = gatewayOutcome{code: "session_closed", text: "MCP gateway call refused: the session's authority is closed and is not reopened for this call (code session_closed)."}
	outcomeSignedOut           = gatewayOutcome{code: "signed_out", text: "MCP gateway call refused: the user signed out everywhere (code signed_out)."}
	outcomeUpstreamRevoked     = gatewayOutcome{code: "upstream_revoked", text: "MCP gateway call refused: the user's company sign-in was revoked; they must sign in again (code upstream_revoked)."}
	outcomeOwnerAction         = gatewayOutcome{code: "owner_action_required", text: "MCP gateway call refused: the user must sign in again (code owner_action_required)."}
	outcomeDenied              = gatewayOutcome{code: "denied", text: "MCP gateway call denied (code denied)."}
	outcomeConsentRequired     = gatewayOutcome{code: "consent_required", text: "MCP gateway call refused: the gateway requires the user's approval for Mecatl (code consent_required)."}
	outcomeSVIDExpired         = gatewayOutcome{code: "svid_expired", text: "MCP gateway call refused: the agent's identity could not be renewed (code svid_expired)."}
	outcomeUnavailable         = gatewayOutcome{code: "unavailable", text: "MCP gateway call failed: authorization is unavailable; the call was not sent and can be retried (code unavailable)."}
	outcomeInternal            = gatewayOutcome{code: "internal", text: "MCP gateway call failed: internal error; the call was not sent (code internal)."}
	outcomeRefusedArguments    = gatewayOutcome{code: "refused_arguments", text: "MCP gateway call refused: the arguments must be a JSON object representable exactly (no integers beyond ±2^53, invalid Unicode, or duplicate keys); change them and retry (code refused_arguments)."}
	outcomeSessionNotFound     = gatewayOutcome{code: "gateway_session_not_found", text: "MCP gateway call not run: the gateway session ended before the call (code gateway_session_not_found)."}
	outcomeTokenRejected       = gatewayOutcome{code: "token_rejected", text: "MCP gateway call not run: the gateway rejected the authorization twice (code token_rejected)."}
	outcomeAlreadySent         = gatewayOutcome{code: "already_sent", text: "MCP gateway call refused: this call was already sent and its outcome is unknown; automatic replay refused (code already_sent)."}
	outcomeSendOnceFull        = gatewayOutcome{code: "send_once_full", text: "MCP gateway call refused: too many gateway calls are held for this session; the call was not sent (code send_once_full)."}
	outcomeUnknown             = gatewayOutcome{code: "outcome_unknown", text: "MCP gateway call outcome unknown: the call was sent but the gateway's answer does not show whether it ran; it may have run and is not retried automatically (code outcome_unknown)."}
)

// outcomeConnectionRequired is the tools/call answered with a URL
// elicitation; link is the validated connect link, or empty.
// Shortcut: a missing provider connection (URL elicitation) fails the call closed with its link; removed by #2537 (fail with a connect link) and #2592 (mcp_authorization_pending).
// Shortcut: requests the gateway sends during a call are declined; removed by #2821 (asking the user through multi round-trip requests).
func outcomeConnectionRequired(server, link string) gatewayOutcome {
	text := "MCP gateway call not run: connect your " + server + " account at the gateway, then retry"
	if link != "" {
		text += ": " + link
	}
	return gatewayOutcome{code: "connection_required", text: text + " (code connection_required)."}
}

// outcomeForAuthorization maps an error before any attempt was sent: from
// the argument hash, the credential lookup, AuthorizeCall, or the send-once
// claim.
func outcomeForAuthorization(err error) gatewayOutcome {
	var brokerErr *identitybroker.Error
	switch {
	case errors.Is(err, errNotIJSON):
		return outcomeRefusedArguments
	case errors.Is(err, ErrAlreadySent):
		return outcomeAlreadySent
	case errors.Is(err, errSendOnceFull):
		return outcomeSendOnceFull
	case errors.Is(err, agentcredential.ErrNoCredential), errors.Is(err, agentcredential.ErrDropped):
		// Helper agents have no broker MCP tools until #2555 and #2556, so
		// only a main agent's missing or dropped credential reaches this.
		return outcomeIdentityUnavailable
	case errors.Is(err, context.DeadlineExceeded):
		return outcomeUnavailable
	case !errors.As(err, &brokerErr):
		return outcomeInternal
	}
	switch brokerErr.Reason {
	case identitybroker.ReasonNoGrant:
		return outcomeNoGrant
	case identitybroker.ReasonSessionClosed:
		return outcomeSessionClosed
	// Shortcut: owner_action_required is a tool error, not the mcp_owner_action_required outcome, and signed_out, upstream_revoked, and owner_action_required fail the call without stopping the run; removed by #2568 (sign-out and revocation outcomes; the run stops).
	case identitybroker.ReasonSignedOut:
		return outcomeSignedOut
	case identitybroker.ReasonUpstreamRevoked:
		return outcomeUpstreamRevoked
	case identitybroker.ReasonOwnerActionRequired:
		return outcomeOwnerAction
	// Shortcut: consent_required carries no approval link; removed by #2735 (the approval link).
	case identitybroker.ReasonConsentRequired:
		return outcomeConsentRequired
	case identitybroker.ReasonSVIDExpired:
		return outcomeSVIDExpired
	}
	switch brokerErr.Code {
	case identitybroker.CodePermissionDenied:
		return outcomeDenied
	case identitybroker.CodeUnavailable:
		return outcomeUnavailable
	default:
		return outcomeInternal
	}
}

// outcomeForAttempts maps the last attempt of a call that did not complete;
// retried reports that it was the retry after a rejected token.
func outcomeForAttempts(server string, last mcpadapter.GatewayAttempt, retried bool) gatewayOutcome {
	switch {
	case last.Outcome == mcpadapter.GatewayAttemptUnknown:
		return outcomeUnknown
	case last.Rejection == mcpadapter.GatewayRejectionURLElicitation:
		return outcomeConnectionRequired(server, last.ConnectURL)
	case last.Rejection == mcpadapter.GatewayRejectionSessionNotFound:
		return outcomeSessionNotFound
	case last.Rejection == mcpadapter.GatewayRejectionInvalidToken && retried:
		return outcomeTokenRejected
	case last.Rejection == mcpadapter.GatewayRejectionChanged:
		return outcomeInternal
	default:
		// Expired authorization, a failed connection or handshake, or a
		// cancellation before the tools/call was written: not sent.
		return outcomeUnavailable
	}
}

// withDeclinedNote appends gatewayDeclinedNote to res.
func withDeclinedNote(res session.ToolResult) session.ToolResult {
	if res.Content != "" {
		res.Content += "\n\n"
	}
	res.Content += gatewayDeclinedNote
	if len(res.Parts) != 0 {
		res.Parts = append(res.Parts, session.NewTextBlock(gatewayDeclinedNote))
	}
	return res
}
