package port

import (
	"bytes"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

// TestRouteToolResultPartsCapabilityGated is the core T6 contract: the
// composition-driven projection drops image blocks for a text-only provider,
// keeps them for an image-capable one, gates audio on the audio capability,
// and passes text / resource-link / embedded-resource / structured-content
// unconditionally. Audience (untrusted self-attestation, CWE-345) NEVER
// suppresses a block.
func TestRouteToolResultPartsCapabilityGated(t *testing.T) {
	img, err := session.NewImageContent("image/png", []byte{1, 2, 3})
	if err != nil {
		t.Fatalf("NewImageContent: %v", err)
	}
	img.BlockKind = session.BlockImage

	audio, err := session.NewAudioContent("audio/wav", []byte{4, 5, 6})
	if err != nil {
		t.Fatalf("NewAudioContent: %v", err)
	}
	audio.BlockKind = session.BlockAudio

	audienceBlock := session.NewResourceLinkBlock(
		"https://example.com/res", "name", "title", "desc", "text/plain", 7,
		[]string{"user"}) // untrusted audience — must NOT be filtered

	embedded, err := session.NewEmbeddedResourceBlock(
		"https://example.com/blob", "application/octet-stream", "blob text", nil, []string{"assistant"})
	if err != nil {
		t.Fatalf("NewEmbeddedResourceBlock: %v", err)
	}
	embeddedBlob, err := session.NewEmbeddedResourceBlock(
		"https://example.com/blob2", "application/octet-stream", "", []byte{9, 9, 9}, nil)
	if err != nil {
		t.Fatalf("NewEmbeddedResourceBlock blob: %v", err)
	}
	legacyMedia, err := session.NewImageContent("image/png", []byte{7, 7, 7})
	if err != nil {
		t.Fatalf("NewImageContent legacy: %v", err)
	}
	// legacyMedia keeps BlockKind == "" (the user-message media shape, not a
	// tool-result block); it must be DROPPED by the tool-result projection.

	allParts := []session.Content{
		session.NewTextBlock("hello"),
		img,
		audio,
		audienceBlock,
		embedded,
		embeddedBlob,
		legacyMedia,
		session.NewStructuredContentBlock(`{"k":"v"}`),
	}

	textOnly := ProviderCapabilities{}            // Image=false, Audio=false
	imageCap := ProviderCapabilities{Image: true} // Audio=false
	audioCap := ProviderCapabilities{Audio: true} // Image=false
	full := ProviderCapabilities{Image: true, Audio: true}

	// text-only: image + audio + legacy-media dropped; text, resource-link (incl.
	// the audience-tagged one), embedded-resource (text + blob), structured kept.
	got := RouteToolResultParts(session.ToolResult{Parts: allParts}, textOnly)
	want := []session.Content{
		allParts[0], // text
		allParts[3], // resource-link (audience-tagged — survives)
		allParts[4], // embedded-resource text
		allParts[5], // embedded-resource blob
		allParts[7], // structured
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("text-only projection mismatch:\ngot  = %v\nwant = %v", got, want)
	}
	if blockHasAudience(got, allParts[3]) && !contains(got, audienceBlock) {
		t.Fatalf("audience-tagged resource-link was suppressed for a text-only provider; audience must never filter")
	}

	// image-capable: image kept, audio still dropped.
	got = RouteToolResultParts(session.ToolResult{Parts: allParts}, imageCap)
	if !contains(got, img) {
		t.Fatalf("image block dropped for an image-capable provider")
	}
	if contains(got, audio) {
		t.Fatalf("audio block kept for an audio-incapable provider")
	}
	if contains(got, legacyMedia) {
		t.Fatalf("legacy media part kept by the tool-result projection")
	}

	// audio-capable (no image): audio kept, image dropped.
	got = RouteToolResultParts(session.ToolResult{Parts: allParts}, audioCap)
	if contains(got, img) {
		t.Fatalf("image block kept for an image-incapable provider")
	}
	if !contains(got, audio) {
		t.Fatalf("audio block dropped for an audio-capable provider")
	}

	// full: both media blocks survive (legacy media still dropped).
	got = RouteToolResultParts(session.ToolResult{Parts: allParts}, full)
	if !contains(got, img) || !contains(got, audio) {
		t.Fatalf("media blocks dropped for a fully-capable provider")
	}
	if contains(got, legacyMedia) {
		t.Fatalf("legacy media part kept by the tool-result projection")
	}
	// audience-tagged resource-link survives under full caps too.
	if !contains(got, audienceBlock) {
		t.Fatalf("audience-tagged resource-link dropped under full caps; audience must never filter")
	}
}

// TestRouteToolResultPartsReadOnlyProjection asserts the CRITICAL invariant
// (Risk #1): the projection never mutates the recorded *session.ToolResult —
// neither the slice header nor any element. The input is a shared backing array
// the session holds; a write back would pollute persisted history and break the
// recorded-history == client-stream == model-view guarantee.
func TestRouteToolResultPartsReadOnlyProjection(t *testing.T) {
	img, _ := session.NewImageContent("image/png", []byte{1, 2, 3})
	img.BlockKind = session.BlockImage
	parts := []session.Content{session.NewTextBlock("t"), img, session.NewTextBlock("t2")}
	tr := session.ToolResult{CallID: "c", Content: "fallback", Parts: parts}

	// Snapshot the input before projection.
	origParts := make([]session.Content, len(tr.Parts))
	copy(origParts, tr.Parts)
	origContent := tr.Content
	origCallID := tr.CallID

	got := RouteToolResultParts(tr, ProviderCapabilities{}) // text-only drops the image

	// The recorded ToolResult is untouched.
	if !reflect.DeepEqual(tr.Parts, origParts) {
		t.Fatalf("projection mutated tr.Parts:\ngot  = %v\nwant = %v", tr.Parts, origParts)
	}
	if tr.Content != origContent || tr.CallID != origCallID {
		t.Fatalf("projection mutated tr.Content/CallID")
	}
	// The returned slice is a fresh array, not an alias of the input backing store
	// that the session would observe: it is a subset (2 of 3), and its element
	// payloads are the immutable value copies. Mutating the OUTPUT must not reach
	// the input.
	if len(got) != 2 {
		t.Fatalf("text-only projection len = %d, want 2", len(got))
	}
	got[0].Text = "MUTATED"
	if tr.Parts[0].Text == "MUTATED" {
		t.Fatalf("mutating the output reached the recorded input slice (aliasing bug)")
	}
	// And the image block data buffer in the input is untouched.
	if !bytes.Equal(tr.Parts[1].Data, []byte{1, 2, 3}) {
		t.Fatalf("projection mutated the input image block data")
	}
}

// TestRouteToolResultPartsNilEmptyDegrades asserts nil/empty Parts returns nil
// (the caller degrades to the model-facing Content string).
func TestRouteToolResultPartsNilEmptyDegrades(t *testing.T) {
	if got := RouteToolResultParts(session.ToolResult{}, ProviderCapabilities{Image: true}); got != nil {
		t.Fatalf("nil Parts returned non-nil: %v", got)
	}
	if got := RouteToolResultParts(session.ToolResult{Parts: nil}, ProviderCapabilities{Image: true}); got != nil {
		t.Fatalf("nil Parts field returned non-nil: %v", got)
	}
	if got := RouteToolResultParts(session.ToolResult{Parts: []session.Content{}}, ProviderCapabilities{Image: true}); got != nil {
		t.Fatalf("empty Parts returned non-nil: %v", got)
	}
	// A result whose only block is dropped (image under a text-only provider) also
	// degrades to nil so the caller falls back to Content.
	img, _ := session.NewImageContent("image/png", []byte{1})
	img.BlockKind = session.BlockImage
	if got := RouteToolResultParts(session.ToolResult{Content: "fallback", Parts: []session.Content{img}}, ProviderCapabilities{}); got != nil {
		t.Fatalf("all-dropped projection returned non-nil (must degrade to Content): %v", got)
	}
}

// TestRouteToolResultPartsAudienceNeverSuppresses drills the CWE-345 invariant
// directly: a block carrying an audience list that claims a restricted audience
// still passes to the model under every capability set, because audience is
// advisory display routing only — never an access-control gate.
func TestRouteToolResultPartsAudienceNeverSuppresses(t *testing.T) {
	restricted := session.NewResourceLinkBlock(
		"https://example.com/secret", "n", "t", "d", "text/plain", 1, []string{"assistant", "llm"})
	for _, caps := range []ProviderCapabilities{
		{},
		{Image: true},
		{Audio: true},
		{Image: true, Audio: true, EmbeddedContext: true},
	} {
		got := RouteToolResultParts(session.ToolResult{Parts: []session.Content{restricted}}, caps)
		if len(got) != 1 || !reflect.DeepEqual(got[0], restricted) {
			t.Fatalf("caps=%v: audience-restricted block was filtered (got %v); audience must never suppress", caps, got)
		}
	}
}

// TestRouteToolResultPartsUnknownBlockKindDropped asserts the fail-CLOSED
// default: a block carrying an unrecognised BlockKind (a future/unknown kind
// this build doesn't know about) is DROPPED from the projection rather than
// falling through ungated, while a known block alongside it survives.
func TestRouteToolResultPartsUnknownBlockKindDropped(t *testing.T) {
	unknown := session.Content{BlockKind: session.BlockKind("future_kind_xyz"), Text: "mystery"}
	known := session.NewTextBlock("hello")

	got := RouteToolResultParts(session.ToolResult{Parts: []session.Content{known, unknown}}, ProviderCapabilities{Image: true, Audio: true})
	if contains(got, unknown) {
		t.Fatalf("unknown BlockKind was not dropped: %v", got)
	}
	if !contains(got, known) {
		t.Fatalf("known block alongside an unknown one was dropped: %v", got)
	}
	if len(got) != 1 {
		t.Fatalf("projection len = %d, want 1 (only the known block survives)", len(got))
	}
}

// TestRouteToolResultPartsEmptyTextDropped is the regression for the Moonshot /
// OpenRouter 400 "text content is empty" brick: a tool result whose only Part is
// a single empty-text BlockText renders to no surviving block, so the projection
// returns nil and the caller degrades to the (placeholder-guarded) Content
// string. A mixed empty + non-empty result keeps only the non-empty blocks.
func TestRouteToolResultPartsEmptyTextDropped(t *testing.T) {
	// Sole empty-text block → nil (degrade to the single-string fallback).
	emptyOnly := session.ToolResult{
		Content: "fallback",
		Parts:   []session.Content{session.NewTextBlock("")},
	}
	if got := RouteToolResultParts(emptyOnly, ProviderCapabilities{Image: true, Audio: true}); got != nil {
		t.Fatalf("sole empty-text block not dropped (would put an empty text block on the wire): %v", got)
	}

	// Mixed: an empty text block, an empty structured block, and a non-empty text
	// block → only the non-empty one survives.
	nonEmpty := session.NewTextBlock("real output")
	mixed := session.ToolResult{Parts: []session.Content{
		session.NewTextBlock(""),
		session.NewStructuredContentBlock(""),
		nonEmpty,
	}}
	got := RouteToolResultParts(mixed, ProviderCapabilities{})
	if len(got) != 1 || !reflect.DeepEqual(got[0], nonEmpty) {
		t.Fatalf("mixed empty/non-empty projection = %v, want only the non-empty block", got)
	}
}

func contains(haystack []session.Content, needle session.Content) bool {
	for _, h := range haystack {
		if reflect.DeepEqual(h, needle) {
			return true
		}
	}
	return false
}

func blockHasAudience(haystack []session.Content, needle session.Content) bool {
	for _, h := range haystack {
		if reflect.DeepEqual(h, needle) {
			return len(h.Audience) > 0
		}
	}
	return false
}
