package session

import (
	"encoding/base64"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
)

func brokerAuthoritySession(t *testing.T, scope *[]string) *Session {
	t.Helper()
	s := New("broker-authority", ModeDefault, EnvironmentRef{Kind: EnvKindLocal, ID: "test", Revision: "v1"}, Limits{}, time.Unix(1, 0))
	if err := s.BindAuthority(Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"Read", "resource://opaque"}, FileSystem: true, DirectWrite: true, RemainingDelegationDepth: 3}, Provenance: "test", BrokerToolScope: scope}); err != nil {
		t.Fatal(err)
	}
	return s
}

func brokerAuthorityRefs() (BrokerSessionRef, BrokerCatalogueRef) {
	return BrokerSessionRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32))), BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
}

func TestBrokerAuthorityContributions(t *testing.T) {
	for _, grant := range []string{"none", "before", "after"} {
		t.Run(grant, func(t *testing.T) {
			s := brokerAuthoritySession(t, nil)
			ref, cat := brokerAuthorityRefs()
			if grant == "before" {
				if err := s.GrantToolAuthority([]string{"mcp__test__x"}); err != nil {
					t.Fatal(err)
				}
			}
			names := []string{"mcp__test__x", "mcp__test__y"}
			if err := s.AdoptBrokerCatalogue(ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), names); err != nil {
				t.Fatal(err)
			}
			names[0] = "mutated"
			if grant == "after" {
				if err := s.GrantToolAuthority([]string{"mcp__test__x", "mcp__test__x"}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.PrepareBrokerInvocation(ref, cat, NewToolCall("attempted", "mcp__test__x", []byte(`{}`)), time.Unix(2, 0)); err != nil {
				t.Fatal(err)
			}
			if err := s.WithdrawBrokerAccess(); err != nil {
				t.Fatal(err)
			}
			want := []string{"Read", "resource://opaque"}
			if grant != "none" {
				want = append(want, "mcp__test__x")
			}
			if !reflect.DeepEqual(s.Authority.CapabilitySet.Tools, want) {
				t.Fatalf("withdrawal tools: %v want %v", s.Authority.CapabilitySet.Tools, want)
			}
			a, _ := s.BrokerAccess()
			if !a.Withdrawn || a.BrokerTools == nil || len(a.BrokerTools) != 0 || a.Current == nil || a.Current.CallID != "attempted" || a.Current.Phase != "reserved" {
				t.Fatalf("withdrawal: %+v", a)
			}
			if err := s.AdoptBrokerCatalogue(ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), []string{"mcp__test__y"}); err == nil {
				t.Fatal("ordinary adoption reopened withdrawal")
			}
			pending := PendingWorkspaceEnrollment{ID: "reenroll", RequiredServices: 1, ExpiresAt: time.Unix(100, 0)}
			if err := s.BeginWorkspaceEnrollment(pending); err != nil {
				t.Fatal(err)
			}
			before := s.Authority.Clone()
			bad := pending
			bad.RequiredServices++
			if err := s.CompleteWorkspaceEnrollmentWithBrokerCatalogue(bad, ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), []string{"mcp__test__y"}); err == nil {
				t.Fatal("mismatched completion")
			}
			if !reflect.DeepEqual(before, s.Authority) {
				t.Fatal("failed completion mutated authority")
			}
			if err := s.CompleteWorkspaceEnrollmentWithBrokerCatalogue(pending, ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), []string{"mcp__test__y"}); err != nil {
				t.Fatal(err)
			}
			a, _ = s.BrokerAccess()
			if a.Withdrawn || a.Current == nil || a.Current.CallID != "attempted" || a.Current.Phase != "reserved" || !reflect.DeepEqual(a.IndependentTools, want) {
				t.Fatalf("reenrollment: %+v", a)
			}
			if _, ok := s.PendingWorkspaceEnrollment(); ok {
				t.Fatal("enrollment not settled")
			}
			if !s.Authority.CapabilitySet.FileSystem || !s.Authority.CapabilitySet.DirectWrite || s.Authority.CapabilitySet.RemainingDelegationDepth != 3 {
				t.Fatal("non-tool axes changed")
			}
		})
	}
}

