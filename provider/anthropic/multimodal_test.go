package anthropic

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestUserImageBase64Block(t *testing.T) {
	p := testProvider()
	img := session.Content{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50, 0x4e, 0x47}}
	req := port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewUserMessageWithParts("look", []session.Content{img})},
	}
	params, err := p.buildParams(req)
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	m := params.Messages[0]
	if m.Content[0].OfText == nil || m.Content[0].OfText.Text != "look" {
		t.Fatalf("first block should be the text 'look', got %+v", m.Content[0])
	}
	imgBlock := m.Content[1].OfImage
	if imgBlock == nil {
		t.Fatal("second block should be an image block")
		return
	}
	if imgBlock.Source.OfBase64 == nil {
		t.Fatal("image source should be base64")
		return
	}
	if string(imgBlock.Source.OfBase64.MediaType) != "image/png" {
		t.Errorf("media_type = %q, want image/png", imgBlock.Source.OfBase64.MediaType)
	}
	if imgBlock.Source.OfBase64.Data == "" {
		t.Error("base64 data must be non-empty")
	}
}

func TestUserImageURLBlock(t *testing.T) {
	p := testProvider()
	img := session.Content{Kind: session.MediaImage, MIMEType: "image/png", URL: "https://example.com/x.png"}
	params, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewUserMessageWithParts("", []session.Content{img})},
	})
	if err != nil {
		t.Fatalf("buildParams: %v", err)
	}
	imgBlock := params.Messages[0].Content[0].OfImage
	if imgBlock == nil || imgBlock.Source.OfURL == nil {
		t.Fatalf("expected a URL image source, got %+v", params.Messages[0].Content[0])
	}
	if imgBlock.Source.OfURL.URL != "https://example.com/x.png" {
		t.Errorf("url = %q", imgBlock.Source.OfURL.URL)
	}
}

func TestUserAudioIsLoudError(t *testing.T) {
	p := testProvider()
	audio := session.Content{Kind: session.MediaAudio, MIMEType: "audio/wav", Data: []byte{1, 2, 3}}
	_, err := p.buildParams(port.LLMRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []session.Message{session.NewUserMessageWithParts("", []session.Content{audio})},
	})
	if err == nil || !strings.Contains(err.Error(), "audio") {
		t.Fatalf("audio part should be a loud error, got %v", err)
	}
}

func TestCapabilities(t *testing.T) {
	caps := (&Provider{}).Capabilities()
	want := port.ProviderCapabilities{Image: true, Audio: false, EmbeddedContext: true}
	if caps != want {
		t.Fatalf("Capabilities() = %+v, want %+v", caps, want)
	}
}
