package mcp

import (
	"context"
	"errors"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// ErrGatewayList reports a failed gateway tools/list. It never carries the
// gateway's answer, which could echo the token.
var ErrGatewayList = errors.New("mcp: gateway tools/list failed")

// Default gateway listing limits. Each bounds one listing of one gateway route:
// the size limits take the values the catalogue's budget uses, though that
// budget is shared by every MCP server listed together. A gateway over one of
// them is refused, not listed in part.
const (
	defaultGatewayListTimeout  = 30 * time.Second
	defaultGatewayListMaxTools = 10_000
	defaultGatewayListMaxPages = 256
	defaultGatewayListMaxBytes = 16 << 20
)

// GatewayListLimits bounds one gateway tools/list. A zero or negative field
// takes its default.
type GatewayListLimits struct {
	// Timeout bounds the listing: the session's setup and every page. The
	// session's closing DELETE is bounded by the SDK on its own, so a
	// listing can return up to 5 s after its timeout.
	Timeout time.Duration
	// MaxTools bounds the tools listed across every page.
	MaxTools int
	// MaxPages bounds the pages followed.
	MaxPages int
	// MaxBytes bounds the response bytes read, counted as they are read.
	MaxBytes int
}

func (l GatewayListLimits) withDefaults() GatewayListLimits {
	if l.Timeout <= 0 {
		l.Timeout = defaultGatewayListTimeout
	}
	if l.MaxTools <= 0 {
		l.MaxTools = defaultGatewayListMaxTools
	}
	if l.MaxPages <= 0 {
		l.MaxPages = defaultGatewayListMaxPages
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = defaultGatewayListMaxBytes
	}
	return l
}

// ListGatewayTools lists every tool the gateway at endpoint advertises, every
// page, over a session of its own, within limits. token is only ever set in
// the Authorization header; client's Transport and Timeout are used, and
// redirects are never followed. A listing over a limit, with a repeated
// cursor, or past its timeout fails rather than returning part of the tools.
// An error is ErrGatewayList or ctx's error.
func ListGatewayTools(ctx context.Context, endpoint string, client *http.Client, token identitybroker.Token, limits GatewayListLimits) ([]*mcpsdk.Tool, error) {
	if endpoint == "" || client == nil || token.IsZero() {
		return nil, ErrGatewayList
	}
	limits = limits.withDefaults()
	listCtx, cancel := context.WithTimeout(ctx, limits.Timeout)
	defer cancel()
	budget := NewCandidateListBudget(limits.MaxTools, limits.MaxPages, limits.MaxBytes)
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &mcpsdk.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{
			Transport: bearerTransport{
				base:          candidateBudgetRoundTripper{base: base, budget: budget},
				authorization: "Bearer " + token.Value(),
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Timeout:       client.Timeout,
		},
		DisableStandaloneSSE: true,
		// Never resume a dropped answer: the SDK would resume it with a GET,
		// whose body the byte budget does not count.
		MaxRetries: -1,
	}
	sdkClient := mcpsdk.NewClient(&mcpsdk.Implementation{Name: clientName, Version: clientVersion}, nil)
	// A listing would work on 2026-07-28 too; it speaks the calls' revision
	// so the gateway sees one protocol (see gatewayProtocolVersion).
	cs, err := sdkClient.Connect(listCtx, transport, &mcpsdk.ClientSessionOptions{ProtocolVersion: gatewayProtocolVersion})
	if err != nil {
		return nil, listError(ctx)
	}
	defer func() { _ = cs.Close() }()
	var tools []*mcpsdk.Tool
	err = walkToolPages(listCtx, cs, budget, func(tool *mcpsdk.Tool) error {
		if tool == nil {
			return ErrGatewayList
		}
		tools = append(tools, tool)
		return nil
	})
	if err != nil {
		return nil, listError(ctx)
	}
	return tools, nil
}

// listError is ctx's error when the caller's context ended, and ErrGatewayList
// otherwise, including when only the listing's own timeout ran out.
func listError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return ErrGatewayList
}

// bearerTransport sets the Authorization header of every request.
type bearerTransport struct {
	base          http.RoundTripper
	authorization string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	out := req.Clone(req.Context())
	out.Header.Set("Authorization", t.authorization)
	return t.base.RoundTrip(out)
}
