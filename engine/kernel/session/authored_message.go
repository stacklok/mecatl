package session

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// AuthoredMessage preserves one supplied message boundary with user origin.
// Assignment shares Content; Clone detaches it. Empty content is valid.
type AuthoredMessage struct{ Content Content }

// Validate checks ordered content without operational limits.
func (v AuthoredMessage) Validate() error { return v.Content.Validate() }

// Clone returns independently owned content.
func (v AuthoredMessage) Clone() AuthoredMessage { return AuthoredMessage{Content: v.Content.Clone()} }

// Equal compares exact content, treating nil and empty slices alike.
func (v AuthoredMessage) Equal(other AuthoredMessage) bool { return v.Content.Equal(other.Content) }
func (v AuthoredMessage) toFormat() sessionformat.AuthoredMessage {
	return sessionformat.AuthoredMessage{Content: v.Content.toFormat()}
}
func messagesToFormat(v []AuthoredMessage) []sessionformat.AuthoredMessage {
	if v == nil {
		return nil
	}
	out := make([]sessionformat.AuthoredMessage, len(v))
	for i := range v {
		out[i] = v[i].toFormat()
	}
	return out
}
func messagesFromFormat(v []sessionformat.AuthoredMessage) []AuthoredMessage {
	if v == nil {
		return nil
	}
	out := make([]AuthoredMessage, len(v))
	for i := range v {
		out[i] = AuthoredMessage{Content: contentFromFormat(v[i].Content)}
	}
	return out
}
