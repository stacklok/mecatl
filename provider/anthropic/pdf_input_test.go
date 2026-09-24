package anthropic

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
	part.Data = data // Transient provider request; stored history is reference-only.
	return part
}

func TestPDFInputMessagesFixture(t *testing.T) {
	pdf := pdfInputFixture(t)
	msgs := []session.Message{session.NewUserMessageWithParts("Read the report", []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		pdf,
	})}
	wire, _, err := buildMessages(msgs, port.ProviderCapabilities{Image: true, PDF: true})
	if err != nil {
		t.Fatalf("buildMessages: %v", err)
	}
	raw, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	var messages []struct {
		Role    string `json:"role"`
		Content []struct {
			Type   string `json:"type"`
			Text   string `json:"text"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &messages); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Role != "user" || len(messages[0].Content) != 3 {
		t.Fatalf("unexpected user content: %s", raw)
	}
	parts := messages[0].Content
	if parts[0].Type != "text" || parts[0].Text != "Read the report" || parts[1].Type != "image" {
		t.Fatalf("text or image part changed: %s", raw)
	}
	if parts[2].Type != "document" || parts[2].Source.Type != "base64" || parts[2].Source.MediaType != "application/pdf" || parts[2].Source.Data != base64.StdEncoding.EncodeToString(pdf.Data) {
		t.Fatalf("PDF document = %+v, want base64 fixture bytes", parts[2])
	}
	if strings.Contains(string(raw), pdf.ArtifactID) {
		t.Fatalf("private artifact ID leaked into provider request: %s", raw)
	}
}

func TestPDFInputMessagesRequiresNativeOptIn(t *testing.T) {
	p := New(WithProviderCapabilities(port.ProviderCapabilities{PDF: true}))
	if p.Capabilities().PDF {
		t.Fatal("an Anthropic-compatible endpoint must not advertise native PDF input by default")
	}
	_, err := p.buildParams(port.LLMRequest{Model: "claude-sonnet-4-6", Messages: []session.Message{
		session.NewUserMessageWithParts("read", []session.Content{pdfInputFixture(t)}),
	}})
	if err == nil {
		t.Fatal("an Anthropic-compatible endpoint accepted PDF input without native opt-in")
	}
}

func TestPDFInputMessagesRequiresSelectedModelBit(t *testing.T) {
	request := port.LLMRequest{Model: "claude-sonnet-4-6", Messages: []session.Message{
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
				t.Fatal("native Messages adapter did not advertise PDF support")
			}
			_, err := tc.p.buildParams(request)
			if (err == nil) != tc.want {
				t.Fatalf("PDF translation succeeded = %t, want %t; err = %v", err == nil, tc.want, err)
			}
		})
	}
}

func TestPDFInputMessagesRejectsUnresolvedOrAlteredBytes(t *testing.T) {
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
			_, _, err := buildMessages([]session.Message{session.NewUserMessageWithParts("read", []session.Content{part})}, port.ProviderCapabilities{PDF: true})
			if err == nil {
				t.Fatal("unsafe or unresolved PDF reached the provider request")
			}
		})
	}
}
