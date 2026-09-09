package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

const (
	agentModelDiscoveryToolName       = "DiscoverModels"
	defaultAgentModelDiscoveryLimit   = 20
	maxAgentModelDiscoveryLimit       = 50
	maxAgentModelDiscoveryOutputBytes = 32 << 10
	maxAgentModelDiscoveryErrorBytes  = 256
	agentModelDiscoveryInvalidArgs    = "invalid model discovery arguments; use only provider_id, model_id, and limit"
)

// resolvedModelInventory is the composition-owned resolved inventory shared by
// ListModels and the model-facing discovery tool.
type resolvedModelInventory struct {
	models atomic.Pointer[[]*mecatlv1.ModelInfo]
}

func newResolvedModelInventory(models []*mecatlv1.ModelInfo) *resolvedModelInventory {
	inventory := &resolvedModelInventory{}
	inventory.SetModels(models)
	return inventory
}

func (i *resolvedModelInventory) SetModels(models []*mecatlv1.ModelInfo) {
	if models == nil {
		models = []*mecatlv1.ModelInfo{}
	}
	copyOfModels := append([]*mecatlv1.ModelInfo(nil), models...)
	i.models.Store(&copyOfModels)
}

func (i *resolvedModelInventory) CurrentModels() []*mecatlv1.ModelInfo {
	if i == nil {
		return nil
	}
	if models := i.models.Load(); models != nil {
		return *models
	}
	return nil
}

// Models is retained as a composition-local convenience for tests and callers
// that do not need the server adapter's inventory interface name.
func (i *resolvedModelInventory) Models() []*mecatlv1.ModelInfo { return i.CurrentModels() }

func modelDiscoveryAvailable(reg *providerRegistry, inventory *resolvedModelInventory) bool {
	return len(inventory.CurrentModels()) > 0 || anyProviderHasLister(reg)
}

type agentModelDiscoveryTool struct {
	inventory *resolvedModelInventory
}

type agentModelDiscoveryArgs struct {
	ProviderID *string `json:"provider_id"`
	ModelID    *string `json:"model_id"`
	Limit      *int    `json:"limit"`
}

type agentModelDiscoveryModel struct {
	ProviderID   string `json:"provider_id"`
	ModelID      string `json:"model_id"`
	DisplayName  string `json:"display_name"`
	Image        bool   `json:"image"`
	Reasoning    bool   `json:"reasoning"`
	ContextLimit int64  `json:"context_limit"`
}

type agentModelDiscoveryResult struct {
	Models    []agentModelDiscoveryModel `json:"models"`
	Returned  int                        `json:"returned"`
	Available int                        `json:"available"`
	Truncated bool                       `json:"truncated"`
}

func newAgentModelDiscoveryTool(inventory *resolvedModelInventory) agentModelDiscoveryTool {
	return agentModelDiscoveryTool{inventory: inventory}
}

func (agentModelDiscoveryTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name:        agentModelDiscoveryToolName,
		Description: "Inspect the bounded resolved model inventory. Each provider_id plus model_id pair is an exact configured inference-target selection handle. Filters are exact matches over those safe fields only; this tool never probes providers or changes the current session.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "provider_id": {"type": "string", "description": "Optional exact provider id to retain; omit or use an empty string for no provider filter."},
    "model_id": {"type": "string", "description": "Optional exact model id to retain; omit or use an empty string for no model filter; never implies a provider."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum entries to return (default 20, maximum 50)."}
  },
  "additionalProperties": false
}`),
	}
}

func (agentModelDiscoveryTool) ReadOnly() bool { return true }

func (t agentModelDiscoveryTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	args, errMessage := parseAgentModelDiscoveryArgs(call.Args)
	if errMessage != "" {
		return session.NewToolError(call.ID, errMessage), nil
	}
	limit := defaultAgentModelDiscoveryLimit
	if args.Limit != nil {
		limit = *args.Limit
	}
	models := t.inventory.CurrentModels()
	matching := make([]*mecatlv1.ModelInfo, 0, len(models))
	for _, model := range models {
		if model == nil || model.GetProviderId() == "" || model.GetId() == "" ||
			(args.ProviderID != nil && model.GetProviderId() != *args.ProviderID) || (args.ModelID != nil && model.GetId() != *args.ModelID) {
			continue
		}
		matching = append(matching, model)
	}

	out := agentModelDiscoveryResult{Models: make([]agentModelDiscoveryModel, 0, min(limit, len(matching))), Available: len(matching)}
	for _, model := range matching {
		if len(out.Models) == limit {
			break
		}
		entry := agentModelDiscoveryModel{
			ProviderID: model.GetProviderId(), ModelID: model.GetId(), DisplayName: model.GetDisplayName(),
			Image: model.GetImage(), Reasoning: model.GetReasoning(), ContextLimit: model.GetContextLimit(),
		}
		candidate := out
		candidate.Models = append(append([]agentModelDiscoveryModel(nil), out.Models...), entry)
		candidate.Returned = len(candidate.Models)
		candidate.Truncated = candidate.Returned < candidate.Available
		encoded, err := json.Marshal(candidate)
		if err != nil || len(encoded) > maxAgentModelDiscoveryOutputBytes {
			break
		}
		out.Models = candidate.Models
	}
	out.Returned = len(out.Models)
	out.Truncated = out.Returned < out.Available
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > maxAgentModelDiscoveryOutputBytes {
		return session.NewToolError(call.ID, "model inventory cannot be represented within the safe output bound"), nil
	}
	return session.NewToolResult(call.ID, string(encoded)), nil
}

func parseAgentModelDiscoveryArgs(raw json.RawMessage) (agentModelDiscoveryArgs, string) {
	var args agentModelDiscoveryArgs
	if len(raw) == 0 {
		return args, ""
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return agentModelDiscoveryArgs{}, agentModelDiscoveryInvalidArgs
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return agentModelDiscoveryArgs{}, agentModelDiscoveryInvalidArgs
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return agentModelDiscoveryArgs{}, agentModelDiscoveryInvalidArgs
	}
	if errMessage := validateAgentModelDiscoveryRawFields(raw); errMessage != "" {
		return agentModelDiscoveryArgs{}, errMessage
	}
	if args.ProviderID != nil && *args.ProviderID == "" {
		args.ProviderID = nil
	}
	if args.ModelID != nil && *args.ModelID == "" {
		args.ModelID = nil
	}
	if args.ProviderID != nil && !validDiscoveryFilter(*args.ProviderID) {
		return agentModelDiscoveryArgs{}, "provider_id must be a bounded exact value without surrounding or control whitespace"
	}
	if args.ModelID != nil && !validDiscoveryFilter(*args.ModelID) {
		return agentModelDiscoveryArgs{}, "model_id must be a bounded exact value without surrounding or control whitespace"
	}
	if args.Limit != nil && (*args.Limit < 1 || *args.Limit > maxAgentModelDiscoveryLimit) {
		return agentModelDiscoveryArgs{}, "limit must be between 1 and 50"
	}
	return args, ""
}

func validateAgentModelDiscoveryRawFields(raw json.RawMessage) string {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return agentModelDiscoveryInvalidArgs
	}
	for _, name := range []string{"provider_id", "model_id", "limit"} {
		value, ok := fields[name]
		if !ok {
			continue
		}
		if !utf8.Valid(value) {
			return agentModelDiscoveryInvalidArgs
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return "invalid model discovery arguments; filter values cannot be null"
		}
	}
	return ""
}

func validDiscoveryFilter(value string) bool {
	if len(value) > 512 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
