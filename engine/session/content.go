package session

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MediaKind discriminates a non-text content Part of a user Message.
type MediaKind string

const (
	// MediaImage is an image part (e.g. image/png, image/jpeg).
	MediaImage MediaKind = "image"
	// MediaAudio is an audio part (e.g. audio/wav, audio/mp3).
	MediaAudio MediaKind = "audio"
	// MediaPDF is a session-owned PDF artifact reference in a user prompt.
	MediaPDF MediaKind = "pdf"
)

// Media size caps. They bound the memory/persistence/per-turn-resend cost of a
// multimodal prompt (CWE-770). They apply ONLY to inline Data bytes — a part
// sourced from a URL is fetched by the remote provider, not held here, so its
// payload is not counted (its URL is validated by ValidateMediaURL instead).
const (
	// MaxMediaBytes is the cap on a single Content part's inline Data. 10 MiB
	// comfortably holds a high-resolution screenshot or photo while bounding the
	// cost of statelessly re-sending it every turn and persisting it on disk.
	MaxMediaBytes = 10 << 20 // 10 MiB
	// MaxPromptMediaBytes caps the sum of inline bytes and server-resolved PDF
	// artifact sizes in one prompt, so references do not bypass the prompt budget.
	MaxPromptMediaBytes = 20 << 20 // 20 MiB
	// MaxPromptMediaParts is the cap on the number of parts in one prompt, so a
	// flood of tiny parts cannot exhaust memory either.
	MaxPromptMediaParts = 16
	// MaxPDFBytes is the maximum size of a stored PDF artifact.
	MaxPDFBytes = 20 << 20
)

// MaxToolResultTextBytes caps the byte length of a single TEXT tool-result block.
// It is the domain-level upper bound on ANY tool's text result — not only the
// filesystem tools, but MCP results, memory tools, and any consumer-supplied tool
// — at 25 KiB (25,600 bytes). The adapter-layer output caps
// (engine/adapter/fstools.MaxOutputBytes and internal/adapter/toolkit.MaxOutputBytes,
// both 25,000 bytes) are STRICTER and sit under this bound, so a truncated tool
// result always validates here; the ~600-byte headroom is deliberate, not a drift
// to reconcile. Defined locally (not imported) because engine/session is the
// domain leaf: it may not import an adapter (even fstools, which lives in this
// module), and toolkit lives under the host repo's internal/adapter tree.
const MaxToolResultTextBytes = 25 << 10 // 25 KiB (25,600 bytes)

// MaxToolResultBytes is the cap on the SUM of all block bytes in one tool
// result, mirroring MaxPromptMediaBytes so a tool result cannot collectively
// blow memory even when each block is under its per-block cap. It counts inline
// text + blob bytes (URL-sourced resource links contribute none — the provider
// fetches those).
const MaxToolResultBytes = 20 << 20 // 20 MiB

// BlockKind discriminates a Content block variant. The zero value ("")
// designates a LEGACY media part on Message.Parts (image/audio via Kind), so the
// existing ACP / provider-request media path stays byte-identical. Tool-result
// blocks live on ToolResult.Parts and always set BlockKind.
type BlockKind string

const (
	// BlockText is a plain-text tool-result block (e.g. an MCP text content).
	BlockText BlockKind = "text"
	// BlockImage reuses the existing media-image fields (Kind=MediaImage,
	// MIMEType, Data/URL) for an image tool-result block.
	BlockImage BlockKind = "image"
	// BlockAudio reuses the existing media-audio fields (Kind=MediaAudio,
	// MIMEType, Data/URL) for an audio tool-result block.
	BlockAudio BlockKind = "audio"
	// BlockResourceLink is a REFERENCE to a resource (URI + metadata); it is
	// NOT fetched here, so its URL is NOT validated by ValidateMediaURL.
	BlockResourceLink BlockKind = "resource_link"
	// BlockEmbeddedResource carries a resource inline as either a text form (Text)
	// or a binary blob (Data), exactly one of which is populated.
	BlockEmbeddedResource BlockKind = "embedded_resource"
	// BlockStructuredContent carries a JSON-stringified structured payload as a
	// text block — the backward-compat mirror of the legacy TextContent path.
	BlockStructuredContent BlockKind = "structured"
	// BlockPDFArtifact is a downloadable session-owned PDF reference.
	BlockPDFArtifact BlockKind = "pdf_artifact"
)

