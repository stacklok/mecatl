package lint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var adr0101Fixtures = []string{
	"provider/openai/testdata/subscription_compatibility_text.sse",
	"provider/openai/testdata/subscription_compatibility_tool_call.sse",
	"provider/openai/testdata/subscription_compatibility_tool_continuation.sse",
}

type adr0101Record struct {
	Schema        string               `json:"schema"`
	ObservedAt    string               `json:"observed_at"`
	Originator    string               `json:"originator"`
	UserAgent     string               `json:"user_agent"`
	Gate          string               `json:"gate"`
	Models        adr0101Models        `json:"models"`
	Text          adr0101Response      `json:"text_response"`
	Tool          adr0101Tool          `json:"function_tool_roundtrip"`
	ErrorEvidence adr0101ErrorEvidence `json:"error_evidence"`
	Fixtures      []string             `json:"fixtures"`
}

type adr0101Models struct {
	Provenance     string `json:"provenance"`
	StatusClass    string `json:"status_class"`
	NonEmpty       bool   `json:"non_empty"`
	EnvelopeValid  bool   `json:"envelope_valid"`
	HasSlug        bool   `json:"has_slug"`
	HasDisplayName bool   `json:"has_display_name"`
	HasVisibility  bool   `json:"has_visibility"`
}

type adr0101Response struct {
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

type adr0101Tool struct {
	Initial                   adr0101Response `json:"initial"`
	Continuation              adr0101Response `json:"continuation"`
	FunctionCallObserved      bool            `json:"function_call_observed"`
	ExactlyOneFunctionCall    bool            `json:"exactly_one_function_call"`
	FunctionNameValid         bool            `json:"function_name_valid"`
	FunctionArgumentsValid    bool            `json:"function_arguments_valid"`
	CallIdentifierPresent     bool            `json:"call_identifier_present"`
	ItemIdentifierPresent     bool            `json:"item_identifier_present"`
	FunctionOutputReplayed    bool            `json:"function_output_replayed"`
	ContinuationMatched       bool            `json:"continuation_matched"`
	Completed                 bool            `json:"completed"`
	NoUnknownActionableEvents bool            `json:"no_unknown_actionable_events"`
}

type adr0101ErrorEvidence struct {
	Provenance     string `json:"provenance"`
	ModelsAuth     bool   `json:"models_401_403_classified"`
	ModelsQuota    bool   `json:"models_429_classified"`
	ResponsesAuth  bool   `json:"responses_401_403_classified"`
	ResponsesQuota bool   `json:"responses_429_classified"`
}

func TestADR_0101_CompatibilityContract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	adrBytes, err := os.ReadFile(filepath.Join(root, "docs", "adr", "0101-openai-subscription-manual-token.md"))
	if err != nil {
		t.Fatalf("read ADR 0101: %v", err)
	}
	adr := string(adrBytes)
	for _, required := range []string{
		"- Status: Accepted", "honest mecatl originator", "manual access token", "live-only",
		"explicit selector", "plaintext", "mecak8s", "stop", "last-known-good",
		"subscription_compatibility_text.sse", "subscription_compatibility_tool_call.sse",
		"subscription_compatibility_tool_continuation.sse",
	} {
		if !strings.Contains(adr, required) {
			t.Errorf("ADR 0101 missing compatibility-contract clause %q", required)
		}
	}
	indexBytes, err := os.ReadFile(filepath.Join(root, "docs", "adr", "README.md"))
	if err != nil {
		t.Fatalf("read ADR index: %v", err)
	}
	if !strings.Contains(string(indexBytes),
		"[0101 — OpenAI subscription with a manual access token](./0101-openai-subscription-manual-token.md)") {
		t.Error("ADR 0101 is not indexed under Providers & APIs")
	}

	recordBytes, err := os.ReadFile(filepath.Join(root, "docs", "adr", "0101-openai-subscription-contract.json"))
	if err != nil {
		t.Fatalf("read sanitized compatibility record: %v", err)
	}
	record, err := decodeADR0101Record(recordBytes)
	if err != nil {
		t.Fatalf("strict compatibility record validation: %v", err)
	}
	if !reflect.DeepEqual(record.Fixtures, adr0101Fixtures) {
		t.Fatalf("fixture inventory = %#v, want %#v", record.Fixtures, adr0101Fixtures)
	}
	for _, name := range adr0101Fixtures {
		assertADR0101Fixture(t, filepath.Join(root, name), name)
	}
	for _, forbidden := range []string{
		"access_token", "refresh_token", "id_token", "account_id", "request_id",
		"response_id", "user_content", "authorization", "bearer ",
	} {
		if strings.Contains(strings.ToLower(string(recordBytes)), forbidden) {
			t.Errorf("sanitized compatibility record contains forbidden material %q", forbidden)
		}
	}
}

