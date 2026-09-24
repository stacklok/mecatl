package mcpbroker_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestContinuityPrincipalPartitionsAreRoleSeparated(t *testing.T) {
	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42"}
	owner, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionOwner, principal)
	if err != nil {
		t.Fatal(err)
	}
	workload, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionWorkload, principal)
	if err != nil {
		t.Fatal(err)
	}
	if owner == workload {
		t.Fatal("owner and workload partitions collided")
	}
	if got := owner; got != [32]byte{0x9a, 0x76, 0x1e, 0xf7, 0xe4, 0xfc, 0x80, 0x4f, 0x7b, 0x4b, 0x22, 0xf4, 0x79, 0xbb, 0xa7, 0xc1, 0x69, 0x4f, 0x90, 0x11, 0x87, 0x22, 0xe3, 0xd6, 0x38, 0x34, 0xa5, 0x42, 0xb5, 0x00, 0x84, 0xf9} {
		t.Fatalf("owner fixed vector = %x", got)
	}
	if got := workload; got != [32]byte{0x72, 0xcd, 0x38, 0xb5, 0xea, 0xac, 0x80, 0xfe, 0xc7, 0x01, 0x14, 0x91, 0xbc, 0xa5, 0x83, 0x25, 0x5a, 0x05, 0x98, 0xfa, 0xf9, 0xce, 0xd6, 0x4e, 0x81, 0x08, 0xc3, 0x51, 0x35, 0x2e, 0x9b, 0x49} {
		t.Fatalf("workload fixed vector = %x", got)
	}
}

func TestContinuityPrincipalPartitionUsesExactIssuerSubject(t *testing.T) {
	base := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42"}
	changedIssuer := &session.Principal{Issuer: "https://issuer.example/", Subject: "owner-42"}
	changedSubject := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42 "}
	want, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionOwner, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []*session.Principal{changedIssuer, changedSubject} {
		got, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionOwner, principal)
		if err != nil || got == want {
			t.Fatalf("partition(%+v) = (%x, %v), want distinct valid partition", principal, got, err)
		}
	}
}

func TestContinuityPrincipalPartitionRejectsNilAndUnsafeFraming(t *testing.T) {
	for _, principal := range []*session.Principal{nil, {Issuer: "issuer\x00", Subject: "subject"}} {
		if _, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionOwner, principal); !errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
			t.Fatalf("partition(%+v) error = %v", principal, err)
		}
	}
}

func TestProtectedProfileDigestIsDeterministic(t *testing.T) {
	profiles := []mcpbroker.ProtectedProfile{{
		Provider: "github", Destination: "https://github.example/mcp", Issuer: "https://accounts.github.example",
		ClientID: "client-a", AuthMode: "oauth", Scopes: []string{"repo", "read:user"}, RequestRefreshToken: true,
	}}
	got, providers, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles)
	if err != nil {
		t.Fatal(err)
	}
	want := [32]byte{0x7c, 0x38, 0x06, 0xb9, 0xb4, 0xbd, 0x65, 0x3e, 0x4b, 0xf0, 0xf7, 0x77, 0x26, 0x7e, 0xdf, 0x4d, 0x53, 0x14, 0xe4, 0xc7, 0x37, 0x99, 0x64, 0xe0, 0x69, 0x7c, 0x07, 0xfd, 0x65, 0xc0, 0x7f, 0xe9}
	if got != want || !reflect.DeepEqual(providers, []string{"github"}) {
		t.Fatalf("fixed digest = %x, providers = %#v", got, providers)
	}
	reordered := profiles
	reordered[0].Scopes = []string{"read:user", "repo"}
	if digest, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", reordered); err != nil || digest != got {
		t.Fatalf("reordered scopes = (%x, %v)", digest, err)
	}
}

func TestProtectedProfileDigestRejectsDuplicateProviders(t *testing.T) {
	profiles := []mcpbroker.ProtectedProfile{
		{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth"},
		{Provider: "github", Destination: "https://other.example/mcp", AuthMode: "oauth"},
	}
	if _, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles); !errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
		t.Fatalf("duplicate providers error = %v", err)
	}
}

