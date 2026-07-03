package session

import (
	"net"
	"strings"
	"testing"
	"time"
)

func TestNewUserMessageWithParts(t *testing.T) {
	parts := []Content{
		{Kind: MediaImage, MIMEType: "image/png", Data: []byte{0x89, 0x50}},
		{Kind: MediaAudio, MIMEType: "audio/wav", URL: "https://example.com/a.wav"},
	}
	m := NewUserMessageWithParts("look at this", parts)
	if m.Role != RoleUser {
		t.Fatalf("role = %q, want user", m.Role)
	}
	if m.Text != "look at this" {
		t.Fatalf("text = %q", m.Text)
	}
	if len(m.Parts) != 2 {
		t.Fatalf("parts len = %d, want 2", len(m.Parts))
	}
	if m.Parts[0].Kind != MediaImage || m.Parts[0].MIMEType != "image/png" {
		t.Fatalf("part[0] = %+v", m.Parts[0])
	}
	if m.Parts[1].Kind != MediaAudio || m.Parts[1].URL != "https://example.com/a.wav" {
		t.Fatalf("part[1] = %+v", m.Parts[1])
	}
}

func TestNewUserMessageWithPartsNilIsTextOnly(t *testing.T) {
	m := NewUserMessageWithParts("hi", nil)
	if m.Parts != nil {
		t.Fatalf("parts = %v, want nil", m.Parts)
	}
	plain := NewUserMessage("hi")
	if m.Role != plain.Role || m.Text != plain.Text {
		t.Fatalf("with-parts(nil) %+v != NewUserMessage %+v", m, plain)
	}
}

func TestRecordUserPromptWithParts(t *testing.T) {
	s := New("s1", ModeDefault, "/ws", Limits{}, time.Unix(0, 0))
	parts := []Content{{Kind: MediaImage, MIMEType: "image/jpeg", Data: []byte{1, 2, 3}}}
	if err := s.RecordUserPromptWithParts("describe", parts, nil); err != nil {
		t.Fatalf("RecordUserPromptWithParts: %v", err)
	}
	if got := s.Conversation.Len(); got != 1 {
		t.Fatalf("len = %d, want 1", got)
	}
	m := s.Conversation.Messages[0]
	if m.Role != RoleUser || m.Text != "describe" || len(m.Parts) != 1 {
		t.Fatalf("recorded = %+v", m)
	}
}

func TestRecordUserPromptWithPartsInstructionsPrepended(t *testing.T) {
	s := New("s1", ModeDefault, "/ws", Limits{}, time.Unix(0, 0))
	instr := []Message{NewSystemMessage("CLAUDE.md")}
	parts := []Content{{Kind: MediaImage, MIMEType: "image/png", Data: []byte{9}}}
	if err := s.RecordUserPromptWithParts("go", parts, instr); err != nil {
		t.Fatalf("record: %v", err)
	}
	if s.Conversation.Len() != 2 {
		t.Fatalf("len = %d, want 2 (instruction + user)", s.Conversation.Len())
	}
	if s.Conversation.Messages[0].Role != RoleSystem {
		t.Fatalf("first message role = %q, want system", s.Conversation.Messages[0].Role)
	}
	if got := s.Conversation.Messages[1].Parts; len(got) != 1 {
		t.Fatalf("user parts = %v", got)
	}
}

func TestRecordUserPromptWithPartsTerminalRejected(t *testing.T) {
	s := New("s1", ModeDefault, "/ws", Limits{}, time.Unix(0, 0))
	if err := s.Complete(); err != nil {
		t.Fatalf("complete: %v", err)
	}
	err := s.RecordUserPromptWithParts("x", []Content{{Kind: MediaImage}}, nil)
	if err == nil {
		t.Fatal("expected error recording from terminal state")
	}
}

