// Command manualprobe performs ADR 0101's explicit one-shot live
// compatibility check. Automated tests exercise its parsers with local servers;
// only an operator running this command reads ~/.codex/auth.json or uses network.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	baseURL   = "https://chatgpt.com/backend-api/codex"
	userAgent = "mecatl-manual-compatibility-probe/1.0"
	maxBody   = 4 << 20
	typeKey   = "type"

	liveProvenance  = "live_observation"
	errorProvenance = "offline_status_mapping"
)

var fixtureInventory = []string{
	"provider/openai/testdata/subscription_compatibility_text.sse",
	"provider/openai/testdata/subscription_compatibility_tool_call.sse",
	"provider/openai/testdata/subscription_compatibility_tool_continuation.sse",
}

type authFile struct {
	Tokens struct {
		AccessToken string `json:"access_token"`
		AccountID   string `json:"account_id"`
	} `json:"tokens"`
}

type contract struct {
	Schema        string           `json:"schema"`
	ObservedAt    string           `json:"observed_at"`
	Originator    string           `json:"originator"`
	UserAgent     string           `json:"user_agent"`
	Gate          string           `json:"gate"`
	Failure       string           `json:"failure_category,omitempty"`
	Models        modelsContract   `json:"models"`
	Text          responseContract `json:"text_response"`
	Tool          toolContract     `json:"function_tool_roundtrip"`
	ErrorEvidence errorEvidence    `json:"error_evidence"`
	Fixtures      []string         `json:"fixtures"`
}

type modelsContract struct {
	Provenance     string `json:"provenance"`
	StatusClass    string `json:"status_class"`
	NonEmpty       bool   `json:"non_empty"`
	EnvelopeValid  bool   `json:"envelope_valid"`
	HasSlug        bool   `json:"has_slug"`
	HasDisplayName bool   `json:"has_display_name"`
	HasVisibility  bool   `json:"has_visibility"`
}

type responseContract struct {
	Provenance         string `json:"provenance"`
	StatusClass        string `json:"status_class"`
	Completed          bool   `json:"completed"`
	HasPhase           bool   `json:"has_phase"`
	HasReplayItemID    bool   `json:"has_replay_item_identifier"`
	HasUsage           bool   `json:"has_usage"`
	OutputTextObserved bool   `json:"output_text_observed"`
	EventTopologyValid bool   `json:"event_topology_valid"`
	ObservedEventCount int    `json:"observed_event_count"`
}

type toolContract struct {
	Initial                   responseContract `json:"initial"`
	Continuation              responseContract `json:"continuation"`
	FunctionCallObserved      bool             `json:"function_call_observed"`
	ExactlyOneFunctionCall    bool             `json:"exactly_one_function_call"`
	FunctionNameValid         bool             `json:"function_name_valid"`
	FunctionArgumentsValid    bool             `json:"function_arguments_valid"`
	CallIdentifierPresent     bool             `json:"call_identifier_present"`
	ItemIdentifierPresent     bool             `json:"item_identifier_present"`
	FunctionOutputReplayed    bool             `json:"function_output_replayed"`
	ContinuationMatched       bool             `json:"continuation_matched"`
	Completed                 bool             `json:"completed"`
	NoUnknownActionableEvents bool             `json:"no_unknown_actionable_events"`
}

type errorEvidence struct {
	Provenance     string `json:"provenance"`
	ModelsAuth     bool   `json:"models_401_403_classified"`
	ModelsQuota    bool   `json:"models_429_classified"`
	ResponsesAuth  bool   `json:"responses_401_403_classified"`
	ResponsesQuota bool   `json:"responses_429_classified"`
}

type functionCall struct {
	ItemID    string
	CallID    string
	Name      string
	Arguments string
}

type wireResult struct {
	contract      responseContract
	items         []map[string]any
	functionCalls []functionCall
	textSeen      bool
}

type probeError struct{ category string }

func (e probeError) Error() string { return e.category }

func errCategory(err error) string {
	var pe probeError
	if errors.As(err, &pe) {
		return pe.category
	}
	if err != nil {
		return "local_probe_failure"
	}
	return ""
}

