package resourcegroup

import (
	"errors"

	"github.com/stacklok/mecatl/engine/kernel/history"
)

// ErrInvalidPolicy reports missing or nonpositive factory resource bounds.
var ErrInvalidPolicy = errors.New("invalid resource group factory policy")

// FactoryPolicy supplies explicit bounds for one resource type, with no defaults.
type FactoryPolicy struct {
	// History bounds each private resource History independently.
	History history.Policy
	// MaxInstances is the positive per-Group cap for this type's resident
	// instances, including construction and unresolved completion obligations.
	MaxInstances int
}

// Validate checks the history policy and positive instance cap without I/O.
func (p FactoryPolicy) Validate() error {
	if p.History.Validate() != nil || p.MaxInstances <= 0 {
		return ErrInvalidPolicy
	}
	return nil
}
