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
	return []session.ContentPart{{Media: &session.Media{Kind: session.MediaKindDocument, MIMEType: "application/octet-stream", Source: session.MediaSourceInline, InlineData: data}}}
}
func formatByteParts(data []byte) []sessionformat.ContentPart {
	if len(data) == 0 {
		return nil
	}
	return []sessionformat.ContentPart{{Media: &sessionformat.Media{Kind: "document", MIMEType: "application/octet-stream", Source: "inline", InlineData: data}}}
}
func inputBytes(v session.UserInputView) []byte {
	if v.PartCount() == 0 {
		return nil
	}
	media, _ := v.PartAt(0).Media()
	data, _ := media.InlineData()
	return data
}
func dtoInputBytes(v sessionformat.UserInput) []byte {
	if len(v.Parts) == 0 {
		return nil
	}
	return v.Parts[0].Media.InlineData
}
