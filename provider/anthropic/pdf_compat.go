package anthropic

import (
	"reflect"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

// Provider modules pin a published engine version that predates PDF support.
// These optional fields are read only when the host supplies a PDF-capable
// engine through its workspace or a later published release. Missing or
// unexpected fields fail closed.
const pdfMediaKind session.MediaKind = "pdf"
const maxPDFBytes = 20 << 20

func hasPDFCapability(caps port.ProviderCapabilities) bool {
	field := reflect.ValueOf(caps).FieldByName("PDF")
	return pdfContentShapeAvailable() && field.IsValid() && field.Kind() == reflect.Bool && field.Bool()
}

func withPDFCapability(caps port.ProviderCapabilities, enabled bool) port.ProviderCapabilities {
	field := reflect.ValueOf(&caps).Elem().FieldByName("PDF")
	if field.IsValid() && field.Kind() == reflect.Bool && field.CanSet() {
		field.SetBool(enabled && pdfContentShapeAvailable())
	}
	return caps
}

func pdfContentShapeAvailable() bool {
	typ := reflect.TypeFor[session.Content]()
	id, hasID := typ.FieldByName("ArtifactID")
	digest, hasDigest := typ.FieldByName("SHA256")
	return hasID && hasDigest && id.Type.Kind() == reflect.String && digest.Type.Kind() == reflect.String
}

// pdfMetadata mirrors the session PDF constructor's metadata rules until a
// published engine tag contains that constructor. Bytes are checked by the
// request builder before they can reach the provider SDK.
func pdfMetadata(part session.Content) (string, bool) {
	value := reflect.ValueOf(part)
	idField := value.FieldByName("ArtifactID")
	digestField := value.FieldByName("SHA256")
	if !idField.IsValid() || idField.Kind() != reflect.String ||
		!digestField.IsValid() || digestField.Kind() != reflect.String {
		return "", false
	}
	id, digest := idField.String(), digestField.String()
	if part.Kind != pdfMediaKind || !validArtifactID(id) ||
		!validPDFName(part.Name) || part.Size <= 0 || part.Size > maxPDFBytes ||
		!validPDFDigest(digest) {
		return "", false
	}
	return digest, true
}

func validArtifactID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z',
			ch >= '0' && ch <= '9', ch == '_', ch == '-':
		default:
			return false
		}
	}
	return true
}

func validPDFName(name string) bool {
	if !utf8.ValidString(name) || name == "." || name == ".." ||
		utf8.RuneCountInString(name) < 1 || utf8.RuneCountInString(name) > 255 {
		return false
	}
	for _, ch := range name {
		if ch == '/' || ch == '\\' || unicode.IsControl(ch) {
			return false
		}
	}
	return true
}

func validPDFDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, ch := range digest {
		switch {
		case ch >= '0' && ch <= '9', ch >= 'a' && ch <= 'f':
		default:
			return false
		}
	}
	return true
}
