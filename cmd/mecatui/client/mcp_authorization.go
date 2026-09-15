package client

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
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
	stream, err := c.svc.RecheckMcpAuthorization(ctx)
	if err != nil {
		return nil, fmt.Errorf("recheck MCP authorization: %w", err)
	}
	if err := stream.Send(&mecatlv1.RecheckMcpAuthorizationRequest{SessionId: sessionID, AuthorizationId: authorizationID}); err != nil {
		return nil, fmt.Errorf("recheck MCP authorization: %w", err)
	}
	adapter := &recheckAuthorizationStream{stream: stream}
	return NewAuthorizationEventStream(adapter, adapter), nil
}

// CancelMCPAuthorization starts the bidirectional authorization cancellation.
func (c *Client) CancelMCPAuthorization(ctx context.Context, sessionID, authorizationID string) (*EventStream, error) {
	stream, err := c.svc.CancelMcpAuthorization(ctx)
	if err != nil {
		return nil, fmt.Errorf("cancel MCP authorization: %w", err)
	}
	if err := stream.Send(&mecatlv1.CancelMcpAuthorizationRequest{SessionId: sessionID, AuthorizationId: authorizationID}); err != nil {
		return nil, fmt.Errorf("cancel MCP authorization: %w", err)
	}
	adapter := &cancelAuthorizationStream{stream: stream}
	return NewAuthorizationEventStream(adapter, adapter), nil
}

type recheckAuthorizationStream struct {
	stream mecatlv1.HarnessService_RecheckMcpAuthorizationClient
	mu     sync.Mutex
}

func (r *recheckAuthorizationStream) Recv() (*mecatlv1.Event, error) {
	response, err := r.stream.Recv()
	if err != nil {
		return nil, err
	}
	return response.GetEvent(), nil
}
func (r *recheckAuthorizationStream) SendApprovalForScope(id string, v Verdict, scope *GuardrailApprovalScope, expectedRunID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stream.Send(&mecatlv1.RecheckMcpAuthorizationRequest{Control: &mecatlv1.RecheckMcpAuthorizationRequest_ResumeApproval{ResumeApproval: resumeApprovalForScope(id, v, scope, expectedRunID)}})
}
func (r *recheckAuthorizationStream) SendCancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stream.Send(&mecatlv1.RecheckMcpAuthorizationRequest{Control: &mecatlv1.RecheckMcpAuthorizationRequest_Cancel{Cancel: &mecatlv1.Cancel{}}})
}

type cancelAuthorizationStream struct {
	stream mecatlv1.HarnessService_CancelMcpAuthorizationClient
	mu     sync.Mutex
}

func (r *cancelAuthorizationStream) Recv() (*mecatlv1.Event, error) {
	response, err := r.stream.Recv()
	if err != nil {
		return nil, err
	}
	return response.GetEvent(), nil
}
func (r *cancelAuthorizationStream) SendApprovalForScope(id string, v Verdict, scope *GuardrailApprovalScope, expectedRunID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stream.Send(&mecatlv1.CancelMcpAuthorizationRequest{Control: &mecatlv1.CancelMcpAuthorizationRequest_ResumeApproval{ResumeApproval: resumeApprovalForScope(id, v, scope, expectedRunID)}})
}
func (r *cancelAuthorizationStream) SendCancel() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stream.Send(&mecatlv1.CancelMcpAuthorizationRequest{Control: &mecatlv1.CancelMcpAuthorizationRequest_Cancel{Cancel: &mecatlv1.Cancel{}}})
}
