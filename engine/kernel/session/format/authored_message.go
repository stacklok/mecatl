package format

// AuthoredMessage preserves one user-supplied message boundary, including empty
// content. Assignment shares Content and its children; Clone detaches them.
type AuthoredMessage struct {
	Content Content `cbor:"c"`
}
