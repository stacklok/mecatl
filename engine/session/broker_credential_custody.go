package session

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// BrokerRecoveryReferenceBytes is the canonical unpadded base64url length of
	// the 32 random bytes used to locate a custody record.
	BrokerRecoveryReferenceBytes = 43
	// MaxBrokerCustodyProviders bounds the exact protected-provider set.
	MaxBrokerCustodyProviders = 64
	// MaxBrokerCustodyProviderBytes bounds one opaque provider name.
	MaxBrokerCustodyProviderBytes = 256
)

// BrokerCredentialCustody is immutable durable evidence that a session may ask
// a broker to resolve its exact retained provider credentials. It intentionally
// contains no token, broker binding, handle, or process witness.
type BrokerCredentialCustody struct {
	recoveryReference string
	incarnation       IncarnationID
	ownerPartition    [32]byte
	workloadPartition [32]byte
	profileDigest     [32]byte
	providers         []string
	expiresAt         time.Time
}

// NewBrokerCredentialCustody validates and constructs immutable custody evidence.
func NewBrokerCredentialCustody(recoveryReference string, incarnation IncarnationID, ownerPartition, workloadPartition, profileDigest [32]byte, providers []string, expiresAt time.Time) (BrokerCredentialCustody, error) {
	custody := BrokerCredentialCustody{
		recoveryReference: recoveryReference,
		incarnation:       incarnation,
		ownerPartition:    ownerPartition,
		workloadPartition: workloadPartition,
		profileDigest:     profileDigest,
		providers:         append([]string(nil), providers...),
		expiresAt:         expiresAt,
	}
	if !custody.Valid() {
		return BrokerCredentialCustody{}, errors.New("session: invalid broker credential custody")
	}
	return custody, nil
}

// Valid reports whether c has the closed durable custody representation.
func (c BrokerCredentialCustody) Valid() bool {
	if len(c.recoveryReference) != BrokerRecoveryReferenceBytes || !c.incarnation.Valid() || !nonzero(c.ownerPartition) || !nonzero(c.workloadPartition) || !nonzero(c.profileDigest) || !validBrokerRecoveryReference(c.recoveryReference) || !validBrokerCustodyProviders(c.providers) || c.expiresAt.IsZero() || c.expiresAt != c.expiresAt.UTC() || c.expiresAt != c.expiresAt.Round(0) {
		return false
	}
	return true
}

// Active reports whether c is structurally valid and expires strictly after now.
func (c BrokerCredentialCustody) Active(now time.Time) bool {
	return c.Valid() && c.expiresAt.After(now)
}

// Clone returns an independent custody value.
func (c BrokerCredentialCustody) Clone() BrokerCredentialCustody {
	c.providers = append([]string(nil), c.providers...)
	return c
}

// RecoveryReference returns the opaque custody locator.
func (c BrokerCredentialCustody) RecoveryReference() string { return c.recoveryReference }

// SessionIncarnation returns the durable session incarnation guarded by c.
func (c BrokerCredentialCustody) SessionIncarnation() IncarnationID { return c.incarnation }

// OwnerPartition returns the one-way owner partition.
func (c BrokerCredentialCustody) OwnerPartition() [32]byte { return c.ownerPartition }

// WorkloadPartition returns the one-way workload partition.
func (c BrokerCredentialCustody) WorkloadPartition() [32]byte { return c.workloadPartition }

// ProfileDigest returns the immutable protected-profile digest.
func (c BrokerCredentialCustody) ProfileDigest() [32]byte { return c.profileDigest }

// Providers returns an independent exact provider set.
func (c BrokerCredentialCustody) Providers() []string { return append([]string(nil), c.providers...) }

// ExpiresAt returns the fixed custody expiry.
func (c BrokerCredentialCustody) ExpiresAt() time.Time { return c.expiresAt }

func validBrokerRecoveryReference(ref string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(ref)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == ref
}
func nonzero(value [32]byte) bool { var zero [32]byte; return value != zero }
func validBrokerCustodyProviders(providers []string) bool {
	if len(providers) == 0 || len(providers) > MaxBrokerCustodyProviders {
		return false
	}
	for i, provider := range providers {
		if provider == "" || len(provider) > MaxBrokerCustodyProviderBytes || !utf8.ValidString(provider) {
			return false
		}
		for _, r := range provider {
			if r == 0 || r == 0x7f || (r >= 0 && r < 0x20) || unicode.IsControl(r) {
				return false
			}
		}
		if i > 0 && providers[i-1] >= provider {
			return false
		}
	}
	return true
}

