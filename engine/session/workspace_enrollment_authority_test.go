package session

import (
	"errors"
	"reflect"
	"testing"
)

func TestWorkspaceEnrollmentAuthority_Scenario1_ReplacesDynamicBrokerBundle(t *testing.T) {
	s := newEnrollmentSession(t)
	s.Authority.CapabilitySet.Tools = []string{"Read", "Memory", "broker-old", "broker-excluded"}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"broker-old", "broker-excluded"}, true); err != nil {
		t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
	}
	// Model an intentionally attenuated carried authority: the prior broker key is
	// recorded but excluded, while unrelated capability absence remains untouched.
	s.Authority.CapabilitySet.Tools = []string{"Read", "Memory", "broker-old"}
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"broker-excluded", "broker-new"}); err != nil {
		t.Fatalf("CompleteWorkspaceEnrollment: %v", err)
	}

	got, bound := s.BoundAuthority()
	if !bound {
		t.Fatal("authority is not bound")
	}
	if want := []string{"Read", "Memory", "broker-new"}; !reflect.DeepEqual(got.CapabilitySet.Tools, want) {
		t.Fatalf("authority tools = %q, want %q", got.CapabilitySet.Tools, want)
	}
	keys, present := s.WorkspaceEnrollmentBrokerKeys()
	if !present || !reflect.DeepEqual(keys, []string{"broker-excluded", "broker-new"}) {
		t.Fatalf("broker ledger = %q, %t; want completed bundle, true", keys, present)
	}
}

func TestWorkspaceEnrollmentAuthority_Scenario1_ExclusionSurvivesOmittingRefresh(t *testing.T) {
	s := newEnrollmentSession(t)
	s.Authority.CapabilitySet.Tools = []string{"Read", "A"}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"A", "C", "B"}, true); err != nil {
		t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
	}

	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment: %v", err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"A"}); err != nil {
		t.Fatalf("CompleteWorkspaceEnrollment omitting B: %v", err)
	}
	keys, present := s.WorkspaceEnrollmentBrokerKeys()
	if !present || !reflect.DeepEqual(keys, []string{"A", "B", "C"}) {
		t.Fatalf("ledger after omitting B and C = %q, %t; want [A B C], true", keys, present)
	}

	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatalf("BeginWorkspaceEnrollment after refresh: %v", err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"A", "B"}); err != nil {
		t.Fatalf("CompleteWorkspaceEnrollment reoffering B: %v", err)
	}
	got, _ := s.BoundAuthority()
	if want := []string{"Read", "A"}; !reflect.DeepEqual(got.CapabilitySet.Tools, want) {
		t.Fatalf("authority after reoffering B = %q, want %q", got.CapabilitySet.Tools, want)
	}

	t.Run("overflow is atomic", func(t *testing.T) {
		s := newEnrollmentSession(t)
		excluded := make([]string, maxWorkspaceEnrollmentTools)
		for i := range excluded {
			excluded[i] = string(rune('a'+i/26)) + string(rune('a'+i%26))
		}
		if err := s.RestoreWorkspaceEnrollmentBrokerKeys(excluded, true); err != nil {
			t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
		}
		pending := testWorkspaceEnrollment()
		if err := s.BeginWorkspaceEnrollment(pending); err != nil {
			t.Fatalf("BeginWorkspaceEnrollment: %v", err)
		}
		beforeAuthority, _ := s.BoundAuthority()
		beforeKeys, beforePresent := s.WorkspaceEnrollmentBrokerKeys()
		if err := s.CompleteWorkspaceEnrollment(pending, []string{"new"}); !errors.Is(err, ErrIllegalTransition) {
			t.Fatalf("CompleteWorkspaceEnrollment overflow error = %v, want ErrIllegalTransition", err)
		}
		afterAuthority, _ := s.BoundAuthority()
		afterKeys, afterPresent := s.WorkspaceEnrollmentBrokerKeys()
		if !reflect.DeepEqual(afterAuthority, beforeAuthority) || !reflect.DeepEqual(afterKeys, beforeKeys) || afterPresent != beforePresent {
			t.Fatalf("overflow mutated authority or ledger: authority=%#v keys=%q present=%t", afterAuthority, afterKeys, afterPresent)
		}
		if got, ok := s.PendingWorkspaceEnrollment(); !ok || got != pending {
			t.Fatalf("overflow changed pending enrollment: %+v, %t", got, ok)
		}
	})
}