func main() {
	record := contract{
		Schema:     "mecatl-openai-subscription-compatibility/v1",
		ObservedAt: time.Now().UTC().Format("2006-01-02"),
		Originator: "mecatl",
		UserAgent:  userAgent,
		Gate:       "pending",
		Fixtures:   append([]string(nil), fixtureInventory...),
	}
	auth, err := readAuth()
	if err == nil {
		err = run(context.Background(), auth, &record)
	}
	if err != nil {
		record.Failure = errCategory(err)
	} else {
		record.Gate = "passed"
		if err = writeFixtures("."); err == nil {
			err = validatePassingContract(record)
		}
	}
	if err != nil && record.Failure == "" {
		record.Gate = "pending"
		record.Failure = errCategory(err)
	}
	if err := writeContract(filepath.Join("docs", "adr", "0101-openai-subscription-contract.json"), record); err != nil {
		fmt.Fprintln(os.Stderr, "probe failed: could not write sanitized record")
		os.Exit(1)
	}
	if record.Gate != "passed" {
		fmt.Fprintf(os.Stderr, "compatibility gate did not pass (%s)\n", record.Failure)
		os.Exit(1)
	}
	fmt.Println("compatibility gate passed; sanitized structural record and fixtures written")
}

func readAuth() (authFile, error) {
	var a authFile
	home, err := os.UserHomeDir()
	if err != nil {
		return a, probeError{"credential_file_unavailable"}
	}
	b, err := os.ReadFile(filepath.Join(home, ".codex", "auth.json"))
	if err != nil || json.Unmarshal(b, &a) != nil {
		return a, probeError{"credential_file_unavailable"}
	}
	if a.Tokens.AccessToken == "" || a.Tokens.AccountID == "" {
		return a, probeError{"credential_material_missing"}
	}
	return a, nil
}

