package session_test

import (
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// byteParts keeps arbitrary fixture bytes in media, never UTF-8 text.
func byteParts(data []byte) []session.ContentPart {
	if len(data) == 0 {
		return nil
	}
	return []session.ContentPart{{Kind: session.ContentDocument, Media: &session.Media{MediaType: "application/octet-stream", Source: session.MediaInline, Data: data}}}
}
func formatByteParts(data []byte) []sessionformat.ContentPart {
	if len(data) == 0 {
		return nil
	}
	return []sessionformat.ContentPart{{Kind: "document", Media: &sessionformat.Media{MediaType: "application/octet-stream", Source: "inline", Data: data}}}
}
func inputBytes(v session.InputView) []byte {
	p := v.Parts()
	if len(p) == 0 {
		return nil
	}
	return p[0].Media.Data
}
func dtoInputBytes(v sessionformat.UserInput) []byte {
	if len(v.Parts) == 0 {
		return nil
	}
	return v.Parts[0].Media.Data
}
