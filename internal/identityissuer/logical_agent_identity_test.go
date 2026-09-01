package identityissuer

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestADR_0252_CanonicalLogicalAgentSubject(t *testing.T) {
	const trustDomain = "agents.customer.example"
	wantDigests := map[DefinitionTier]string{
		DefinitionTierSystem:  "lcpozajz4blonusg5fjaa5j5ydb6fzxn4kaduodutd76rziz5r7a",
		DefinitionTierManaged: "vowcou55jd57xu7npp7s4whwj4yldjspoyc2x3ea6da56heoinvq",
		DefinitionTierDriver:  "6od4fjwysrtx2vhkmr3jwatwqzvdteq5ebvo2opyel6rbgyuuq6q",
		DefinitionTierUser:    "khzomn7tx55jceacds6oq76oimkm4vncgleyfg5xdlsnnyuqlqca",
		DefinitionTierProject: "sqbw55ab2fa66i2uxkip2uj55vk3sjigmxavjvo3xjmfprvgeynq",
	}
	for tier, digest := range wantDigests {
		t.Run(string(tier), func(t *testing.T) {
			identity, err := NewLogicalAgentIdentity(trustDomain, tier, "Code Reviewer")
			if err != nil {
				t.Fatal(err)
			}
			want := "spiffe://" + trustDomain + "/mecatl/agent-definition/v1/" + string(tier) + "/code-reviewer--" + digest
			if identity.Subject != want {
				t.Fatalf("Subject = %q, want %q", identity.Subject, want)
			}
		})
	}
}

func TestLogicalAgentIdentityProjection_Scenario1_DefinitionIdentityDoesNotCollapse(t *testing.T) {
	const trustDomain = "agents.customer.example"
	identities := []struct {
		tier DefinitionTier
		name string
	}{
		{DefinitionTierManaged, "Code Reviewer"},
		{DefinitionTierManaged, "code_reviewer"}, // same display slug
		{DefinitionTierProject, "Code Reviewer"},
		{DefinitionTierManaged, "code reviewer"},
		{DefinitionTierManaged, "café"},
		{DefinitionTierManaged, "cafe\u0301"},
		{DefinitionTierManaged, "Code Reviewer Renamed"},
	}
	seen := make(map[string]struct{}, len(identities))
	for _, tc := range identities {
		identity, err := NewLogicalAgentIdentity(trustDomain, tc.tier, tc.name)
		if err != nil {
			t.Fatalf("NewLogicalAgentIdentity(%q, %q): %v", tc.tier, tc.name, err)
		}
		if _, duplicate := seen[identity.Subject]; duplicate {
			t.Fatalf("identity collapsed for tier=%q name=%q: %q", tc.tier, tc.name, identity.Subject)
		}
		seen[identity.Subject] = struct{}{}
	}
}

func TestADR_0252_SubjectAndDefinitionBoundsFailClosed(t *testing.T) {
	const trustDomain = "agents.customer.example"
	for _, tc := range []struct {
		name, subject string
	}{
		{"empty", ""},
		{"invalid UTF-8", string([]byte{0xff})},
		{"malformed path", "spiffe://agents.customer.example/mecatl//agent-definition"},
		{"percent encoded", "spiffe://agents.customer.example/mecatl/%61gent-definition/v1/managed/a--b"},
		{"foreign trust domain", "spiffe://other.example/mecatl/agent-definition/v1/managed/a--b"},
		{"query", "spiffe://agents.customer.example/mecatl/agent-definition/v1/managed/a--b?x=y"},
		{"fragment", "spiffe://agents.customer.example/mecatl/agent-definition/v1/managed/a--b#x"},
		{"over cap", "spiffe://agents.customer.example/" + strings.Repeat("a", 2049)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateLogicalAgentSubject(trustDomain, tc.subject); err == nil {
				t.Fatal("ValidateLogicalAgentSubject() succeeded")
			}
		})
	}
	for _, tc := range []struct {
		name  string
		tier  DefinitionTier
		value string
	}{
		{"empty name", DefinitionTierManaged, ""},
		{"invalid UTF-8", DefinitionTierManaged, string([]byte{0xff})},
		{"control", DefinitionTierManaged, "code\nreviewer"},
		{"oversized", DefinitionTierManaged, strings.Repeat("a", 129)},
		{"unknown tier", "unknown", "code-reviewer"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity, err := NewLogicalAgentIdentity(trustDomain, tc.tier, tc.value)
			if err == nil {
				t.Fatal("NewLogicalAgentIdentity() succeeded")
			}
			if identity != (LogicalAgentIdentity{}) {
				t.Fatalf("NewLogicalAgentIdentity() returned partial identity: %#v", identity)
			}
		})
	}
}