func newProbeClient() *http.Client {
	return &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func run(parent context.Context, auth authFile, record *contract) error {
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	client := newProbeClient()

	models, model, err := probeModels(ctx, client, baseURL, auth)
	record.Models = models
	if err != nil {
		return err
	}

	text, err := postResponses(ctx, client, baseURL, auth, map[string]any{
		"model": model, "instructions": "Complete the compatibility check.",
		"input":   userInput("Reply with the single word OK."),
		"include": []string{"reasoning.encrypted_content"}, "stream": true, "store": false,
	})
	record.Text = text.contract
	if err != nil {
		return err
	}
	if !text.contract.Completed || !text.textSeen || !text.contract.HasPhase || !text.contract.HasUsage {
		return probeError{"text_shape_incomplete"}
	}

	toolReq := map[string]any{
		"model": model, "instructions": "Perform the requested compatibility check.",
		"input": userInput("Call compatibility_probe exactly once with value OK."),
		"tools": []any{map[string]any{
			typeKey: "function", "name": "compatibility_probe",
			"description": "Returns a fixed compatibility result.",
			"parameters": map[string]any{
				typeKey: "object", "properties": map[string]any{"value": map[string]any{typeKey: "string"}},
				"required": []string{"value"}, "additionalProperties": false,
			},
		}},
		"include": []string{"reasoning.encrypted_content"}, "stream": true, "store": false,
	}
	first, err := postResponses(ctx, client, baseURL, auth, toolReq)
	record.Tool.Initial = first.contract
	if err != nil {
		return err
	}
	if err := validateFunctionCalls(first.functionCalls); err != nil {
		return err
	}
	call := first.functionCalls[0]
	record.Tool.FunctionCallObserved = true
	record.Tool.ExactlyOneFunctionCall = true
	record.Tool.FunctionNameValid = true
	record.Tool.FunctionArgumentsValid = true
	record.Tool.CallIdentifierPresent = true
	record.Tool.ItemIdentifierPresent = true

	input := make([]any, 0, len(first.items)+1)
	for _, item := range first.items {
		input = append(input, item)
	}
	input = append(input, map[string]any{
		typeKey: "function_call_output", "call_id": call.CallID, "output": "OK",
	})
	second, err := postResponses(ctx, client, baseURL, auth, map[string]any{
		"model": model, "instructions": "Perform the requested compatibility check.",
		"input": input, "tools": toolReq["tools"],
		"include": []string{"reasoning.encrypted_content"}, "stream": true, "store": false,
	})
	record.Tool.Continuation = second.contract
	if err != nil {
		return err
	}
	if len(second.functionCalls) != 0 || !second.contract.Completed || !second.textSeen ||
		!second.contract.HasPhase || !second.contract.HasUsage {
		return probeError{"function_continuation_incomplete"}
	}
	record.Tool.FunctionOutputReplayed = true
	record.Tool.ContinuationMatched = true
	record.Tool.Completed = true
	record.Tool.NoUnknownActionableEvents = true
	record.ErrorEvidence = classificationEvidence()
	return nil
}

func classificationEvidence() errorEvidence {
	return errorEvidence{
		Provenance: errorProvenance,
		ModelsAuth: statusFailure("models", 401) == "models_authentication_or_originator_rejected" &&
			statusFailure("models", 403) == "models_authentication_or_originator_rejected",
		ModelsQuota: statusFailure("models", 429) == "models_quota_or_rate_limited",
		ResponsesAuth: statusFailure("responses", 401) == "responses_authentication_or_originator_rejected" &&
			statusFailure("responses", 403) == "responses_authentication_or_originator_rejected",
		ResponsesQuota: statusFailure("responses", 429) == "responses_quota_or_rate_limited",
	}
}

type modelsEnvelope struct {
	Models []modelWire `json:"models"`
}

// modelWire intentionally enumerates the observed v1 vocabulary. Except for
// the three shape fields the values stay opaque and are never persisted.
type modelWire struct {
	Slug                              string          `json:"slug"`
	DisplayName                       string          `json:"display_name"`
	Visibility                        string          `json:"visibility"`
	AdditionalSpeedTiers              json.RawMessage `json:"additional_speed_tiers"`
	ApplyPatchToolType                json.RawMessage `json:"apply_patch_tool_type"`
	AutoCompactTokenLimit             json.RawMessage `json:"auto_compact_token_limit"`
	AutoReviewModelOverride           json.RawMessage `json:"auto_review_model_override"`
	AvailabilityNux                   json.RawMessage `json:"availability_nux"`
	AvailableInPlans                  json.RawMessage `json:"available_in_plans"`
	BaseInstructions                  json.RawMessage `json:"base_instructions"`
	CompHash                          json.RawMessage `json:"comp_hash"`
	ContextWindow                     json.RawMessage `json:"context_window"`
	DefaultReasoningLevel             json.RawMessage `json:"default_reasoning_level"`
	DefaultReasoningSummary           json.RawMessage `json:"default_reasoning_summary"`
	DefaultServiceTier                json.RawMessage `json:"default_service_tier"`
	DefaultVerbosity                  json.RawMessage `json:"default_verbosity"`
	Description                       json.RawMessage `json:"description"`
	ExperimentalSupportedTools        json.RawMessage `json:"experimental_supported_tools"`
	IncludePluginUsageInstructions    json.RawMessage `json:"include_plugin_usage_instructions"`
	IncludeSkillsUsageInstructions    json.RawMessage `json:"include_skills_usage_instructions"`
	InputModalities                   json.RawMessage `json:"input_modalities"`
	MaxContextWindow                  json.RawMessage `json:"max_context_window"`
	MinimalClientVersion              json.RawMessage `json:"minimal_client_version"`
	ModelMessages                     json.RawMessage `json:"model_messages"`
	ModelSpecialty                    json.RawMessage `json:"model_specialty"`
	MultiAgentVersion                 json.RawMessage `json:"multi_agent_version"`
	PreferWebsockets                  json.RawMessage `json:"prefer_websockets"`
	Priority                          json.RawMessage `json:"priority"`
	ReasoningSummaryFormat            json.RawMessage `json:"reasoning_summary_format"`
	ServiceTiers                      json.RawMessage `json:"service_tiers"`
	ShellType                         json.RawMessage `json:"shell_type"`
	SupportVerbosity                  json.RawMessage `json:"support_verbosity"`
	SupportedInAPI                    json.RawMessage `json:"supported_in_api"`
	SupportedReasoningLevels          json.RawMessage `json:"supported_reasoning_levels"`
	SupportsImageDetailOriginal       json.RawMessage `json:"supports_image_detail_original"`
	SupportsParallelToolCalls         json.RawMessage `json:"supports_parallel_tool_calls"`
	SupportsReasoningSummaries        json.RawMessage `json:"supports_reasoning_summaries"`
	SupportsReasoningSummaryParameter json.RawMessage `json:"supports_reasoning_summary_parameter"`
	SupportsSearchTool                json.RawMessage `json:"supports_search_tool"`
	ToolMode                          json.RawMessage `json:"tool_mode"`
	TruncationPolicy                  json.RawMessage `json:"truncation_policy"`
	Upgrade                           json.RawMessage `json:"upgrade"`
	UseResponsesLite                  json.RawMessage `json:"use_responses_lite"`
	WebSearchToolType                 json.RawMessage `json:"web_search_tool_type"`
}

func probeModels(ctx context.Context, client *http.Client, endpoint string, auth authFile) (modelsContract, string, error) {
	c := modelsContract{Provenance: liveProvenance}
	u, err := url.Parse(endpoint + "/models")
	if err != nil {
		return c, "", probeError{"models_invalid_endpoint"}
	}
	q := u.Query()
	q.Set("client_version", "1.0.0")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return c, "", probeError{"models_invalid_endpoint"}
	}
	setHeaders(req, auth)
	resp, err := client.Do(req)
	if err != nil {
		return c, "", probeError{"models_transport_error"}
	}
	defer func() { _ = resp.Body.Close() }()
	c.StatusClass = statusClass(resp.StatusCode)
	if resp.StatusCode/100 != 2 {
		return c, "", probeError{statusFailure("models", resp.StatusCode)}
	}
	var envelope modelsEnvelope
	if err := decodeStrictLimited(resp.Body, &envelope); err != nil {
		return c, "", probeError{"models_invalid_or_unknown_shape"}
	}
	c.EnvelopeValid = true
	if len(envelope.Models) == 0 {
		return c, "", probeError{"models_empty"}
	}
	c.NonEmpty = true
	for _, m := range envelope.Models {
		if m.Slug == "" || m.DisplayName == "" || m.Visibility == "" {
			return c, "", probeError{"models_required_shape_missing"}
		}
		c.HasSlug, c.HasDisplayName, c.HasVisibility = true, true, true
		if m.Visibility != "hide" {
			return c, m.Slug, nil
		}
	}
	return c, "", probeError{"models_no_selectable_model"}
}

