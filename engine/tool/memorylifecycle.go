package tool

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// MemoryVersion is an opaque revision token. Callers may persist and compare it,
// but must not interpret its contents.
type MemoryVersion string

// MemoryStatus is the durable lifecycle state of a memory revision.
type MemoryStatus string

const (
	// MemoryStatusActive marks the current readable revision.
	MemoryStatusActive MemoryStatus = "active"
	// MemoryStatusSuperseded marks a revision replaced by a later active value.
	MemoryStatusSuperseded MemoryStatus = "superseded"
	// MemoryStatusDeleted marks a tombstone revision.
	MemoryStatusDeleted MemoryStatus = "deleted"
)

// MemoryWriter identifies the kind of actor that produced a revision. It is
// attribution only and conveys no authority.
type MemoryWriter string

const (
	// MemoryWriterUser identifies a direct user-authored revision.
	MemoryWriterUser MemoryWriter = "user"
	// MemoryWriterModel identifies a model-authored revision.
	MemoryWriterModel MemoryWriter = "model"
	// MemoryWriterSystem identifies a harness-authored revision.
	MemoryWriterSystem MemoryWriter = "system"
)

// MemoryOrigin identifies the workflow that produced a revision.
type MemoryOrigin string

const (
	// MemoryOriginExplicit identifies an explicit remember operation.
	MemoryOriginExplicit MemoryOrigin = "explicit"
	// MemoryOriginLearning identifies automatic completed-trajectory learning.
	MemoryOriginLearning MemoryOrigin = "learning"
	// MemoryOriginConsolidation identifies background dream/consolidation writes.
	MemoryOriginConsolidation MemoryOrigin = "consolidation"
	// MemoryOriginUndo identifies a compensating undo revision.
	MemoryOriginUndo MemoryOrigin = "undo"
)

// MemorySource identifies the optional durable session associated with a revision.
// Empty means unavailable; callers must not invent one.
type MemorySource struct {
	SessionID  string
	ProposalID string
}

// MemoryAttribution carries optional provenance facts for a lifecycle write.
// These values are informational: stores must never derive authorization from
// context attribution.
type MemoryAttribution struct {
	Writer MemoryWriter
	Origin MemoryOrigin
	Source MemorySource
}

type memoryAttributionContextKey struct{}

// WithMemoryAttribution returns a child context carrying provenance facts for a
// future lifecycle write. It grants no authority.
func WithMemoryAttribution(ctx context.Context, attribution MemoryAttribution) context.Context {
	return context.WithValue(ctx, memoryAttributionContextKey{}, attribution)
}

// MemoryAttributionFromContext returns the provenance facts carried by ctx.
func MemoryAttributionFromContext(ctx context.Context) (MemoryAttribution, bool) {
	if ctx == nil {
		return MemoryAttribution{}, false
	}
	attribution, ok := ctx.Value(memoryAttributionContextKey{}).(MemoryAttribution)
	return attribution, ok
}

// WithMemorySource returns a child context with source handles replaced while
// preserving any writer and origin attribution already present.
func WithMemorySource(ctx context.Context, source MemorySource) context.Context {
	attribution, _ := MemoryAttributionFromContext(ctx)
	attribution.Source = source
	return WithMemoryAttribution(ctx, attribution)
}

// MemoryRevision is one immutable revision of a durable memory record.
type MemoryRevision struct {
	Key         string
	Value       string
	Description string
	Version     MemoryVersion
	Status      MemoryStatus
	Writer      MemoryWriter
	Origin      MemoryOrigin
	Source      MemorySource
	UpdatedAt   time.Time
}

// MemoryRecord is the current revision plus its revision history. Revisions are
// ordered oldest to newest; Current is repeated explicitly for cheap remote
// projections and profile reads.
type MemoryRecord struct {
	Current   MemoryRevision
	Revisions []MemoryRevision
}

// MemoryCurrent identifies the complete expected current state for an atomic
// mutation. Exists=false means the key must be absent; Exists=true requires the
// exact opaque Version.
type MemoryCurrent struct {
	Exists  bool
	Version MemoryVersion
}

// MemoryVersionConflictError reports a failed lifecycle compare-version
// operation. Actual is empty when no current record exists.
type MemoryVersionConflictError struct {
	Key      string
	Expected MemoryVersion
	Actual   MemoryVersion
}

func (e *MemoryVersionConflictError) Error() string {
	return fmt.Sprintf("memory %q version conflict: expected %q, actual %q", e.Key, e.Expected, e.Actual)
}