func TestRecordUserPromptDelegatesNilParts(t *testing.T) {
	s := New("s1", ModeDefault, "/ws", Limits{}, time.Unix(0, 0))
	if err := s.RecordUserPrompt("plain", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if got := s.Conversation.Messages[0].Parts; got != nil {
		t.Fatalf("parts = %v, want nil after RecordUserPrompt", got)
	}
}

func TestValidateMediaURL(t *testing.T) {
	cases := []struct {
		name string
		url  string
		ok   bool
	}{
		{"public https", "https://media.example.com/cat.png", true},
		{"public https with port", "https://images.example.org:8443/a.jpg", true},
		{"http rejected", "http://media.example.com/a.png", false},
		{"file scheme rejected", "file:///etc/passwd", false},
		{"data scheme rejected", "data:image/png;base64,AAAA", false},
		{"ftp rejected", "ftp://media.example.com/a.png", false},
		{"gopher rejected", "gopher://media.example.com/a", false},
		{"empty rejected", "", false},
		{"relative rejected", "/local/path.png", false},
		{"hostless rejected", "https:///a.png", false},
		{"loopback ip rejected", "https://127.0.0.1/a.png", false},
		{"ipv6 loopback rejected", "https://[::1]/a.png", false},
		{"metadata ip rejected", "https://169.254.169.254/latest/meta-data/", false},
		{"link-local rejected", "https://169.254.0.5/a.png", false},
		{"rfc1918 10 rejected", "https://10.0.0.5/a.png", false},
		{"rfc1918 192.168 rejected", "https://192.168.1.10/a.png", false},
		{"rfc1918 172.16 rejected", "https://172.16.0.9/a.png", false},
		{"unspecified rejected", "https://0.0.0.0/a.png", false},
		{"localhost name rejected", "https://localhost/a.png", false},
		{"single-label host rejected", "https://intranet/a.png", false},
		{".local suffix rejected", "https://printer.local/a.png", false},
		{".internal suffix rejected", "https://db.internal/a.png", false},
		{"public ip allowed", "https://93.184.216.34/a.png", true},
		// inet_aton-style numeric forms a resolver decodes to internal IPs.
		{"inet_aton hex rejected", "https://0x7f.0.0.1/a.png", false},
		{"inet_aton octal rejected", "https://0177.0.0.1/a.png", false},
		{"inet_aton short rejected", "https://127.1/a.png", false},
		{"inet_aton whole-int rejected", "https://2130706433/a.png", false},
		// CGNAT 100.64.0.0/10 — IsPrivate misses it.
		{"cgnat rejected", "https://100.64.0.1/a.png", false},
		// Trailing root dot a resolver strips to reach the same internal target.
		{"metadata ip trailing dot rejected", "https://169.254.169.254./latest/meta-data/", false},
		{"localhost trailing dot rejected", "https://localhost./a.png", false},
		// A canonical PUBLIC IPv6 must still be allowed (numeric-form rejection
		// must not over-block legitimate public literal IPs).
		{"public ipv6 allowed", "https://[2606:2800:220:1::]/a.png", true},
		// RFC 6052 NAT64 well-known prefix 64:ff9b::/96 embeds an IPv4 address in
		// its low 32 bits; on a NAT64/DNS64 egress path this literal is
		// translated back to the embedded IPv4 and dialed, so an embedded
		// internal address must be rejected exactly as the bare IPv4 would be.
		{"nat64 embedded metadata rejected", "https://[64:ff9b::a9fe:a9fe]/a.png", false},
		{"nat64 embedded rfc1918 rejected", "https://[64:ff9b::a00:1]/a.png", false},
		// An embedded PUBLIC v4 is not an SSRF target and must be allowed.
		{"nat64 embedded public allowed", "https://[64:ff9b::808:808]/a.png", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMediaURL(tc.url)
			if tc.ok && err != nil {
				t.Fatalf("ValidateMediaURL(%q) = %v, want nil", tc.url, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidateMediaURL(%q) = nil, want error", tc.url)
			}
		})
	}
}

func TestValidateResolvedIP(t *testing.T) {
	// The dial-layer predicate must reject every internal/non-routable IP shape
	// ValidateMediaURL screens for literal IPs, so a DNS-rebinding fetch (hostname
	// resolves to an internal IP after the URL-string check passed) is caught at
	// the dial layer. Public routable IPs pass.
	cases := []struct {
		name string
		ip   string
		ok   bool
	}{
		{"public ipv4", "93.184.216.34", true},
		{"public ipv6", "2606:2800:220:1::", true},
		{"loopback rejected", "127.0.0.1", false},
		{"ipv6 loopback rejected", "::1", false},
		{"metadata rejected", "169.254.169.254", false},
		{"link-local rejected", "169.254.0.5", false},
		{"rfc1918 10 rejected", "10.0.0.5", false},
		{"rfc1918 192.168 rejected", "192.168.1.10", false},
		{"rfc1918 172.16 rejected", "172.16.0.9", false},
		{"unspecified rejected", "0.0.0.0", false},
		{"cgnat rejected", "100.64.0.1", false},
		{"multicast rejected", "224.0.0.1", false},
		// RFC 6052 NAT64 well-known-prefix literals embedding an internal IPv4
		// in the low 32 bits must be rejected exactly as the bare IPv4 would be
		// (see isGlobalUnicast); an embedded public IPv4 is not an SSRF target.
		{"nat64 embedded metadata rejected", "64:ff9b::a9fe:a9fe", false},
		{"nat64 embedded rfc1918 rejected", "64:ff9b::a00:1", false},
		{"nat64 embedded public allowed", "64:ff9b::808:808", true},
		// net.ParseIP("") == nil (an unresolved/failed-resolve IP). isGlobalUnicast
		// short-circuits on the len(ip) == IPv4len || IPv6len guard, so a nil IP
		// falls through every predicate without panicking and is rejected.
		{"nil ip rejected", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResolvedIP(net.ParseIP(tc.ip))
			if tc.ok && err != nil {
				t.Fatalf("ValidateResolvedIP(%s) = %v, want nil", tc.ip, err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("ValidateResolvedIP(%s) = nil, want error", tc.ip)
			}
		})
	}
}

func TestNewContentEnforcesInvariants(t *testing.T) {
	// Valid inline image.
	if _, err := NewImageContent("image/png", []byte{1, 2}); err != nil {
		t.Fatalf("valid inline image: %v", err)
	}
	// Valid URL image (public https).
	if _, err := NewImageURLContent("image/jpeg", "https://media.example.com/x.jpg"); err != nil {
		t.Fatalf("valid url image: %v", err)
	}
	// Valid inline audio.
	if _, err := NewAudioContent("audio/wav", []byte{1}); err != nil {
		t.Fatalf("valid inline audio: %v", err)
	}

	bad := []struct {
		name string
		fn   func() (Content, error)
	}{
		{"empty kind", func() (Content, error) { return NewContent("", "image/png", []byte{1}, "") }},
		{"empty mime", func() (Content, error) { return NewImageContent("", []byte{1}) }},
		{"both data and url", func() (Content, error) {
			return NewContent(MediaImage, "image/png", []byte{1}, "https://media.example.com/x")
		}},
		{"neither data nor url", func() (Content, error) { return NewContent(MediaImage, "image/png", nil, "") }},
		{"image kind with audio mime", func() (Content, error) { return NewImageContent("audio/wav", []byte{1}) }},
		{"audio kind with image mime", func() (Content, error) { return NewAudioContent("image/png", []byte{1}) }},
		{"url image with internal host", func() (Content, error) { return NewImageURLContent("image/png", "https://localhost/x.png") }},
		{"url image with http", func() (Content, error) { return NewImageURLContent("image/png", "http://media.example.com/x.png") }},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.fn(); err == nil {
				t.Fatalf("%s: expected error, got nil", tc.name)
			}
		})
	}
}