func postResponses(ctx context.Context, client *http.Client, endpoint string, auth authFile, payload map[string]any) (wireResult, error) {
	var out wireResult
	out.contract.Provenance = liveProvenance
	b, err := json.Marshal(payload)
	if err != nil {
		return out, probeError{"local_request_encoding"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/responses", bytes.NewReader(b))
	if err != nil {
		return out, probeError{"responses_invalid_endpoint"}
	}
	setHeaders(req, auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return out, probeError{"responses_transport_error"}
	}
	defer func() { _ = resp.Body.Close() }()
	out.contract.StatusClass = statusClass(resp.StatusCode)
	if resp.StatusCode/100 != 2 {
		return out, probeError{statusFailure("responses", resp.StatusCode)}
	}
	if err := consumeSSE(resp.Body, &out); err != nil {
		return out, err
	}
	return out, nil
}

var allowedEventKeys = map[string]map[string]bool{
	"response.created":                       keySet(typeKey, "sequence_number", "response"),
	"response.in_progress":                   keySet(typeKey, "sequence_number", "response"),
	"response.completed":                     keySet(typeKey, "sequence_number", "response"),
	"response.output_item.added":             keySet(typeKey, "sequence_number", "output_index", "item"),
	"response.output_item.done":              keySet(typeKey, "sequence_number", "output_index", "item"),
	"response.content_part.added":            keySet(typeKey, "sequence_number", "item_id", "output_index", "content_index", "part"),
	"response.content_part.done":             keySet(typeKey, "sequence_number", "item_id", "output_index", "content_index", "part"),
	"response.output_text.delta":             keySet(typeKey, "sequence_number", "item_id", "output_index", "content_index", "delta", "logprobs", "obfuscation"),
	"response.output_text.done":              keySet(typeKey, "sequence_number", "item_id", "output_index", "content_index", "text", "logprobs"),
	"response.function_call_arguments.delta": keySet(typeKey, "sequence_number", "item_id", "output_index", "delta", "obfuscation"),
	"response.function_call_arguments.done":  keySet(typeKey, "sequence_number", "item_id", "output_index", "arguments"),
	"response.reasoning_summary_part.added":  keySet(typeKey, "sequence_number", "item_id", "output_index", "summary_index", "part"),
	"response.reasoning_summary_part.done":   keySet(typeKey, "sequence_number", "item_id", "output_index", "summary_index", "part"),
	"response.reasoning_summary_text.delta":  keySet(typeKey, "sequence_number", "item_id", "output_index", "summary_index", "delta", "obfuscation"),
	"response.reasoning_summary_text.done":   keySet(typeKey, "sequence_number", "item_id", "output_index", "summary_index", "text"),
	"response.reasoning_text.delta":          keySet(typeKey, "sequence_number", "item_id", "output_index", "content_index", "delta", "obfuscation"),
	"response.reasoning_text.done":           keySet(typeKey, "sequence_number", "item_id", "output_index", "content_index", "text"),
}

func keySet(keys ...string) map[string]bool {
	m := make(map[string]bool, len(keys))
	for _, key := range keys {
		m[key] = true
	}
	return m
}

func consumeSSE(r io.Reader, out *wireResult) error {
	b, err := readCapped(r)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	scanner.Buffer(make([]byte, 64<<10), maxBody)
	eventCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event map[string]json.RawMessage
		if err := decodeStrictBytes([]byte(data), &event); err != nil {
			return probeError{"responses_invalid_event_json"}
		}
		var typ string
		if err := json.Unmarshal(event[typeKey], &typ); err != nil || typ == "" {
			return probeError{"responses_event_missing_type"}
		}
		allowed, ok := allowedEventKeys[typ]
		if !ok {
			return probeError{"responses_unknown_event_type"}
		}
		for key := range event {
			if !allowed[key] {
				return probeError{"responses_unknown_event_field"}
			}
		}
		eventCount++
		switch typ {
		case "response.output_item.done":
			if err := consumeOutputItem(event["item"], out); err != nil {
				return err
			}
		case "response.completed":
			if err := consumeCompleted(event["response"], out); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return probeError{"responses_stream_read_error"}
	}
	out.contract.EventTopologyValid = true
	out.contract.ObservedEventCount = eventCount
	out.contract.OutputTextObserved = out.textSeen
	if !out.contract.Completed || !out.contract.HasUsage || eventCount == 0 {
		return probeError{"responses_completion_shape_incomplete"}
	}
	return nil
}

func consumeOutputItem(raw json.RawMessage, out *wireResult) error {
	var item map[string]json.RawMessage
	if err := decodeStrictBytes(raw, &item); err != nil {
		return probeError{"responses_invalid_item"}
	}
	var typ string
	if json.Unmarshal(item[typeKey], &typ) != nil {
		return probeError{"responses_item_missing_type"}
	}
	allowed := map[string]map[string]bool{
		"message":       keySet("id", typeKey, "status", "role", "content", "phase"),
		"function_call": keySet("id", typeKey, "status", "name", "arguments", "call_id"),
		"reasoning":     keySet("id", typeKey, "status", "summary", "encrypted_content", "content"),
	}[typ]
	if allowed == nil {
		return probeError{"responses_unknown_item_type"}
	}
	for key := range item {
		if !allowed[key] {
			return probeError{"responses_unknown_item_field"}
		}
	}
	var id string
	_ = json.Unmarshal(item["id"], &id)
	if id != "" {
		out.contract.HasReplayItemID = true
	}
	var replay map[string]any
	if err := json.Unmarshal(raw, &replay); err != nil {
		return probeError{"responses_invalid_item"}
	}
	out.items = append(out.items, replay)
	switch typ {
	case "message":
		var phase string
		_ = json.Unmarshal(item["phase"], &phase)
		if phase != "commentary" && phase != "final_answer" {
			return probeError{"responses_unknown_phase"}
		}
		out.contract.HasPhase = true
		var content []map[string]json.RawMessage
		if err := json.Unmarshal(item["content"], &content); err != nil {
			return probeError{"responses_invalid_message_content"}
		}
		for _, part := range content {
			var partType string
			_ = json.Unmarshal(part[typeKey], &partType)
			if partType != "output_text" {
				return probeError{"responses_unknown_content_type"}
			}
			for key := range part {
				if !keySet(typeKey, "text", "annotations", "logprobs")[key] {
					return probeError{"responses_unknown_content_field"}
				}
			}
			out.textSeen = true
		}
	case "function_call":
		var call struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			CallID    string `json:"call_id"`
		}
		if err := json.Unmarshal(raw, &call); err != nil {
			return probeError{"function_call_shape_incomplete"}
		}
		out.functionCalls = append(out.functionCalls, functionCall{
			ItemID: id, CallID: call.CallID, Name: call.Name, Arguments: call.Arguments,
		})
	}
	return nil
}

func consumeCompleted(raw json.RawMessage, out *wireResult) error {
	var response map[string]json.RawMessage
	if err := json.Unmarshal(raw, &response); err != nil {
		return probeError{"responses_invalid_completion"}
	}
	var usage map[string]json.RawMessage
	if err := json.Unmarshal(response["usage"], &usage); err != nil {
		return probeError{"responses_usage_missing"}
	}
	expected := keySet("input_tokens", "input_tokens_details", "output_tokens", "output_tokens_details", "total_tokens")
	if len(usage) != len(expected) {
		return probeError{"responses_unknown_or_missing_usage_key"}
	}
	for key := range usage {
		if !expected[key] {
			return probeError{"responses_unknown_or_missing_usage_key"}
		}
	}
	out.contract.Completed = true
	out.contract.HasUsage = true
	return nil
}

func validateFunctionCalls(calls []functionCall) error {
	if len(calls) != 1 {
		return probeError{"function_call_count_invalid"}
	}
	call := calls[0]
	if call.ItemID == "" || call.CallID == "" {
		return probeError{"function_call_identifiers_missing"}
	}
	if call.Name != "compatibility_probe" {
		return probeError{"function_call_name_invalid"}
	}
	var args struct {
		Value string `json:"value"`
	}
	if err := decodeStrictLimited(strings.NewReader(call.Arguments), &args); err != nil || args.Value != "OK" {
		return probeError{"function_call_arguments_invalid"}
	}
	return nil
}

func userInput(text string) []any {
	return []any{map[string]any{typeKey: "message", "role": "user", "content": text}}
}

func setHeaders(req *http.Request, auth authFile) {
	req.Header.Set("Authorization", "Bearer "+auth.Tokens.AccessToken)
	req.Header.Set("ChatGPT-Account-ID", auth.Tokens.AccountID)
	req.Header.Set("originator", "mecatl")
	req.Header.Set("User-Agent", userAgent)
}

func readCapped(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, maxBody+1))
	if err != nil {
		return nil, probeError{"body_read_error"}
	}
	if len(b) > maxBody {
		return nil, probeError{"responses_body_too_large"}
	}
	return b, nil
}

