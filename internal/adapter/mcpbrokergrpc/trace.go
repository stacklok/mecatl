package mcpbrokergrpc

import (
	"encoding/json"

	"github.com/stacklok/mecatl/engine/session"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// AttachmentTrace is the safe correlation data retained for an authenticated
// handle. It contains no request content, OAuth state, URL, or credentials.
type AttachmentTrace struct {
	LogicalSession         string
	Binding                string
	Backend                string
	OutboundCredentialKind contract.OutboundCredentialKind
}

// TraceAttachment returns the safe correlation retained for a current handle.
// Callers must authenticate the request before using it; absence is safe.
func (s *Server) TraceAttachment(handle string) (AttachmentTrace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.handles[handle]
	if a == nil {
		return AttachmentTrace{}, false
	}
	return AttachmentTrace{LogicalSession: string(a.logicalID), Binding: a.binding}, true
}

// TraceExecution adds optional adapter-provided execution provenance to a safe
// attachment correlation. Arguments are parsed only by a metadata provider and
// are never returned or logged.
func (s *Server) TraceExecution(handle, name string, args []byte) (AttachmentTrace, bool) {
	s.mu.Lock()
	a := s.handles[handle]
	if a == nil {
		s.mu.Unlock()
		return AttachmentTrace{}, false
	}
	trace := AttachmentTrace{LogicalSession: string(a.logicalID), Binding: a.binding}
	target := a.tools[name]
	s.mu.Unlock()
	provider, ok := target.(contract.ExecutionMetadataProvider)
	if !ok {
		return trace, true
	}
	call := session.NewToolCall("trace", name, append(json.RawMessage(nil), args...))
	metadata, ok := provider.ExecutionMetadata(call)
	if !ok || !validTraceMetadata(metadata) {
		return trace, true
	}
	trace.Backend, trace.OutboundCredentialKind = metadata.Backend, metadata.OutboundCredentialKind
	return trace, true
}

func validTraceMetadata(metadata contract.ExecutionMetadata) bool {
	if metadata.Backend == "" {
		return false
	}
	switch metadata.OutboundCredentialKind {
	case contract.OutboundCredentialNone, contract.OutboundCredentialRouteOAuth, contract.OutboundCredentialBrokerOAuth:
		return true
	default:
		return false
	}
}