func TestNewTextBlock(t *testing.T) {
	c := NewTextBlock("hello")
	if c.BlockKind != BlockText {
		t.Fatalf("kind = %q, want %q", c.BlockKind, BlockText)
	}
	if c.Text != "hello" {
		t.Fatalf("text = %q", c.Text)
	}
	if c.Data != nil {
		t.Fatalf("data = %v, want nil", c.Data)
	}
}

func TestNewResourceLinkBlock(t *testing.T) {
	aud := []string{"user"}
	c := NewResourceLinkBlock("https://example.com/r.json", "r", "Title", "desc", "application/json", 42, aud)
	if c.BlockKind != BlockResourceLink {
		t.Fatalf("kind = %q", c.BlockKind)
	}
	if c.URL != "https://example.com/r.json" {
		t.Fatalf("url = %q", c.URL)
	}
	if c.Name != "r" || c.Title != "Title" || c.Description != "desc" {
		t.Fatalf("metadata = name=%q title=%q desc=%q", c.Name, c.Title, c.Description)
	}
	if c.MIMEType != "application/json" {
		t.Fatalf("mime = %q", c.MIMEType)
	}
	if c.Size != 42 {
		t.Fatalf("size = %d", c.Size)
	}
	if len(c.Audience) != 1 || c.Audience[0] != "user" {
		t.Fatalf("audience = %v", c.Audience)
	}
	// A resource link is a reference; it must NOT carry inline bytes.
	if c.Data != nil {
		t.Fatalf("data = %v, want nil (resource link is a reference)", c.Data)
	}
}

