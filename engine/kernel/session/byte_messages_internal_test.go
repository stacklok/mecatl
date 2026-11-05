package session

// internalByteMessages keeps arbitrary legacy test bytes in an explicit media
// variant rather than treating them as UTF-8 text.
func internalByteMessages(data []byte) []AuthoredMessage {
	if len(data) == 0 {
		return nil
	}
	return []AuthoredMessage{{Content: Content{{Kind: ContentDocument, Media: &MediaContent{MediaType: "application/octet-stream", Source: MediaSource{Inline: &InlineMedia{Data: data}}}}}}}
}
func internalInputBytes(v InputView) []byte {
	m := v.Messages()
	if len(m) == 0 {
		return nil
	}
	return m[0].Content[0].Media.Source.Inline.Data
}
