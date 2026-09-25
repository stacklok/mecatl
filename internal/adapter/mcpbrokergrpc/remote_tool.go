package mcpbrokergrpc

import (
	"context"
	"errors"
	"unicode/utf8"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type remoteTool struct {
	client     *Client
	handle     string
	instanceID string
	spec       tool.ToolSpec
	readOnly   bool
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
	rpcCtx, cancel := context.WithTimeout(ctx, t.client.cfg.ExecuteDeadline)
	defer cancel()
	r, e := t.client.rpc.Execute(rpcCtx, &brokerv1.ExecuteRequest{Handle: t.handle, Name: call.Name, CallId: string(call.ID), Args: append([]byte(nil), call.Args...), ItemId: call.ItemID, BrokerIncarnation: t.instanceID})
	if e != nil {
		if isDefinitiveSessionLoss(e) {
			return session.NewToolError(call.ID, sessionUnavailableMessage), nil
		}
		if dispatchNotStarted(e) {
			return session.ToolResult{}, clientError(e)
		}
		return session.NewToolError(call.ID, ambiguousOutcomeMessage), nil
	}
	result, err := resultFromWire(r.GetResult())
	if err != nil {
		return session.ToolResult{}, err
	}
	if result.CallID != call.ID {
		return session.ToolResult{}, errors.New("mcpbrokergrpc: tool result call_id mismatch")
	}
	return result, nil
}

type remoteSerialTool struct{ *remoteTool }

func (*remoteSerialTool) DispatchSerialTool() {}

func remoteTools(c *Client, r *brokerv1.AttachResponse) ([]tool.Tool, error) {
	out := make([]tool.Tool, 0, len(r.GetTools()))
	seen := map[string]bool{}
	for _, d := range r.GetTools() {
		if d == nil || d.GetName() == "" || seen[d.GetName()] || !utf8.ValidString(d.GetName()) || !utf8.ValidString(d.GetDescription()) || !validJSONObject(d.GetSchema()) {
			return nil, errors.New("mcpbrokergrpc: malformed tool descriptor")
		}
		seen[d.GetName()] = true
		base := &remoteTool{client: c, handle: r.GetHandle(), instanceID: r.GetBrokerIncarnation(), spec: tool.ToolSpec{Name: d.GetName(), Description: d.GetDescription(), Schema: append([]byte(nil), d.GetSchema()...)}, readOnly: d.GetReadOnly()}
		if d.GetDispatchSerial() {
			out = append(out, &remoteSerialTool{base})
		} else {
			out = append(out, base)
		}
	}
	return out, nil
}
