package session_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/session"
)

func TestStandaloneMediaMetadata(t *testing.T) {
	for _, kind := range []session.MediaKind{session.MediaKindImage, session.MediaKindAudio, session.MediaKindDocument} {
		for _, source := range []session.MediaSourceKind{session.MediaSourceInline, session.MediaSourceRemote, session.MediaSourceProvider} {
			v := session.Media{Kind: kind, Source: source}
			switch source {
			case session.MediaSourceInline:
				v.MIMEType = "application/custom"
			case session.MediaSourceRemote:
				v.RemoteURI = "urn:exact"
			case session.MediaSourceProvider:
				v.ProviderName, v.ProviderScope, v.ProviderReferenceID = "p", "s", "i"
			}
			if err := v.Validate(); err != nil {
				t.Fatalf("%s/%s: %v", kind, source, err)
			}
			for _, metadata := range []struct{ filename, title string }{{"file", ""}, {"", "title"}} {
				v.Filename, v.Title = metadata.filename, metadata.title
				if err := v.Validate(); (err == nil) != (kind == session.MediaKindDocument) {
					t.Fatalf("standalone %s/%s metadata: %v", kind, source, err)
				}
			}
			v.Kind = "text"
			if err := v.Validate(); err == nil {
				t.Fatal("text accepted as a media kind")
			}
		}
	}
}
