package anthropic

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func requirePDFEngine(t *testing.T) {
	t.Helper()
	_, hasID := reflect.TypeFor[session.Content]().FieldByName("ArtifactID")
	_, hasDigest := reflect.TypeFor[session.Content]().FieldByName("SHA256")
	capability, hasCapability := reflect.TypeFor[port.ProviderCapabilities]().FieldByName("PDF")
	if !hasID && !hasDigest && !hasCapability {
		t.Skip("published engine predates PDF fields and capability")
	}
	if !hasID || !hasDigest || !hasCapability || capability.Type.Kind() != reflect.Bool {
		t.Fatal("engine has an incomplete PDF interface")
	}
}

func pdfCapabilities(t *testing.T, enabled bool) port.ProviderCapabilities {
	t.Helper()
	requirePDFEngine(t)
	caps := port.ProviderCapabilities{}
	reflect.ValueOf(&caps).Elem().FieldByName("PDF").SetBool(enabled)
	return caps
}

func setPDFStringField(t *testing.T, part *session.Content, name, value string) {
	t.Helper()
	requirePDFEngine(t)
	field := reflect.ValueOf(part).Elem().FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String || !field.CanSet() {
		t.Fatalf("PDF field %s unavailable", name)
	}
	field.SetString(value)
}

func artifactID(t *testing.T, part session.Content) string {
	t.Helper()
	requirePDFEngine(t)
	return reflect.ValueOf(part).FieldByName("ArtifactID").String()
}

func pdfInputFixture(t *testing.T) session.Content {
	t.Helper()
	requirePDFEngine(t)
	data, err := os.ReadFile("testdata/pdf_input.pdf")
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	part := session.Content{Kind: pdfMediaKind, MIMEType: "application/pdf", Name: "report.pdf", Size: int64(len(data))}
	setPDFStringField(t, &part, "ArtifactID", "artifact_1")
	setPDFStringField(t, &part, "SHA256", hex.EncodeToString(digest[:]))
	part.Data = data // Transient provider request; stored history is reference-only.
	return part
}

func TestPDFInputMessagesFixture(t *testing.T) {
	pdf := pdfInputFixture(t)
	msgs := []session.Message{session.NewUserMessageWithParts("Read the report", []session.Content{
		{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}},
		pdf,
	})}
	caps := pdfCapabilities(t, true)
	caps.Image = true
	wire, _, err := buildMessages(msgs, caps)
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
	if strings.Contains(string(raw), artifactID(t, pdf)) {
		t.Fatalf("private artifact ID leaked into provider request: %s", raw)
	}
}

func TestPDFInputMessagesRequiresNativeOptIn(t *testing.T) {
	p := New(WithProviderCapabilities(pdfCapabilities(t, true)))
	if hasPDFCapability(p.Capabilities()) {
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
		{"selected model supports PDF", New(WithNativePDFInput(), WithProviderCapabilities(pdfCapabilities(t, true))), true},
		{"selected model lacks PDF", New(WithNativePDFInput(), WithProviderCapabilities(port.ProviderCapabilities{})), false},
		{"selected model unknown", New(WithNativePDFInput()), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasPDFCapability(tc.p.Capabilities()); !got {
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
		{"missing artifact ID", func(p *session.Content) { setPDFStringField(t, p, "ArtifactID", "") }},
		{"invalid artifact ID", func(p *session.Content) { setPDFStringField(t, p, "ArtifactID", "outside/path") }},
		{"unsafe filename", func(p *session.Content) { p.Name = "../report.pdf" }},
		{"oversized metadata", func(p *session.Content) { p.Size = maxPDFBytes + 1 }},
		{"invalid digest metadata", func(p *session.Content) { setPDFStringField(t, p, "SHA256", "wrong") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := base
			part.Data = append([]byte(nil), base.Data...)
			tc.edit(&part)
			_, _, err := buildMessages([]session.Message{session.NewUserMessageWithParts("read", []session.Content{part})}, pdfCapabilities(t, true))
			if err == nil {
				t.Fatal("unsafe or unresolved PDF reached the provider request")
			}
		})
	}
}
