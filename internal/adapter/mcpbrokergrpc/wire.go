package mcpbrokergrpc

import (
	"encoding/json"
	"errors"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func descriptors(in []tool.Tool) ([]*brokerv1.ToolDescriptor, map[string]tool.Tool, error) {
	out := make([]*brokerv1.ToolDescriptor, 0, len(in))
	tools := make(map[string]tool.Tool, len(in))
	for _, t := range in {
		if t == nil {
			return nil, nil, errors.New("nil tool")
		}
		spec := t.Spec()
		if spec.Name == "" || !utf8.ValidString(spec.Name) || !utf8.ValidString(spec.Description) || !validJSONObject(spec.Schema) {
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
func validJSONObject(raw []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(raw, &object) == nil && object != nil
}
func invalid(msg string) error { return status.Error(codes.InvalidArgument, msg) }

const (
	maxInvocationCallIDBytes = 256
	maxInvocationNameBytes   = 256
	maxInvocationItemIDBytes = 1024
	maxInvocationArgsBytes   = 256 << 10
)

func callFrom(name, id string, args []byte, item string) (session.ToolCall, error) {
	if !validInvocationText(name, maxInvocationNameBytes) || !validInvocationText(id, maxInvocationCallIDBytes) || (item != "" && !validInvocationText(item, maxInvocationItemIDBytes)) || len(args) == 0 || len(args) > maxInvocationArgsBytes || !json.Valid(args) {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(args, &object) != nil {
		return session.ToolCall{}, errors.New("mcpbrokergrpc: malformed invocation")
	}
	return session.ToolCall{ID: session.ToolCallID(id), Name: name, Args: append([]byte(nil), args...), ItemID: item}, nil
}

func wireCall(call c.Call) (*brokerv1.Call, error) {
	if _, err := callFrom(call.Name, string(call.ID), call.Arguments, ""); err != nil {
		return nil, err
	}
	return &brokerv1.Call{Id: string(call.ID), Name: call.Name, Arguments: append([]byte(nil), call.Arguments...)}, nil
}

func validInvocationText(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
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