func TestADR_0101_CompatibilityContractRejectsMutations(t *testing.T) {
	t.Parallel()
	valid := validADR0101RecordForTest()
	tests := map[string]func(*adr0101Record, map[string]any){
		"provider_string": func(r *adr0101Record, _ map[string]any) { r.Originator = "provider-originator" },
		"false_fact":      func(r *adr0101Record, _ map[string]any) { r.Models.NonEmpty = false },
		"empty_array":     func(r *adr0101Record, _ map[string]any) { r.Fixtures = []string{} },
		"invalid_date":    func(r *adr0101Record, _ map[string]any) { r.ObservedAt = "2026-8-5" },
		"unknown_key":     func(_ *adr0101Record, m map[string]any) { m["provider_field"] = "provider-value" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := valid
			r.Fixtures = append([]string(nil), valid.Fixtures...)
			b, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(b, &m); err != nil {
				t.Fatal(err)
			}
			mutate(&r, m)
			if name != "unknown_key" {
				b, err = json.Marshal(r)
			} else {
				b, err = json.Marshal(m)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeADR0101Record(b); err == nil {
				t.Fatal("mutated contract accepted")
			}
		})
	}
}

func decodeADR0101Record(b []byte) (adr0101Record, error) {
	var r adr0101Record
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return r, err
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		return r, fmt.Errorf("record must contain exactly one JSON value")
	}
	observedAt, dateErr := time.Parse("2006-01-02", r.ObservedAt)
	dateValid := dateErr == nil && observedAt.Format("2006-01-02") == r.ObservedAt
	if r.Schema != "mecatl-openai-subscription-compatibility/v1" ||
		r.Originator != "mecatl" || r.UserAgent != "mecatl-manual-compatibility-probe/1.0" ||
		r.Gate != "passed" || !dateValid || !validADR0101Models(r.Models) ||
		!validADR0101Response(r.Text, true) || !validADR0101Response(r.Tool.Initial, false) ||
		!validADR0101Response(r.Tool.Continuation, true) || !r.Tool.FunctionCallObserved ||
		!r.Tool.ExactlyOneFunctionCall || !r.Tool.FunctionNameValid || !r.Tool.FunctionArgumentsValid ||
		!r.Tool.CallIdentifierPresent || !r.Tool.ItemIdentifierPresent || !r.Tool.FunctionOutputReplayed ||
		!r.Tool.ContinuationMatched || !r.Tool.Completed || !r.Tool.NoUnknownActionableEvents ||
		r.ErrorEvidence.Provenance != "offline_status_mapping" || !r.ErrorEvidence.ModelsAuth ||
		!r.ErrorEvidence.ModelsQuota || !r.ErrorEvidence.ResponsesAuth || !r.ErrorEvidence.ResponsesQuota ||
		!reflect.DeepEqual(r.Fixtures, adr0101Fixtures) {
		return r, fmt.Errorf("record contains a false, missing, or non-vocabulary fact")
	}
	return r, nil
}

func validADR0101Models(m adr0101Models) bool {
	return m.Provenance == "live_observation" && m.StatusClass == "2xx" && m.NonEmpty &&
		m.EnvelopeValid && m.HasSlug && m.HasDisplayName && m.HasVisibility
}

func validADR0101Response(r adr0101Response, wantText bool) bool {
	return r.Provenance == "live_observation" && r.StatusClass == "2xx" && r.Completed &&
		r.HasReplayItemID && r.HasUsage && r.EventTopologyValid && r.ObservedEventCount > 0 &&
		r.OutputTextObserved == wantText && (!wantText || r.HasPhase)
}

