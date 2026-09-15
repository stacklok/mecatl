package microvm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// EnvironmentRegistryReader is the read half used by the host reattachment adapter.
type EnvironmentRegistryReader interface {
	Lookup(context.Context, string) (EnvironmentRecord, error)
}

// Resolver validates the exact durable generation before a host adapter constructs
// any live Workspace or CommandRunner. It never provisions a replacement.
type Resolver struct {
	registry   EnvironmentRegistryReader
	reattacher RuntimeReattacher
}

// NewResolver constructs the fail-closed driver-side resolution seam.
func NewResolver(registry EnvironmentRegistryReader) *Resolver { return &Resolver{registry: registry} }

// NewReattachingResolver additionally verifies that the exact daemon generation
// is live. The reattacher is verification-only and must never provision.
func NewReattachingResolver(registry EnvironmentRegistryReader, reattacher RuntimeReattacher) *Resolver {
	return &Resolver{registry: registry, reattacher: reattacher}
}

// Resolve returns the exact ready record for ref and owner.
func (r *Resolver) Resolve(ctx context.Context, ref EnvironmentRef, owner string) (EnvironmentRecord, error) {
	environmentID, generation, err := parseEnvironmentRef(ref)
	if err != nil || owner == "" || r == nil || r.registry == nil {
		return EnvironmentRecord{}, ErrInvalidEnvironmentRef
	}
	record, err := r.registry.Lookup(ctx, environmentID)
	if err != nil {
		if errors.Is(err, ErrEnvironmentUnknown) {
			return EnvironmentRecord{}, ErrEnvironmentUnknown
		}
		return EnvironmentRecord{}, fmt.Errorf("lookup microvm environment: %w", err)
	}
	if record.Owner != owner {
		return EnvironmentRecord{}, ErrEnvironmentForeign
	}
	if record.State == EnvironmentDestroyed {
		return EnvironmentRecord{}, ErrEnvironmentDestroyed
	}
	if record.State != EnvironmentReady || record.EnvironmentID != environmentID || record.Ref != ref || record.Generation != generation {
		return EnvironmentRecord{}, ErrEnvironmentStale
	}
	if err := validateAgreement(record.Agreement); err != nil {
		return EnvironmentRecord{}, ErrEnvironmentIncompatible
	}
	if r.reattacher != nil {
		if err := r.reattacher.Reattach(ctx, record); err != nil {
			if errors.Is(err, ErrEnvironmentUnavailable) || errors.Is(err, ErrRuntimeIdentityMismatch) {
				return EnvironmentRecord{}, err
			}
			return EnvironmentRecord{}, fmt.Errorf("reattach exact microvm generation: %w", err)
		}
	}
	return cloneEnvironmentRecord(record), nil
}

func parseEnvironmentRef(ref EnvironmentRef) (string, uint32, error) {
	if ref.Kind != Kind || strings.TrimSpace(ref.ID) != ref.ID {
		return "", 0, ErrInvalidEnvironmentRef
	}
	separator := strings.LastIndexByte(ref.ID, '@')
	if separator <= 0 || separator == len(ref.ID)-1 {
		return "", 0, ErrInvalidEnvironmentRef
	}
	environmentID := ref.ID[:separator]
	if strings.TrimSpace(environmentID) != environmentID {
		return "", 0, ErrInvalidEnvironmentRef
	}
	generation, err := strconv.ParseUint(ref.ID[separator+1:], 10, 32)
	if err != nil || generation == 0 {
		return "", 0, ErrInvalidEnvironmentRef
	}
	return environmentID, uint32(generation), nil
}