// Content is an immutable value object. It serves TWO roles, distinguished by
// BlockKind:
//
//   - LEGACY media part (BlockKind == ""): a non-text part of a USER Message
//     (Message.Parts). The source is EITHER inline bytes (Data, already
//     base64-decoded) OR a remote reference (URL) — never both. MIMEType is the
//     IANA media type (e.g. "image/png", "audio/wav"). Construct via the
//     validating constructors (NewImageContent / NewImageURLContent /
//     NewAudioContent / NewAudioURLContent, or NewContent for the dynamic case)
//     so the exactly-one-of(Data,URL), kind-set, and mime-consistency
//     invariants hold structurally rather than by prose. This path stays
//     MEDIA-ONLY: a tool-result block must never ride on Message.Parts — it
//     lives on ToolResult.Parts (decision #1 / Risk #4 mitigation (a)).
//
//   - TOOL-RESULT block (BlockKind != ""): a typed block variant on a
//     ToolResult.Parts. Construct via NewTextBlock / NewResourceLinkBlock /
//     NewEmbeddedResourceBlock / NewStructuredContentBlock. Image/audio blocks
//     reuse the existing Kind/MIMEType/Data/URL fields (no new fields).
//
// It carries no mutating methods; treat it as immutable. Data []byte is
// technically mutable; by convention callers MUST NOT mutate Data after
// construction — the same treatment ToolCall.Args (json.RawMessage) already
// receives.
type Content struct {
	// BlockKind discriminates the block variant; "" = legacy media part. Set
	// for tool-result blocks (BlockText/BlockImage/BlockAudio/BlockResourceLink/
	// BlockEmbeddedResource/BlockStructuredContent).
	BlockKind BlockKind `json:"block_kind,omitempty"`
	// Kind discriminates the media kind (image / audio) for legacy media parts
	// and the BlockImage/BlockAudio block variants.
	Kind MediaKind
	// MIMEType is the IANA media type of the part (image, audio, resource).
	MIMEType string
	// Data is the inline content bytes; nil when the part is URL-sourced. For
	// BlockEmbeddedResource it is the binary blob form (exactly one of Text/Data
	// populated); the provider summarizes it, this does NOT base64-dump.
	Data []byte
	// URL is the remote reference; "" when the part is inline. For
	// BlockResourceLink it is the resource URI (a reference, NOT fetched here).
	URL string
	// Text carries the text form for BlockText and BlockStructuredContent, and
	// the text form of a BlockEmbeddedResource (exactly one of Text/Data set).
	Text string `json:"Text,omitempty"`
	// Name is the resource name for a BlockResourceLink.
	Name string `json:"Name,omitempty"`
	// Title is the resource title for a BlockResourceLink.
	Title string `json:"Title,omitempty"`
	// Description is the resource description for a BlockResourceLink.
	Description string `json:"Description,omitempty"`
	// Size is the resource byte size for a BlockResourceLink (advisory).
	Size int64 `json:"Size,omitempty"`
	// ArtifactID identifies a private session-owned PDF; it grants no access by itself.
	ArtifactID string `json:"ArtifactID,omitempty"`
	// SHA256 is the lowercase hexadecimal digest of the stored PDF bytes.
	SHA256 string `json:"SHA256,omitempty"`
	// Audience is the advisory intended-audience list for a resource block.
	//
	// SECURITY: this is UNTRUSTED SERVER SELF-ATTESTATION (CWE-345). An MCP
	// server (or any tool-result producer) asserts who may view a resource; the
	// harness MUST NOT treat this as authoritative. It is carried through for
	// operator/model visibility ONLY — it NEVER suppresses model-visible
	// content and NEVER gates access control. Enforcement lives in the
	// permission layer, not here.
	Audience []string `json:"Audience,omitempty"`
	// Priority is carried through for a resource block; not consumed in v1.
	Priority float64 `json:"Priority,omitempty"`
	// LastModified is carried through for a resource block; not consumed in v1.
	LastModified string `json:"LastModified,omitempty"`
}

// Errors returned by the Content constructors / validators.
var (
	// ErrInvalidContent is returned when a Content part violates a structural
	// invariant (no kind, no mime, both/neither of Data/URL, mime/kind mismatch).
	ErrInvalidContent = errors.New("session: invalid media content")
	// ErrInvalidMediaURL is returned by ValidateMediaURL for a URL that is not an
	// absolute https URL to a non-internal host.
	ErrInvalidMediaURL = errors.New("session: invalid media URL")
)