func TestLogicalAgentIdentityProjection_Scenario1_ClosedBoundedValue(t *testing.T) {
	claim, err := NewLogicalAgentClaim(DefinitionTierManaged, "Code Reviewer", "session-42", []string{"Write", "Read"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(claim.Tools, ","), "Read,Write"; got != want {
		t.Fatalf("Tools = %q, want %q", got, want)
	}
	encoded, err := json.Marshal(claim)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "null") {
		t.Fatalf("encoded claim contains null: %s", encoded)
	}
	withoutInstance, err := NewLogicalAgentClaim(DefinitionTierManaged, "Code Reviewer", "", []string{})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(withoutInstance)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "instance") || strings.Contains(string(encoded), "null") {
		t.Fatalf("encoded claim does not omit absent instance safely: %s", encoded)
	}
	if _, err := NewLogicalAgentClaim(DefinitionTierManaged, "Code Reviewer", "", []string{"Read", "Read"}); err == nil {
		t.Fatal("NewLogicalAgentClaim() accepted duplicate requested tools")
	}

	for _, raw := range []string{
		`{}`,
		`{"definition_tier":"managed","definition_name":"Code Reviewer","tools":[]}`, // canonical positive control
		`{"definition_tier":"managed","definition_name":"Code Reviewer","tools":["Read","Read"]}`,
		`{"definition_tier":"managed","definition_name":"Code Reviewer","tools":["Write","Read"]}`,
		`{"definition_tier":"managed","definition_name":"Code Reviewer","tools":[],"unknown":true}`,
		`{"definition_tier":"managed","definition_tier":"managed","definition_name":"Code Reviewer","tools":[]}`,
		`{"definition_tier":"managed","definition_name":"Code Reviewer","instance":null,"tools":[]}`,
	} {
		parsed, err := ParseLogicalAgentClaim([]byte(raw))
		valid := raw == `{"definition_tier":"managed","definition_name":"Code Reviewer","tools":[]}`
		if valid && err != nil {
			t.Fatalf("ParseLogicalAgentClaim(%s): %v", raw, err)
		}
		if !valid && (err == nil || parsed.DefinitionTier != "" || parsed.DefinitionName != "" || parsed.Instance != "" || parsed.Tools != nil) {
			t.Fatalf("ParseLogicalAgentClaim(%s) = %#v, %v; want zero value and error", raw, parsed, err)
		}
	}

	overTotal := make([]string, 33)
	for i := range overTotal {
		overTotal[i] = fmt.Sprintf("%03d", i) + strings.Repeat("a", 253)
	}
	for _, tc := range []LogicalAgentClaim{
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: []string{"Read", "Read"}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: []string{"Write", "Read"}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: string([]byte{0xff}), Tools: []string{}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code\nReviewer", Tools: []string{}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: strings.Repeat("a", 129), Tools: []string{}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: []string{string([]byte{0xff})}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: []string{"\t"}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: make([]string, 257)},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: []string{strings.Repeat("a", 257)}},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Tools: overTotal},
		{DefinitionTier: DefinitionTierManaged, DefinitionName: "Code Reviewer", Instance: strings.Repeat("a", 257)},
	} {
		if err := tc.Validate(); err == nil {
			t.Fatalf("Validate() succeeded for %#v", tc)
		}
	}
}
