package agent

import (
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

var (
	errParentAuthorityUnbound = errors.New("agent: parent authority is not bound")
	errResumeAuthorityUnbound = errors.New("agent: resumed child authority is not bound")
)

// deriveDelegatedAuthority is the single authority attenuation operation used by
// delegation producers. It is deliberately pure: callers must complete it before
// acquiring an engine, environment, runner, worktree, or child session.
func deriveDelegatedAuthority(parent session.Authority, candidate governance.CapabilitySet, ceiling *governance.CapabilitySet, tightening *DelegationTightening) (session.Authority, error) {
	set := governance.Narrow(parent.CapabilitySet, candidate)
	if ceiling != nil {
		set = governance.Narrow(set, *ceiling)
	}
	if err := applyDelegationTightening(&set, tightening); err != nil {
		return session.Authority{}, err
	}
	set, err := governance.ConsumeDelegationHop(set)
	if err != nil {
		return session.Authority{}, fmt.Errorf("agent: delegation authority: %w", err)
	}
	return session.Authority{
		CapabilitySet:      set,
		Provenance:         "delegated",
		DefinitionIdentity: parent.DefinitionIdentity,
	}, nil
}

func applyDelegationTightening(set *governance.CapabilitySet, tightening *DelegationTightening) error {
	if tightening == nil {
		return nil
	}
	if tightening.Tools != nil {
		required := governance.CapabilitySet{Tools: tightening.Tools}
		if !set.Contains(required) {
			return errors.New("agent: delegation tools tightening exceeds derived authority")
		}
		set.Tools = append([]string(nil), tightening.Tools...)
	}
	if tightening.RemainingDelegationDepth != nil {
		if *tightening.RemainingDelegationDepth < 0 || *tightening.RemainingDelegationDepth > set.RemainingDelegationDepth {
			return errors.New("agent: delegation depth tightening exceeds derived authority")
		}
		set.RemainingDelegationDepth = *tightening.RemainingDelegationDepth
	}
	if tightening.FileSystem != nil {
		if *tightening.FileSystem && !set.FileSystem {
			return errors.New("agent: delegation filesystem posture exceeds derived authority")
		}
		set.FileSystem = *tightening.FileSystem
	}
	if tightening.DirectWrite != nil {
		if *tightening.DirectWrite && !set.DirectWrite {
			return errors.New("agent: delegation direct-write posture exceeds derived authority")
		}
		set.DirectWrite = *tightening.DirectWrite
	}
	return nil
}

// validateResumedAuthority preserves the persisted child capability set exactly.
// A resume is containment-only: it neither consumes another hop nor re-applies a
// specialist ceiling.
func validateResumedAuthority(parent session.Authority, persisted session.Authority, persistedBound bool) error {
	if parent.Provenance == "" {
		return errParentAuthorityUnbound
	}
	if !persistedBound {
		return errResumeAuthorityUnbound
	}
	if !parent.CapabilitySet.Contains(persisted.CapabilitySet) {
		return errors.New("agent: resumed child authority exceeds current parent authority")
	}
	return nil
}

// stampDelegatedLabels keeps ownership and authority as independent aggregate
// labels while ensuring they are applied at the same child-creation seam.
func stampDelegatedLabels(child *session.Session, owner *session.Principal, authority session.Authority) error {
	if child == nil {
		return errors.New("agent: nil delegated child")
	}
	return child.RestoreLabels(owner, authority)
}
