package format

// Content preserves ordered text and media parts. Nil and empty are equivalent.
// Assignment shares storage; Clone detaches all nested slices and pointers.
type Content []ContentPart
