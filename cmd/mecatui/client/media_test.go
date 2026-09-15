package client

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// readFixturePNG loads the checked-in 1×1 PNG fixture; http.DetectContentType
// sniffs it as image/png (asserted by the image test below).
func readFixturePNG(t *testing.T) []byte {
	t.Helper()
	return readFixture(t, "pixel.png")
}

// readFixtureWAV loads the checked-in minimal WAV fixture; http.DetectContentType
// sniffs a RIFF/WAVE header as "audio/wave".
func readFixtureWAV(t *testing.T) []byte {
	t.Helper()
	return readFixture(t, "clip.wav")
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func mentionAttachments(paths ...string) []MentionAttachment {
	out := make([]MentionAttachment, len(paths))
	for i, path := range paths {
		out[i] = MentionAttachment{Path: path, Label: path}
	}
	return out
}

// TestExpandMentionsImagePart asserts an @-mentioned image file, with an
// image-capable server, becomes one inline image Content part: kind IMAGE, the
// sniffed image/png MIME, the exact file bytes, a "(inline)" descriptor, and no
// inlined text.
func TestExpandMentionsImagePart(t *testing.T) {
	png := readFixturePNG(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{Image: true})
	if err != nil {
		t.Fatalf("ExpandMentions: %v", err)
	}
	if len(res.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(res.Parts))
	}
	p := res.Parts[0]
	if p.GetKind() != mecatlv1.Content_KIND_IMAGE {
		t.Errorf("kind = %v, want KIND_IMAGE", p.GetKind())
	}
	if p.GetMimeType() != "image/png" {
		t.Errorf("mime = %q, want image/png", p.GetMimeType())
	}
	if !bytes.Equal(p.GetData(), png) {
		t.Errorf("data mismatch: got %d bytes, want %d", len(p.GetData()), len(png))
	}
	if len(res.Descriptors) != 1 || res.Descriptors[0] != "image/png (inline)" {
		t.Errorf("descriptors = %v, want [image/png (inline)]", res.Descriptors)
	}
	if len(res.InlineText) != 0 {
		t.Errorf("inline text = %v, want none", res.InlineText)
	}
}

// TestExpandMentionsImageCapGated asserts an image mention is REFUSED (loud error,
// zero parts) when the server's provider does not accept images.
func TestExpandMentionsImageCapGated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, readFixturePNG(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{Image: false})
	if err == nil {
		t.Fatalf("want error for image with Image:false, got parts=%v", res.Parts)
	}
	if !strings.Contains(err.Error(), "does not accept images") {
		t.Errorf("error = %q, want it to explain the image cap", err)
	}
	if len(res.Parts) != 0 {
		t.Errorf("parts = %d, want 0 on refusal", len(res.Parts))
	}
}

// TestExpandMentionsOversizeRefused asserts a single file over the per-file cap is
// refused with no part produced. The bytes start with the PNG signature so they
// sniff as image/png (taking the media branch where the cap is enforced).
func TestExpandMentionsOversizeRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	big := make([]byte, maxMediaBytes+1)
	copy(big, readFixturePNG(t)) // PNG signature → sniffed as image/png
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{Image: true})
	if err == nil {
		t.Fatalf("want oversize error, got parts=%v", res.Parts)
	}
	if !strings.Contains(err.Error(), "per-file limit") {
		t.Errorf("error = %q, want a per-file-limit message", err)
	}
	if len(res.Parts) != 0 {
		t.Errorf("parts = %d, want 0", len(res.Parts))
	}
}

// TestExpandMentionsTextInlined asserts a non-media file is inlined as a delimited
// text block (no media part, no descriptor) — caps are irrelevant for text.
func TestExpandMentionsTextInlined(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	body := "hello from a text file"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{}) // no media caps
	if err != nil {
		t.Fatalf("ExpandMentions: %v", err)
	}
	if len(res.Parts) != 0 {
		t.Fatalf("parts = %d, want 0 for a text file", len(res.Parts))
	}
	if len(res.InlineText) != 1 {
		t.Fatalf("inline text blocks = %d, want 1", len(res.InlineText))
	}
	block := res.InlineText[0]
	if !strings.Contains(block, body) {
		t.Errorf("inline block missing body: %q", block)
	}
	if !strings.Contains(block, path) {
		t.Errorf("inline block missing path provenance: %q", block)
	}
}

