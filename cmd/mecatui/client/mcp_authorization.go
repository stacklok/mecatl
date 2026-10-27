package client

import (
	"context"
	"fmt"
	"net/url"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// MCPAuthorizationPendingCode is the stable server application code returned
// while a parked MCP authorization remains live.
const MCPAuthorizationPendingCode = "mcp_authorization_pending"

// IsMCPAuthorizationPending reports the exact gRPC application condition. It
// deliberately ignores status text so server wording cannot change the UI flow.
func IsMCPAuthorizationPending(err error) bool {
	st, ok := status.FromError(err)
	if !ok || st.Code() != codes.FailedPrecondition {
		return false
	}
	for _, detail := range st.Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if ok && info.GetDomain() == "mecatl.stacklok.com" && info.GetReason() == MCPAuthorizationPendingCode {
			return true
		}
	}
	return false
}

// MCPAuthorizationController opens the separate browser-authorization control
// surface. The client cannot assert OAuth success; an opened continuation stream
// may only carry ordinary permission verdicts and run cancellation.
type MCPAuthorizationController interface {
	MCPAuthorizationPresentation(context.Context, string, string) (string, error)
	RecheckMCPAuthorization(context.Context, string, string) (*EventStream, error)
	CancelMCPAuthorization(context.Context, string, string) (*EventStream, error)
}

// MCPAuthorizationPresentation returns the owned authorization's live browser
// URL. It is intentionally separate from stream events: URLs are presentation
// secrets and are never replayed or rendered from durable event data.
func (c *Client) MCPAuthorizationPresentation(ctx context.Context, sessionID, authorizationID string) (string, error) {
	response, err := c.svc.GetMcpAuthorizationPresentation(ctx, &mecatlv1.GetMcpAuthorizationPresentationRequest{SessionId: sessionID, AuthorizationId: authorizationID})
	if err != nil {
		return "", fmt.Errorf("get MCP authorization presentation: %w", err)
	}
	presentationURL := response.GetUrl()
	parsed, parseErr := url.Parse(presentationURL)
	if parseErr != nil || parsed.Host == "" || parsed.Scheme != "https" && parsed.Scheme != "http" {
		return "", fmt.Errorf("get MCP authorization presentation: server returned an invalid HTTP(S) URL")
	}
	return presentationURL, nil
}

// RecheckMCPAuthorization starts the bidirectional authorization-control stream.
func (c *Client) RecheckMCPAuthorization(ctx context.Context, sessionID, authorizationID string) (*EventStream, error) {
	control, err := openAuthorizationControl(ctx, c, sessionID, func(ctx context.Context) (mecatlv1.HarnessService_RecheckMcpAuthorizationClient, error) {
		return c.svc.RecheckMcpAuthorization(ctx)
	})
	if err != nil {
		return nil, fmt.Errorf("recheck MCP authorization: %w", err)
	}
	if err := control.sendFrame(&mecatlv1.RecheckMcpAuthorizationRequest{SessionId: sessionID, AuthorizationId: authorizationID}); err != nil {
		return nil, fmt.Errorf("recheck MCP authorization: %w", err)
	}
	adapter := recheckAuthorizationStream{control}
	return NewAuthorizationEventStream(adapter, adapter), nil
}

// CancelMCPAuthorization starts the bidirectional authorization cancellation.
func (c *Client) CancelMCPAuthorization(ctx context.Context, sessionID, authorizationID string) (*EventStream, error) {
	control, err := openAuthorizationControl(ctx, c, sessionID, func(ctx context.Context) (mecatlv1.HarnessService_CancelMcpAuthorizationClient, error) {
		return c.svc.CancelMcpAuthorization(ctx)
	})
	if err != nil {
		return nil, fmt.Errorf("cancel MCP authorization: %w", err)
	}
	if err := control.sendFrame(&mecatlv1.CancelMcpAuthorizationRequest{SessionId: sessionID, AuthorizationId: authorizationID}); err != nil {
		return nil, fmt.Errorf("cancel MCP authorization: %w", err)
	}
	adapter := cancelAuthorizationStream{control}
	return NewAuthorizationEventStream(adapter, adapter), nil
}

// openAuthorizationControl opens an authorization-control stream as a run
// entry on sessionID. With a refreshing token source it is reopenable like a
// Converse stream (see reopenableStream).
func openAuthorizationControl[Req, Resp any](ctx context.Context, c *Client, sessionID string, open func(context.Context) (grpc.BidiStreamingClient[Req, Resp], error)) (*reopenableStream[Req, Resp], error) {
	stream, err := open(c.runEntryContext(ctx, sessionID))
	if err != nil {
		return nil, err
	}
	control := &reopenableStream[Req, Resp]{recv: stream, send: stream, onAnswered: func() { c.noteOpened(sessionID) }}
	if c.refreshes {
		control.armReopen(func() (recvSide[Resp], sendSide[Req], error) {
			stream, err := open(withFreshToken(ctx))
			if err != nil {
				return nil, nil, err
			}
			return stream, stream, nil
		})
	}
	return control, nil
}

type recheckAuthorizationStream struct {
	*reopenableStream[mecatlv1.RecheckMcpAuthorizationRequest, mecatlv1.RecheckMcpAuthorizationResponse]
}

func (r recheckAuthorizationStream) Recv() (*mecatlv1.Event, error) {
	response, err := r.receive()
	if err != nil {
		return nil, err
	}
	return response.GetEvent(), nil
}
func (r recheckAuthorizationStream) SendApprovalForScope(id string, v Verdict, scope *GuardrailApprovalScope, expectedRunID string) error {
	return r.sendFrame(&mecatlv1.RecheckMcpAuthorizationRequest{Control: &mecatlv1.RecheckMcpAuthorizationRequest_ResumeApproval{ResumeApproval: resumeApprovalForScope(id, v, scope, expectedRunID)}})
}
func (r recheckAuthorizationStream) SendCancel() error {
	return r.sendFrame(&mecatlv1.RecheckMcpAuthorizationRequest{Control: &mecatlv1.RecheckMcpAuthorizationRequest_Cancel{Cancel: &mecatlv1.Cancel{}}})
}

type cancelAuthorizationStream struct {
	*reopenableStream[mecatlv1.CancelMcpAuthorizationRequest, mecatlv1.CancelMcpAuthorizationResponse]
}

func (r cancelAuthorizationStream) Recv() (*mecatlv1.Event, error) {
	response, err := r.receive()
	if err != nil {
		return nil, err
	}
	return response.GetEvent(), nil
}
func (r cancelAuthorizationStream) SendApprovalForScope(id string, v Verdict, scope *GuardrailApprovalScope, expectedRunID string) error {
	return r.sendFrame(&mecatlv1.CancelMcpAuthorizationRequest{Control: &mecatlv1.CancelMcpAuthorizationRequest_ResumeApproval{ResumeApproval: resumeApprovalForScope(id, v, scope, expectedRunID)}})
}
func (r cancelAuthorizationStream) SendCancel() error {
	return r.sendFrame(&mecatlv1.CancelMcpAuthorizationRequest{Control: &mecatlv1.CancelMcpAuthorizationRequest_Cancel{Cancel: &mecatlv1.Cancel{}}})
}
