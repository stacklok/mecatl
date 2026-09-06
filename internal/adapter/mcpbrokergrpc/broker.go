// Package mcpbrokergrpc adapts the neutral internal MCP broker seam to the
// versioned mecatl.broker.v1 RPC protocol. It deliberately carries only logical
// bindings, process-local handles, frozen tool descriptors, and invocation data.
package mcpbrokergrpc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"
	"unicode/utf8"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

const defaultMaxHandles = 128

type Server struct {
	brokerv1.UnimplementedBrokerServiceServer
	service    mcpbroker.Service
	mu         sync.Mutex
	handles    map[string]*serverAttachment
	maxHandles int
}
type serverAttachment struct {
	attachment mcpbroker.Attachment
	tools      map[string]tool.Tool
}

func NewServer(service mcpbroker.Service, maxHandles int) (*Server, error) {
	if service == nil {
		return nil, errors.New("mcpbrokergrpc: service is required")
	}
	if maxHandles <= 0 {
		maxHandles = defaultMaxHandles
	}
	return &Server{service: service, handles: make(map[string]*serverAttachment), maxHandles: maxHandles}, nil
}
func Register(reg grpc.ServiceRegistrar, service mcpbroker.Service) {
	server, err := NewServer(service, defaultMaxHandles)
	if err != nil {
		panic(err)
	}
	brokerv1.RegisterBrokerServiceServer(reg, server)
}
func (s *Server) Attach(ctx context.Context, req *brokerv1.AttachRequest) (*brokerv1.AttachResponse, error) {
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	a, outcome, err := s.service.AttachSession(ctx, session.SessionID(req.GetSessionId()))
	if err != nil {
		return nil, brokerStatus(err)
	}
	desc, tools, err := descriptors(a.Tools())
	if err != nil {
		_, _ = a.Close(context.Background())
		return nil, invalid(err.Error())
	}
	h, err := newHandle()
	if err != nil {
		_, _ = a.Close(context.Background())
		return nil, status.Error(codes.Internal, "mint attachment handle")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.handles) >= s.maxHandles {
		_, _ = a.Close(context.Background())
		return nil, status.Error(codes.ResourceExhausted, "attachment handle capacity reached")
	}
	s.handles[h] = &serverAttachment{attachment: a, tools: tools}
	return &brokerv1.AttachResponse{Binding: string(a.Binding()), Handle: h, Outcome: string(outcome), Tools: desc}, nil
}
func (s *Server) get(handle string) (*serverAttachment, error) {
	if handle == "" {
		return nil, invalid("handle is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.handles[handle]
	if a == nil {
		return nil, status.Error(codes.NotFound, "attachment handle not found")
	}
	return a, nil
}
func (s *Server) Commit(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.Empty, error) {
	a, e := s.get(req.GetHandle())
	if e != nil {
		return nil, e
	}
	if e = a.attachment.Commit(ctx); e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.Empty{}, nil
}
func (s *Server) Abort(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.Empty, error) {
	a, e := s.get(req.GetHandle())
	if e != nil {
		return nil, e
	}
	if e = a.attachment.Abort(ctx); e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.Empty{}, nil
}
func (s *Server) Close(ctx context.Context, req *brokerv1.HandleRequest) (*brokerv1.CloseResponse, error) {
	a, e := s.get(req.GetHandle())
	if e != nil {
		return nil, e
	}
	out, e := a.attachment.Close(ctx)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.CloseResponse{Outcome: string(out)}, nil
}
func (s *Server) Delete(ctx context.Context, req *brokerv1.DeleteRequest) (*brokerv1.DeleteResponse, error) {
	if req.GetSessionId() == "" {
		return nil, invalid("session_id is required")
	}
	var out mcpbroker.DeleteOutcome
	var err error
	if req.GetBinding() == "" {
		out, err = s.service.DeleteSession(ctx, session.SessionID(req.GetSessionId()))
	} else if d, ok := s.service.(mcpbroker.BindingSessionDeleter); ok {
		out, err = d.DeleteSessionIfBinding(ctx, session.SessionID(req.GetSessionId()), session.ExternalBinding(req.GetBinding()))
	} else {
		return nil, status.Error(codes.FailedPrecondition, "binding delete is unsupported")
	}
	if err != nil {
		return nil, brokerStatus(err)
	}
	return &brokerv1.DeleteResponse{Outcome: string(out)}, nil
}
func (s *Server) Run(ctx context.Context, req *brokerv1.RunRequest) (*brokerv1.RunResponse, error) {
	a, e := s.get(req.GetHandle())
	if e != nil {
		return nil, e
	}
	call, e := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if e != nil {
		return nil, e
	}
	target := a.tools[call.Name]
	if target == nil {
		return nil, invalid("unknown tool")
	}
	result, e := target.Execute(ctx, call, tool.Environment{})
	if e != nil {
		return nil, brokerStatus(e)
	}
	wire, e := resultToWire(result)
	if e != nil {
		return nil, invalid(e.Error())
	}
	return &brokerv1.RunResponse{Result: wire}, nil
}
func (s *Server) RequestAuthorization(ctx context.Context, req *brokerv1.RequestAuthorizationRequest) (*brokerv1.RequestAuthorizationResponse, error) {
	a, e := s.get(req.GetHandle())
	if e != nil {
		return nil, e
	}
	call, e := callFrom(req.GetName(), req.GetCallId(), req.GetArgs(), req.GetItemId())
	if e != nil {
		return nil, e
	}
	target, ok := a.tools[call.Name].(tool.AuthorizationRequester)
	if !ok {
		return nil, invalid("tool is not authorization-capable")
	}
	auth, required, e := target.RequestAuthorization(ctx, call)
	if e != nil {
		return nil, brokerStatus(e)
	}
	return &brokerv1.RequestAuthorizationResponse{Authorization: authToWire(auth), Required: required}, nil
}
func (s *Server) AbortAuthorization(ctx context.Context, req *brokerv1.AbortAuthorizationRequest) (*brokerv1.Empty, error) {
	a, e := s.get(req.GetHandle())
	if e != nil {
		return nil, e
	}
	auth, e := authFromWire(req.GetAuthorization())
	if e != nil {
		return nil, e
	}
	for _, target := range a.tools {
		if requester, ok := target.(tool.AuthorizationRequester); ok {
			if e = requester.AbortAuthorization(ctx, auth); e == nil {
				return &brokerv1.Empty{}, nil
			}
		}
	}
	if e == nil {
		return nil, invalid("tool is not authorization-capable")
	}
	return nil, brokerStatus(e)
}

func descriptors(in []tool.Tool) ([]*brokerv1.ToolDescriptor, map[string]tool.Tool, error) {
	out := make([]*brokerv1.ToolDescriptor, 0, len(in))
	tools := make(map[string]tool.Tool, len(in))
	for _, t := range in {
		if t == nil {
			return nil, nil, errors.New("nil tool")
		}
		spec := t.Spec()
		if spec.Name == "" || !utf8.ValidString(spec.Name) || !utf8.ValidString(spec.Description) || !json.Valid(spec.Schema) {
			return nil, nil, errors.New("invalid tool descriptor")
		}
		if _, ok := tools[spec.Name]; ok {
			return nil, nil, errors.New("duplicate tool descriptor")
		}
		_, serial := t.(tool.DispatchSerial)
		_, auth := t.(tool.AuthorizationRequester)
		out = append(out, &brokerv1.ToolDescriptor{Name: spec.Name, Description: spec.Description, Schema: append([]byte(nil), spec.Schema...), ReadOnly: t.ReadOnly(), DispatchSerial: serial, AuthorizationCapable: auth})
		tools[spec.Name] = t
	}
	return out, tools, nil
}
func newHandle() (string, error) {
	b := make([]byte, 24)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func invalid(msg string) error { return status.Error(codes.InvalidArgument, msg) }
func brokerStatus(err error) error {
	switch {
	case errors.Is(err, mcpbroker.ErrAttachmentClosed):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, mcpbroker.ErrStateUnavailable):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, mcpbroker.ErrAuthorizationNotFound):
		return status.Error(codes.NotFound, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}

// Client implements mcpbroker.Service over the generated RPC client.
type Client struct{ rpc brokerv1.BrokerServiceClient }

func NewClient(conn grpc.ClientConnInterface) *Client {
	return &Client{rpc: brokerv1.NewBrokerServiceClient(conn)}
}
func (c *Client) AttachSession(ctx context.Context, id session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	if id == "" {
		return nil, "", errors.New("mcpbrokergrpc: session id is required")
	}
	r, e := c.rpc.Attach(ctx, &brokerv1.AttachRequest{SessionId: string(id)})
	if e != nil {
		return nil, "", clientError(e)
	}
	if r.GetHandle() == "" || r.GetBinding() == "" || !validAttachOutcome(r.GetOutcome()) {
		return nil, "", errors.New("mcpbrokergrpc: malformed attach response")
	}
	tools, e := remoteTools(c, r)
	if e != nil {
		return nil, "", e
	}
	return &clientAttachment{client: c, handle: r.GetHandle(), binding: session.ExternalBinding(r.GetBinding()), tools: tools}, mcpbroker.AttachOutcome(r.GetOutcome()), nil
}
func (c *Client) DeleteSession(ctx context.Context, id session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return c.delete(ctx, id, "")
}
func (c *Client) DeleteSessionIfBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	if binding == "" {
		return "", errors.New("mcpbrokergrpc: binding is required")
	}
	return c.delete(ctx, id, binding)
}
func (c *Client) delete(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	r, e := c.rpc.Delete(ctx, &brokerv1.DeleteRequest{SessionId: string(id), Binding: string(binding)})
	if e != nil {
		return "", clientError(e)
	}
	if r.GetOutcome() != string(mcpbroker.DeleteDeleted) && r.GetOutcome() != string(mcpbroker.DeleteNotFound) {
		return "", errors.New("mcpbrokergrpc: invalid delete outcome")
	}
	return mcpbroker.DeleteOutcome(r.GetOutcome()), nil
}

type clientAttachment struct {
	client  *Client
	handle  string
	binding session.ExternalBinding
	tools   []tool.Tool
}

func (a *clientAttachment) Binding() session.ExternalBinding { return a.binding }
func (a *clientAttachment) Tools() []tool.Tool               { return append([]tool.Tool(nil), a.tools...) }
func (a *clientAttachment) Commit(ctx context.Context) error {
	_, e := a.client.rpc.Commit(ctx, &brokerv1.HandleRequest{Handle: a.handle})
	return clientError(e)
}
func (a *clientAttachment) Abort(ctx context.Context) error {
	_, e := a.client.rpc.Abort(ctx, &brokerv1.HandleRequest{Handle: a.handle})
	return clientError(e)
}
func (a *clientAttachment) Close(ctx context.Context) (mcpbroker.CloseOutcome, error) {
	r, e := a.client.rpc.Close(ctx, &brokerv1.HandleRequest{Handle: a.handle})
	if e != nil {
		return "", clientError(e)
	}
	if r.GetOutcome() != string(mcpbroker.CloseClosed) && r.GetOutcome() != string(mcpbroker.CloseAlreadyClosed) {
		return "", errors.New("mcpbrokergrpc: invalid close outcome")
	}
	return mcpbroker.CloseOutcome(r.GetOutcome()), nil
}
func (*clientAttachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	return "", errors.New("mcpbrokergrpc: presentation is unavailable over this protocol")
}
func (*clientAttachment) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	return "", errors.New("mcpbrokergrpc: status is unavailable over this protocol")
}
func (*clientAttachment) CancelAuthorization(context.Context, session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	return "", errors.New("mcpbrokergrpc: cancellation is unavailable over this protocol")
}

type remoteTool struct {
	client   *Client
	handle   string
	spec     tool.ToolSpec
	readOnly bool
}

func (t *remoteTool) Spec() tool.ToolSpec {
	s := t.spec
	s.Schema = append([]byte(nil), s.Schema...)
	return s
}
func (t *remoteTool) ReadOnly() bool { return t.readOnly }
func (t *remoteTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	if call.Name != t.spec.Name {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: tool name does not match descriptor")
	}
	if _, e := callFrom(call.Name, string(call.ID), call.Args, call.ItemID); e != nil {
		return session.ToolResult{}, e
	}
	r, e := t.client.rpc.Run(ctx, &brokerv1.RunRequest{Handle: t.handle, Name: call.Name, CallId: string(call.ID), Args: append([]byte(nil), call.Args...), ItemId: call.ItemID})
	if e != nil {
		return session.ToolResult{}, clientError(e)
	}
	return resultFromWire(r.GetResult())
}

type remoteSerialTool struct{ *remoteTool }

func (*remoteSerialTool) DispatchSerialTool() {}

type remoteAuthorizationTool struct{ *remoteTool }

func (t *remoteAuthorizationTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	r, e := t.client.rpc.RequestAuthorization(ctx, &brokerv1.RequestAuthorizationRequest{Handle: t.handle, Name: call.Name, CallId: string(call.ID), Args: append([]byte(nil), call.Args...), ItemId: call.ItemID})
	if e != nil {
		return session.ExternalAuthorization{}, false, clientError(e)
	}
	a, e := authFromWire(r.GetAuthorization())
	return a, r.GetRequired(), e
}
func (t *remoteAuthorizationTool) AbortAuthorization(ctx context.Context, a session.ExternalAuthorization) error {
	_, e := t.client.rpc.AbortAuthorization(ctx, &brokerv1.AbortAuthorizationRequest{Handle: t.handle, Authorization: authToWire(a)})
	return clientError(e)
}

type remoteAuthorizationSerialTool struct{ *remoteAuthorizationTool }

func (*remoteAuthorizationSerialTool) DispatchSerialTool() {}
func remoteTools(c *Client, r *brokerv1.AttachResponse) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(r.GetTools()))
	seen := map[string]bool{}
	for _, d := range r.GetTools() {
		if d == nil || d.GetName() == "" || seen[d.GetName()] || !utf8.ValidString(d.GetName()) || !utf8.ValidString(d.GetDescription()) || !json.Valid(d.GetSchema()) {
			return nil, errors.New("mcpbrokergrpc: malformed tool descriptor")
		}
		seen[d.GetName()] = true
		base := &remoteTool{client: c, handle: r.GetHandle(), spec: tool.ToolSpec{Name: d.GetName(), Description: d.GetDescription(), Schema: append([]byte(nil), d.GetSchema()...)}, readOnly: d.GetReadOnly()}
		if d.GetAuthorizationCapable() {
			a := &remoteAuthorizationTool{remoteTool: base}
			if d.GetDispatchSerial() {
				out = append(out, &remoteAuthorizationSerialTool{a})
			} else {
				out = append(out, a)
			}
		} else if d.GetDispatchSerial() {
			out = append(out, &remoteSerialTool{base})
		} else {
			out = append(out, base)
		}
	}
	return out, nil
}
func validAttachOutcome(v string) bool {
	return v == string(mcpbroker.AttachCreated) || v == string(mcpbroker.AttachReattached)
}
func clientError(err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.FailedPrecondition:
		if status.Convert(err).Message() == mcpbroker.ErrAttachmentClosed.Error() {
			return mcpbroker.ErrAttachmentClosed
		}
		return mcpbroker.ErrStateUnavailable
	case codes.NotFound:
		return mcpbroker.ErrAuthorizationNotFound
	default:
		return err
	}
}
func callFrom(name, id string, args []byte, item string) (session.ToolCall, error) {
	if name == "" || id == "" || !utf8.ValidString(name) || !utf8.ValidString(id) || !utf8.ValidString(item) || !json.Valid(args) {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: name, Args: append([]byte(nil), args...), ItemID: item}, nil
}
func resultToWire(r session.ToolResult) (*brokerv1.ToolResult, error) {
	if r.CallID == "" || !utf8.ValidString(string(r.CallID)) || !utf8.ValidString(r.Content) || !validParts(r.Parts) {
		return nil, errors.New("mcpbrokergrpc: malformed tool result")
	}
	parts := make([]*brokerv1.ResultPart, 0, len(r.Parts))
	for _, p := range r.Parts {
		parts = append(parts, &brokerv1.ResultPart{BlockKind: string(p.BlockKind), MediaKind: string(p.Kind), MimeType: p.MIMEType, Data: append([]byte(nil), p.Data...), Url: p.URL, Text: p.Text, Name: p.Name, Title: p.Title, Description: p.Description, Size: p.Size, Audience: append([]string(nil), p.Audience...), Priority: p.Priority, LastModified: p.LastModified})
	}
	return &brokerv1.ToolResult{CallId: string(r.CallID), Content: r.Content, IsError: r.IsError, Parts: parts}, nil
}
func resultFromWire(r *brokerv1.ToolResult) (session.ToolResult, error) {
	if r == nil || r.GetCallId() == "" || !utf8.ValidString(r.GetCallId()) || !utf8.ValidString(r.GetContent()) {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed tool result")
	}
	parts := make([]session.Content, 0, len(r.GetParts()))
	for _, p := range r.GetParts() {
		if p == nil {
			return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed result part")
		}
		q := session.Content{BlockKind: session.BlockKind(p.GetBlockKind()), Kind: session.MediaKind(p.GetMediaKind()), MIMEType: p.GetMimeType(), Data: append([]byte(nil), p.GetData()...), URL: p.GetUrl(), Text: p.GetText(), Name: p.GetName(), Title: p.GetTitle(), Description: p.GetDescription(), Size: p.GetSize(), Audience: append([]string(nil), p.GetAudience()...), Priority: p.GetPriority(), LastModified: p.GetLastModified()}
		parts = append(parts, q)
	}
	if !validParts(parts) {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: malformed tool result")
	}
	return session.ToolResult{CallID: session.ToolCallID(r.GetCallId()), Content: r.GetContent(), IsError: r.GetIsError(), Parts: parts}, nil
}
func validParts(parts []session.Content) bool {
	for _, p := range parts {
		for _, s := range []string{string(p.BlockKind), string(p.Kind), p.MIMEType, p.URL, p.Text, p.Name, p.Title, p.Description, p.LastModified} {
			if !utf8.ValidString(s) {
				return false
			}
		}
		for _, a := range p.Audience {
			if !utf8.ValidString(a) {
				return false
			}
		}
	}
	return session.ValidateToolResultParts(parts) == nil
}
func authToWire(a session.ExternalAuthorization) *brokerv1.Authorization {
	return &brokerv1.Authorization{Id: a.ID, Binding: string(a.Binding), ExpiresAt: timestamppb.New(a.ExpiresAt)}
}
func authFromWire(a *brokerv1.Authorization) (session.ExternalAuthorization, error) {
	if a == nil || a.GetId() == "" || a.GetBinding() == "" || !utf8.ValidString(a.GetId()) || !utf8.ValidString(a.GetBinding()) || a.GetExpiresAt() == nil || !a.GetExpiresAt().IsValid() {
		return session.ExternalAuthorization{}, errors.New("mcpbrokergrpc: malformed authorization")
	}
	return session.ExternalAuthorization{ID: a.GetId(), Binding: session.AuthorizationBinding(a.GetBinding()), ExpiresAt: a.GetExpiresAt().AsTime()}, nil
}

var _ mcpbroker.Service = (*Client)(nil)
var _ mcpbroker.BindingSessionDeleter = (*Client)(nil)