func TestNewEmbeddedResourceBlock(t *testing.T) {
	aud := []string{"admin"}
	t.Run("text form", func(t *testing.T) {
		c, err := NewEmbeddedResourceBlock("https://example.com/r.txt", "text/plain", "body", nil, aud)
		if err != nil {
			t.Fatalf("text form: %v", err)
		}
		if c.BlockKind != BlockEmbeddedResource {
			t.Fatalf("kind = %q", c.BlockKind)
		}
		if c.Text != "body" {
			t.Fatalf("text = %q", c.Text)
		}
		if c.Data != nil {
			t.Fatalf("data = %v, want nil (text form)", c.Data)
		}
		if len(c.Audience) != 1 || c.Audience[0] != "admin" {
			t.Fatalf("audience = %v", c.Audience)
		}
	})
	t.Run("blob form", func(t *testing.T) {
		c, err := NewEmbeddedResourceBlock("https://example.com/r.bin", "application/octet-stream", "", []byte{1, 2, 3}, nil)
		if err != nil {
			t.Fatalf("blob form: %v", err)
		}
		if c.Text != "" {
			t.Fatalf("text = %q, want empty (blob form)", c.Text)
		}
		if len(c.Data) != 3 {
			t.Fatalf("data len = %d", len(c.Data))
		}
	})
	t.Run("both rejected", func(t *testing.T) {
		if _, err := NewEmbeddedResourceBlock("u", "m", "t", []byte{1}, nil); err == nil {
			t.Fatal("expected error when both text and blob set")
		}
	})
	t.Run("neither rejected", func(t *testing.T) {
		if _, err := NewEmbeddedResourceBlock("u", "m", "", nil, nil); err == nil {
			t.Fatal("expected error when neither text nor blob set")
		}
	})
}

func TestNewStructuredContentBlock(t *testing.T) {
	c := NewStructuredContentBlock(`{"k":"v"}`)
	if c.BlockKind != BlockStructuredContent {
		t.Fatalf("kind = %q", c.BlockKind)
	}
	if c.Text != `{"k":"v"}` {
		t.Fatalf("text = %q", c.Text)
	}
}

func TestToolBlockText(t *testing.T) {
	tests := []struct {
		name string
		c    Content
		want string
	}{
		{
			name: "text block",
			c:    NewTextBlock("hello"),
			want: "hello",
		},
		{
			name: "structured content block",
			c:    NewStructuredContentBlock(`{"k":"v"}`),
			want: `{"k":"v"}`,
		},
		{
			name: "resource link with title",
			c:    NewResourceLinkBlock("https://example.com/r.json", "r", "Title", "desc", "application/json", 42, nil),
			want: "Title (https://example.com/r.json)",
		},
		{
			name: "resource link with name only",
			c:    NewResourceLinkBlock("https://example.com/r.json", "r", "", "desc", "application/json", 42, nil),
			want: "r (https://example.com/r.json)",
		},
		{
			name: "resource link with neither title nor name",
			c:    NewResourceLinkBlock("https://example.com/r.json", "", "", "desc", "application/json", 42, nil),
			want: "https://example.com/r.json",
		},
		{
			name: "embedded resource text form",
			c:    mustEmbeddedResourceBlock(t, "https://example.com/r.txt", "text/plain", "body", nil),
			want: "body",
		},
		{
			name: "embedded resource blob form renders a pointer",
			c:    mustEmbeddedResourceBlock(t, "https://example.com/r.bin", "application/octet-stream", "", []byte{1, 2, 3}),
			want: "https://example.com/r.bin (application/octet-stream)",
		},
		{
			name: "legacy media part with no block kind falls to the default text arm",
			c:    Content{Text: "legacy"},
			want: "legacy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToolBlockText(tt.c); got != tt.want {
				t.Fatalf("ToolBlockText() = %q, want %q", got, tt.want)
			}
		})
	}
}

