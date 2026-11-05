package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// MediaSource selects exactly one nonnil pointer. Zero is invalid; a nonnil
// Inline with empty data is valid. Assignment shares storage; Clone detaches it.
// References are retained labels, not permission to fetch or access data.
type MediaSource struct {
	Inline   *InlineMedia
	Remote   *RemoteMedia
	Provider *ProviderMedia
}

// InlineMedia retains raw bytes, including explicitly empty or non-UTF-8 data.
// Assignment shares Data; Clone detaches it.
type InlineMedia struct{ Data []byte }

// RemoteMedia retains an exact absolute URI, not dereference authority.
type RemoteMedia struct{ URI string }

// ProviderMedia retains nonempty logical provider, storage scope and reference ID.
// These strings are neither credentials nor authorization proofs.
type ProviderMedia struct {
	Provider string
	Scope    string
	ID       string
}

// Validate checks source exclusivity and the selected source.
func (v MediaSource) Validate() error {
	if err := v.toFormat().Validate(); err != nil {
		return err
	}
	if v.Inline != nil {
		return v.Inline.Validate()
	}
	if v.Remote != nil {
		return v.Remote.Validate()
	}
	return v.Provider.Validate()
}

// Clone returns a deeply detached source.
func (v MediaSource) Clone() MediaSource { return mediaSourceFromFormat(v.toFormat().Clone()) }

// Equal compares exact values and source presence.
func (v MediaSource) Equal(other MediaSource) bool { return v.toFormat().Equal(other.toFormat()) }
func (v MediaSource) toFormat() sessionformat.MediaSource {
	out := sessionformat.MediaSource{}
	if v.Inline != nil {
		m := sessionformat.InlineMedia(*v.Inline)
		out.Inline = &m
	}
	if v.Remote != nil {
		m := sessionformat.RemoteMedia(*v.Remote)
		out.Remote = &m
	}
	if v.Provider != nil {
		m := sessionformat.ProviderMedia(*v.Provider)
		out.Provider = &m
	}
	return out
}
func mediaSourceFromFormat(v sessionformat.MediaSource) MediaSource {
	out := MediaSource{}
	if v.Inline != nil {
		m := InlineMedia(*v.Inline)
		out.Inline = &m
	}
	if v.Remote != nil {
		m := RemoteMedia(*v.Remote)
		out.Remote = &m
	}
	if v.Provider != nil {
		m := ProviderMedia(*v.Provider)
		out.Provider = &m
	}
	return out
}
func (v MediaSource) checkSize(b *valueBudget) {
	if v.Inline != nil {
		b.pointer()
		b.bytes(len(v.Inline.Data))
	}
	if v.Remote != nil {
		b.pointer()
		b.text(v.Remote.URI)
	}
	if v.Provider != nil {
		b.pointer()
		b.text(v.Provider.Provider)
		b.text(v.Provider.Scope)
		b.text(v.Provider.ID)
	}
}

// Validate accepts all raw byte values; limits belong to record construction.
func (InlineMedia) Validate() error { return nil }

// Clone detaches Data, preserving nil.
func (v InlineMedia) Clone() InlineMedia { return InlineMedia(sessionformat.InlineMedia(v).Clone()) }

// Equal compares bytes; nil and empty are equivalent.
func (v InlineMedia) Equal(other InlineMedia) bool {
	return sessionformat.InlineMedia(v).Equal(sessionformat.InlineMedia(other))
}

// Validate checks UTF-8 and absolute URI syntax without I/O.
func (v RemoteMedia) Validate() error { return sessionformat.RemoteMedia(v).Validate() }

// Clone returns the immutable string value unchanged.
func (v RemoteMedia) Clone() RemoteMedia { return v }

// Equal compares the exact URI.
func (v RemoteMedia) Equal(other RemoteMedia) bool { return v == other }

// Validate checks nonempty UTF-8 logical binding fields.
func (v ProviderMedia) Validate() error { return sessionformat.ProviderMedia(v).Validate() }

// Clone returns the immutable strings unchanged.
func (v ProviderMedia) Clone() ProviderMedia { return v }

// Equal compares the exact binding and ID.
func (v ProviderMedia) Equal(other ProviderMedia) bool { return v == other }