// NewContent builds and validates a media Content part. EXACTLY ONE of data/url
// must be set; kind must be MediaImage or MediaAudio; mime must be non-empty and
// consistent with kind (image/* for MediaImage, audio/* for MediaAudio). A URL
// source is additionally validated by ValidateMediaURL (absolute https, no
// internal host) — the SSRF backstop, since a remote provider DEREFERENCES it.
// It is the blessed wire→domain construction path the mappers (and ACP) use; the
// raw struct stays usable for tests that intentionally bypass validation.
func NewContent(kind MediaKind, mime string, data []byte, rawURL string) (Content, error) {
	if kind != MediaImage && kind != MediaAudio {
		return Content{}, fmt.Errorf("%w: kind is required (got %q)", ErrInvalidContent, kind)
	}
	if mime == "" {
		return Content{}, fmt.Errorf("%w: mime type is required", ErrInvalidContent)
	}
	if err := validateMIME(kind, mime); err != nil {
		return Content{}, err
	}
	hasData := len(data) > 0
	hasURL := rawURL != ""
	switch {
	case hasData && hasURL:
		return Content{}, fmt.Errorf("%w: exactly one of data/url must be set, not both", ErrInvalidContent)
	case !hasData && !hasURL:
		return Content{}, fmt.Errorf("%w: exactly one of data/url must be set, got neither", ErrInvalidContent)
	}
	if hasURL {
		if err := ValidateMediaURL(rawURL); err != nil {
			return Content{}, err
		}
		return Content{Kind: kind, MIMEType: mime, URL: rawURL}, nil
	}
	return Content{Kind: kind, MIMEType: mime, Data: data}, nil
}

// NewImageContent builds a validated inline-image part.
func NewImageContent(mime string, data []byte) (Content, error) {
	return NewContent(MediaImage, mime, data, "")
}

// NewImageURLContent builds a validated URL-sourced image part.
func NewImageURLContent(mime, rawURL string) (Content, error) {
	return NewContent(MediaImage, mime, nil, rawURL)
}

// NewAudioContent builds a validated inline-audio part.
func NewAudioContent(mime string, data []byte) (Content, error) {
	return NewContent(MediaAudio, mime, data, "")
}

// NewAudioURLContent builds a validated URL-sourced audio part.
func NewAudioURLContent(mime, rawURL string) (Content, error) {
	return NewContent(MediaAudio, mime, nil, rawURL)
}

// NewPDFContent builds a server-resolved, reference-only PDF prompt part.
func NewPDFContent(id, name string, size int64, sha256 string) (Content, error) {
	if err := validatePDFMetadata(id, name, size, sha256); err != nil {
		return Content{}, err
	}
	return Content{Kind: MediaPDF, MIMEType: "application/pdf", ArtifactID: id, Name: name, Size: size, SHA256: sha256}, nil
}

// NewPDFArtifactBlock builds a downloadable, reference-only PDF tool-result block.
func NewPDFArtifactBlock(id, name string, size int64, sha256 string) (Content, error) {
	if err := validatePDFMetadata(id, name, size, sha256); err != nil {
		return Content{}, err
	}
	return Content{BlockKind: BlockPDFArtifact, MIMEType: "application/pdf", ArtifactID: id, Name: name, Size: size, SHA256: sha256}, nil
}

func validatePDFMetadata(id, name string, size int64, sha256 string) error {
	if !validPDFID(id) {
		return fmt.Errorf("%w: PDF artifact ID is invalid", ErrInvalidContent)
	}
	if !validPDFName(name) {
		return fmt.Errorf("%w: PDF name is invalid", ErrInvalidContent)
	}
	if size <= 0 || size > MaxPDFBytes {
		return fmt.Errorf("%w: PDF size is invalid", ErrInvalidContent)
	}
	if !validPDFSHA256(sha256) {
		return fmt.Errorf("%w: PDF SHA-256 is invalid", ErrInvalidContent)
	}
	return nil
}

