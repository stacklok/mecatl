package format

import sessionformat "github.com/stacklok/mecatl/engine/kernel/session/format"

// ContentBlock preserves one prepared root/prefix or assistant content boundary.
// It carries no provenance or additional authority. Zero is valid empty content.
// Assignment shares parts and their storage; Clone detaches them.
type ContentBlock struct {
	// Parts preserves content order and duplicates. Nil and empty are equivalent.
	Parts []sessionformat.ContentPart `cbor:"p"`
}

// Validate checks each typed part without interpreting opaque media bytes.
func (v ContentBlock) Validate() error {
	for _, p := range v.Parts {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}
