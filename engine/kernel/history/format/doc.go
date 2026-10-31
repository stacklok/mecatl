// Package format owns the isolated kernel's persistence representation and
// content identities. Commit and Record are mutable persistence DTOs, separate
// from accepted-state aggregates. Validate checks their data contract; codecs
// additionally enforce canonical encoding and per-operation resource limits.
// It does not establish history membership or acceptance.
package format
