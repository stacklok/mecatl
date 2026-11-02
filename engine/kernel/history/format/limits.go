package format

import "errors"

// Limits bounds each codec operation. Every field must be positive; zero does
// not mean unlimited and no defaults are applied. Payload limits are per record.
// These budgets do not affect Commit.Validate or Record.Validate.
type Limits struct {
	// MaxCommitBytes bounds the complete canonical envelope in bytes.
	MaxCommitBytes int
	// MaxRecords bounds the number of records in one commit.
	MaxRecords int
	// MaxKindBytes bounds each UTF-8 kind in bytes.
	MaxKindBytes int
	// MaxPayloadBytes bounds each opaque payload in bytes.
	MaxPayloadBytes int
}

// Validate rejects incomplete codec policies before either codec examines
// content. It does not mutate Limits.
func (l Limits) Validate() error {
	if l.MaxCommitBytes <= 0 || l.MaxRecords <= 0 || l.MaxKindBytes <= 0 || l.MaxPayloadBytes <= 0 {
		return errors.New("all codec limits must be positive")
	}
	return nil
}
