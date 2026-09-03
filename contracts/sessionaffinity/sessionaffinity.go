// Package sessionaffinity defines the root transport contract for mecatl's
// optional session routing hint. The header grants no authority.
package sessionaffinity

const (
	// HeaderName is the canonical HTTP/gRPC metadata field name.
	HeaderName = "X-Mecatl-Session-ID"
	// MaxValueBytes bounds the exact field value accepted at transport ingress.
	MaxValueBytes = 256
)

// ValidValue reports whether value can be carried unchanged by HTTP and gRPC.
// It deliberately performs no trimming, encoding, or other normalization.
func ValidValue(value string) bool {
	if value == "" || len(value) > MaxValueBytes || value[0] == ' ' || value[len(value)-1] == ' ' {
		return false
	}
	for i := range len(value) {
		if value[i] < ' ' || value[i] > '~' {
			return false
		}
	}
	return true
}
