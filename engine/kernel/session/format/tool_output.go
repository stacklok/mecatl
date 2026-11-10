package format

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

// ToolEmbeddedResource retains a reference and exactly one inline body. Empty
// text/blob preserves presence; zero is invalid. Assignment shares nested storage;
// Clone detaches it. Reference metadata grants no dereference authority.
//
//dtogen:validate validateLocal
type ToolEmbeddedResource struct {
	// Resource is required reference metadata, independent of retained body size.
	Resource ToolResource `cbor:"r"`
	// Text optionally retains exact UTF-8 text, at most 4194304 bytes, exclusive with Blob.
	Text *string `cbor:"t,omitempty" cborlimit:"bytes=4194304"`
	// Blob optionally retains opaque bytes, at most 4194304, exclusive with Text.
	// A nonnil pointer to nil or empty bytes is a present empty body.
	Blob *[]byte `cbor:"b,omitempty" cborlimit:"bytes=4194304"`
}

// Validate checks body presence and bounds and reference metadata.
func (v ToolEmbeddedResource) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	return v.Resource.Validate()
}

// validateLocal checks immediate body bounds before scanning text.
func (v ToolEmbeddedResource) validateLocal() error {
	if (v.Text == nil) == (v.Blob == nil) {
		return errors.New("embedded resource requires exactly one body")
	}
	if v.Text != nil && (len(*v.Text) > 4194304 || !utf8.ValidString(*v.Text)) {
		return errors.New("embedded text requires bounded UTF-8")
	}
	if v.Blob != nil && len(*v.Blob) > 4194304 {
		return errors.New("embedded blob exceeds schema bound")
	}
	return nil
}

// ToolOutputPart is one canonical ordered output occurrence with exactly one
// branch. JSON may coexist with ordinary content as distinct parts, not duplicate
// structured-result/text fallbacks. Assignment shares storage; Clone detaches it.
// Zero is invalid. Retention itself grants no disclosure or execution permission.
//
//dtogen:validate validateLocal
type ToolOutputPart struct {
	// Content optionally retains existing text/media leaves, under shared codec and
	// enclosing payload/commit budgets rather than new leaf schema bounds.
	Content *ContentPart `cbor:"c,omitempty"`
	// JSON optionally retains exact valid UTF-8 JSON syntax, at most 4194304 bytes.
	// It is not normalized, reencoded or schema-interpreted; present empty is invalid.
	JSON *[]byte `cbor:"j,omitempty" cborlimit:"bytes=4194304"`
	// ResourceLink optionally retains reference metadata without fetching its body.
	ResourceLink *ToolResource `cbor:"l,omitempty"`
	// Resource optionally retains reference metadata and one inline body.
	Resource *ToolEmbeddedResource `cbor:"r,omitempty"`
}

// Validate checks branch exclusivity, bounded exact JSON and all children.
func (v ToolOutputPart) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	if v.Content != nil {
		if err := v.Content.Validate(); err != nil {
			return err
		}
	}
	if v.ResourceLink != nil {
		if err := v.ResourceLink.Validate(); err != nil {
			return err
		}
	}
	if v.Resource != nil {
		if err := v.Resource.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateLocal bounds JSON before UTF-8 and syntax scans; it never validates children.
func (v ToolOutputPart) validateLocal() error {
	n := 0
	if v.Content != nil {
		n++
	}
	if v.JSON != nil {
		n++
	}
	if v.ResourceLink != nil {
		n++
	}
	if v.Resource != nil {
		n++
	}
	if n != 1 {
		return errors.New("tool output part requires exactly one branch")
	}
	if v.JSON != nil {
		if len(*v.JSON) > 4194304 {
			return errors.New("tool JSON exceeds schema bound")
		}
		if !utf8.Valid(*v.JSON) || !json.Valid(*v.JSON) {
			return errors.New("tool JSON requires valid UTF-8 JSON syntax")
		}
	}
	return nil
}

// ToolCaptureState is a closed capture-completeness vocabulary, not tool outcome.
// Empty and unknown states are invalid.
type ToolCaptureState string

const (
	// ToolCaptureComplete records complete terminal output, which may be empty.
	ToolCaptureComplete ToolCaptureState = "complete"
	// ToolCaptureTruncated records incomplete capture of whole parts, possibly zero
	// parts or a non-prefix subset; retained order remains canonical.
	ToolCaptureTruncated ToolCaptureState = "truncated"
	// ToolCaptureOmitted records no retained parts, not necessarily no original output.
	ToolCaptureOmitted ToolCaptureState = "omitted"
)

// ToolOutput retains ordered canonical whole parts. Assignment shares mutable
// storage; Clone detaches it. Zero is invalid. Schema caps do not replace shared
// codec or enclosing session per-payload/commit policy. Interrupted part capture
// needs an explicit future representation; size/count accounting and producer
// observations likewise need typed ancillary records, not extension fields.
//
//dtogen:validate validateLocal
type ToolOutput struct {
	// Capture is required, closed-vocabulary and at most 32 UTF-8 bytes.
	Capture ToolCaptureState `cbor:"c" cborlimit:"bytes=32"`
	// Parts retains canonical order, boundaries and duplicates, at most 4096 whole
	// parts. Nil and empty are equivalent; omitted capture requires empty parts.
	// Truncated capture may retain zero or a non-prefix subset in canonical order.
	Parts []ToolOutputPart `cbor:"p" cborlimit:"items=4096"`
}

// Validate checks capture and all retained whole parts, bounding part count before
// traversal. Reused ContentPart/Media retain their own contracts.
func (v ToolOutput) Validate() error {
	if err := v.validateLocal(); err != nil {
		return err
	}
	for _, part := range v.Parts {
		if err := part.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateLocal checks immediate state using only bounded length inspection of
// children, not recursive validation.
func (v ToolOutput) validateLocal() error {
	if len(v.Parts) > 4096 {
		return errors.New("tool output exceeds part schema bound")
	}
	switch v.Capture {
	case ToolCaptureComplete, ToolCaptureTruncated:
	case ToolCaptureOmitted:
		if len(v.Parts) != 0 {
			return errors.New("omitted tool output cannot retain parts")
		}
	default:
		return errors.New("unknown tool capture state")
	}
	return nil
}
