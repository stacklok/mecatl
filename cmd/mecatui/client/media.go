package client

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"syscall"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// Client-side media caps. These DUPLICATE the domain caps in
// engine/session/content.go:29-36 (MaxMediaBytes / MaxPromptMediaBytes /
// MaxPromptMediaParts) so the ui can fail fast — before opening a stream — with a
// clear inline error rather than round-tripping a doomed prompt. They are NOT the
// source of truth: the server re-validates every part via session.ValidateMediaParts
// at the wire→domain boundary, so a drift here only changes WHERE the rejection
// surfaces, never WHETHER an oversize prompt is accepted. The ui layer must not
// import engine/session (the ui→no-engine/internal layering rule), hence the copy; the
// drift sentinel TestClientMediaLimitsMatchDomain pins the numbers to the domain.
const (
	maxMediaBytes       = 10 << 20 // 10 MiB — single inline part (== session.MaxMediaBytes)
	maxPromptMediaBytes = 20 << 20 // 20 MiB — sum of all inline parts (== session.MaxPromptMediaBytes)
	maxPromptMediaParts = 16       // count cap (== session.MaxPromptMediaParts)
)

// sniffLen is how many leading bytes http.DetectContentType inspects; it never
// reads past 512 itself, so capping the slice avoids copying a large file's tail
// into the sniff window.
const sniffLen = 512

// MediaResult is the outcome of expanding a prompt's @-mentions: the built media
// parts to send over the wire, a human-readable descriptor per part (for the
// transcript's "📎 …" placeholder lines), and any inlined text-file bodies (each
// already wrapped in a delimited block) the ui splices into the prompt text.
type MediaResult struct {
	// Parts are the proto Content parts to attach to the Prompt frame.
	Parts []*mecatlv1.Content
	// Descriptors mirror Parts 1:1: one human descriptor per media part, e.g.
	// "image/png (inline)". They feed conversation.addUserWithMedia for the 📎 lines.
	Descriptors []string
	// InlineText holds the delimited bodies of @-mentioned TEXT files (no media
	// kind sniffed), in mention order. The ui appends these to the prompt text so
	// the model sees the file content inline (Gemini-style), since text is not a
	// media part.
	InlineText []string
}

// CheckAggregateCaps re-validates the per-PROMPT media aggregate (part count and
// total bytes) over Parts. ExpandMentions already enforces these caps over its own
// mention parts, but the ui appends clipboard / pasted-path parts to the same
// MediaResult AFTER that check, so the COMBINED set can exceed the caps. The ui
// calls this once after merging so a mention-heavy + clipboard-heavy prompt
// loud-rejects client-side (keep input, send nothing) instead of being rejected
// post-send by the server. The per-FILE cap is enforced at construction
// (buildMediaPart), so this only re-checks the two aggregate limits.
func (r MediaResult) CheckAggregateCaps() error {
	if len(r.Parts) > maxPromptMediaParts {
		return fmt.Errorf("too many media attachments (%d, limit %d)", len(r.Parts), maxPromptMediaParts)
	}
	total := 0
	for _, p := range r.Parts {
		total += len(p.GetData())
	}
	if total > maxPromptMediaBytes {
		return fmt.Errorf("media attachments total %d bytes, over the %d-byte prompt limit", total, maxPromptMediaBytes)
	}
	return nil
}

// MentionAttachment separates the local path read by mecatui from the original
// mention spelling shown to the model as attachment provenance.
type MentionAttachment struct {
	// Path is the resolved local filesystem path used only for the client-side read.
	Path string
	// Label is the original path spelling after "@". An empty label falls back to Path.
	Label string
}

