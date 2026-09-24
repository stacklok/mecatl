package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	agentModelDiscoveryToolName       = "DiscoverModels"
	defaultAgentModelDiscoveryLimit   = 20
	maxAgentModelDiscoveryLimit       = 50
	maxAgentModelDiscoveryOutputBytes = 32 << 10
	maxAgentModelDiscoveryErrorBytes  = 256
	maxAgentModelDiscoveryCursorBytes = 4096
	maxAgentModelDiscoveryFilterBytes = 512
	maxAgentModelDiscoveryQueryTerms  = 8
	maxAgentModelDiscoveryTermBytes   = 64
	maxAgentModelDiscoveryTermsBytes  = 256
	agentModelDiscoveryCursorVersion  = 1

	agentModelDiscoveryInvalidArgs   = "invalid model discovery arguments; use only provider_id, model_id, query, cursor, and limit"
	agentModelDiscoveryInvalidCursor = "invalid model discovery cursor; restart without a cursor"
	agentModelDiscoveryStaleCursor   = "model inventory changed; restart without a cursor"
	agentModelDiscoveryOutputError   = "model inventory cannot make progress within the safe output bound"
)

func modelDiscoveryAvailable(reg *providerRegistry, inventory server.ModelInventory) bool {
	return (inventory != nil && len(inventory.CurrentModelSnapshot().Models) > 0) || anyProviderHasLister(reg)
}

type agentModelDiscoveryTool struct {
	inventory server.ModelInventory
}