func TestExpandMentionsUsesOriginalLabel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("labelled content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	res, err := ExpandMentions([]MentionAttachment{{Path: path, Label: "~/notes.txt"}}, Capabilities{})
	if err != nil {
		t.Fatalf("ExpandMentions: %v", err)
	}
	if len(res.InlineText) != 1 || !strings.Contains(res.InlineText[0], "--- ~/notes.txt ---") {
		t.Fatalf("inline text = %q, want original mention label", res.InlineText)
	}
	if strings.Contains(res.InlineText[0], dir) {
		t.Fatalf("inline text leaks resolved directory %q: %q", dir, res.InlineText[0])
	}
}

func TestExpandMentionsRejectsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := ExpandMentions([]MentionAttachment{{Path: link, Label: "link.txt"}}, Capabilities{}); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want final-symlink rejection", err)
	}
}

func TestOpenMentionNoFollowRejectsFinalSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.txt")
	link := filepath.Join(dir, "link.txt")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	f, err := openMentionNoFollow(link)
	if err == nil {
		_ = f.Close()
		t.Fatal("openMentionNoFollow followed a final symlink")
	}
}

func TestReadMentionRejectsIdentityReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	oldPath := filepath.Join(dir, "old.txt")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	openAfterReplace := func(name string) (*os.File, error) {
		if err := os.Rename(name, oldPath); err != nil {
			return nil, err
		}
		if err := os.WriteFile(name, []byte("replacement"), 0o600); err != nil {
			return nil, err
		}
		return os.Open(name)
	}
	if _, err := readMention(path, openAfterReplace); err == nil || !strings.Contains(err.Error(), "changed while opening") {
		t.Fatalf("error = %v, want identity-change rejection", err)
	}
}