func TestWorkspaceEnrollmentAuthority_Scenario1_PreservesUnrelatedCapabilities(t *testing.T) {
	s := newEnrollmentSession(t)
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys(nil, true); err != nil {
		t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
	}
	s.Authority.CapabilitySet.Tools = []string{"Read", "Memory"}
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"broker-new"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.BoundAuthority()
	if want := []string{"Read", "Memory", "broker-new"}; !reflect.DeepEqual(got.CapabilitySet.Tools, want) {
		t.Fatalf("authority tools = %q, want %q", got.CapabilitySet.Tools, want)
	}
}

func TestWorkspaceEnrollmentAuthority_Scenario1_DoesNotWidenAttenuatedAuthority(t *testing.T) {
	s := newEnrollmentSession(t)
	s.Authority.CapabilitySet.Tools = []string{"Read"}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"broker-old"}, true); err != nil {
		t.Fatal(err)
	}
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"broker-old", "broker-new"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.BoundAuthority()
	if want := []string{"Read", "broker-new"}; !reflect.DeepEqual(got.CapabilitySet.Tools, want) {
		t.Fatalf("authority tools = %q, want %q", got.CapabilitySet.Tools, want)
	}
}

func TestWorkspaceEnrollmentAuthority_Scenario1_PreservesSupportedRestrictions(t *testing.T) {
	s := newEnrollmentSession(t)
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys(nil, true); err != nil {
		t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
	}
	before := s.Authority.Clone()
	s.Authority.CapabilitySet.Tools = []string{"Read"}
	before.CapabilitySet.Tools = []string{"Read"}
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"broker"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.BoundAuthority()
	got.CapabilitySet.Tools = nil
	before.CapabilitySet.Tools = nil
	if !reflect.DeepEqual(got, before) {
		t.Fatalf("completion changed non-tool authority axes: got %#v, want %#v", got, before)
	}
}

func TestWorkspaceEnrollmentAuthority_Scenario2_RefreshSubtractsRestoredBrokerKeys(t *testing.T) {
	s := newEnrollmentSession(t)
	s.Authority.CapabilitySet.Tools = []string{"Read", "broker-old"}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"broker-old"}, true); err != nil {
		t.Fatal(err)
	}
	pending := testWorkspaceEnrollment()
	if err := s.BeginWorkspaceEnrollment(pending); err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteWorkspaceEnrollment(pending, []string{"broker-new"}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.BoundAuthority()
	if want := []string{"Read", "broker-new"}; !reflect.DeepEqual(got.CapabilitySet.Tools, want) {
		t.Fatalf("authority after restored-ledger refresh = %q, want %q", got.CapabilitySet.Tools, want)
	}
}
func TestWorkspaceEnrollmentAuthority_Scenario2_ProvenancePresenceAndBounds(t *testing.T) {
	s := newEnrollmentSession(t)
	keys, present := s.WorkspaceEnrollmentBrokerKeys()
	if present || len(keys) != 0 {
		t.Fatalf("new session ledger = %q, %t; want absent", keys, present)
	}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys(nil, true); err != nil {
		t.Fatalf("RestoreWorkspaceEnrollmentBrokerKeys: %v", err)
	}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys(nil, true); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("second RestoreWorkspaceEnrollmentBrokerKeys error = %v, want ErrIllegalTransition", err)
	}

	s = newEnrollmentSession(t)
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"broker"}, false); err == nil {
		t.Fatal("accepted keys with absent provenance")
	}
	if err := s.RestoreWorkspaceEnrollmentBrokerKeys([]string{"broker", "broker"}, true); err == nil {
		t.Fatal("accepted duplicate broker keys")
	}
}
