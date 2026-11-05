package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// PrepareGrantedAuthorization refreshes the catalogue for the exact granted
// authorization, adds this handle's CallMcpWithQuery wrapper (the refresh
// returns only catalogue tools, and a parked CallMcpWithQuery call must resume
// into a catalogue that still has it), and checks the parked arguments against
// the authenticated schema.
func (a *Attachment) PrepareGrantedAuthorization(ctx context.Context, claimed session.PendingAuthorization) (contract.GrantedAuthorization, error) {
	tools, err := a.RefreshGrantedAuthorizationCatalogue(ctx, claimed.Authorization)
	if err != nil {
		return contract.GrantedAuthorization{}, err
	}
	if query := a.CallMcpWithQueryTool(); query != nil {
		tools = append(tools, query)
	}
	return contract.GrantedAuthorization{Tools: tools, ArgumentsErr: validateGrantedAuthorizationArguments(tools, claimed.Call)}, nil
}

// validateGrantedAuthorizationArguments is deliberately scoped to the lazy
// granted-authorization replacement transition. Unlike ordinary tool dispatch,
// this boundary must fail closed: it receives the authenticated schema after a
// parked call was authored against the static declaration.
func validateGrantedAuthorizationArguments(tools []tool.Tool, call session.ToolCall) error {
	if call.Name != mcpadapter.CallMcpWithQuerySpec().Name {
		return validateAuthenticatedToolArguments(tools, call)
	}
	canonical, target, err := canonicalizeQueryAuthorizationCall(call)
	if err != nil {
		return err
	}
	if err := validateAuthenticatedToolArguments(tools, canonical); err != nil {
		return err
	}
	return validateAuthenticatedToolArguments(tools, target)
}

func canonicalizeQueryAuthorizationCall(call session.ToolCall) (session.ToolCall, session.ToolCall, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(call.Args, &envelope); err != nil || envelope == nil {
		return session.ToolCall{}, session.ToolCall{}, errors.New("parked CallMcpWithQuery arguments are not a JSON object")
	}
	var query struct {
		Server string          `json:"server"`
		Tool   string          `json:"tool"`
		Args   json.RawMessage `json:"args"`
	}
	if err := json.Unmarshal(call.Args, &query); err != nil {
		return session.ToolCall{}, session.ToolCall{}, errors.New("parked CallMcpWithQuery arguments are malformed")
	}
	remoteArgs, message := mcpadapter.NormalizeRemoteArgs(query.Args)
	if message != "" {
		return session.ToolCall{}, session.ToolCall{}, errors.New("parked CallMcpWithQuery remote arguments are not a JSON object")
	}
	if len(remoteArgs) == 0 {
		remoteArgs = json.RawMessage(`{}`)
	}
	envelope["args"] = remoteArgs
	canonicalArgs, err := json.Marshal(envelope)
	if err != nil {
		return session.ToolCall{}, session.ToolCall{}, errors.New("parked CallMcpWithQuery arguments cannot be canonicalized")
	}
	server, toolName := strings.TrimSpace(query.Server), strings.TrimSpace(query.Tool)
	if server == "" || toolName == "" || strings.Contains(server, "__") || strings.Contains(toolName, "__") {
		return session.ToolCall{}, session.ToolCall{}, errors.New("parked CallMcpWithQuery target is invalid")
	}
	return session.NewToolCall(call.ID, call.Name, canonicalArgs), session.NewToolCall(call.ID, "mcp__"+server+"__"+toolName, remoteArgs), nil
}

func validateAuthenticatedToolArguments(tools []tool.Tool, call session.ToolCall) error {
	var schemaRaw json.RawMessage
	for _, candidate := range tools {
		if candidate != nil && candidate.Spec().Name == call.Name {
			if schemaRaw != nil {
				return errors.New("ambiguous authenticated replacement tool")
			}
			schemaRaw = candidate.Spec().Schema
		}
	}
	if len(schemaRaw) == 0 {
		return errors.New("authenticated replacement tool schema is unavailable")
	}
	var schemaObject map[string]json.RawMessage
	if err := json.Unmarshal(schemaRaw, &schemaObject); err != nil || schemaObject == nil {
		return errors.New("authenticated replacement tool schema is not a JSON object")
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(schemaRaw, &schema); err != nil {
		return errors.New("authenticated replacement tool schema is malformed")
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return errors.New("authenticated replacement tool schema cannot be resolved")
	}
	var args map[string]any
	if err := json.Unmarshal(call.Args, &args); err != nil || args == nil {
		return errors.New("parked tool arguments are not a JSON object")
	}
	if err := resolved.Validate(args); err != nil {
		return errors.New("parked tool arguments do not conform to authenticated replacement schema")
	}
	return nil
}