func decodeLimited(r io.Reader, dst any) error {
	return decodeWithMode(r, dst, false)
}

func decodeStrictLimited(r io.Reader, dst any) error {
	return decodeWithMode(r, dst, true)
}

func decodeWithMode(r io.Reader, dst any, strict bool) error {
	b, err := readCapped(r)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func decodeStrictBytes(b []byte, dst any) error {
	return decodeStrictLimited(bytes.NewReader(b), dst)
}

func writeContract(path string, record contract) error {
	b, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func statusFailure(endpoint string, code int) string {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return endpoint + "_authentication_or_originator_rejected"
	case http.StatusTooManyRequests:
		return endpoint + "_quota_or_rate_limited"
	default:
		return endpoint + "_http_" + statusClass(code)
	}
}

func statusClass(code int) string { return fmt.Sprintf("%dxx", code/100) }

func validatePassingContract(c contract) error {
	if !validContractIdentity(c) || !validModels(c.Models) || !validResponses(c) ||
		!validTool(c.Tool) || !validErrorEvidence(c.ErrorEvidence) ||
		!sameStrings(c.Fixtures, fixtureInventory) {
		return probeError{"sanitized_contract_incomplete"}
	}
	return nil
}

func validContractIdentity(c contract) bool {
	return c.Schema == "mecatl-openai-subscription-compatibility/v1" &&
		c.Originator == "mecatl" && c.UserAgent == userAgent &&
		c.Gate == "passed" && c.Failure == ""
}

func validModels(c modelsContract) bool {
	return c.Provenance == liveProvenance && c.StatusClass == "2xx" &&
		c.NonEmpty && c.EnvelopeValid && c.HasSlug && c.HasDisplayName && c.HasVisibility
}

func validResponses(c contract) bool {
	return validResponse(c.Text, true) && validResponse(c.Tool.Initial, false) &&
		validResponse(c.Tool.Continuation, true)
}

func validTool(c toolContract) bool {
	return c.FunctionCallObserved && c.ExactlyOneFunctionCall && c.FunctionNameValid &&
		c.FunctionArgumentsValid && c.CallIdentifierPresent && c.ItemIdentifierPresent &&
		c.FunctionOutputReplayed && c.ContinuationMatched && c.Completed &&
		c.NoUnknownActionableEvents
}

func validErrorEvidence(c errorEvidence) bool {
	return c.Provenance == errorProvenance && c.ModelsAuth && c.ModelsQuota &&
		c.ResponsesAuth && c.ResponsesQuota
}

func validResponse(c responseContract, wantText bool) bool {
	return c.Provenance == liveProvenance && c.StatusClass == "2xx" && c.Completed &&
		c.HasReplayItemID && c.HasUsage && c.EventTopologyValid && c.ObservedEventCount > 0 &&
		c.OutputTextObserved == wantText && (!wantText || c.HasPhase)
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var fixtureBodies = map[string]string{
	fixtureInventory[0]: "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"response_synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"item_id\":\"message_synthetic\",\"output_index\":0,\"content_index\":0,\"delta\":\"TEXT_SYNTHETIC\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"id\":\"message_synthetic\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"TEXT_SYNTHETIC\",\"annotations\":[]}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"response_synthetic\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"input_tokens_details\":{},\"output_tokens\":1,\"output_tokens_details\":{},\"total_tokens\":2}}}\n\n" +
		": sanitized fixture terminator\n",
	fixtureInventory[1]: "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"response_synthetic\",\"status\":\"in_progress\",\"output\":[]}}\n\n" +
		"event: response.function_call_arguments.delta\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"sequence_number\":1,\"item_id\":\"function_synthetic\",\"output_index\":0,\"delta\":\"{\\\"value\\\":\\\"OK\\\"}\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"output_index\":0,\"item\":{\"id\":\"function_synthetic\",\"type\":\"function_call\",\"status\":\"completed\",\"name\":\"compatibility_probe\",\"arguments\":\"{\\\"value\\\":\\\"OK\\\"}\",\"call_id\":\"call_synthetic\"}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":3,\"response\":{\"id\":\"response_synthetic\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"input_tokens_details\":{},\"output_tokens\":1,\"output_tokens_details\":{},\"total_tokens\":2}}}\n\n" +
		": sanitized fixture terminator\n",
	fixtureInventory[2]: "event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"sequence_number\":0,\"item_id\":\"message_synthetic\",\"output_index\":0,\"content_index\":0,\"delta\":\"TOOL_RESULT_SYNTHETIC\"}\n\n" +
		"event: response.output_item.done\n" +
		"data: {\"type\":\"response.output_item.done\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"message_synthetic\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"phase\":\"final_answer\",\"content\":[{\"type\":\"output_text\",\"text\":\"TOOL_RESULT_SYNTHETIC\",\"annotations\":[]}]}}\n\n" +
		"event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"response_synthetic\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"input_tokens_details\":{},\"output_tokens\":1,\"output_tokens_details\":{},\"total_tokens\":2}}}\n\n" +
		": sanitized fixture terminator\n",
}

func writeFixtures(root string) error {
	for _, name := range fixtureInventory {
		body, ok := fixtureBodies[name]
		if !ok {
			return probeError{"fixture_inventory_invalid"}
		}
		var parsed wireResult
		if err := consumeSSE(strings.NewReader(body), &parsed); err != nil {
			return probeError{"fixture_validation_failed"}
		}
		if strings.Contains(name, "tool_call") {
			if err := validateFunctionCalls(parsed.functionCalls); err != nil {
				return probeError{"fixture_validation_failed"}
			}
		}
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			return probeError{"fixture_write_failed"}
		}
	}
	return nil
}