type agentModelDiscoveryArgs struct {
	ProviderID *string `json:"provider_id"`
	ModelID    *string `json:"model_id"`
	Query      *string `json:"query"`
	Cursor     *string `json:"cursor"`
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

type agentModelDiscoveryProvider struct {
	ProviderID string `json:"provider_id"`
	ModelCount int    `json:"model_count"`
}

type agentModelDiscoveryResult struct {
	Models     []agentModelDiscoveryModel     `json:"models"`
	Providers  *[]agentModelDiscoveryProvider `json:"providers,omitempty"`
	Returned   int                            `json:"returned"`
	Available  int                            `json:"available"`
	Truncated  bool                           `json:"truncated"`
	NextCursor string                         `json:"next_cursor,omitempty"`
}

type agentModelDiscoveryCursor struct {
	Version    int      `json:"v"`
	ProviderID string   `json:"provider_id,omitempty"`
	ModelID    string   `json:"model_id,omitempty"`
	Terms      []string `json:"terms,omitempty"`
	Limit      int      `json:"limit"`
	Offset     int      `json:"offset"`
	Digest     string   `json:"digest"`
}

func newAgentModelDiscoveryTool(inventory server.ModelInventory) agentModelDiscoveryTool {
	return agentModelDiscoveryTool{inventory: inventory}
}

func (agentModelDiscoveryTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{
		Name: agentModelDiscoveryToolName,
		Description: "Find exact selectable model handles in the bounded resolved inventory. Omit provider_id to search all selectable providers; an unfiltered first call also returns providers with model counts. " +
			"provider_id and model_id are exact filters. query is split with strings.Fields and lowercased with strings.ToLower; every literal term must match one safe field. " +
			"limit defaults to 20 and is at most 50. Pass next_cursor back as cursor by itself to continue; if inventory changed, restart without a cursor. Results are bounded to 32 KiB. " +
			"This read-only tool never probes providers, never selects a model, and never changes the current session.",
		Schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "provider_id": {"type": "string", "description": "Optional byte-exact provider id. Omit to search all selectable providers."},
    "model_id": {"type": "string", "description": "Optional byte-exact model id; never implies a provider."},
    "query": {"type": "string", "description": "Optional bounded whitespace-separated literal terms matched across provider id, model id, and display name."},
    "cursor": {"type": "string", "description": "Opaque next_cursor from the preceding page. When set, omit every other field."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50, "description": "Maximum complete model rows to return (default 20, maximum 50)."}
  },
  "additionalProperties": false
}`),
	}
}

func (agentModelDiscoveryTool) ReadOnly() bool { return true }

type agentModelDiscoveryScope struct {
	ProviderID   string
	ModelID      string
	Terms        []string
	Limit        int
	Offset       int
	Continuation bool
}

func (t agentModelDiscoveryTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	args, errMessage := parseAgentModelDiscoveryArgs(call.Args)
	if errMessage != "" {
		return session.NewToolError(call.ID, errMessage), nil
	}

	projection := discoveryProjection(t.inventory.CurrentModelSnapshot().Models)
	digest := discoveryProjectionDigest(projection)
	scope, errMessage := resolveAgentModelDiscoveryScope(args, digest)
	if errMessage != "" {
		return session.NewToolError(call.ID, errMessage), nil
	}
	encoded, errMessage := renderAgentModelDiscoveryPage(projection, scope, digest)
	if errMessage != "" {
		return session.NewToolError(call.ID, errMessage), nil
	}
	return session.NewToolResult(call.ID, string(encoded)), nil
}

func resolveAgentModelDiscoveryScope(args agentModelDiscoveryArgs, digest string) (agentModelDiscoveryScope, string) {
	scope := agentModelDiscoveryScope{
		ProviderID: value(args.ProviderID), ModelID: value(args.ModelID),
		Limit: defaultAgentModelDiscoveryLimit,
	}
	if args.Cursor != nil {
		restored, ok := decodeAgentModelDiscoveryCursor(*args.Cursor)
		if !ok {
			return agentModelDiscoveryScope{}, agentModelDiscoveryInvalidCursor
		}
		if restored.Digest != digest {
			return agentModelDiscoveryScope{}, agentModelDiscoveryStaleCursor
		}
		return agentModelDiscoveryScope{
			ProviderID: restored.ProviderID, ModelID: restored.ModelID, Terms: restored.Terms,
			Limit: restored.Limit, Offset: restored.Offset, Continuation: true,
		}, ""
	}
	if args.Query != nil {
		scope.Terms = normalizeDiscoveryQuery(*args.Query)
	}
	if args.Limit != nil {
		scope.Limit = *args.Limit
	}
	return scope, ""
}

func renderAgentModelDiscoveryPage(projection []agentModelDiscoveryModel, scope agentModelDiscoveryScope, digest string) ([]byte, string) {
	matching := filterDiscoveryProjection(projection, scope.ProviderID, scope.ModelID, scope.Terms)
	if scope.Continuation && (scope.Offset <= 0 || scope.Offset >= len(matching)) {
		return nil, agentModelDiscoveryInvalidCursor
	}
	out := agentModelDiscoveryResult{
		Models:    make([]agentModelDiscoveryModel, 0, min(scope.Limit, len(matching)-scope.Offset)),
		Available: len(matching),
	}
	if !scope.Continuation && scope.ProviderID == "" && scope.ModelID == "" && len(scope.Terms) == 0 {
		providers := discoveryProviderFacets(projection)
		out.Providers = &providers
	}

	for next := scope.Offset; next < len(matching) && len(out.Models) < scope.Limit; next++ {
		candidate := nextAgentModelDiscoveryResult(out, matching[next], next+1, len(matching), scope, digest)
		if candidate.Truncated && candidate.NextCursor == "" {
			break
		}
		encoded, err := json.Marshal(candidate)
		if err != nil || len(encoded) > maxAgentModelDiscoveryOutputBytes {
			break
		}
		out = candidate
	}
	if scope.Offset < len(matching) && len(out.Models) == 0 {
		return nil, agentModelDiscoveryOutputError
	}
	out.Returned = len(out.Models)
	encoded, err := json.Marshal(out)
	if err != nil || len(encoded) > maxAgentModelDiscoveryOutputBytes {
		return nil, agentModelDiscoveryOutputError
	}
	return encoded, ""
}

func nextAgentModelDiscoveryResult(out agentModelDiscoveryResult, model agentModelDiscoveryModel, next, available int, scope agentModelDiscoveryScope, digest string) agentModelDiscoveryResult {
	out.Models = append(append([]agentModelDiscoveryModel(nil), out.Models...), model)
	out.Returned = len(out.Models)
	if next >= available {
		out.NextCursor = ""
		out.Truncated = false
		return out
	}
	out.NextCursor = encodeAgentModelDiscoveryCursor(agentModelDiscoveryCursor{
		Version: agentModelDiscoveryCursorVersion, ProviderID: scope.ProviderID, ModelID: scope.ModelID,
		Terms: scope.Terms, Limit: scope.Limit, Offset: next, Digest: digest,
	})
	out.Truncated = true
	return out
}

func discoveryProjection(models []*mecatlv1.ModelInfo) []agentModelDiscoveryModel {
	projection := make([]agentModelDiscoveryModel, 0, len(models))
	for _, model := range models {
		if model == nil || model.GetProviderId() == "" || model.GetId() == "" {
			continue
		}
		projection = append(projection, agentModelDiscoveryModel{
			ProviderID: model.GetProviderId(), ModelID: model.GetId(), DisplayName: model.GetDisplayName(),
			Image: model.GetImage(), Reasoning: model.GetReasoning(), ContextLimit: model.GetContextLimit(),
		})
	}
	sort.Slice(projection, func(a, b int) bool {
		if projection[a].ProviderID != projection[b].ProviderID {
			return projection[a].ProviderID < projection[b].ProviderID
		}
		if projection[a].ModelID != projection[b].ModelID {
			return projection[a].ModelID < projection[b].ModelID
		}
		if projection[a].DisplayName != projection[b].DisplayName {
			return projection[a].DisplayName < projection[b].DisplayName
		}
		if projection[a].Image != projection[b].Image {
			return !projection[a].Image
		}
		if projection[a].Reasoning != projection[b].Reasoning {
			return !projection[a].Reasoning
		}
		return projection[a].ContextLimit < projection[b].ContextLimit
	})
	return projection
}

func discoveryProjectionDigest(projection []agentModelDiscoveryModel) string {
	encoded, _ := json.Marshal(projection)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func discoveryProviderFacets(projection []agentModelDiscoveryModel) []agentModelDiscoveryProvider {
	providers := make([]agentModelDiscoveryProvider, 0)
	for _, model := range projection {
		if len(providers) == 0 || providers[len(providers)-1].ProviderID != model.ProviderID {
			providers = append(providers, agentModelDiscoveryProvider{ProviderID: model.ProviderID})
		}
		providers[len(providers)-1].ModelCount++
	}
	return providers
}

func filterDiscoveryProjection(projection []agentModelDiscoveryModel, providerID, modelID string, terms []string) []agentModelDiscoveryModel {
	matching := make([]agentModelDiscoveryModel, 0, len(projection))
	for _, model := range projection {
		if providerID != "" && model.ProviderID != providerID || modelID != "" && model.ModelID != modelID {
			continue
		}
		fields := []string{strings.ToLower(model.ProviderID), strings.ToLower(model.ModelID), strings.ToLower(model.DisplayName)}
		matches := true
		for _, term := range terms {
			found := false
			for _, field := range fields {
				if strings.Contains(field, term) {
					found = true
					break
				}
			}
			if !found {
				matches = false
				break
			}
		}
		if matches {
			matching = append(matching, model)
		}
	}
	return matching
}

func parseAgentModelDiscoveryArgs(raw json.RawMessage) (agentModelDiscoveryArgs, string) {
	var args agentModelDiscoveryArgs
	if len(raw) == 0 {
		return args, ""
	}
	if errMessage := validateAgentModelDiscoveryRawFields(raw); errMessage != "" {
		return agentModelDiscoveryArgs{}, errMessage
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
	if errMessage := normalizeAndValidateAgentModelDiscoveryArgs(&args); errMessage != "" {
		return agentModelDiscoveryArgs{}, errMessage
	}
	return args, ""
}

func normalizeAndValidateAgentModelDiscoveryArgs(args *agentModelDiscoveryArgs) string {
	omitEmpty(&args.ProviderID)
	omitEmpty(&args.ModelID)
	omitEmpty(&args.Cursor)
	if args.Query != nil {
		if errMessage := validateDiscoveryQuery(*args.Query); errMessage != "" {
			return errMessage
		}
		if len(strings.Fields(*args.Query)) == 0 {
			args.Query = nil
		}
	}
	if args.ProviderID != nil && !validDiscoveryFilter(*args.ProviderID) {
		return "provider_id must be a bounded exact value without surrounding whitespace or controls"
	}
	if args.ModelID != nil && !validDiscoveryFilter(*args.ModelID) {
		return "model_id must be a bounded exact value without surrounding whitespace or controls"
	}
	if args.Limit != nil && (*args.Limit < 1 || *args.Limit > maxAgentModelDiscoveryLimit) {
		return "limit must be between 1 and 50"
	}
	if args.Cursor != nil && (len(*args.Cursor) > maxAgentModelDiscoveryCursorBytes || args.ProviderID != nil || args.ModelID != nil || args.Query != nil || args.Limit != nil) {
		return agentModelDiscoveryInvalidCursor
	}
	return ""
}

func validateAgentModelDiscoveryRawFields(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' || !utf8.Valid(raw) {
		return agentModelDiscoveryInvalidArgs
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return agentModelDiscoveryInvalidArgs
	}
	allowed := map[string]struct{}{
		"provider_id": {}, "model_id": {}, "query": {}, "cursor": {}, "limit": {},
	}
	seen := make(map[string]struct{}, len(allowed))
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return agentModelDiscoveryInvalidArgs
		}
		if _, ok := allowed[name]; !ok {
			return agentModelDiscoveryInvalidArgs
		}
		if _, duplicate := seen[name]; duplicate {
			return agentModelDiscoveryInvalidArgs
		}
		seen[name] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil || !utf8.Valid(value) {
			return agentModelDiscoveryInvalidArgs
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return "invalid model discovery arguments; values cannot be null"
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return agentModelDiscoveryInvalidArgs
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return agentModelDiscoveryInvalidArgs
	}
	return ""
}

func omitEmpty(value **string) {
	if *value != nil && **value == "" {
		*value = nil
	}
}

func value(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validDiscoveryFilter(value string) bool {
	if len(value) > maxAgentModelDiscoveryFilterBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	return !containsUnicodeControl(value)
}

func validateDiscoveryQuery(query string) string {
	if !utf8.ValidString(query) || len(query) > maxAgentModelDiscoveryFilterBytes || containsUnicodeControl(query) {
		return "query must be valid UTF-8 without controls and at most 512 bytes"
	}
	terms := normalizeDiscoveryQuery(query)
	if len(terms) > maxAgentModelDiscoveryQueryTerms {
		return "query must contain at most eight bounded literal terms"
	}
	total := 0
	for _, term := range terms {
		if len(term) > maxAgentModelDiscoveryTermBytes {
			return "query terms must be at most 64 bytes"
		}
		total += len(term)
	}
	if total > maxAgentModelDiscoveryTermsBytes {
		return "query terms must total at most 256 bytes"
	}
	return ""
}

func normalizeDiscoveryQuery(query string) []string {
	terms := strings.Fields(query)
	for index := range terms {
		terms[index] = strings.ToLower(terms[index])
	}
	return terms
}

func containsUnicodeControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func encodeAgentModelDiscoveryCursor(cursor agentModelDiscoveryCursor) string {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return ""
	}
	cursorValue := base64.RawURLEncoding.EncodeToString(encoded)
	if len(cursorValue) > maxAgentModelDiscoveryCursorBytes {
		return ""
	}
	return cursorValue
}

func decodeAgentModelDiscoveryCursor(encoded string) (agentModelDiscoveryCursor, bool) {
	if encoded == "" || len(encoded) > maxAgentModelDiscoveryCursorBytes {
		return agentModelDiscoveryCursor{}, false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return agentModelDiscoveryCursor{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var cursor agentModelDiscoveryCursor
	if err := decoder.Decode(&cursor); err != nil {
		return agentModelDiscoveryCursor{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return agentModelDiscoveryCursor{}, false
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, decoded) || !validRestoredDiscoveryCursor(cursor) {
		return agentModelDiscoveryCursor{}, false
	}
	return cursor, true
}

func validRestoredDiscoveryCursor(cursor agentModelDiscoveryCursor) bool {
	if cursor.Version != agentModelDiscoveryCursorVersion || cursor.Limit < 1 || cursor.Limit > maxAgentModelDiscoveryLimit ||
		cursor.Offset <= 0 || len(cursor.Digest) != sha256.Size*2 {
		return false
	}
	if _, err := hex.DecodeString(cursor.Digest); err != nil || strings.ToLower(cursor.Digest) != cursor.Digest || !validRestoredDiscoveryTerms(cursor.Terms) {
		return false
	}
	return (cursor.ProviderID == "" || validDiscoveryFilter(cursor.ProviderID)) &&
		(cursor.ModelID == "" || validDiscoveryFilter(cursor.ModelID))
}

func validRestoredDiscoveryTerms(terms []string) bool {
	if len(terms) > maxAgentModelDiscoveryQueryTerms {
		return false
	}
	total := 0
	for _, term := range terms {
		if term == "" || !utf8.ValidString(term) || len(term) > maxAgentModelDiscoveryTermBytes || containsUnicodeControl(term) ||
			strings.ToLower(term) != term || len(strings.Fields(term)) != 1 || strings.Fields(term)[0] != term {
			return false
		}
		total += len(term)
	}
	return total <= maxAgentModelDiscoveryTermsBytes && strings.Join(normalizeDiscoveryQuery(strings.Join(terms, " ")), "\x00") == strings.Join(terms, "\x00")
}
