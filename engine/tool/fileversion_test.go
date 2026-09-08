package tool

import "testing"

func TestFileVersionPersistenceRoundTrip(t *testing.T) {
	for _, token := range []string{"", "opaque-version"} {
		want := NewFileVersion(token)
		encoded, err := EncodeFileVersion(want)
		if err != nil {
			t.Fatalf("EncodeFileVersion(%q): %v", token, err)
		}
		got := DecodeFileVersion(encoded)
		if !got.Equal(want) {
			t.Fatalf("DecodeFileVersion(EncodeFileVersion(%q)) did not round-trip", token)
		}
	}
	if _, err := EncodeFileVersion(FileVersion{}); err == nil {
		t.Fatal("EncodeFileVersion(zero) must reject an invalid FileVersion")
	}
}
