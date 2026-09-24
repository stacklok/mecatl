package openai

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func pdfInputFixture(t *testing.T) session.Content {
	t.Helper()
	data, err := os.ReadFile("testdata/pdf_input.pdf")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	part, err := session.NewPDFContent("artifact_1", "report.pdf", int64(len(data)), hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	// The session-bound wrapper hydrates a temporary provider request. The
	// durable message retains this part without Data.
	part.Data = data
	return part
}

func TestPDFInputResponsesFixture(t *testing.T) {
	pdf := pdfInputFixture(t)
	msgs := []session.Message{session.NewUserMessageWithParts("Read the report", []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		pdf,
	})}
	items, err := buildInput(msgs, port.ProviderCapabilities{Image: true, PDF: true}, -1)
	if err != nil {
		t.Fatalf("buildInput: %v", err)
	}
	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	var wire []struct {
		Role    string           `json:"role"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire) != 1 || wire[0].Role != "user" || len(wire[0].Content) != 3 {
		t.Fatalf("unexpected user content: %s", raw)
	}
	if wire[0].Content[0]["type"] != "input_text" || wire[0].Content[0]["text"] != "Read the report" {
		t.Fatalf("text part changed: %s", raw)
	}
	if wire[0].Content[1]["type"] != "input_image" {
		t.Fatalf("image part changed: %s", raw)
	}
	file := wire[0].Content[2]
	wantData := "data:application/pdf;base64," + base64.StdEncoding.EncodeToString(pdf.Data)
	if file["type"] != "input_file" || file["filename"] != pdf.Name || file["file_data"] != wantData {
		t.Fatalf("PDF input file = %v, want fixture bytes and safe name", file)
	}
	if strings.Contains(string(raw), pdf.ArtifactID) {
		t.Fatalf("private artifact ID leaked into provider request: %s", raw)
	}
}

func TestPDFInputResponsesRequiresNativeOptIn(t *testing.T) {
	p := New(WithProviderCapabilities(port.ProviderCapabilities{PDF: true}))
	if p.Capabilities().PDF {
		t.Fatal("an OpenAI-compatible endpoint must not advertise native PDF input by default")
	}
	_, err := p.buildParams(port.LLMRequest{Model: "gpt-5", Messages: []session.Message{
		session.NewUserMessageWithParts("read", []session.Content{pdfInputFixture(t)}),
	}})
	if err == nil {
		t.Fatal("an OpenAI-compatible endpoint accepted PDF input without native opt-in")
	}
}

func TestPDFInputResponsesRequiresSelectedModelBit(t *testing.T) {
	request := port.LLMRequest{Model: "gpt-5", Messages: []session.Message{
		session.NewUserMessageWithParts("read", []session.Content{pdfInputFixture(t)}),
	}}
	for _, tc := range []struct {
		name string
		p    *Provider
		want bool
	}{
		{"selected model supports PDF", New(WithNativePDFInput(), WithProviderCapabilities(port.ProviderCapabilities{PDF: true})), true},
		{"selected model lacks PDF", New(WithNativePDFInput(), WithProviderCapabilities(port.ProviderCapabilities{})), false},
		{"selected model unknown", New(WithNativePDFInput()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.p.Capabilities().PDF; !got {
				t.Fatal("native Responses adapter did not advertise PDF support")
			}
			_, err := tc.p.buildParams(request)
			if (err == nil) != tc.want {
				t.Fatalf("PDF translation succeeded = %t, want %t; err = %v", err == nil, tc.want, err)
			}
		})
	}
}

func TestPDFInputResponsesRejectsUnresolvedOrAlteredBytes(t *testing.T) {
	base := pdfInputFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*session.Content)
	}{
		{"reference only", func(p *session.Content) { p.Data = nil }},
		{"wrong digest", func(p *session.Content) { p.Data[0] = 'X' }},
		{"remote URL", func(p *session.Content) { p.URL = "https://example.com/report.pdf" }},
		{"missing artifact ID", func(p *session.Content) { p.ArtifactID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := base
			part.Data = append([]byte(nil), base.Data...)
			tc.edit(&part)
			_, err := buildInput([]session.Message{session.NewUserMessageWithParts("read", []session.Content{part})}, port.ProviderCapabilities{PDF: true}, -1)
			if err == nil {
				t.Fatal("unsafe or unresolved PDF reached the provider request")
			}
		})
	}
}