// ExpandMentions reads each @-mentioned path (the UI has already lstat-filtered
// these to EXISTING REGULAR FILES; a token that is not a real file stays literal
// prose and never reaches here), sniffs its content type, and routes it to exactly
// one of the three spec outcomes:
//
//   - image/* or audio/* → an inline media Content part (caps-gated and size-capped);
//   - text/*             → inlined into the prompt as a delimited block;
//   - anything else      → a CLEAR ERROR (an explicitly-@'d binary such as a PDF or
//     zip is NOT inlined as raw-byte garbage; the user gets a loud refusal).
//
// It is the SINGLE place proto Content is constructed from a file on the client
// side — the ui passes a resolved local read path plus the original mention label
// and the proto-free Capabilities, keeping the ui free of proto construction.
//
// It is LOUD on any failure (unreadable file, an unsupported file type, a media
// kind the server's provider cannot consume, an oversize part, too many parts, or
// too many total bytes): ANY error returns a zero result and that error, and the
// caller must send NOTHING (loud-reject, never silent-drop). It stays defensive —
// it still errors if handed a path it cannot read — but the UI stat-filter is the
// gate that decides attachment-vs-prose. Per-part and aggregate size caps mirror
// the domain (see the const block); the server re-validates regardless.
func ExpandMentions(mentions []MentionAttachment, caps Capabilities) (MediaResult, error) {
	var res MediaResult
	total := 0
	for _, mention := range mentions {
		label := mention.Label
		if label == "" {
			label = mention.Path
		}
		data, err := readMention(mention.Path, openMentionNoFollow)
		if err != nil {
			return MediaResult{}, fmt.Errorf("read %q: %w", label, err)
		}
		mime := http.DetectContentType(data[:min(sniffLen, len(data))])
		if strings.HasPrefix(mime, "text/") {
			// Not media: inline the text body (a delimited block) into the prompt.
			res.InlineText = append(res.InlineText, inlineTextBlock(label, data))
			continue
		}
		part, desc, err := buildMediaPart(mime, data, caps)
		if err != nil {
			return MediaResult{}, fmt.Errorf("%q: %w", label, err)
		}
		total += len(data)
		res.Parts = append(res.Parts, part)
		res.Descriptors = append(res.Descriptors, desc)
	}
	if len(res.Parts) > maxPromptMediaParts {
		return MediaResult{}, fmt.Errorf("too many media attachments (%d, limit %d)", len(res.Parts), maxPromptMediaParts)
	}
	if total > maxPromptMediaBytes {
		return MediaResult{}, fmt.Errorf("media attachments total %d bytes, over the %d-byte prompt limit", total, maxPromptMediaBytes)
	}
	return res, nil
}

func openMentionNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0) //nolint:gosec // O_NOFOLLOW atomically rejects a raced-in final symlink; O_NONBLOCK avoids blocking on a raced-in FIFO/device.
}

func readMention(path string, open func(string) (*os.File, error)) (_ []byte, retErr error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	f, err := open(path) //nolint:gosec // path is a user-selected local attachment.
	if err != nil {
		return nil, err
	}
	defer func() { retErr = errors.Join(retErr, f.Close()) }()
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("file changed while opening")
	}
	return io.ReadAll(f)
}

// buildMediaPart is the SINGLE proto-construction choke point for a media (image
// or audio) blob: it cap-gates the kind against the server's capabilities,
// enforces the per-file size cap, and builds the inline Content part plus its
// human descriptor. The caller has already established this is NOT text (a text/*
// mime is inlined upstream, never reaches here); a non-media mime (a PDF, a zip)
// is a loud "not a text, image, or audio file" error so an explicitly-attached
// binary is never shipped as raw-byte garbage.
//
// Both ExpandMentions (the @-mention path) and the clipboard/path-staging wrappers
// below route through here, so the cap-gate, the size-cap, and the proto shape are
// defined once. Errors are bare (no %q path prefix): ExpandMentions wraps each with
// its mention path, the clipboard path keeps them as-is.
func buildMediaPart(mime string, data []byte, caps Capabilities) (*mecatlv1.Content, string, error) {
	kind := mediaKind(mime)
	switch kind {
	case mecatlv1.Content_KIND_IMAGE:
		if !caps.Image {
			return nil, "", fmt.Errorf("is an image (%s) but the server's model does not accept images", mime)
		}
	case mecatlv1.Content_KIND_AUDIO:
		if !caps.Audio {
			return nil, "", fmt.Errorf("is audio (%s) but the server's model does not accept audio", mime)
		}
	default: // KIND_UNSPECIFIED: not media (text is handled upstream) → unsupported.
		return nil, "", fmt.Errorf("(%s) is not a text, image, or audio file", mime)
	}
	if len(data) > maxMediaBytes {
		return nil, "", fmt.Errorf("is %d bytes, over the %d-byte per-file limit", len(data), maxMediaBytes)
	}
	return &mecatlv1.Content{Kind: kind, MimeType: mime, Data: data}, mime + " (inline)", nil
}