func TestProtectedProfileDigestChangesOnCredentialDestinationOrAuthConfig(t *testing.T) {
	base := mcpbroker.ProtectedProfile{Provider: "github", Destination: "https://github.example/mcp", Issuer: "https://issuer.example", ClientID: "client", AuthMode: "oauth", Scopes: []string{"repo"}}
	want, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", []mcpbroker.ProtectedProfile{base})
	if err != nil {
		t.Fatal(err)
	}
	for _, changed := range []mcpbroker.ProtectedProfile{
		func() mcpbroker.ProtectedProfile { v := base; v.Destination = "https://other.example/mcp"; return v }(),
		func() mcpbroker.ProtectedProfile { v := base; v.ClientID = "other-client"; return v }(),
		func() mcpbroker.ProtectedProfile { v := base; v.AuthMode = "dcr"; return v }(),
	} {
		got, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", []mcpbroker.ProtectedProfile{changed})
		if err != nil || got == want {
			t.Fatalf("changed profile digest = (%x, %v), want a distinct valid digest", got, err)
		}
	}
}

func TestProtectedProfileDigestChangesOnCallbackOrDerivedIssuer(t *testing.T) {
	profiles := []mcpbroker.ProtectedProfile{{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth"}}
	base, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"https://broker.example/other", "https://broker.example/oauth"}, {"https://broker.example/callback", "https://broker.example/other"}} {
		got, _, err := mcpbroker.ProtectedProfileDigest(pair[0], pair[1], profiles)
		if err != nil || got == base {
			t.Fatalf("digest(%q, %q) = (%x, %v), want a distinct valid digest", pair[0], pair[1], got, err)
		}
	}
}

func TestProtectedProfileDigestIgnoresSecretBytesAndPaths(t *testing.T) {
	// ProtectedProfile deliberately has no secret or filesystem-path fields; its
	// value is copied before canonicalization, so caller-owned configuration bytes
	// cannot become a durable digest input after the call.
	profiles := []mcpbroker.ProtectedProfile{{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth", Scopes: []string{"repo"}}}
	got, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles)
	if err != nil {
		t.Fatal(err)
	}
	profiles[0].Scopes[0] = "admin"
	again, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", []mcpbroker.ProtectedProfile{{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth", Scopes: []string{"repo"}}})
	if err != nil || again != got {
		t.Fatalf("digest retained caller-owned data = (%x, %v)", again, err)
	}
}

func TestProtectedProfileDigestCanonicalizesScopesAndProviders(t *testing.T) {
	profiles := []mcpbroker.ProtectedProfile{
		{Provider: "slack", Destination: "https://slack.example/mcp", AuthMode: "oauth", Scopes: []string{"chat:write", "chat:read"}},
		{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth", Scopes: []string{"repo", "read:user"}},
	}
	first, providers, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles)
	if err != nil || !reflect.DeepEqual(providers, []string{"github", "slack"}) {
		t.Fatalf("canonical providers = %#v, %v", providers, err)
	}
	profiles[0], profiles[1] = profiles[1], profiles[0]
	profiles[0].Scopes[0], profiles[0].Scopes[1] = profiles[0].Scopes[1], profiles[0].Scopes[0]
	second, providers, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles)
	if err != nil || second != first || !reflect.DeepEqual(providers, []string{"github", "slack"}) {
		t.Fatalf("canonicalized digest = (%x, %#v, %v)", second, providers, err)
	}
}

func TestBrokerCredentialContinuity_Scenario2_ProviderIsolation(t *testing.T) {
	base := []mcpbroker.ProtectedProfile{{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth"}}
	github, providers, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", base)
	if err != nil || !reflect.DeepEqual(providers, []string{"github"}) {
		t.Fatalf("github profile = (%x, %#v, %v)", github, providers, err)
	}
	slack, providers, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", append(base, mcpbroker.ProtectedProfile{Provider: "slack", Destination: "https://slack.example/mcp", AuthMode: "oauth"}))
	if err != nil || github == slack || !reflect.DeepEqual(providers, []string{"github", "slack"}) {
		t.Fatalf("provider set did not isolate digest = (%x, %#v, %v)", slack, providers, err)
	}
}

func TestValidContinuityAttemptDeadlineIsBoundedToTwoMinutes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if !mcpbroker.ValidContinuityAttemptDeadline(now, now.Add(2*time.Minute)) {
		t.Fatal("two-minute deadline rejected")
	}
	if mcpbroker.ValidContinuityAttemptDeadline(now, now.Add(2*time.Minute+time.Nanosecond)) {
		t.Fatal("overlong deadline accepted")
	}
}