func validPDFID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func validPDFName(name string) bool {
	if !utf8.ValidString(name) || name == "." || name == ".." || utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 255 {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validPDFSHA256(sha256 string) bool {
	if len(sha256) != 64 {
		return false
	}
	for _, ch := range sha256 {
		if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
			return false
		}
	}
	return true
}

// ValidateMediaParts enforces the per-prompt media caps (CWE-770) on an already
// constructed slice of parts: at most MaxPromptMediaParts parts, each inline
// part at most MaxMediaBytes, and the sum of inline bytes and resolved PDF sizes
// at most MaxPromptMediaBytes. URL-sourced image/audio parts contribute no bytes
// (the provider fetches those). A nil/empty slice passes.
func ValidateMediaParts(parts []Content) error {
	if len(parts) > MaxPromptMediaParts {
		return fmt.Errorf("%w: too many media parts (%d > %d)", ErrInvalidContent, len(parts), MaxPromptMediaParts)
	}
	total := 0
	for i, p := range parts {
		n := len(p.Data)
		if n > MaxMediaBytes {
			return fmt.Errorf("%w: part[%d] inline data %d bytes exceeds the %d-byte cap", ErrInvalidContent, i, n, MaxMediaBytes)
		}
		total += n
		if p.Kind == MediaPDF {
			// A reference consumes no inline bytes but still spends the prompt's
			// total PDF budget after server-side metadata resolution.
			if p.Size < 0 || p.Size > MaxPDFBytes {
				return fmt.Errorf("%w: part[%d] PDF size is invalid", ErrInvalidContent, i)
			}
			total += int(p.Size)
		}
		if total > MaxPromptMediaBytes {
			return fmt.Errorf("%w: total prompt media exceeds the %d-byte cap", ErrInvalidContent, MaxPromptMediaBytes)
		}
	}
	return nil
}

// NewTextBlock builds a plain-text tool-result block.
func NewTextBlock(text string) Content {
	return Content{BlockKind: BlockText, Text: text}
}

// NewResourceLinkBlock builds a resource-link block: a REFERENCE to a resource
// (uri + metadata). It is NOT fetched here, so uri is NOT validated by
// ValidateMediaURL — the producer is trusted to hand a dereferenceable URI and
// any fetch is the provider's responsibility. mimeType/size are advisory
// metadata. audience is untrusted self-attestation (see the Audience field doc).
func NewResourceLinkBlock(uri, name, title, description, mimeType string, size int64, audience []string) Content {
	return Content{
		BlockKind:   BlockResourceLink,
		URL:         uri,
		Name:        name,
		Title:       title,
		Description: description,
		MIMEType:    mimeType,
		Size:        size,
		Audience:    audience,
	}
}

// NewEmbeddedResourceBlock builds an embedded-resource block carrying a resource
// inline. Exactly one of text (text form) or blob (binary form) must be
// populated; the blob path does NOT base64-dump — the provider summarizes it.
// uri/mimeType identify the resource; audience is untrusted self-attestation.
func NewEmbeddedResourceBlock(uri, mimeType, text string, blob []byte, audience []string) (Content, error) {
	hasText := text != ""
	hasBlob := len(blob) > 0
	switch {
	case hasText && hasBlob:
		return Content{}, fmt.Errorf("%w: embedded resource: exactly one of text/blob must be set, not both", ErrInvalidContent)
	case !hasText && !hasBlob:
		return Content{}, fmt.Errorf("%w: embedded resource: exactly one of text/blob must be set, got neither", ErrInvalidContent)
	}
	c := Content{
		BlockKind: BlockEmbeddedResource,
		URL:       uri,
		MIMEType:  mimeType,
		Audience:  audience,
	}
	if hasText {
		c.Text = text
	} else {
		c.Data = blob
	}
	return c, nil
}

// NewStructuredContentBlock carries a JSON-stringified structured payload as a
// text block — the backward-compat mirror of the legacy TextContent path. The
// caller is responsible for JSON-stringifying structuredJSON; this does not
// re-validate it.
func NewStructuredContentBlock(structuredJSON string) Content {
	return Content{BlockKind: BlockStructuredContent, Text: structuredJSON}
}

// ValidateToolResultParts enforces the per-result byte caps (CWE-770) on an
// already-constructed slice of tool-result blocks: each text block at most
// MaxToolResultTextBytes, each blob block at most MaxMediaBytes except a PDF
// embedded resource (MaxPDFBytes), and the SUM of
// inline text+blob bytes at most MaxToolResultBytes. URL-sourced resource links
// contribute no bytes. It is called at the wire→domain choke point after the
// blocks are built via the constructors. A nil/empty slice passes. It is
// DISTINCT from ValidateMediaParts (which caps user-message media) — the two
// paths are read separately by providers and must not be collapsed.
func ValidateToolResultParts(parts []Content) error {
	total := 0
	for i, p := range parts {
		switch p.BlockKind {
		case BlockText, BlockStructuredContent:
			n := len(p.Text)
			if n > MaxToolResultTextBytes {
				return fmt.Errorf("%w: block[%d] text %d bytes exceeds the %d-byte cap", ErrInvalidContent, i, n, MaxToolResultTextBytes)
			}
			total += n
		case BlockEmbeddedResource:
			n := len(p.Data)
			limit := MaxMediaBytes
			if strings.EqualFold(p.MIMEType, "application/pdf") && n > 0 {
				limit = MaxPDFBytes
			}
			if n > limit {
				return fmt.Errorf("%w: block[%d] blob %d bytes exceeds the %d-byte cap", ErrInvalidContent, i, n, limit)
			}
			total += n + len(p.Text)
		case BlockImage, BlockAudio:
			n := len(p.Data)
			if n > MaxMediaBytes {
				return fmt.Errorf("%w: block[%d] inline data %d bytes exceeds the %d-byte cap", ErrInvalidContent, i, n, MaxMediaBytes)
			}
			total += n
		case BlockResourceLink, BlockPDFArtifact:
			// Reference only — no inline bytes.
		case "":
			// A legacy media part on a tool result is unexpected but harmless
			// to size-account; treat its inline bytes as a blob.
			n := len(p.Data)
			if n > MaxMediaBytes {
				return fmt.Errorf("%w: block[%d] inline data %d bytes exceeds the %d-byte cap", ErrInvalidContent, i, n, MaxMediaBytes)
			}
			total += n
		default:
			return fmt.Errorf("%w: block[%d] unknown block kind %q", ErrInvalidContent, i, p.BlockKind)
		}
		if total > MaxToolResultBytes {
			return fmt.Errorf("%w: total tool-result bytes exceeds the %d-byte cap", ErrInvalidContent, MaxToolResultBytes)
		}
	}
	return nil
}

// ToolBlockText renders a non-image tool-result block as its model-facing text
// form. BlockText / BlockStructuredContent carry their text in Text; a resource
// link renders its URI + name/title; an embedded-resource blob (no Text)
// renders a pointer (URI + mime) rather than a base64 dump. It is the single
// shared projection consumed by both the OpenAI and Anthropic adapters so their
// tool-result rendering cannot drift.
func ToolBlockText(b Content) string {
	switch b.BlockKind {
	case BlockPDFArtifact:
		return fmt.Sprintf("PDF artifact: %s (%d bytes)", b.Name, b.Size)
	case BlockResourceLink:
		if b.Title != "" {
			return b.Title + " (" + b.URL + ")"
		}
		if b.Name != "" {
			return b.Name + " (" + b.URL + ")"
		}
		return b.URL
	case BlockEmbeddedResource:
		if b.Text != "" {
			return b.Text
		}
		return b.URL + " (" + b.MIMEType + ")"
	default: // BlockText, BlockStructuredContent, and any text-bearing block.
		return b.Text
	}
}

// validateMIME enforces that mime names a type consistent with kind: image/*
// for MediaImage, audio/* for MediaAudio. It is honest input validation (a
// mismatched mime is a client error), not a security boundary — JSON encodes the
// value safely regardless.
func validateMIME(kind MediaKind, mime string) error {
	want := string(kind) + "/" // "image/" or "audio/"
	if !strings.HasPrefix(strings.ToLower(mime), want) {
		return fmt.Errorf("%w: mime %q is not %s* for kind %q", ErrInvalidContent, mime, kind, kind)
	}
	return nil
}

// ValidateMediaURL is the SSRF backstop (CWE-918) for a CLIENT-SUPPLIED media
// URL. Because the URL is handed to a REMOTE vision/audio provider that
// DEREFERENCES it — possibly a self-hosted/proxying provider behind the
// provider-agnostic port — an unvalidated URL could reach the cloud metadata
// endpoint or an internal host. It is STRICTER than mcp.ValidateClientURL: there
// is NO plaintext-http-to-loopback allowance, because the consumer is a remote
// service, not a local trusted process.
//
// The contract: the URL must be an absolute "https" URL with a host. A single
// trailing root dot is normalized away first. A literal IP is rejected unless it
// is global-unicast (NOT loopback, private/RFC1918, CGNAT 100.64.0.0/10,
// link-local incl. the 169.254.169.254 metadata IP, unspecified, or multicast).
// A non-canonical inet_aton-style numeric host ("0x7f.0.0.1", "0177.0.0.1",
// "127.1", "2130706433") is rejected — a resolver would decode it to a real
// (often internal) IP that net.ParseIP never canonicalised. A DNS hostname that
// obviously names an internal target ("localhost", a bare single-label name, or
// a ".local"/".internal"/".localhost" suffix) is rejected too.
//
// Residual risk (documented honestly): a PUBLIC https URL can still point at any
// public address, and the provider will fetch it. This validator blocks the
// internal/metadata SSRF shapes; a self-hosted provider that dereferences media
// URLs should ALSO apply network egress controls (deny RFC1918/link-local at the
// provider's own egress) for defense in depth. Inline base64 data is the
// primary, unaffected path and is preferred when the bytes are available.
func ValidateMediaURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("%w: url is required", ErrInvalidMediaURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: %q: %v", ErrInvalidMediaURL, raw, err)
	}
	if !u.IsAbs() {
		return fmt.Errorf("%w: %q must be absolute", ErrInvalidMediaURL, raw)
	}
	if strings.ToLower(u.Scheme) != "https" {
		return fmt.Errorf("%w: %q scheme %q not allowed (https only)", ErrInvalidMediaURL, raw, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: %q has no host", ErrInvalidMediaURL, raw)
	}
	// Normalize a single trailing dot (the DNS root label): "169.254.169.254." and
	// "localhost." would otherwise slip past the checks but a resolver strips the
	// dot and reaches the same internal target.
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return fmt.Errorf("%w: %q has no host", ErrInvalidMediaURL, raw)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !isGlobalUnicast(ip) {
			return fmt.Errorf("%w: %q resolves to a non-global/internal address %s", ErrInvalidMediaURL, raw, ip)
		}
		return nil
	}
	// A host that is NOT a canonical net.ParseIP IP but is "numeric-ish"
	// (all-numeric/dotted, or carries a 0x.../0... octal/hex label) is an
	// inet_aton-style address — e.g. "0x7f.0.0.1", "0177.0.0.1", "127.1",
	// "2130706433". The libc resolver/glibc decodes these to a real (often
	// internal) IP that net.ParseIP never canonicalised, so they would otherwise
	// fall through to the DNS branch unscreened. Reject them outright: a legitimate
	// public host always has an alphabetic TLD, and a legitimate public literal IP
	// already passed the canonical net.ParseIP screen above.
	if isNumericish(host) {
		return fmt.Errorf("%w: %q is a non-canonical numeric (inet_aton-style) host", ErrInvalidMediaURL, raw)
	}
	// DNS hostname: reject obvious-internal forms. We deliberately do NOT resolve
	// the name here (a TOCTOU resolve-then-fetch would be racy and is the
	// provider's egress responsibility per the residual note); we block the
	// clearly-internal shapes that should never be sent to a remote provider.
	if isInternalHostname(host) {
		return fmt.Errorf("%w: %q names an internal host", ErrInvalidMediaURL, raw)
	}
	return nil
}