var (
	// ErrInvalidMemoryKey reports a new-write key outside ValidMemoryKey's grammar.
	ErrInvalidMemoryKey = errors.New("invalid lifecycle memory key")
	// ErrSecretMemoryValue reports a rejected high-confidence credential shape.
	ErrSecretMemoryValue = errors.New("secret-shaped memory value or description")
	// ErrInstructionMemory reports a model-authored user memory that attempts to
	// persist a role or instruction override as future system context.
	ErrInstructionMemory = errors.New("instruction-shaped operator memory")
	// ErrMemoryNotFound reports a lifecycle mutation requiring a missing record.
	ErrMemoryNotFound = errors.New("memory record not found")
)

// ValidateMemoryWrite applies the strict key grammar and content validation used
// by lifecycle writes. It validates the key before inspecting content so malformed
// keys consistently report ErrInvalidMemoryKey rather than a content-classification
// result.
func ValidateMemoryWrite(key, value string) error {
	if !ValidMemoryKey(key) {
		return ErrInvalidMemoryKey
	}
	return ValidateMemoryContent(key, value, "")
}

// ValidateMemoryEntry applies lifecycle validation to an entire entry, including
// its description.
func ValidateMemoryEntry(entry MemoryEntry) error {
	return ValidateMemoryEntryWrite(entry, MemoryAttribution{})
}

// ValidateMemoryEntryWrite validates all persisted text at the authoritative
// write boundary. Instruction scanning is intentionally limited to model-authored
// user-scope facts: ordinary facts, Unicode prose, and user-authored imports remain
// accepted, while direct role/system override payloads cannot become durable
// system-prompt context.
func ValidateMemoryEntryWrite(entry MemoryEntry, attribution MemoryAttribution) error {
	if !ValidMemoryKey(entry.Key) {
		return ErrInvalidMemoryKey
	}
	return ValidateMemoryContentWrite(entry.Key, entry.Value, entry.Description, attribution)
}

func instructionShapedMemory(value string) bool {
	return DirectiveShapedUserMemory(value)
}

// ValidateMemoryContent rejects high-confidence credentials in either value or
// description without changing the legacy key grammar.
func ValidateMemoryContent(key, value, description string) error {
	return ValidateMemoryContentWrite(key, value, description, MemoryAttribution{})
}

// ValidateMemoryContentWrite is ValidateMemoryContent plus the narrow
// model-authored operator-instruction check, without imposing the lifecycle key
// grammar on legacy stores.
func ValidateMemoryContentWrite(key, value, description string, attribution MemoryAttribution) error {
	if SecretShapedMemoryValue(key, value) || SecretShapedMemoryValue(key, description) {
		return ErrSecretMemoryValue
	}
	modelAuthored := attribution.Writer == MemoryWriterModel || attribution.Origin == MemoryOriginLearning || attribution.Origin == MemoryOriginConsolidation
	if modelAuthored && strings.HasPrefix(key, "user/") &&
		(instructionShapedMemory(value) || instructionShapedMemory(description)) {
		return ErrInstructionMemory
	}
	return nil
}

// ValidMemoryKey reports whether key follows the strict grammar for new writes:
// lowercase ASCII path segments beginning with a letter, with digits, '-' and
// '_' allowed thereafter, and a 128-byte total limit.
func ValidMemoryKey(key string) bool {
	if key == "" || len(key) > 128 || !utf8.ValidString(key) || strings.HasPrefix(key, "/") || strings.HasSuffix(key, "/") {
		return false
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment[0] < 'a' || segment[0] > 'z' {
			return false
		}
		for _, r := range segment[1:] {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
				continue
			}
			return false
		}
	}
	return true
}

// CanonicalMemoryText returns the single representation used to classify and
// render memory text. It repairs malformed UTF-8 and removes Unicode format
// characters plus controls other than LF and tab, matching the final memory
// profile, tool-result, and wire projections.
func CanonicalMemoryText(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) || unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, strings.ToValidUTF8(value, "\uFFFD"))
}

// SecretShapedMemoryValue reports only high-confidence credential shapes. It
// deliberately does not reject instruction-like or security-related prose.
func SecretShapedMemoryValue(key, value string) bool {
	key = CanonicalMemoryText(key)
	value = CanonicalMemoryText(value)
	candidate, labeledSecret := unwrapCredential(value)
	lowerValue := strings.ToLower(candidate)
	firstLine := strings.SplitN(lowerValue, "\n", 2)[0]
	if strings.HasPrefix(firstLine, "-----begin ") && strings.Contains(firstLine, "private key-----") {
		return true
	}
	return knownCredentialPrefix(candidate, lowerValue) || jwtShaped(lowerValue) ||
		secretNamedToken(key, candidate) || labeledSecret && opaqueCredentialToken(candidate)
}

