package mcpbrokergrpc

import (
	"context"
	"errors"
	"time"
	"unicode/utf8"

	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var errSessionWire = errors.New("mcpbrokergrpc: invalid session response")

// SessionClient owns its connection and applies a finite deadline to lifecycle RPCs.
type SessionClient struct {
	conn                         *grpc.ClientConn
	rpc                          p.SessionServiceClient
	rpcDeadline, executeDeadline time.Duration
}

func NewSessionClient(target string, rpcDeadline, executeDeadline time.Duration, options ...grpc.DialOption) (*SessionClient, error) {
	if target == "" || rpcDeadline <= 0 || executeDeadline <= 0 {
		return nil, c.ErrStateUnavailable
	}
	options = append(append([]grpc.DialOption(nil), options...), grpc.WithDisableRetry())
	conn, err := grpc.NewClient(target, options...)
	if err != nil {
		return nil, err
	}
	return &SessionClient{conn: conn, rpc: p.NewSessionServiceClient(conn), rpcDeadline: rpcDeadline, executeDeadline: executeDeadline}, nil
}

func (s *SessionClient) Close() error { return s.conn.Close() }

func cleanSessionWire(message proto.Message) bool {
	if message == nil {
		return false
	}
	m := message.ProtoReflect()
	if !m.IsValid() || len(m.GetUnknown()) != 0 {
		return false
	}
	clean := true
	m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind {
			return true
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if !cleanSessionWire(list.Get(i).Message().Interface()) {
					clean = false
					return false
				}
			}
		} else if !cleanSessionWire(value.Message().Interface()) {
			clean = false
		}
		return clean
	})
	return clean
}

func (s *SessionClient) catalogue(ref c.SessionRef, wire *p.Catalogue) (c.Catalogue, error) {
	if !cleanSessionWire(wire) || !validSessionRef(wire.Ref) || !validSessionRef(string(ref)) || (wire.ConnectionRef != nil && !validSessionRef(*wire.ConnectionRef)) || len(wire.Tools) > 1024 {
		return nil, errSessionWire
	}
	tools := make([]tool.Tool, 0, len(wire.Tools))
	seen := make(map[string]bool, len(wire.Tools))
	for _, descriptor := range wire.Tools {
		if descriptor == nil || seen[descriptor.Name] || !utf8.ValidString(descriptor.Name) || !utf8.ValidString(descriptor.Description) || !utf8.Valid(descriptor.Schema) {
			return nil, errSessionWire
		}
		seen[descriptor.Name] = true
		base := &sessionRemoteTool{spec: tool.ToolSpec{Name: descriptor.Name, Description: descriptor.Description, Schema: append([]byte(nil), descriptor.Schema...)}, readOnly: descriptor.ReadOnly}
		tools = append(tools, sessionToolMarkers(base, descriptor.AuthorizationCapable, descriptor.DispatchSerial))
	}
	catalogue, err := c.NewCatalogue(c.CatalogueRef(wire.Ref), c.ConnectionRef(wire.GetConnectionRef()), tools)
	if err != nil {
		return nil, errSessionWire
	}
	return catalogue, nil
}

func (s *SessionClient) OpenSession(ctx context.Context, saved *c.SessionRef) (c.SessionSnapshot, error) {
	request := &p.OpenSessionRequest{}
	if saved != nil {
		if !validSessionRef(string(*saved)) {
			return c.SessionSnapshot{}, errSessionWire
		}
		value := string(*saved)
		request.SavedRef = &value
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.OpenSession(ctx, request, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	if !cleanSessionWire(response) || !validSessionRef(response.Ref) || response.ExpiresAt == nil || !response.ExpiresAt.IsValid() || !response.ExpiresAt.AsTime().After(time.Now()) || (saved != nil && response.Ref != string(*saved)) {
		return c.SessionSnapshot{}, errSessionWire
	}
	catalogue, err := s.catalogue(c.SessionRef(response.Ref), response.Catalogue)
	if err != nil {
		return c.SessionSnapshot{}, err
	}
	return c.SessionSnapshot{Ref: c.SessionRef(response.Ref), ExpiresAt: response.ExpiresAt.AsTime(), Catalogue: catalogue}, nil
}

func (s *SessionClient) DeleteSession(ctx context.Context, ref c.SessionRef) (c.DeleteResult, error) {
	if !validSessionRef(string(ref)) {
		return 0, errSessionWire
	}
	ctx, cancel := context.WithTimeout(ctx, s.rpcDeadline)
	defer cancel()
	response, err := s.rpc.DeleteSession(ctx, &p.DeleteSessionRequest{SessionRef: string(ref)}, grpc.MaxRetryRPCBufferSize(0))
	if err != nil {
		return 0, err
	}
	if !cleanSessionWire(response) || response.Outcome < p.DeleteOutcome_DELETED || response.Outcome > p.DeleteOutcome_ALREADY_ABSENT {
		return 0, errSessionWire
	}
	return c.DeleteResult(response.Outcome), nil
}

var _ c.SessionService = (*SessionClient)(nil)
var _ tool.Tool = (*sessionRemoteTool)(nil)
var _ tool.AuthorizationRequester = (*sessionAuthorizationTool)(nil)
var _ tool.DispatchSerial = (*sessionSerialTool)(nil)
var _ tool.DispatchSerial = (*sessionAuthorizationSerialTool)(nil)