// cgnatNet is the 100.64.0.0/10 carrier-grade NAT range (RFC 6598). Go's
// net.IP.IsPrivate does NOT cover it, so we screen it explicitly. Parsed once.
var cgnatNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("100.64.0.0/10")
	return n
}()

// nat64WellKnownNet is the RFC 6052 "Well-Known Prefix" 64:ff9b::/96 used by
// NAT64/DNS64 to synthesize an IPv6 address embedding an IPv4 address in its
// low 32 bits (e.g. 64:ff9b::a9fe:a9fe embeds 169.254.169.254). On an
// IPv6-only egress path with NAT64 (common on GKE/EKS), that literal is
// translated back to the embedded IPv4 and dialed — so it must be screened as
// if it were the bare IPv4 address. This covers only the well-known prefix;
// Network-Specific Prefixes (NSPs, RFC 6052 §3.1) are operator-chosen and
// cannot be detected without site configuration, so they remain out of scope.
// Parsed once.
var nat64WellKnownNet = func() *net.IPNet {
	_, n, _ := net.ParseCIDR("64:ff9b::/96")
	return n
}()

// isGlobalUnicast reports whether ip is a routable, public address — i.e. it is
// NOT loopback, link-local (unicast or multicast, which covers 169.254.0.0/16
// and the 169.254.169.254 metadata IP), private/RFC1918, CGNAT (100.64.0.0/10,
// which IsPrivate misses), unspecified, or multicast, or an RFC 6052 NAT64
// well-known-prefix literal whose embedded IPv4 fails this same screen. Only
// such addresses are permitted as a literal-IP media host.
func isGlobalUnicast(ip net.IP) bool {
	if nat64WellKnownNet != nil && nat64WellKnownNet.Contains(ip) {
		// Extract the embedded IPv4 (the low 4 bytes of the 16-byte form) and
		// re-run the SAME screen on it, so an embedded metadata/private/loopback
		// address is rejected exactly as the bare IPv4 would be.
		embedded := ip.To16()[12:16]
		return isGlobalUnicast(embedded)
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return false
	}
	if cgnatNet != nil && cgnatNet.Contains(ip) {
		return false
	}
	return ip.IsGlobalUnicast()
}

