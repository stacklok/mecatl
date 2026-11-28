package openaichat

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// audioRequest builds a request with one user message carrying text and part.
func audioRequest(part session.Content) port.LLMRequest {
	return port.LLMRequest{Model: "m", Messages: []session.Message{
		session.NewUserMessageWithParts("transcribe this", []session.Content{part}),
	}}
}

// assertInputAudio checks that the request's single user message carries text
// followed by an input_audio part with format and data.
func assertInputAudio(t *testing.T, req map[string]any, format string, data []byte) {
	t.Helper()
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("messages = %v, want one user message", req["messages"])
	}
	um, _ := msgs[0].(map[string]any)
	content, _ := um["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("user content parts = %v, want text + input_audio", um["content"])
	}
	part, _ := content[1].(map[string]any)
	audio, _ := part["input_audio"].(map[string]any)
	if part["type"] != "input_audio" || audio["format"] != format ||
		audio["data"] != base64.StdEncoding.EncodeToString(data) {
		t.Errorf("audio part = %v, want input_audio %s with the base64 data", part, format)
	}
}

// TestBuildParamsImagePart covers an inline image part sent as a data URL.
func TestBuildParamsImagePart(t *testing.T) {
	img := port.LLMRequest{Model: "m", Messages: []session.Message{{
		Role:  session.RoleUser,
		Text:  "what is this?",
		Parts: []session.Content{{Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{0x1, 0x2}}},
	}}}
	got := marshalParams(t, img, "")
	msgs, _ := got["messages"].([]any)
	um, _ := msgs[0].(map[string]any)
	content, ok := um["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("user content parts = %v, want text+image list", um["content"])
	}
}

// TestBuildParamsAudioPart proves inline audio is sent as input_audio with the
// format taken from the MIME type.
func TestBuildParamsAudioPart(t *testing.T) {
	audioData := []byte{0x1, 0x2, 0x3}
	for _, tc := range []struct {
		name   string
		mime   string
		format string
	}{
		{name: "wav", mime: "audio/wav", format: "wav"},
		{name: "x-wav", mime: "audio/x-wav", format: "wav"},
		{name: "wave as sniffed by mecatui", mime: "audio/wave", format: "wav"},
		{name: "vnd.wave as reported by GIO", mime: "audio/vnd.wave", format: "wav"},
		{name: "mixed case with parameter", mime: "Audio/WAV; codecs=1", format: "wav"},
		{name: "mpeg", mime: "audio/mpeg", format: "mp3"},
		{name: "mp3", mime: "audio/mp3", format: "mp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := marshalParams(t, audioRequest(session.Content{Kind: session.MediaAudio, MIMEType: tc.mime, Data: audioData}), "")
			assertInputAudio(t, got, tc.format, audioData)
		})
	}
}

// TestBuildParamsAudioPartRefused proves audio that input_audio cannot carry is
// refused with an error naming the cause.
func TestBuildParamsAudioPartRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		part    session.Content
		wantErr string
	}{
		{
			name:    "unsupported format",
			part:    session.Content{Kind: session.MediaAudio, MIMEType: "audio/ogg", Data: []byte{0x1}},
			wantErr: `unsupported audio MIME type "audio/ogg"`,
		},
		{
			name:    "malformed MIME type",
			part:    session.Content{Kind: session.MediaAudio, MIMEType: "audio/wav; =", Data: []byte{0x1}},
			wantErr: `unsupported audio MIME type "audio/wav; ="`,
		},
		{
			name:    "URL source",
			part:    session.Content{Kind: session.MediaAudio, MIMEType: "audio/wav", URL: "https://example.com/a.wav"},
			wantErr: "audio part must be inline data",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildParams(audioRequest(tc.part), "")
			if err == nil {
				t.Fatal("buildParams: expected an error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// TestStreamSendsInputAudio proves an audio part reaches the HTTP request body
// as input_audio.
func TestStreamSendsInputAudio(t *testing.T) {
	bodies := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, completedChatSSE)
	}))
	defer srv.Close()

	audioData := []byte{0x1, 0x2, 0x3}
	p := New(WithAPIKey("k"), WithBaseURL(srv.URL+"/v1"))
	drainStream(t.Context(), t, p, audioRequest(session.Content{Kind: session.MediaAudio, MIMEType: "audio/mpeg", Data: audioData}))

	var req map[string]any
	if err := json.Unmarshal(<-bodies, &req); err != nil {
		t.Fatalf("decode request body: %v", err)
	}
	assertInputAudio(t, req, "mp3", audioData)
}

// TestProviderCapabilities pins what the Chat Completions wire can carry.
func TestProviderCapabilities(t *testing.T) {
	want := port.ProviderCapabilities{Image: true, Audio: true, EmbeddedContext: true}
	if got := (&Provider{}).Capabilities(); got != want {
		t.Fatalf("Capabilities() = %+v, want %+v", got, want)
	}
}
