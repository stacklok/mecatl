// Package control defines the authenticated local microvmd control plane and
// its bounded, versioned guest handshake.
package control

import (
	"errors"
	"fmt"
)

var (
	// ErrUnauthenticatedPeer means the Unix peer is not the configured daemon account.
	ErrUnauthenticatedPeer = errors.New("microvm control peer is not authenticated")
	// ErrBindingMismatch means an operation does not match its registered owner/session/ref/generation.
	ErrBindingMismatch = errors.New("microvm control binding mismatch")
)

// Binding identifies one immutable environment generation. Its fields are identifiers,
// not credentials; authorization additionally requires an authenticated Unix peer.
type Binding struct {
	Owner         string `json:"owner"`
	SessionID     string `json:"session_id"`
	EnvironmentID string `json:"environment_id"`
	Ref           string `json:"ref"`
	Generation    uint32 `json:"generation"`
	// AssignedRoot is the guest-visible root authenticated for repository-logical
	// environments. Legacy session-per-VM bindings leave it empty.
	AssignedRoot string `json:"assigned_root,omitempty"`
}

func (b Binding) validate() error {
	if b.Owner == "" || b.SessionID == "" || b.EnvironmentID == "" || b.Ref == "" || b.Generation == 0 {
		return fmt.Errorf("invalid microvm control binding")
	}
	return nil
}

// ValidateLogical verifies the complete repository-logical authentication tuple.
func (b Binding) ValidateLogical(owner string, generation uint32) error {
	if b.validate() != nil || b.Owner != owner || b.Generation != generation || b.AssignedRoot == "" {
		return ErrBindingMismatch
	}
	return nil
}