// ValidateResolvedIP is the dial-layer SSRF backstop (CWE-918) for a fetch the
// HARNESS itself performs (not a remote provider). ValidateMediaURL screens the
// hostname string but deliberately does NOT resolve DNS (a TOCTOU resolve-then-
// fetch would be racy against a remote provider's egress). When the harness
// itself dials, however, that DNS-rebinding window is live: an attacker-controlled
// resolver can answer ValidateMediaURL's hostname check with a public IP, then
// return 169.254.169.254 (or RFC1918) when the dialer connects. A fetch tool that
// dials directly MUST install a custom DialContext that resolves the hostname and
// calls this on each resolved IP, rejecting any that is not a routable public
// address (the same isGlobalUnicast predicate ValidateMediaURL uses for literal
// IPs). It returns nil for a permitted IP and a non-nil error naming the rejection
// otherwise. Re-exported as the single dial-layer IP predicate so the fetch path
// and the URL-string path share ONE screening definition.
func ValidateResolvedIP(ip net.IP) error {
	if !isGlobalUnicast(ip) {
		return fmt.Errorf("%w: resolved IP %s is not a routable public address", ErrInvalidMediaURL, ip)
	}
	return nil
}

// isNumericish reports whether host is composed ONLY of dot-separated labels
// that are each purely decimal, or a 0x-prefixed hex / 0-prefixed octal token —
// i.e. an inet_aton-style numeric address (whole-integer "2130706433", short
// "127.1", octal "0177.0.0.1", hex "0x7f.0.0.1"). Such a host has no alphabetic
// TLD and is never a legitimate public DNS name; a canonical public literal IP
// is already handled by the net.ParseIP branch before this is consulted, so any
// numeric-ish host reaching here is a non-canonical form to reject. An empty
// host or a label with non-numeric/non-hex characters is NOT numeric-ish.
func isNumericish(host string) bool {
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if !isNumericLabel(label) {
			return false
		}
	}
	return true
}