// InstallBrokerCredentialCustody attaches custody only to the aggregate's exact
// pending enrollment. A byte-identical retry is idempotent.
func (s *Session) InstallBrokerCredentialCustody(pending PendingWorkspaceEnrollment, custody BrokerCredentialCustody, now time.Time) error {
	if !s.prePromptIdle() || s.pendingWorkspaceEnrollment == nil || !samePendingWorkspaceEnrollment(*s.pendingWorkspaceEnrollment, pending) || !custody.Active(now) || custody.incarnation != s.incarnation {
		return fmt.Errorf("%w: invalid broker credential custody installation", ErrIllegalTransition)
	}
	if s.brokerCredentialCustody != nil {
		if sameBrokerCredentialCustody(*s.brokerCredentialCustody, custody) {
			return nil
		}
		return fmt.Errorf("%w: broker credential custody already installed", ErrIllegalTransition)
	}
	custodyCopy := custody.Clone()
	s.brokerCredentialCustody = &custodyCopy
	return nil
}

// BrokerCredentialCustody returns an independent custody copy, when installed.
func (s *Session) BrokerCredentialCustody() (BrokerCredentialCustody, bool) {
	if s == nil || s.brokerCredentialCustody == nil {
		return BrokerCredentialCustody{}, false
	}
	return s.brokerCredentialCustody.Clone(), true
}

// ClearBrokerCredentialCustody removes only the exact custody reference and
// returns an independent copy for authority-first invalidation.
func (s *Session) ClearBrokerCredentialCustody(expectedRecoveryReference string) (BrokerCredentialCustody, error) {
	if s == nil || s.brokerCredentialCustody == nil || s.brokerCredentialCustody.recoveryReference != expectedRecoveryReference {
		return BrokerCredentialCustody{}, errors.New("session: broker credential custody is not installed")
	}
	custody := s.brokerCredentialCustody.Clone()
	s.brokerCredentialCustody = nil
	return custody, nil
}

// RestoreBrokerCredentialCustody restores structurally valid custody during idle
// aggregate reconstruction. Historical expiry is intentionally permitted.
func (s *Session) RestoreBrokerCredentialCustody(custody BrokerCredentialCustody) error {
	if s == nil || s.State != StateIdle || s.brokerCredentialCustody != nil || !s.authorityBound || !custody.Valid() || custody.incarnation != s.incarnation ||
		(s.pendingWorkspaceEnrollment != nil && (s.Conversation == nil || len(s.Conversation.Messages) != 0)) {
		return fmt.Errorf("%w: invalid broker credential custody restoration", ErrIllegalTransition)
	}
	custodyCopy := custody.Clone()
	s.brokerCredentialCustody = &custodyCopy
	return nil
}

// AdoptRecoveredBrokerCatalogue atomically adopts fresh broker authority after
// durable custody has authorized replacement discovery. ExternalBinding is the
// sole broker-process comparator.
func (s *Session) AdoptRecoveredBrokerCatalogue(expectedRecoveryReference string, expectedExternalBinding, replacementExternalBinding ExternalBinding, exactTools []string) error {
	if !s.prePromptIdle() || s.pendingWorkspaceEnrollment != nil || s.pending != nil || s.pendingAuthorization != nil || s.brokerCredentialCustody == nil || s.brokerCredentialCustody.recoveryReference != expectedRecoveryReference || replacementExternalBinding == "" || !s.authorityBound || !ValidWorkspaceEnrollmentToolNames(exactTools) {
		return fmt.Errorf("%w: invalid recovered broker catalogue adoption", ErrIllegalTransition)
	}
	if s.ExternalBinding != expectedExternalBinding {
		return fmt.Errorf("%w: invalid recovered broker catalogue adoption", ErrIllegalTransition)
	}
	if replacementExternalBinding == expectedExternalBinding {
		if slices.Equal(s.Authority.CapabilitySet.Tools, exactTools) {
			return nil
		}
		return fmt.Errorf("%w: invalid recovered broker catalogue adoption", ErrIllegalTransition)
	}
	authority := s.Authority.Clone()
	authority.CapabilitySet.Tools = append([]string(nil), exactTools...)
	s.ExternalBinding = replacementExternalBinding
	s.Authority = authority
	return nil
}

func sameBrokerCredentialCustody(a, b BrokerCredentialCustody) bool {
	return a.recoveryReference == b.recoveryReference && a.incarnation == b.incarnation && a.ownerPartition == b.ownerPartition && a.workloadPartition == b.workloadPartition && a.profileDigest == b.profileDigest && a.expiresAt.Equal(b.expiresAt) && slices.Equal(a.providers, b.providers)
}

func (s *Session) prePromptIdle() bool {
	return s != nil && s.State == StateIdle && s.Conversation != nil && len(s.Conversation.Messages) == 0
}
func samePendingWorkspaceEnrollment(a, b PendingWorkspaceEnrollment) bool {
	return a.ID == b.ID && a.RequiredServices == b.RequiredServices && a.ExpiresAt.Equal(b.ExpiresAt)
}