func mustEmbeddedResourceBlock(t *testing.T, uri, mimeType, text string, blob []byte) Content {
	t.Helper()
	c, err := NewEmbeddedResourceBlock(uri, mimeType, text, blob, nil)
	if err != nil {
		t.Fatalf("NewEmbeddedResourceBlock: %v", err)
	}
	return c
}

func TestValidateToolResultParts(t *testing.T) {
	// Empty passes.
	if err := ValidateToolResultParts(nil); err != nil {
		t.Fatalf("nil parts: %v", err)
	}
	// A valid text block passes.
	if err := ValidateToolResultParts([]Content{NewTextBlock("ok")}); err != nil {
		t.Fatalf("valid text block: %v", err)
	}
	// Text block over the cap rejected.
	big := strings.Repeat("x", MaxToolResultTextBytes+1)
	if err := ValidateToolResultParts([]Content{NewTextBlock(big)}); err == nil {
		t.Fatal("expected reject for oversized text block")
	}
	// Blob over MaxMediaBytes rejected.
	hugeBlob := make([]byte, MaxMediaBytes+1)
	er, err := NewEmbeddedResourceBlock("u", "application/octet-stream", "", hugeBlob, nil)
	if err != nil {
		t.Fatalf("build oversized embedded: %v", err)
	}
	if err := ValidateToolResultParts([]Content{er}); err == nil {
		t.Fatal("expected reject for oversized blob block")
	}
	// Total over cap rejected: many text blocks each under the per-block cap but
	// summing over MaxToolResultBytes.
	per := MaxToolResultTextBytes
	n := (MaxToolResultBytes / per) + 1
	parts := make([]Content, n)
	for i := range parts {
		parts[i] = NewTextBlock(strings.Repeat("x", per))
	}
	if err := ValidateToolResultParts(parts); err == nil {
		t.Fatal("expected reject for total tool-result bytes over the cap")
	}
	// A resource link contributes no bytes — many pass.
	links := make([]Content, 100)
	for i := range links {
		links[i] = NewResourceLinkBlock("https://example.com/r", "n", "t", "d", "text/plain", 1, nil)
	}
	if err := ValidateToolResultParts(links); err != nil {
		t.Fatalf("resource links: %v", err)
	}
}

func TestValidateMediaParts(t *testing.T) {
	// Empty passes.
	if err := ValidateMediaParts(nil); err != nil {
		t.Fatalf("nil parts: %v", err)
	}
	// A part over the per-part cap is rejected.
	over := []Content{{Kind: MediaImage, MIMEType: "image/png", Data: make([]byte, MaxMediaBytes+1)}}
	if err := ValidateMediaParts(over); err == nil {
		t.Fatal("expected reject for oversized part")
	}
	// Too many parts rejected.
	many := make([]Content, MaxPromptMediaParts+1)
	for i := range many {
		many[i] = Content{Kind: MediaImage, MIMEType: "image/png", Data: []byte{1}}
	}
	if err := ValidateMediaParts(many); err == nil {
		t.Fatal("expected reject for too many parts")
	}
	// Sum over the prompt cap rejected (each under per-part, sum over total).
	per := MaxMediaBytes
	n := (MaxPromptMediaBytes / per) + 1
	if n > MaxPromptMediaParts {
		n = MaxPromptMediaParts // keep under the count cap so the SIZE cap is what trips
	}
	sum := make([]Content, n)
	for i := range sum {
		sum[i] = Content{Kind: MediaImage, MIMEType: "image/png", Data: make([]byte, per)}
	}
	if err := ValidateMediaParts(sum); err == nil {
		t.Fatal("expected reject for total inline media over the prompt cap")
	}
	// URL parts contribute no bytes — many URL parts under the count cap pass.
	urls := make([]Content, MaxPromptMediaParts)
	for i := range urls {
		urls[i] = Content{Kind: MediaImage, MIMEType: "image/png", URL: "https://media.example.com/x.png"}
	}
	if err := ValidateMediaParts(urls); err != nil {
		t.Fatalf("url parts under count cap: %v", err)
	}
}
