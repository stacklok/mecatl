package session

import (
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// MediaKind is a closed content category, not a provider capability.
type MediaKind = sessionformat.MediaKind

const (
	// MediaKindImage selects image media.
	MediaKindImage MediaKind = sessionformat.MediaKindImage
	// MediaKindAudio selects audio media.
	MediaKindAudio MediaKind = sessionformat.MediaKindAudio
	// MediaKindDocument selects document media, permitting Filename and Title.
	MediaKindDocument MediaKind = sessionformat.MediaKindDocument
)

// MediaSourceKind selects a closed storage variant, not an access capability.
type MediaSourceKind = sessionformat.MediaSourceKind

const (
	// MediaSourceInline retains bytes, including explicitly empty bytes.
	MediaSourceInline MediaSourceKind = sessionformat.MediaSourceInline
	// MediaSourceRemote retains an absolute URI without dereferencing it.
	MediaSourceRemote MediaSourceKind = sessionformat.MediaSourceRemote
	// MediaSourceProvider retains a complete logical provider storage binding.
	MediaSourceProvider MediaSourceKind = sessionformat.MediaSourceProvider
)

// Media retains exact metadata and a tagged source. Assignment borrows inline
// bytes; Clone detaches them. References grant no access authority.
type Media = sessionformat.Media

// MediaView retains immutable backing storage, including inline bytes. Capture
// and metadata navigation cost O(1). Zero has empty metadata and no source branch.
//
//dtogen:include
type MediaView struct{ value *sessionformat.Media }

func checkMediaSize(v Media, b *valueBudget) {
	b.text(string(v.Kind))
	b.text(string(v.Source))
	b.bytes(len(v.InlineData))
	b.text(v.RemoteURI)
	b.text(v.ProviderName)
	b.text(v.ProviderScope)
	b.text(v.ProviderReferenceID)
	b.text(v.MIMEType)
	b.text(v.FormatHint)
	b.text(v.Filename)
	b.text(v.Title)
}
