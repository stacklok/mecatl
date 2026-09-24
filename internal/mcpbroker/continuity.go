package mcpbroker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	// ContinuityAttemptTTL bounds an individual Stage or Recover attempt. It is
	// deliberately independent from custody retention.
	ContinuityAttemptTTL = 2 * time.Minute
	// MaxContinuityProviders and MaxContinuityProviderBytes match the custody
	// provider-set bounds before a guard reaches adapter storage.
	MaxContinuityProviders     = 64
	MaxContinuityProviderBytes = 256

	continuityPartitionDomain = "mecatl.mcpbroker.continuity.partition/v1\x00"
	continuityProfileDomain   = "mecatl.mcpbroker.continuity.profile/v1\x00"
)

// ContinuityPartitionRole separates durable owner and authenticated-workload
// partitions even when they name the same principal.
type ContinuityPartitionRole byte

const (
	ContinuityPartitionOwner    ContinuityPartitionRole = 1
	ContinuityPartitionWorkload ContinuityPartitionRole = 2
)

// ContinuityPrincipalPartition derives the one-way partition for a verified
// principal. It does not expose the principal identity in durable custody.
func ContinuityPrincipalPartition(role ContinuityPartitionRole, principal *session.Principal) ([32]byte, error) {
	if role != ContinuityPartitionOwner && role != ContinuityPartitionWorkload || principal == nil || !principal.IdentityWellFramed() {
		return [32]byte{}, ErrContinuityUnavailable
	}
	value := append([]byte(continuityPartitionDomain), byte(role))
	scope := session.PrincipalScopeHash(principal)
	value = append(value, scope[:]...)
	return sha256.Sum256(value), nil
}

// ProtectedProfile describes immutable, non-secret configuration that binds a
// protected ToolHive provider to continuity custody. Secret values and paths
// are deliberately absent.
type ProtectedProfile struct {
	Provider              string
	Destination           string
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	DCRDiscoveryURL       string
	ClientID              string
	AuthMode              string
	Scopes                []string
	RequestRefreshToken   bool
}

// ProtectedProfileDigest canonicalizes the immutable protected configuration.
// It returns the digest and exact sorted provider set used in continuity guards.
func ProtectedProfileDigest(callbackURL, derivedIssuer string, profiles []ProtectedProfile) ([32]byte, []string, error) {
	if callbackURL == "" || derivedIssuer == "" || len(profiles) == 0 || len(profiles) > MaxContinuityProviders {
		return [32]byte{}, nil, ErrContinuityUnavailable
	}
	canonical := append([]ProtectedProfile(nil), profiles...)
	for i := range canonical {
		profile := &canonical[i]
		if !validProtectedProfile(*profile) {
			return [32]byte{}, nil, ErrContinuityUnavailable
		}
		profile.Scopes = append([]string(nil), profile.Scopes...)
		sort.Strings(profile.Scopes)
		for index := range profile.Scopes {
			if index > 0 && profile.Scopes[index] == profile.Scopes[index-1] {
				return [32]byte{}, nil, ErrContinuityUnavailable
			}
		}
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Provider < canonical[j].Provider })
	providers := make([]string, len(canonical))
	for i := range canonical {
		if i > 0 && canonical[i].Provider == canonical[i-1].Provider {
			return [32]byte{}, nil, ErrContinuityUnavailable
		}
		providers[i] = canonical[i].Provider
	}

	encoded := make([]byte, 0, 512)
	encoded = append(encoded, continuityProfileDomain...)
	encoded = appendContinuityString(encoded, callbackURL)
	encoded = appendContinuityString(encoded, derivedIssuer)
	encoded = appendContinuityUint(encoded, uint32(len(canonical)))
	for _, profile := range canonical {
		encoded = appendContinuityString(encoded, profile.Provider)
		encoded = appendContinuityString(encoded, profile.Destination)
		encoded = appendContinuityString(encoded, profile.Issuer)
		encoded = appendContinuityString(encoded, profile.AuthorizationEndpoint)
		encoded = appendContinuityString(encoded, profile.TokenEndpoint)
		encoded = appendContinuityString(encoded, profile.DCRDiscoveryURL)
		encoded = appendContinuityString(encoded, profile.ClientID)
		encoded = appendContinuityString(encoded, profile.AuthMode)
		if profile.RequestRefreshToken {
			encoded = append(encoded, 1)
		} else {
			encoded = append(encoded, 0)
		}
		encoded = appendContinuityUint(encoded, uint32(len(profile.Scopes)))
		for _, scope := range profile.Scopes {
			encoded = appendContinuityString(encoded, scope)
		}
	}
	return sha256.Sum256(encoded), providers, nil
}

