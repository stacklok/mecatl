package lint

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var adr0104Fixtures = []string{
	"provider/openai/testdata/subscription_compatibility_text.sse",
	"provider/openai/testdata/subscription_compatibility_tool_call.sse",
	"provider/openai/testdata/subscription_compatibility_tool_continuation.sse",
}

type adr0104Record struct {
	Schema        string          `json:"schema"`
	ObservedAt    string          `json:"observed_at"`
	Originator    string          `json:"originator"`
	UserAgent     string          `json:"user_agent"`
	Gate          string          `json:"gate"`
	Models        json.RawMessage `json:"models"`
	Text          json.RawMessage `json:"text_response"`
	Tool          json.RawMessage `json:"function_tool_roundtrip"`
	ErrorEvidence json.RawMessage `json:"error_evidence"`
	Fixtures      []string        `json:"fixtures"`
}

func TestADR_0104_CompatibilityEvidence(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	adr := readADR0104(t, filepath.Join(root, "docs", "adr", "0104-openai-subscription-manual-token.md"))
	for _, required := range []string{
		"- Status: Accepted", "honest mecatl originator", "manual access token", "live-only",
		"explicit selector", "plaintext", "mecak8s", "stop", "last-known-good",
	} {
		if !strings.Contains(string(adr), required) {
			t.Errorf("ADR 0104 missing compatibility clause %q", required)
		}
	}
	index := readADR0104(t, filepath.Join(root, "docs", "adr", "README.md"))
	if !strings.Contains(string(index),
		"[0104 — OpenAI subscription with a manual access token](./0104-openai-subscription-manual-token.md)") {
		t.Error("ADR 0104 is not indexed under Providers & APIs")
	}

	recordBytes := readADR0104(t, filepath.Join(root, "docs", "adr", "0104-openai-subscription-contract.json"))
	var record adr0104Record
	dec := json.NewDecoder(bytes.NewReader(recordBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		t.Fatalf("decode compatibility record: %v", err)
	}
	date, dateErr := time.Parse("2006-01-02", record.ObservedAt)
	if record.Schema != "mecatl-openai-subscription-compatibility/v1" ||
		record.Originator != "mecatl" || record.Gate != "passed" || dateErr != nil ||
		date.Format("2006-01-02") != record.ObservedAt || len(record.Models) == 0 ||
		len(record.Text) == 0 || len(record.Tool) == 0 || len(record.ErrorEvidence) == 0 {
		t.Fatal("compatibility record has an invalid fixed contract field")
	}
	if !reflect.DeepEqual(record.Fixtures, adr0104Fixtures) {
		t.Fatalf("fixture inventory = %#v, want %#v", record.Fixtures, adr0104Fixtures)
	}
	assertADR0104Sanitized(t, "record", recordBytes)
	for _, name := range adr0104Fixtures {
		fixture := readADR0104(t, filepath.Join(root, name))
		assertADR0104Sanitized(t, name, fixture)
		scanner := bufio.NewScanner(bytes.NewReader(fixture))
		events := 0
		for scanner.Scan() {
			line := scanner.Bytes()
			if !bytes.HasPrefix(line, []byte("data: ")) {
				continue
			}
			events++
			if !json.Valid(bytes.TrimPrefix(line, []byte("data: "))) {
				t.Fatalf("fixture %s contains invalid event JSON", name)
			}
		}
		if err := scanner.Err(); err != nil || events == 0 {
			t.Fatalf("fixture %s scan failed or found no events: %v", name, err)
		}
	}
}

func readADR0104(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func assertADR0104Sanitized(t *testing.T, name string, b []byte) {
	t.Helper()
	lower := strings.ToLower(string(b))
	for _, forbidden := range []string{
		"access_token", "refresh_token", "id_token", "account_id", "authorization", "bearer ", "sk-",
	} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("%s contains forbidden secret-shaped material %q", name, forbidden)
		}
	}
}