// isNumericLabel reports whether a single host label is a decimal, hex (0x...),
// or octal (0...) integer token with no alphabetic characters beyond the hex
// digits / the "0x" marker.
func isNumericLabel(label string) bool {
	if label == "" {
		return false
	}
	s := strings.ToLower(label)
	if rest, ok := strings.CutPrefix(s, "0x"); ok {
		if rest == "" {
			return false
		}
		for _, r := range rest {
			if !isHexDigit(r) {
				return false
			}
		}
		return true
	}
	// Decimal or octal: all digits. (Octal "0177" and decimal "127" both qualify;
	// distinguishing them is unnecessary — either way it is a numeric token, not a
	// canonical IP, so it is rejected.)
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isHexDigit reports whether r is a hexadecimal digit (0-9, a-f; s is lowercased
// by the caller).
func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
}

// isInternalHostname reports whether a DNS hostname obviously names an internal
// target that must never be sent to a remote provider: the literal "localhost",
// a bare single-label name (no dot — an intranet short name), or a name ending
// in an internal-only suffix (".local", ".internal", ".localhost"). The caller
// has already stripped a trailing root dot.
func isInternalHostname(host string) bool {
	h := strings.ToLower(host)
	if h == "localhost" {
		return true
	}
	if !strings.Contains(h, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".internal", ".localhost"} {
		if strings.HasSuffix(h, suffix) {
			return true
		}
	}
	return false
}