func validProtectedProfile(profile ProtectedProfile) bool {
	if !ValidContinuityProvider(profile.Provider) || profile.Destination == "" || profile.AuthMode == "" || !validContinuityText(profile.Destination) || !validContinuityText(profile.AuthMode) {
		return false
	}
	for _, value := range []string{profile.Issuer, profile.AuthorizationEndpoint, profile.TokenEndpoint, profile.DCRDiscoveryURL, profile.ClientID} {
		if value != "" && !validContinuityText(value) {
			return false
		}
	}
	for _, scope := range profile.Scopes {
		if scope == "" || !validContinuityText(scope) {
			return false
		}
	}
	return true
}

// ValidContinuityProvider reports whether provider is admissible in a canonical
// custody provider set.
func ValidContinuityProvider(provider string) bool {
	return provider != "" && len(provider) <= MaxContinuityProviderBytes && validContinuityText(provider)
}

func validContinuityText(value string) bool {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return false
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func appendContinuityString(dst []byte, value string) []byte {
	dst = appendContinuityUint(dst, uint32(len(value)))
	return append(dst, value...)
}

func appendContinuityUint(dst []byte, value uint32) []byte {
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], value)
	return append(dst, encoded[:]...)
}

// ValidContinuityAttemptDeadline requires a finite, future deadline within the
// fixed attempt window. Callers also cap it at custody expiry and their context.
func ValidContinuityAttemptDeadline(now, deadline time.Time) bool {
	return !deadline.IsZero() && deadline.After(now) && !deadline.After(now.Add(ContinuityAttemptTTL))
}

// ContinuityGuard is the safe host assertion guard for custody operations.
type ContinuityGuard struct {
	SessionID          session.SessionID
	SessionIncarnation session.IncarnationID
	OwnerPartition     [32]byte
	WorkloadPartition  [32]byte
	ProfileDigest      [32]byte
	Providers          []string
}

// CustodyAssertion is bounded request data, never durable authority.
type CustodyAssertion struct {
	Guard             ContinuityGuard
	RecoveryReference string
	AttemptDeadline   time.Time
}

// StagedCredentialCustody is the safe durable result of Stage.
type StagedCredentialCustody struct {
	RecoveryReference string
	ExpiresAt         time.Time
	ProfileDigest     [32]byte
	Providers         []string
}

// RecoveredCredentialAttachment contains only a process-local provisional
// attachment. Hosts persist its binding, never its handle.
type RecoveredCredentialAttachment struct {
	Attachment SessionHandle
}

// CredentialCustodyStager is available only on handles that completed protected
// enrollment and can derive a verified ToolHive session identity.
type CredentialCustodyStager interface {
	StageCredentialCustody(context.Context, string, ContinuityGuard, WorkspaceEnrollmentRef, time.Time) (StagedCredentialCustody, error)
}

// CredentialContinuityAdvertiser reports whether a handle's broker offers
// credential continuity (encrypted custody is configured). A host requires
// custody exactly when this reports true: it fails enrollment closed rather than
// silently completing without custody, and keeps the legacy path otherwise, so a
// broker without protected storage (or an older broker) is never mistaken for a
// continuity failure.
type CredentialContinuityAdvertiser interface {
	CredentialContinuity() bool
}

// CredentialContinuityService remains usable after B1's handle is gone.
type CredentialContinuityService interface {
	CommitCredentialCustody(context.Context, CustodyAssertion) error
	RecoverCredentialAttachment(context.Context, CustodyAssertion, string) (RecoveredCredentialAttachment, error)
	TombstoneCredentialCustody(context.Context, CustodyAssertion) error
}

// ExpectedBindingAttacher classifies a persisted opaque binding without creating
// a new logical session. It is the only binding-aware reattach capability.
type ExpectedBindingAttacher interface {
	AttachSessionExpectedBinding(context.Context, session.SessionID, session.ExternalBinding) (SessionHandle, AttachOutcome, error)
}
