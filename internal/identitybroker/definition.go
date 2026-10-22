package identitybroker

import (
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"unicode/utf8"
)

// DigestSHA256 is the only digest algorithm this package accepts.
const DigestSHA256 = "sha256"

// MaxSegmentBytes bounds DefinitionRef.Source and DefinitionRef.Name. Both
// become segments of agent SPIFFE IDs, and the cap keeps the longest planned ID
// within the 2,048 bytes SPIFFE-ID section 2.3 advises against exceeding.
const MaxSegmentBytes = 63

// DefinitionRef identifies one agent definition: the configured source it was
// loaded from, its name within that source, and the selected source revision.
// Two references are equal only when all three fields are byte-identical, so
// the same name from different sources, or at different revisions, names
// different definitions. DefinitionRef is comparable and is the snapshot key.
type DefinitionRef struct {
	// Source identifies where the definition came from, such as "global" for
	// operator-loaded definitions or "system" for built-ins. It becomes the
	// definition category segment of agent SPIFFE IDs.
	Source string
	// Name is the definition name within the source, such as "code-reviewer".
	Name string
	// Revision is the source's own revision identifier, such as a Git commit or
	// an object revision. It is opaque to the broker and is not a content digest.
	Revision string
}

func (r DefinitionRef) validate() error {
	if !isSegment(r.Source) {
		return fmt.Errorf("source %q must match [A-Za-z0-9._-]+, be at most %d bytes, and not be '.' or '..'", r.Source, MaxSegmentBytes)
	}
	if !isSegment(r.Name) {
		return fmt.Errorf("name %q must match [A-Za-z0-9._-]+, be at most %d bytes, and not be '.' or '..'", r.Name, MaxSegmentBytes)
	}
	if !isToken(r.Revision) {
		return fmt.Errorf("revision %q must be non-empty printable text without spaces", r.Revision)
	}
	return nil
}

// Digest is a content digest in canonical form: a supported algorithm and the
// lowercase hexadecimal digest value. The canonical form makes value equality
// mean digest equality.
type Digest struct {
	Algorithm string
	Value     string
}

func (d Digest) validate() error {
	if d.Algorithm != DigestSHA256 {
		return fmt.Errorf("digest algorithm %q is unsupported; use %q", d.Algorithm, DigestSHA256)
	}
	if len(d.Value) != 64 {
		return errors.New("sha256 digest value must be 64 lowercase hexadecimal characters")
	}
	for _, r := range d.Value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return errors.New("sha256 digest value must be 64 lowercase hexadecimal characters")
		}
	}
	return nil
}

// Provenance records where a loaded definition came from and what was
// accepted. The profile names the source kind and versions the meaning of
// Metadata; this package requires a profile but does not interpret it.
type Provenance struct {
	// Profile identifies the provenance profile, such as "git/v1".
	Profile string
	// Metadata holds profile-specific facts, such as a repository and path.
	// Keys must be non-empty printable text without spaces; values must be
	// valid UTF-8. A nil and an empty map are equivalent.
	Metadata map[string]string
	// SourceDigest is the digest of the bytes read from the source.
	SourceDigest Digest
	// DefinitionDigest is the digest of the normalized definition the loader
	// accepted. It differs from SourceDigest whenever parsing normalizes input.
	DefinitionDigest Digest
}

// DefinitionRecord is one loaded definition as the identity core sees it: the
// reference and its provenance. It deliberately carries no instructions,
// tools, or execution settings.
type DefinitionRecord struct {
	Ref        DefinitionRef
	Provenance Provenance
}

// Validate reports whether r is a well-formed definition record, applying the
// same rules as New. The returned error wraps ErrInvalidDefinition. A loader
// that skips bad definitions, rather than failing startup, uses it to filter
// records before calling New.
func (r DefinitionRecord) Validate() error {
	if err := r.validate(); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDefinition, err)
	}
	return nil
}

func (r DefinitionRecord) validate() error {
	if err := r.Ref.validate(); err != nil {
		return err
	}
	p := r.Provenance
	if !isToken(p.Profile) {
		return fmt.Errorf("provenance profile %q must be non-empty printable text without spaces", p.Profile)
	}
	for k, v := range p.Metadata {
		if !isToken(k) {
			return fmt.Errorf("provenance metadata key %q must be non-empty printable text without spaces", k)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("provenance metadata value for %q must be valid UTF-8", k)
		}
	}
	if err := p.SourceDigest.validate(); err != nil {
		return fmt.Errorf("source digest: %w", err)
	}
	if err := p.DefinitionDigest.validate(); err != nil {
		return fmt.Errorf("definition digest: %w", err)
	}
	return nil
}

func (r DefinitionRecord) clone() DefinitionRecord {
	r.Provenance.Metadata = maps.Clone(r.Provenance.Metadata)
	return r
}

func (r DefinitionRecord) equal(o DefinitionRecord) bool {
	return r.Ref == o.Ref &&
		r.Provenance.Profile == o.Provenance.Profile &&
		r.Provenance.SourceDigest == o.Provenance.SourceDigest &&
		r.Provenance.DefinitionDigest == o.Provenance.DefinitionDigest &&
		maps.Equal(r.Provenance.Metadata, o.Provenance.Metadata)
}

// isIncarnation reports whether s has the syntax of a Mecatl incarnation: a
// minted inc_ followed by 26 lowercase base32 characters (130 bits holding 128
// random ones, so the last character carries three random bits and two zero
// padding bits, and has 8 legal values), or a deterministic legacy_ followed
// by 64 hex characters. Both are SPIFFE-path-safe, and neither can be guessed
// or chosen to collide with another user's agent. The syntax mirrors the
// engine's session.IncarnationID.Valid; the broker does not import the engine.
func isIncarnation(s string) bool {
	if rest, ok := strings.CutPrefix(s, "inc_"); ok {
		if len(rest) != 26 {
			return false
		}
		for _, c := range rest {
			if (c < 'a' || c > 'z') && (c < '2' || c > '7') {
				return false
			}
		}
		return strings.IndexByte("aeimquy4", rest[25]) >= 0
	}
	if rest, ok := strings.CutPrefix(s, "legacy_"); ok {
		_, err := hex.DecodeString(rest)
		return len(rest) == 64 && err == nil
	}
	return false
}

func invalidIncarnation(s string) error {
	return invalid(fmt.Sprintf("incarnation %q must be inc_ and 26 lowercase base32 characters, or legacy_ and 64 hex characters", s))
}

// isSegment reports whether s is a SPIFFE-path-safe segment of at most
// MaxSegmentBytes.
func isSegment(s string) bool { return isPathSegment(s, MaxSegmentBytes) }

// isPathSegment reports whether s is a SPIFFE-path-safe segment: non-empty, at
// most maxBytes, [A-Za-z0-9._-] only, and neither "." nor "..". Invalid values
// are rejected, never rewritten.
func isPathSegment(s string, maxBytes int) bool {
	if s == "" || len(s) > maxBytes || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '-' && r != '_' {
			return false
		}
	}
	return true
}