// TestExpandMentionsTooManyParts asserts the per-prompt part-count cap rejects a
// flood of small image mentions.
func TestExpandMentionsTooManyParts(t *testing.T) {
	dir := t.TempDir()
	png := readFixturePNG(t)
	paths := make([]string, maxPromptMediaParts+1)
	for i := range paths {
		p := filepath.Join(dir, "img"+string(rune('a'+i))+".png")
		if err := os.WriteFile(p, png, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		paths[i] = p
	}

	if _, err := ExpandMentions(mentionAttachments(paths...), Capabilities{Image: true}); err == nil {
		t.Fatal("want too-many-parts error")
	} else if !strings.Contains(err.Error(), "too many media attachments") {
		t.Errorf("error = %q, want a too-many-attachments message", err)
	}
}

// TestExpandMentionsTotalBytes asserts the aggregate-bytes cap rejects several
// parts that are each under the per-file cap but together exceed the prompt cap.
// Each part is half the per-file cap, so 3 of them (15 MiB) exceed the 20 MiB
// prompt cap... actually 2.x do; use enough to cross 20 MiB while each stays under
// the 10 MiB per-file cap.
func TestExpandMentionsTotalBytes(t *testing.T) {
	dir := t.TempDir()
	png := readFixturePNG(t)
	// Each ~8 MiB (< 10 MiB per-file cap); three total ~24 MiB > 20 MiB prompt cap.
	part := make([]byte, 8<<20)
	copy(part, png)
	var paths []string
	for i := 0; i < 3; i++ {
		p := filepath.Join(dir, "big"+string(rune('a'+i))+".png")
		if err := os.WriteFile(p, part, 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		paths = append(paths, p)
	}

	if _, err := ExpandMentions(mentionAttachments(paths...), Capabilities{Image: true}); err == nil {
		t.Fatal("want total-bytes error")
	} else if !strings.Contains(err.Error(), "prompt limit") {
		t.Errorf("error = %q, want a prompt-limit message", err)
	}
}

// TestExpandMentionsUnreadable asserts a missing file is a loud error (never a
// silent skip).
func TestExpandMentionsUnreadable(t *testing.T) {
	if _, err := ExpandMentions(mentionAttachments(filepath.Join(t.TempDir(), "nope.png")), Capabilities{Image: true}); err == nil {
		t.Fatal("want read error for a missing file")
	}
}

// TestExpandMentionsAudioPart asserts an @-mentioned audio file, with an
// audio-capable server, becomes one inline audio Content part: kind AUDIO, the
// sniffed audio/wave MIME, the exact file bytes, a "(inline)" descriptor, no text.
func TestExpandMentionsAudioPart(t *testing.T) {
	wav := readFixtureWAV(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")
	if err := os.WriteFile(path, wav, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{Audio: true})
	if err != nil {
		t.Fatalf("ExpandMentions: %v", err)
	}
	if len(res.Parts) != 1 {
		t.Fatalf("parts = %d, want 1", len(res.Parts))
	}
	p := res.Parts[0]
	if p.GetKind() != mecatlv1.Content_KIND_AUDIO {
		t.Errorf("kind = %v, want KIND_AUDIO", p.GetKind())
	}
	if p.GetMimeType() != "audio/wave" {
		t.Errorf("mime = %q, want audio/wave", p.GetMimeType())
	}
	if !bytes.Equal(p.GetData(), wav) {
		t.Errorf("data mismatch: got %d bytes, want %d", len(p.GetData()), len(wav))
	}
	if len(res.Descriptors) != 1 || res.Descriptors[0] != "audio/wave (inline)" {
		t.Errorf("descriptors = %v, want [audio/wave (inline)]", res.Descriptors)
	}
	if len(res.InlineText) != 0 {
		t.Errorf("inline text = %v, want none", res.InlineText)
	}
}

// TestExpandMentionsAudioCapGated asserts an audio mention is REFUSED (loud error,
// zero parts) when the server's provider does not accept audio.
func TestExpandMentionsAudioCapGated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clip.wav")
	if err := os.WriteFile(path, readFixtureWAV(t), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{Audio: false})
	if err == nil {
		t.Fatalf("want error for audio with Audio:false, got parts=%v", res.Parts)
	}
	if !strings.Contains(err.Error(), "does not accept audio") {
		t.Errorf("error = %q, want it to explain the audio cap", err)
	}
	if len(res.Parts) != 0 {
		t.Errorf("parts = %d, want 0 on refusal", len(res.Parts))
	}
}

// TestExpandMentionsUnsupportedFileRefused asserts an existing-but-unsupported file
// (a PDF — sniffs as application/pdf, neither text nor media) is a LOUD error and is
// NOT inlined as raw-byte garbage. The UI stat-filter passes a real file here, so
// ExpandMentions is the one that must reject the unsupported type.
func TestExpandMentionsUnsupportedFileRefused(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(path, readFixture(t, "doc.pdf"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := ExpandMentions(mentionAttachments(path), Capabilities{Image: true, Audio: true})
	if err == nil {
		t.Fatalf("want unsupported-type error, got parts=%v inline=%v", res.Parts, res.InlineText)
	}
	if !strings.Contains(err.Error(), "not a text, image, or audio file") {
		t.Errorf("error = %q, want the unsupported-type message", err)
	}
	if len(res.Parts) != 0 || len(res.InlineText) != 0 {
		t.Errorf("unsupported file leaked: parts=%v inline=%v", res.Parts, res.InlineText)
	}
}

// TestBuildMediaPartImage directly unit-tests the extracted proto-construction
// choke point: an image with the cap on yields an IMAGE part + "(inline)" desc.
func TestBuildMediaPartImage(t *testing.T) {
	png := readFixturePNG(t)
	part, desc, err := buildMediaPart("image/png", png, Capabilities{Image: true})
	if err != nil {
		t.Fatalf("buildMediaPart: %v", err)
	}
	if part.GetKind() != mecatlv1.Content_KIND_IMAGE || part.GetMimeType() != "image/png" {
		t.Errorf("part = %v, want IMAGE image/png", part)
	}
	if !bytes.Equal(part.GetData(), png) {
		t.Errorf("data mismatch")
	}
	if desc != "image/png (inline)" {
		t.Errorf("desc = %q, want image/png (inline)", desc)
	}
}

// TestBuildMediaPartCapGated asserts buildMediaPart refuses an image when the cap
// is off, with a message that survives ExpandMentions' %q wrap.
func TestBuildMediaPartCapGated(t *testing.T) {
	_, _, err := buildMediaPart("image/png", readFixturePNG(t), Capabilities{Image: false})
	if err == nil {
		t.Fatal("want cap-gated error")
	}
	if !strings.Contains(err.Error(), "does not accept images") {
		t.Errorf("error = %q, want the image-cap message", err)
	}
}

// TestBuildMediaPartOversize asserts the per-file cap is enforced in the choke
// point itself (so every caller — @-mention, clipboard, path-paste — shares it).
func TestBuildMediaPartOversize(t *testing.T) {
	big := make([]byte, maxMediaBytes+1)
	copy(big, readFixturePNG(t))
	_, _, err := buildMediaPart("image/png", big, Capabilities{Image: true})
	if err == nil {
		t.Fatal("want oversize error")
	}
	if !strings.Contains(err.Error(), "per-file limit") {
		t.Errorf("error = %q, want the per-file-limit message", err)
	}
}

// TestStagePathMediaImage asserts the path-staging wrapper reads, sniffs, and
// returns an image attachment's mime/data/descriptor.
func TestStagePathMediaImage(t *testing.T) {
	dir := t.TempDir()
	png := readFixturePNG(t)
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mime, data, desc, err := StagePathMedia(path, Capabilities{Image: true})
	if err != nil {
		t.Fatalf("StagePathMedia: %v", err)
	}
	if mime != "image/png" || !bytes.Equal(data, png) || desc != "image/png (inline)" {
		t.Errorf("got mime=%q desc=%q data=%d bytes", mime, desc, len(data))
	}
}

// TestStagePathMediaTextRejected asserts a text file is NOT a media attachment via
// the path-paste branch (it falls back to a literal paste in the ui).
func TestStagePathMediaTextRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(path, []byte("plain text"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, _, _, err := StagePathMedia(path, Capabilities{Image: true}); err == nil {
		t.Fatal("want error for a text file via the media-path branch")
	}
}

// TestCheckAggregateCapsCount asserts the re-check (used after the ui merges
// clipboard parts into a MediaResult) rejects an over-count part set and passes a
// within-limit one — the per-prompt count cap.
func TestCheckAggregateCapsCount(t *testing.T) {
	mk := func(n int) MediaResult {
		var r MediaResult
		for i := 0; i < n; i++ {
			r.Parts = append(r.Parts, &mecatlv1.Content{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{1}})
		}
		return r
	}
	if err := mk(maxPromptMediaParts).CheckAggregateCaps(); err != nil {
		t.Errorf("at the limit should pass: %v", err)
	}
	err := mk(maxPromptMediaParts + 1).CheckAggregateCaps()
	if err == nil || !strings.Contains(err.Error(), "too many media attachments") {
		t.Errorf("over the count cap should report too-many: %v", err)
	}
}

// TestCheckAggregateCapsBytes asserts the total-bytes re-check rejects a combined
// set over the prompt byte cap (each part is under the per-file cap).
func TestCheckAggregateCapsBytes(t *testing.T) {
	var r MediaResult
	for i := 0; i < 3; i++ { // 3 × 8 MiB = 24 MiB > 20 MiB prompt cap
		r.Parts = append(r.Parts, &mecatlv1.Content{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: make([]byte, 8<<20)})
	}
	err := r.CheckAggregateCaps()
	if err == nil || !strings.Contains(err.Error(), "prompt limit") {
		t.Errorf("over the byte cap should report the prompt-limit: %v", err)
	}
}

// TestClientMediaLimitsMatchDomain is the drift sentinel: the client-side fail-
// fast caps MUST equal the domain caps in engine/session/content.go. They are
// duplicated (the ui→no-engine/internal layering forbids importing session), so this
// pins the numbers; if the domain caps change, update both and this test.
func TestClientMediaLimitsMatchDomain(t *testing.T) {
	if maxMediaBytes != 10<<20 {
		t.Errorf("maxMediaBytes = %d, want 10<<20 (session.MaxMediaBytes)", maxMediaBytes)
	}
	if maxPromptMediaBytes != 20<<20 {
		t.Errorf("maxPromptMediaBytes = %d, want 20<<20 (session.MaxPromptMediaBytes)", maxPromptMediaBytes)
	}
	if maxPromptMediaParts != 16 {
		t.Errorf("maxPromptMediaParts = %d, want 16 (session.MaxPromptMediaParts)", maxPromptMediaParts)
	}
}