func unwrapCredential(value string) (string, bool) {
	value = strings.TrimSpace(value)
	secretLabel := false
	for range 8 {
		lower := strings.ToLower(value)
		if strings.HasPrefix(lower, "bearer ") {
			secretLabel = true
			value = strings.TrimSpace(value[len("bearer "):])
			continue
		}
		if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			value = strings.TrimSpace(value[1 : len(value)-1])
			continue
		}
		if i := strings.IndexByte(value, '='); i > 0 && i < 64 && !strings.ContainsAny(value[:i], "\r\n") {
			label := strings.TrimSpace(value[:i])
			if secretShapedName(label) {
				secretLabel = true
				value = strings.TrimSpace(value[i+1:])
				continue
			}
		}
		if i := strings.IndexByte(value, ':'); i > 0 && i < 64 && !strings.ContainsAny(value[:i], "\r\n") {
			label := strings.TrimSpace(value[:i])
			if secretShapedName(label) {
				secretLabel = true
				value = strings.TrimSpace(value[i+1:])
				continue
			}
		}
		break
	}
	return value, secretLabel
}

// DirectiveShapedUserMemory reports narrow, high-confidence attempts to turn a
// model-authored user fact into a role or instruction override. It intentionally
// ignores ordinary preferences and prose discussing security.
func DirectiveShapedUserMemory(value string) bool {
	value = CanonicalMemoryText(value)
	for _, line := range strings.Split(strings.NewReplacer("\r\n", "\n", "\r", "\n", "\u2028", "\n", "\u2029", "\n").Replace(value), "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		for _, prefix := range []string{"system:", "developer:", "assistant:", "system message:", "developer message:", "instructions:"} {
			if strings.HasPrefix(line, prefix) && strings.TrimSpace(strings.TrimPrefix(line, prefix)) != "" {
				return true
			}
		}
		for _, phrase := range []string{"ignore previous instructions", "ignore all previous instructions", "override the system prompt", "disregard the system prompt", "you are now the system"} {
			if strings.HasPrefix(line, phrase) {
				return true
			}
		}
	}
	return false
}

func knownCredentialPrefix(value, lowerValue string) bool {
	for _, prefix := range []string{"sk-", "ghp_", "github_pat_", "xoxb-", "xoxp-"} {
		if strings.HasPrefix(lowerValue, prefix) && len(lowerValue) >= len(prefix)+20 && tokenMemoryRunes(lowerValue) {
			return true
		}
	}
	return (strings.HasPrefix(value, "AKIA") || strings.HasPrefix(value, "ASIA")) &&
		len(value) == 20 && tokenMemoryRunes(value)
}

func jwtShaped(value string) bool {
	parts := strings.Split(value, ".")
	return len(parts) == 3 && len(parts[0]) >= 16 && len(parts[1]) >= 16 &&
		len(parts[2]) >= 16 && tokenMemoryRunes(value)
}

func secretNamedToken(key, value string) bool {
	return secretShapedName(key) && opaqueCredentialToken(value)
}

func secretShapedName(name string) bool {
	fields := strings.FieldsFunc(strings.ToLower(CanonicalMemoryText(name)), func(r rune) bool {
		return r == '/' || r == ':' || r == '=' || unicode.IsSpace(r)
	})
	for _, field := range fields {
		components := strings.FieldsFunc(field, func(r rune) bool {
			return r == '_' || r == '-' || r == '.'
		})
		if secretNameSuffix(components) {
			return true
		}
	}
	return false
}

func secretNameSuffix(components []string) bool {
	if len(components) == 0 {
		return false
	}
	last := components[len(components)-1]
	switch last {
	case "token", "secret", "password", "credential", "credentials":
		return true
	case "key":
		return len(components) >= 2 && components[len(components)-2] == "api" ||
			len(components) >= 3 && components[len(components)-3] == "secret" && components[len(components)-2] == "access"
	}
	return false
}

func opaqueCredentialToken(value string) bool {
	return len(value) >= 16 && !strings.ContainsFunc(value, unicode.IsSpace) && tokenMemoryRunes(value)
}

func tokenMemoryRunes(value string) bool {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("-_.+/=", r) {
			continue
		}
		return false
	}
	return true
}
