package session

// internalByteParts keeps arbitrary fixture bytes in media, not UTF-8 text.
func internalByteParts(data []byte) []ContentPart {
	if len(data) == 0 {
		return nil
	}
	return []ContentPart{{Kind: ContentDocument, Media: &Media{MediaType: "application/octet-stream", Source: MediaInline, Data: data}}}
}
func internalInputBytes(v InputView) []byte {
	p := v.Parts()
	if len(p) == 0 {
		return nil
	}
	return p[0].Media.Data
}
