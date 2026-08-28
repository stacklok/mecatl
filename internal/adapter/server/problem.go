package server

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

// problemContentType is the RFC 9457 media type.
//
// Flipping to it from application/json is observable to a client that
// pattern-matches on the response Content-Type — but the `error` key is retained
// inside the body for compatibility, and RFC 9457 compliance that lies about its
// own media type is not compliance. See ADR 0244.
const problemContentType = "application/problem+json"

// problemTypePrefix builds the RFC 9457 `type` member.
//
// A URN, not an https:// URL. RFC 9457 wants a URI and prefers a dereferenceable
// one, but publishing https://<domain>/errors/<code> would promise a page we do
// not serve; a URI that 404s is worse documentation than one that never claimed
// to be fetchable. The bare code also travels in the `code` extension, which is
// what a machine actually branches on.
const problemTypePrefix = "urn:mecatl:error:"

// maxProblemDetail bounds the RFC 9457 `detail` member.
//
// `detail` carries err.Error(), which may include wrapped context from a
// downstream. Bounding it keeps a pathological error from turning every response
// into an unbounded payload, and keeps the body small enough to log whole.
const maxProblemDetail = 2048

// problemDocument is an RFC 9457 problem-details body.
//
// Field order mirrors the RFC's own ordering for readability on the wire. The
// two members after `instance` are EXTENSIONS, which RFC 9457 explicitly allows:
//   - `code` is the stable mecatl identifier, duplicated out of the `type` URN so
//     a client branches on a bare string rather than parsing a URI.
//   - `error` is the pre-RFC-9457 body shape, retained verbatim during the
//     transition so an existing client reading {"error": "..."} keeps working.
type problemDocument struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
	// Instance identifies this specific occurrence. It is omitted rather than
	// fabricated: mecatl has no request-id concept yet, and inventing one here
	// would make a field look meaningful when nothing correlates it to anything.
	Instance string `json:"instance,omitempty"`
	Code     string `json:"code"`
	Error    string `json:"error,omitempty"`
}

// newProblem builds the body for an entry and an occurrence detail.
//
// The detail is repaired to valid UTF-8 and bounded. UTF-8 repair matters because
// detail can carry a downstream's error text: AGENTS.md documents the class where
// invalid UTF-8 in a producer-influenced string kills a protobuf marshal, and the
// same text reaches the gRPC status message on the sibling path.
func newProblem(entry errorCodeEntry, detail string) problemDocument {
	d := clampProblemDetail(session.ToValidUTF8(detail))
	return problemDocument{
		Type:   problemTypePrefix + entry.Code,
		Title:  entry.Title,
		Status: entry.HTTPStatus,
		Detail: d,
		Code:   entry.Code,
		Error:  d,
	}
}

// clampProblemDetail bounds s to maxProblemDetail, cutting on a rune boundary so
// the result never ends in a partial rune (which would be invalid UTF-8 — the
// exact fault the repair above exists to prevent).
func clampProblemDetail(s string) string {
	if len(s) <= maxProblemDetail {
		return s
	}
	cut := maxProblemDetail
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return strings.TrimRight(s[:cut], " \t\r\n") + "…"
}

// writeProblem writes an RFC 9457 problem-details response.
func writeProblem(w http.ResponseWriter, entry errorCodeEntry, detail string) {
	w.Header().Set("Content-Type", problemContentType)
	w.WriteHeader(entry.HTTPStatus)
	writeJSONBody(w, newProblem(entry, detail))
}