func validADR0101RecordForTest() adr0101Record {
	text := adr0101Response{
		Provenance: "live_observation", StatusClass: "2xx", Completed: true, HasPhase: true,
		HasReplayItemID: true, HasUsage: true, OutputTextObserved: true,
		EventTopologyValid: true, ObservedEventCount: 1,
	}
	initial := text
	initial.HasPhase, initial.OutputTextObserved = false, false
	return adr0101Record{
		Schema: "mecatl-openai-subscription-compatibility/v1", ObservedAt: "2026-08-05",
		Originator: "mecatl", UserAgent: "mecatl-manual-compatibility-probe/1.0", Gate: "passed",
		Models: adr0101Models{
			Provenance: "live_observation", StatusClass: "2xx", NonEmpty: true,
			EnvelopeValid: true, HasSlug: true, HasDisplayName: true, HasVisibility: true,
		},
		Text: text,
		Tool: adr0101Tool{
			Initial: initial, Continuation: text, FunctionCallObserved: true, ExactlyOneFunctionCall: true,
			FunctionNameValid: true, FunctionArgumentsValid: true, CallIdentifierPresent: true,
			ItemIdentifierPresent: true, FunctionOutputReplayed: true, ContinuationMatched: true,
			Completed: true, NoUnknownActionableEvents: true,
		},
		ErrorEvidence: adr0101ErrorEvidence{
			Provenance: "offline_status_mapping", ModelsAuth: true, ModelsQuota: true,
			ResponsesAuth: true, ResponsesQuota: true,
		},
		Fixtures: append([]string(nil), adr0101Fixtures...),
	}
}

func assertADR0101Fixture(t *testing.T, path, name string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	allowedStrings := map[string]bool{
		"response.created": true, "response.output_text.delta": true,
		"response.output_item.done": true, "response.completed": true,
		"response.function_call_arguments.delta": true,
		"response_synthetic":                     true, "message_synthetic": true, "function_synthetic": true,
		"call_synthetic": true, "in_progress": true, "completed": true, "message": true,
		"assistant": true, "final_answer": true, "output_text": true,
		"TEXT_SYNTHETIC": true, "TOOL_RESULT_SYNTHETIC": true,
		"function_call": true, "compatibility_probe": true, `{"value":"OK"}`: true,
	}
	allowedKeys := map[string]bool{
		"type": true, "sequence_number": true, "response": true, "id": true, "status": true,
		"output": true, "item_id": true, "output_index": true, "content_index": true,
		"delta": true, "item": true, "role": true, "phase": true, "content": true,
		"text": true, "annotations": true, "usage": true, "input_tokens": true,
		"input_tokens_details": true, "output_tokens": true, "output_tokens_details": true,
		"total_tokens": true, "name": true, "arguments": true, "call_id": true,
	}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	dataLines := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataLines++
		var value any
		dec := json.NewDecoder(strings.NewReader(strings.TrimPrefix(line, "data: ")))
		if err := dec.Decode(&value); err != nil {
			t.Fatalf("fixture %s invalid JSON: %v", name, err)
		}
		assertADR0101FixtureValue(t, name, value, allowedKeys, allowedStrings)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture %s: %v", name, err)
	}
	if dataLines == 0 {
		t.Fatalf("fixture %s has no events", name)
	}
}

func assertADR0101FixtureValue(t *testing.T, name string, value any, keys, stringsAllowed map[string]bool) {
	t.Helper()
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			if !keys[key] {
				t.Fatalf("fixture %s contains unknown key %q", name, key)
			}
			assertADR0101FixtureValue(t, name, child, keys, stringsAllowed)
		}
	case []any:
		for _, child := range v {
			assertADR0101FixtureValue(t, name, child, keys, stringsAllowed)
		}
	case string:
		if !stringsAllowed[v] {
			t.Fatalf("fixture %s contains non-synthetic string", name)
		}
	case float64, bool, nil:
	default:
		t.Fatalf("fixture %s contains unsupported JSON value %T", name, value)
	}
}
