// Package soulbody validates soul text before it enters a model context.
package soulbody

import (
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/adapter/skillfs"
)

// DefaultMaxBytes is the load-time byte ceiling on a soul body.
const DefaultMaxBytes = 20 * 1024

const soulCloseTag = "</soul>"

// ValidateBody returns the trimmed body or a rejection reason, applying the
// same byte cap, injection scan, and fence-integrity checks for all sources.
func ValidateBody(body string, maxBytes int) (string, string) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if len(body) > maxBytes {
		return "", fmt.Sprintf("body is %d bytes, over the %d-byte cap (rejected, not truncated)", len(body), maxBytes)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "body is empty or whitespace-only"
	}
	if marker, found := skillfs.ScanForInjection(body); found {
		return "", fmt.Sprintf("injection marker detected: %s", marker)
	}
	if strings.Contains(body, soulCloseTag) {
		return "", fmt.Sprintf("body contains the data-fence close-tag %s", soulCloseTag)
	}
	return body, ""
}
