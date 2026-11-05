package session_test

import (
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"
)

// byteMessages migrates opaque-byte lifecycle fixtures to explicit inline media.
// Arbitrary bytes remain legal media, never invalid UTF-8 text.
func byteMessages(data []byte) []session.AuthoredMessage {
	if len(data) == 0 {
		return nil
	}
	return []session.AuthoredMessage{{Content: session.Content{{Kind: session.ContentDocument, Media: &session.MediaContent{MediaType: "application/octet-stream", Source: session.MediaSource{Inline: &session.InlineMedia{Data: data}}}}}}}
}
func formatByteMessages(data []byte) []sessionformat.AuthoredMessage {
	if len(data) == 0 {
		return nil
	}
	return []sessionformat.AuthoredMessage{{Content: sessionformat.Content{{Kind: "document", Media: &sessionformat.MediaContent{MediaType: "application/octet-stream", Source: sessionformat.MediaSource{Inline: &sessionformat.InlineMedia{Data: data}}}}}}}
}
func inputBytes(v session.InputView) []byte {
	m := v.Messages()
	if len(m) == 0 {
		return nil
	}
	return m[0].Content[0].Media.Source.Inline.Data
}
func dtoInputBytes(v sessionformat.UserInput) []byte {
	if len(v.Messages) == 0 {
		return nil
	}
	return v.Messages[0].Content[0].Media.Source.Inline.Data
}