func TestBrokerAuthorityFiniteScopeAndExplicitGrant(t *testing.T) {
	for _, initial := range [][]string{{}, {"mcp__test__x"}} {
		t.Run("scope", func(t *testing.T) {
			scope := append([]string{}, initial...)
			s := brokerAuthoritySession(t, &scope)
			ref, cat := brokerAuthorityRefs()
			names := []string{"mcp__test__x", "mcp__test__y", "*"}
			if err := s.AdoptBrokerCatalogue(ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), names); err != nil {
				t.Fatal(err)
			}
			a, _ := s.BrokerAccess()
			if !reflect.DeepEqual(a.BrokerTools, initial) {
				t.Fatalf("finite scope widened: %+v", a)
			}
			if len(scope) != 0 {
				scope[0] = "mutated"
			}
			if err := s.GrantToolAuthority([]string{"mcp__test__y"}); err != nil {
				t.Fatal(err)
			}
			if err := s.AdoptBrokerCatalogue(ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), names); err != nil {
				t.Fatal(err)
			}
			a, _ = s.BrokerAccess()
			if !s.Authority.CapabilitySet.AllowsTool("mcp__test__y") || s.Authority.CapabilitySet.AllowsTool("*") {
				t.Fatal("grant failed or exact membership widened")
			}
			before := s.Authority.Clone()
			old, _ := s.BrokerAccess()
			if err := s.GrantToolAuthority([]string{"mcp__test__x", "bad\nname"}); err == nil {
				t.Fatal("invalid batch accepted")
			}
			now, _ := s.BrokerAccess()
			if !reflect.DeepEqual(before, s.Authority) || !reflect.DeepEqual(old, now) {
				t.Fatal("invalid grant partially applied")
			}
			a.IndependentTools[0] = "mutated"
			a.BrokerTools[0] = "mutated"
			bound, _ := s.BoundAuthority()
			(*bound.BrokerToolScope)[0] = "mutated"
			now, _ = s.BrokerAccess()
			if now.IndependentTools[0] != "Read" || now.BrokerTools[0] == "mutated" || (*s.Authority.BrokerToolScope)[0] == "mutated" {
				t.Fatal("egress alias")
			}
		})
	}
}

func TestBrokerAuthorityRestoreValidation(t *testing.T) {
	scope := []string{"mcp__test__x"}
	s := brokerAuthoritySession(t, &scope)
	ref, cat := brokerAuthorityRefs()
	if err := s.AdoptBrokerCatalogue(ref, cat, BrokerConnectionRef(cat), time.Unix(100, 0), scope); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*BrokerAccess)
	}{
		{"missing independent", func(a *BrokerAccess) { a.IndependentTools = nil }},
		{"missing broker", func(a *BrokerAccess) { a.BrokerTools = nil }},
		{"duplicate independent", func(a *BrokerAccess) { a.IndependentTools = append(a.IndependentTools, "Read") }},
		{"unsafe independent", func(a *BrokerAccess) { a.IndependentTools[0] = "bad\nname" }},
		{"duplicate broker", func(a *BrokerAccess) { a.BrokerTools = append(a.BrokerTools, "mcp__test__x") }},
		{"out of scope", func(a *BrokerAccess) { a.BrokerTools = []string{"mcp__test__y"} }},
		{"withdrawn contribution", func(a *BrokerAccess) { a.Withdrawn = true }},
		{"projection mismatch", func(a *BrokerAccess) { a.IndependentTools = []string{} }},
		{"bad reference", func(a *BrokerAccess) { a.Session = "bad" }},
		{"bad fence", func(a *BrokerAccess) { a.Pending = "unattempted" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, _ := s.BrokerAccess()
			tc.mutate(&a)
			before, _ := s.BrokerAccess()
			if err := s.RestoreBrokerAccess(a); err == nil {
				t.Fatal("malformed restore accepted")
			}
			after, _ := s.BrokerAccess()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed restore mutated")
			}
		})
	}
	a, _ := s.BrokerAccess()
	if err := s.RestoreBrokerAccess(a); err != nil {
		t.Fatal(err)
	}
	a.IndependentTools[0] = "mutated"
	a.BrokerTools[0] = "mutated"
	now, _ := s.BrokerAccess()
	if now.IndependentTools[0] != "Read" || now.BrokerTools[0] != "mcp__test__x" {
		t.Fatal("ingress alias")
	}
	for _, names := range [][]string{{"Read", "Read"}, {"bad\nname"}} {
		a := s.Authority.Clone()
		a.BrokerToolScope = &names
		if a.Valid() {
			t.Fatal("invalid scope valid")
		}
		other := New("invalid", ModeDefault, s.EnvironmentRef, Limits{}, time.Unix(1, 0))
		if err := other.BindAuthority(a); err == nil {
			t.Fatal("invalid scope bound without broker access")
		}
	}
}
