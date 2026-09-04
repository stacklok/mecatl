package identityissuer

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// LogicalAgentClaimURI is the sole public claim URI for the v1 logical-agent profile.
	LogicalAgentClaimURI = "https://mecatl.dev/claims/logical-agent/v1"

	maxLogicalAgentNameBytes = 128
	maxLogicalAgentSlugBytes = 48
	maxLogicalAgentSubject   = 2048
	maxLogicalAgentInstance  = 256
	maxLogicalAgentTools     = 256
	maxLogicalAgentToolBytes = 256
	maxLogicalAgentToolTotal = 8 << 10
)

// DefinitionTier is the closed identity category of a resolved agent definition.
type DefinitionTier string

const (
	// DefinitionTierSystem identifies harness-owned definitions.
	DefinitionTierSystem DefinitionTier = "system"
	// DefinitionTierManaged identifies operator-managed definitions.
	DefinitionTierManaged DefinitionTier = "managed"
	// DefinitionTierDriver identifies definitions supplied by a trusted driver.
	DefinitionTierDriver DefinitionTier = "driver"
	// DefinitionTierUser identifies user-tier definitions.
	DefinitionTierUser DefinitionTier = "user"
	// DefinitionTierProject identifies project-tier definitions.
	DefinitionTierProject DefinitionTier = "project"
)

// LogicalAgentIdentity is the canonical local SPIFFE identity for one logical definition.
type LogicalAgentIdentity struct {
	Tier    DefinitionTier
	Name    string
	Subject string
}

// LogicalAgentClaim is the closed, canonical v1 logical-agent claim value.
type LogicalAgentClaim struct {
	DefinitionTier DefinitionTier `json:"definition_tier"`
	DefinitionName string         `json:"definition_name"`
	Instance       string         `json:"instance,omitempty"`
	Tools          []string       `json:"tools"`
}

// NewLogicalAgentIdentity validates an exact resolved name and returns its canonical local SPIFFE subject.
func NewLogicalAgentIdentity(trustDomain string, tier DefinitionTier, name string) (LogicalAgentIdentity, error) {
	if err := ValidateTrustDomain(trustDomain); err != nil {
		return LogicalAgentIdentity{}, errors.New("logical agent trust domain is invalid")
	}
	if !validDefinitionTier(tier) || !validLogicalAgentText(name, maxLogicalAgentNameBytes) {
		return LogicalAgentIdentity{}, errors.New("logical agent definition is invalid")
	}

	subject := "spiffe://" + trustDomain + "/mecatl/agent-definition/v1/" + string(tier) + "/" + logicalAgentSlug(name) + "--" + logicalAgentDigest(tier, name)
	if err := ValidateLogicalAgentSubject(trustDomain, subject); err != nil {
		return LogicalAgentIdentity{}, err
	}
	return LogicalAgentIdentity{Tier: tier, Name: name, Subject: subject}, nil
}

// ValidateLogicalAgentSubject validates a complete canonical local logical-agent SPIFFE ID.
//
//nolint:gocyclo // The closed URI profile is clearest as one fail-fast validation pass.
func ValidateLogicalAgentSubject(trustDomain, subject string) error {
	if err := ValidateTrustDomain(trustDomain); err != nil {
		return errors.New("logical agent trust domain is invalid")
	}
	if !utf8.ValidString(subject) || len(subject) == 0 || len(subject) > maxLogicalAgentSubject || strings.Contains(subject, "%") {
		return errors.New("logical agent subject is invalid")
	}
	u, err := url.Parse(subject)
	if err != nil || u.Scheme != "spiffe" || u.Host != trustDomain || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return errors.New("logical agent subject is invalid")
	}
	segments := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if len(segments) != 5 || segments[0] != "mecatl" || segments[1] != "agent-definition" || segments[2] != "v1" || !validDefinitionTier(DefinitionTier(segments[3])) {
		return errors.New("logical agent subject is invalid")
	}
	parts := strings.Split(segments[4], "--")
	if len(parts) != 2 || !validSlug(parts[0]) || !validDigest(parts[1]) {
		return errors.New("logical agent subject is invalid")
	}
	return nil
}

// NewLogicalAgentClaim constructs a canonical v1 claim. Input tools are sorted
// byte-lexicographically; duplicates and invalid values are rejected.
func NewLogicalAgentClaim(tier DefinitionTier, name, instance string, tools []string) (LogicalAgentClaim, error) {
	claim := LogicalAgentClaim{
		DefinitionTier: tier,
		DefinitionName: name,
		Instance:       instance,
		Tools:          make([]string, len(tools)),
	}
	copy(claim.Tools, tools)
	if err := validateClaimFields(claim, false); err != nil {
		return LogicalAgentClaim{}, err
	}
	sort.Strings(claim.Tools)
	if err := claim.Validate(); err != nil {
		return LogicalAgentClaim{}, err
	}
	return claim, nil
}

