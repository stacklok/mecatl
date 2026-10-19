package mcpbroker

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
)

// The types in this file are a broker-internal contract between a broker's
// session layer and its credential-custody store. They are not a host-facing API:
// a host never builds, sends or interprets them.

const (
	// ContinuityAttemptTTL bounds an individual Stage or Recover attempt. It is
	// deliberately independent from custody retention.
	ContinuityAttemptTTL = 2 * time.Minute
	// MaxContinuityProviders and MaxContinuityProviderBytes bound a guard's
	// provider set before it reaches adapter storage.
	MaxContinuityProviders = 64
	// MaxContinuityProviderBytes bounds one provider name's encoded length.
	MaxContinuityProviderBytes = 256

	continuityPartitionDomain = "mecatl.mcpbroker.continuity.partition/v1\x00"
	continuityProfileDomain   = "mecatl.mcpbroker.continuity.profile/v1\x00"
)

// OwnerPartition, WorkloadPartition and ProfileDigest are fixed-size one-way
// values. They are distinct types so that one cannot be passed for another: a
// guard built with the two partitions swapped does not compile. Compare with ==
// or the constant-time Equal.

// OwnerPartition is the one-way fingerprint of the durable owner principal.
type OwnerPartition [32]byte

// WorkloadPartition is the one-way fingerprint of the authenticated workload
// principal.
type WorkloadPartition [32]byte

// ProfileDigest is the digest of the protected provider configuration.
type ProfileDigest [32]byte

// IsZero reports whether the partition is unset.
func (p OwnerPartition) IsZero() bool { return p == OwnerPartition{} }

// Equal compares in constant time.
func (p OwnerPartition) Equal(other OwnerPartition) bool { return equal32(p, other) }

// IsZero reports whether the partition is unset.
func (p WorkloadPartition) IsZero() bool { return p == WorkloadPartition{} }

// Equal compares in constant time.
func (p WorkloadPartition) Equal(other WorkloadPartition) bool { return equal32(p, other) }

// IsZero reports whether the digest is unset.
func (d ProfileDigest) IsZero() bool { return d == ProfileDigest{} }

// Equal compares in constant time.
func (d ProfileDigest) Equal(other ProfileDigest) bool { return equal32(d, other) }

func equal32(a, b [32]byte) bool { return subtle.ConstantTimeCompare(a[:], b[:]) == 1 }

// The role byte separates owner and workload partitions even when both name the
// same principal.
const (
	continuityRoleOwner    byte = 1
	continuityRoleWorkload byte = 2
)

// ContinuityOwnerPartition derives the one-way owner partition for a verified
// principal. The hash keeps the identity out of stored records, but it is
// unkeyed: anyone who can guess an issuer and subject can recompute it. Treat it
// as a stable identifier, not a secret.
func ContinuityOwnerPartition(principal *session.Principal) (OwnerPartition, error) {
	p, err := continuityPrincipalPartition(continuityRoleOwner, principal)
	return OwnerPartition(p), err
}

// ContinuityWorkloadPartition derives the one-way workload partition for the
// authenticated workload principal.
func ContinuityWorkloadPartition(principal *session.Principal) (WorkloadPartition, error) {
	p, err := continuityPrincipalPartition(continuityRoleWorkload, principal)
	return WorkloadPartition(p), err
}

func continuityPrincipalPartition(role byte, principal *session.Principal) ([32]byte, error) {
	if principal == nil || !principal.IdentityWellFramed() {
		return [32]byte{}, ErrContinuityUnavailable
	}
	value := append([]byte(continuityPartitionDomain), role)
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
func ProtectedProfileDigest(callbackURL, derivedIssuer string, profiles []ProtectedProfile) (ProfileDigest, []string, error) {
	if callbackURL == "" || derivedIssuer == "" || len(profiles) == 0 || len(profiles) > MaxContinuityProviders {
		return ProfileDigest{}, nil, ErrContinuityUnavailable
	}
	canonical := append([]ProtectedProfile(nil), profiles...)
	for i := range canonical {
		profile := &canonical[i]
		if !validProtectedProfile(*profile) {
			return ProfileDigest{}, nil, ErrContinuityUnavailable
		}
		profile.Scopes = append([]string(nil), profile.Scopes...)
		sort.Strings(profile.Scopes)
		for index := range profile.Scopes {
			if index > 0 && profile.Scopes[index] == profile.Scopes[index-1] {
				return ProfileDigest{}, nil, ErrContinuityUnavailable
			}
		}
	}
	sort.Slice(canonical, func(i, j int) bool { return canonical[i].Provider < canonical[j].Provider })
	providers := make([]string, len(canonical))
	for i := range canonical {
		if i > 0 && canonical[i].Provider == canonical[i-1].Provider {
			return ProfileDigest{}, nil, ErrContinuityUnavailable
		}
		providers[i] = canonical[i].Provider
	}

	encoded := make([]byte, 0, 512)
	encoded = append(encoded, continuityProfileDomain...)
	encoded = appendContinuityString(encoded, callbackURL)
	encoded = appendContinuityString(encoded, derivedIssuer)
	encoded = appendContinuityUint(encoded, uint32(len(canonical))) // #nosec G115 -- bounded configured-profile count.
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
		encoded = appendContinuityUint(encoded, uint32(len(profile.Scopes))) // #nosec G115 -- bounded configured-scope count.
		for _, scope := range profile.Scopes {
			encoded = appendContinuityString(encoded, scope)
		}
	}
	return ProfileDigest(sha256.Sum256(encoded)), providers, nil
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
	dst = appendContinuityUint(dst, uint32(len(value))) // #nosec G115 -- bounded continuity field length.
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

// ContinuityGuard identifies the custody record an operation applies to.
//
// OwnerPartition and WorkloadPartition are derived by the broker's session layer
// from the verified caller (see ContinuityOwnerPartition and ContinuityWorkloadPartition). A custody
// implementation treats them as already verified and only compares them with the
// stored record, so every caller must derive them itself and never forward a
// value received from a client.
//
// For Stage, ProfileDigest and Providers must be zero: the broker fills them
// from its own configuration and returns them in StagedCredentialCustody. Commit,
// Recover and Tombstone carry the staged values back.
type ContinuityGuard struct {
	SessionID          session.SessionID
	SessionIncarnation session.IncarnationID
	OwnerPartition     OwnerPartition
	WorkloadPartition  WorkloadPartition
	ProfileDigest      ProfileDigest
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
	ProfileDigest     ProfileDigest
	Providers         []string
}

// RecoveredCredentialAttachment contains only a process-local provisional
// attachment. Hosts persist its binding, never its handle.
type RecoveredCredentialAttachment struct {
	Attachment Attachment
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

// CredentialContinuityService remains usable after the session handle that
// staged custody has been closed. Its guards are already verified by the caller
// (see ContinuityGuard); implementations enforce them against the stored record.
type CredentialContinuityService interface {
	CommitCredentialCustody(context.Context, CustodyAssertion) error
	RecoverCredentialAttachment(context.Context, CustodyAssertion, string) (RecoveredCredentialAttachment, error)
	TombstoneCredentialCustody(context.Context, CustodyAssertion) error
}

// ExpectedBindingAttacher classifies a persisted opaque binding without creating
// a new logical session. It is the only binding-aware reattach capability.
type ExpectedBindingAttacher interface {
	AttachSessionExpectedBinding(context.Context, session.SessionID, session.ExternalBinding) (Attachment, AttachOutcome, error)
}