// StagePathMedia reads a file the UI wants to attach via a drag-and-drop / pasted
// path, sniffs it, and runs it through the SAME sniff/cap/size logic as an
// @-mention — returning the sniffed mime, the raw bytes, and the human descriptor
// for a successful media (image/audio) attachment. It errors for anything that is
// not a media attachment: a text file (the path-paste branch wants a real media
// file, not an inline-text body — text falls through to literal paste), an
// unsupported binary, a cap-gated kind, an oversize file, or an unreadable path.
// The UI uses this for the pasted-image-PATH branch (it stats for a fast reject,
// then calls here for the read + sniff + build); on ANY error the UI falls back to
// inserting the path literally. net/http + proto stay here in client.
func StagePathMedia(path string, caps Capabilities) (mime string, data []byte, descriptor string, err error) {
	data, err = os.ReadFile(path) //nolint:gosec // path is a user-pasted file path the user chose to attach; reading it is the feature.
	if err != nil {
		return "", nil, "", fmt.Errorf("read %q: %w", path, err)
	}
	mime = http.DetectContentType(data[:min(sniffLen, len(data))])
	if strings.HasPrefix(mime, "text/") {
		return "", nil, "", fmt.Errorf("%q (%s) is a text file, not a media attachment", path, mime)
	}
	_, descriptor, err = buildMediaPart(mime, data, caps)
	if err != nil {
		return "", nil, "", fmt.Errorf("%q: %w", path, err)
	}
	return mime, data, descriptor, nil
}

// StageClipboardImage rebuilds clipboard bytes the UI staged (proto-free, as a
// mime+data pair under an "[Image #N]" marker) into an inline media Content part
// at SUBMIT time, applying the same cap-gate + size-cap as every other path. The
// UI appends the returned part to its opaque media.Parts slice and the descriptor
// to media.Descriptors without ever naming the proto type. It is the submit-side
// counterpart to onClipboardPaste's staging.
func StageClipboardImage(mime string, data []byte, caps Capabilities) (*mecatlv1.Content, string, error) {
	return buildMediaPart(mime, data, caps)
}

// mediaKind maps a sniffed MIME type to a proto Content kind: image/* → IMAGE,
// audio/* → AUDIO, anything else → KIND_UNSPECIFIED (the caller distinguishes text
// from unsupported). mime may carry a "; charset=…" suffix (http.DetectContentType
// adds one for text), so it is matched by prefix.
func mediaKind(mime string) mecatlv1.Content_Kind {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return mecatlv1.Content_KIND_IMAGE
	case strings.HasPrefix(mime, "audio/"):
		return mecatlv1.Content_KIND_AUDIO
	default:
		return mecatlv1.Content_KIND_UNSPECIFIED
	}
}

// inlineTextBlock wraps a text file's content in a labelled, fenced block so the
// model sees it as an attachment with provenance rather than ambiguous prose
// blended into the prompt. The path is the user-typed mention.
func inlineTextBlock(path string, data []byte) string {
	return fmt.Sprintf("--- %s ---\n%s\n--- end %s ---", path, string(data), path)
}