// Validate checks that a claim is complete, bounded, closed by its Go type, and canonically ordered.
func (c LogicalAgentClaim) Validate() error {
	return validateClaimFields(c, true)
}

// ParseLogicalAgentClaim strictly decodes one closed v1 claim object. Duplicate and
// unknown object members are rejected before a claim value is returned.
//
//nolint:gocyclo // Explicit member-by-member decoding is the duplicate-claim security boundary.
func ParseLogicalAgentClaim(data []byte) (LogicalAgentClaim, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
	}
	var claim LogicalAgentClaim
	seen := make(map[string]bool, 4)
	for decoder.More() {
		key, err := decoder.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] {
			return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
		}
		seen[name] = true
		switch name {
		case "definition_tier":
			var value string
			if err := decoder.Decode(&value); err != nil {
				return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
			}
			claim.DefinitionTier = DefinitionTier(value)
		case "definition_name":
			if err := decoder.Decode(&claim.DefinitionName); err != nil {
				return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
			}
		case "instance":
			if err := decoder.Decode(&claim.Instance); err != nil || claim.Instance == "" {
				return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
			}
		case "tools":
			if err := decoder.Decode(&claim.Tools); err != nil || claim.Tools == nil {
				return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
			}
		default:
			return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
	}
	if err := ensureEOF(decoder); err != nil || !seen["definition_tier"] || !seen["definition_name"] || !seen["tools"] {
		return LogicalAgentClaim{}, errors.New("logical agent claim is invalid")
	}
	if err := claim.Validate(); err != nil {
		return LogicalAgentClaim{}, err
	}
	claim.Tools = append([]string(nil), claim.Tools...)
	return claim, nil
}

func validateClaimFields(c LogicalAgentClaim, canonical bool) error {
	if !validDefinitionTier(c.DefinitionTier) || !validLogicalAgentText(c.DefinitionName, maxLogicalAgentNameBytes) || (c.Instance != "" && !validLogicalAgentText(c.Instance, maxLogicalAgentInstance)) || len(c.Tools) > maxLogicalAgentTools {
		return errors.New("logical agent claim is invalid")
	}
	total := 0
	for i, tool := range c.Tools {
		if !validLogicalAgentText(tool, maxLogicalAgentToolBytes) {
			return errors.New("logical agent claim is invalid")
		}
		total += len(tool)
		if total > maxLogicalAgentToolTotal || i > 0 && (c.Tools[i-1] == tool || canonical && c.Tools[i-1] > tool) {
			return errors.New("logical agent claim is invalid")
		}
	}
	return nil
}

func validDefinitionTier(tier DefinitionTier) bool {
	switch tier {
	case DefinitionTierSystem, DefinitionTierManaged, DefinitionTierDriver, DefinitionTierUser, DefinitionTierProject:
		return true
	default:
		return false
	}
}

func validLogicalAgentText(value string, maxBytes int) bool {
	if len(value) == 0 || len(value) > maxBytes || !utf8.ValidString(value) {
		return false
	}
	return strings.IndexFunc(value, unicode.IsControl) < 0
}

func logicalAgentSlug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if b.Len() < maxLogicalAgentSlugBytes {
				b.WriteRune(r)
			}
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash && b.Len() < maxLogicalAgentSlugBytes {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "agent"
	}
	return slug
}

func logicalAgentDigest(tier DefinitionTier, name string) string {
	var input bytes.Buffer
	input.WriteString("mecatl-agent-definition-subject-v1")
	input.WriteByte(0)
	for _, value := range []string{string(tier), name} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value))) //nolint:gosec // values are bounded to 128 bytes before digesting.
		input.Write(length[:])
		input.WriteString(value)
	}
	digest := sha256.Sum256(input.Bytes())
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(digest[:]))
}

func validSlug(value string) bool {
	return len(value) > 0 && len(value) <= maxLogicalAgentSlugBytes && value[0] != '-' && value[len(value)-1] != '-' && strings.IndexFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-'
	}) < 0
}

func validDigest(value string) bool {
	if len(value) != 52 {
		return false
	}
	return strings.IndexFunc(value, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '2' || r > '7')
	}) < 0
}
