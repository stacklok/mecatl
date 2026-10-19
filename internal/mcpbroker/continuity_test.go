package mcpbroker_test

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestContinuityPrincipalPartitionsAreRoleSeparated(t *testing.T) {
	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42"}
	owner, err := mcpbroker.ContinuityOwnerPartition(principal)
	if err != nil {
		t.Fatal(err)
	}
	workload, err := mcpbroker.ContinuityWorkloadPartition(principal)
	if err != nil {
		t.Fatal(err)
	}
	if [32]byte(owner) == [32]byte(workload) {
		t.Fatal("owner and workload partitions collided")
	}
	if got := owner; got != (mcpbroker.OwnerPartition{0x9a, 0x76, 0x1e, 0xf7, 0xe4, 0xfc, 0x80, 0x4f, 0x7b, 0x4b, 0x22, 0xf4, 0x79, 0xbb, 0xa7, 0xc1, 0x69, 0x4f, 0x90, 0x11, 0x87, 0x22, 0xe3, 0xd6, 0x38, 0x34, 0xa5, 0x42, 0xb5, 0x00, 0x84, 0xf9}) {
		t.Fatalf("owner fixed vector = %x", got)
	}
	if got := workload; got != (mcpbroker.WorkloadPartition{0x72, 0xcd, 0x38, 0xb5, 0xea, 0xac, 0x80, 0xfe, 0xc7, 0x01, 0x14, 0x91, 0xbc, 0xa5, 0x83, 0x25, 0x5a, 0x05, 0x98, 0xfa, 0xf9, 0xce, 0xd6, 0x4e, 0x81, 0x08, 0xc3, 0x51, 0x35, 0x2e, 0x9b, 0x49}) {
		t.Fatalf("workload fixed vector = %x", got)
	}
}

func TestContinuityPrincipalPartitionUsesExactIssuerSubject(t *testing.T) {
	base := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42"}
	changedIssuer := &session.Principal{Issuer: "https://issuer.example/", Subject: "owner-42"}
	changedSubject := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42 "}
	want, err := mcpbroker.ContinuityOwnerPartition(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, principal := range []*session.Principal{changedIssuer, changedSubject} {
		got, err := mcpbroker.ContinuityOwnerPartition(principal)
		if err != nil || got == want {
			t.Fatalf("partition(%+v) = (%x, %v), want distinct valid partition", principal, got, err)
		}
	}
}

func TestContinuityPrincipalPartitionRejectsNilAndUnsafeFraming(t *testing.T) {
	for _, principal := range []*session.Principal{nil, {Issuer: "issuer\x00", Subject: "subject"}, {Issuer: "issuer", Subject: "sub\x00ject"}} {
		if _, err := mcpbroker.ContinuityOwnerPartition(principal); !errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
			t.Fatalf("owner partition(%+v) error = %v", principal, err)
		}
		if _, err := mcpbroker.ContinuityWorkloadPartition(principal); !errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
			t.Fatalf("workload partition(%+v) error = %v", principal, err)
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
	want := mcpbroker.ProfileDigest{0x7c, 0x38, 0x06, 0xb9, 0xb4, 0xbd, 0x65, 0x3e, 0x4b, 0xf0, 0xf7, 0x77, 0x26, 0x7e, 0xdf, 0x4d, 0x53, 0x14, 0xe4, 0xc7, 0x37, 0x99, 0x64, 0xe0, 0x69, 0x7c, 0x07, 0xfd, 0x65, 0xc0, 0x7f, 0xe9}
	if got != want || !reflect.DeepEqual(providers, []string{"github"}) {
		t.Fatalf("fixed digest = %x, providers = %#v", got, providers)
	}
	reordered := append([]mcpbroker.ProtectedProfile(nil), profiles...)
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

func TestProtectedProfileDigestDoesNotMutateInput(t *testing.T) {
	// The digest sorts scopes and providers internally. Sorting must happen on
	// copies: the caller's slices keep their order, so one configuration slice can
	// be digested and then used again without surprises.
	profiles := []mcpbroker.ProtectedProfile{
		{Provider: "slack", Destination: "https://slack.example/mcp", AuthMode: "oauth", Scopes: []string{"chat:write", "chat:read"}},
		{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth", Scopes: []string{"repo", "read:user"}},
	}
	before := []mcpbroker.ProtectedProfile{
		{Provider: "slack", Destination: "https://slack.example/mcp", AuthMode: "oauth", Scopes: []string{"chat:write", "chat:read"}},
		{Provider: "github", Destination: "https://github.example/mcp", AuthMode: "oauth", Scopes: []string{"repo", "read:user"}},
	}
	if _, _, err := mcpbroker.ProtectedProfileDigest("https://broker.example/callback", "https://broker.example/oauth", profiles); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(profiles, before) {
		t.Fatalf("input mutated: %#v", profiles)
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

func TestProtectedProfileDigestDiffersByProviderSet(t *testing.T) {
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

func TestContinuityValueTypesZeroAndEqual(t *testing.T) {
	principal := &session.Principal{Issuer: "https://issuer.example", Subject: "owner-42"}
	owner, err := mcpbroker.ContinuityOwnerPartition(principal)
	if err != nil {
		t.Fatal(err)
	}
	other, err := mcpbroker.ContinuityOwnerPartition(&session.Principal{Issuer: "https://issuer.example", Subject: "owner-43"})
	if err != nil {
		t.Fatal(err)
	}
	if owner.IsZero() || !(mcpbroker.OwnerPartition{}).IsZero() {
		t.Fatal("OwnerPartition.IsZero is wrong")
	}
	if !owner.Equal(owner) || owner.Equal(other) || owner.Equal(mcpbroker.OwnerPartition{}) {
		t.Fatal("OwnerPartition.Equal is wrong")
	}
	workload, err := mcpbroker.ContinuityWorkloadPartition(principal)
	if err != nil {
		t.Fatal(err)
	}
	if workload.IsZero() || !workload.Equal(workload) || workload.Equal(mcpbroker.WorkloadPartition{}) {
		t.Fatal("WorkloadPartition IsZero/Equal is wrong")
	}
	// Values that differ only in their last byte must not compare equal.
	var ownerLow, ownerHigh mcpbroker.OwnerPartition
	ownerHigh[31] = 1
	var workloadLow, workloadHigh mcpbroker.WorkloadPartition
	workloadHigh[31] = 1
	var digestLow, digestHigh mcpbroker.ProfileDigest
	digestHigh[31] = 1
	if ownerLow.Equal(ownerHigh) || workloadLow.Equal(workloadHigh) || digestLow.Equal(digestHigh) {
		t.Fatal("Equal ignored the last byte")
	}
	digest := digestOf(t, fullProfile())
	changed := fullProfile()
	changed.ClientID = "other-client"
	if digest.IsZero() || !(mcpbroker.ProfileDigest{}).IsZero() || !digest.Equal(digest) || digest.Equal(digestOf(t, changed)) {
		t.Fatal("ProfileDigest IsZero/Equal is wrong")
	}
}

func TestValidContinuityAttemptDeadlineRejectsZeroPastAndNow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for name, deadline := range map[string]time.Time{
		"zero": {}, "past": now.Add(-time.Second), "now": now,
	} {
		if mcpbroker.ValidContinuityAttemptDeadline(now, deadline) {
			t.Fatalf("%s deadline accepted", name)
		}
	}
	if !mcpbroker.ValidContinuityAttemptDeadline(now, now.Add(time.Nanosecond)) {
		t.Fatal("deadline just after now rejected")
	}
}

func TestValidContinuityProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"typical", "github", true},
		{"non-ascii utf8", "g\u00efthub", true},
		{"at byte limit", strings.Repeat("a", mcpbroker.MaxContinuityProviderBytes), true},
		{"over byte limit", strings.Repeat("a", mcpbroker.MaxContinuityProviderBytes+1), false},
		{"empty", "", false},
		{"nul", "git\x00hub", false},
		{"control below space", "git\x1fhub", false},
		{"delete", "git\x7fhub", false},
		{"invalid utf8", "git\xffhub", false},
	} {
		if got := mcpbroker.ValidContinuityProvider(tc.value); got != tc.want {
			t.Errorf("%s: ValidContinuityProvider(%q) = %v, want %v", tc.name, tc.value, got, tc.want)
		}
	}
}

const (
	testCallback = "https://broker.example/callback"
	testIssuer   = "https://broker.example/oauth"
)

func fullProfile() mcpbroker.ProtectedProfile {
	return mcpbroker.ProtectedProfile{
		Provider: "github", Destination: "https://github.example/mcp", Issuer: "https://issuer.example",
		AuthorizationEndpoint: "https://issuer.example/authorize", TokenEndpoint: "https://issuer.example/token",
		DCRDiscoveryURL: "https://issuer.example/register", ClientID: "client", AuthMode: "oauth",
		Scopes: []string{"repo", "read:user"}, RequestRefreshToken: true,
	}
}

func digestOf(t *testing.T, profile mcpbroker.ProtectedProfile) mcpbroker.ProfileDigest {
	t.Helper()
	digest, _, err := mcpbroker.ProtectedProfileDigest(testCallback, testIssuer, []mcpbroker.ProtectedProfile{profile})
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// Every field of a fully populated profile must change the digest, and no two
// single-field changes may collide. A field dropped from the encoding, or two
// fields swapped in it, would otherwise go unnoticed.
func TestProtectedProfileDigestCoversEveryField(t *testing.T) {
	base := digestOf(t, fullProfile())
	mutators := map[string]func(*mcpbroker.ProtectedProfile){
		"Provider":              func(p *mcpbroker.ProtectedProfile) { p.Provider = "gitlab" },
		"Destination":           func(p *mcpbroker.ProtectedProfile) { p.Destination = "https://other.example/mcp" },
		"Issuer":                func(p *mcpbroker.ProtectedProfile) { p.Issuer = "https://other.example" },
		"AuthorizationEndpoint": func(p *mcpbroker.ProtectedProfile) { p.AuthorizationEndpoint = "https://issuer.example/other" },
		"TokenEndpoint":         func(p *mcpbroker.ProtectedProfile) { p.TokenEndpoint = "https://issuer.example/other" },
		"DCRDiscoveryURL":       func(p *mcpbroker.ProtectedProfile) { p.DCRDiscoveryURL = "https://issuer.example/other" },
		"ClientID":              func(p *mcpbroker.ProtectedProfile) { p.ClientID = "other-client" },
		"AuthMode":              func(p *mcpbroker.ProtectedProfile) { p.AuthMode = "dcr" },
		"RequestRefreshToken":   func(p *mcpbroker.ProtectedProfile) { p.RequestRefreshToken = false },
		"Scopes replaced":       func(p *mcpbroker.ProtectedProfile) { p.Scopes = []string{"repo", "admin"} },
		"Scopes dropped":        func(p *mcpbroker.ProtectedProfile) { p.Scopes = []string{"repo"} },
	}
	seen := map[mcpbroker.ProfileDigest]string{base: "base"}
	for name, mutate := range mutators {
		profile := fullProfile()
		mutate(&profile)
		got := digestOf(t, profile)
		if other, dup := seen[got]; dup {
			t.Errorf("changing %s collides with %s", name, other)
		}
		seen[got] = name
	}
}

// The digest is persisted in custody records, so its encoding is a stored format:
// changing field order or framing would silently orphan every existing record.
// This vector uses a fully populated profile so that reordering fields that are
// empty in simpler fixtures cannot go unnoticed. Update it only with a migration.
func TestProtectedProfileDigestFixedVectorForFullProfile(t *testing.T) {
	want := mcpbroker.ProfileDigest{0x12, 0xee, 0xe0, 0xe9, 0xaa, 0x26, 0xf1, 0xfe, 0xfc, 0x59, 0x4a, 0x8a, 0x5f, 0x1f, 0xa7, 0xf3, 0x4e, 0xa5, 0x17, 0xe4, 0xbf, 0x4c, 0x8d, 0x68, 0x32, 0xe6, 0xce, 0x1a, 0x38, 0xb9, 0xf8, 0x87}
	if got := digestOf(t, fullProfile()); got != want {
		t.Fatalf("full-profile digest = %x, want %x", got, want)
	}
}

// Fields are length-prefixed, so moving bytes across a field boundary changes
// the digest instead of producing the same concatenation.
func TestProtectedProfileDigestFramesFieldBoundaries(t *testing.T) {
	a, b := fullProfile(), fullProfile()
	a.Issuer, a.AuthorizationEndpoint = "ab", ""
	b.Issuer, b.AuthorizationEndpoint = "a", "b"
	if digestOf(t, a) == digestOf(t, b) {
		t.Fatal("issuer/authorization endpoint boundary is ambiguous")
	}
	c, d := fullProfile(), fullProfile()
	c.Scopes = []string{"ab"}
	d.Scopes = []string{"a", "b"}
	if digestOf(t, c) == digestOf(t, d) {
		t.Fatal("scope boundary is ambiguous")
	}
}

func TestProtectedProfileDigestRejectsInvalidInput(t *testing.T) {
	with := func(mutate func(*mcpbroker.ProtectedProfile)) []mcpbroker.ProtectedProfile {
		p := fullProfile()
		mutate(&p)
		return []mcpbroker.ProtectedProfile{p}
	}
	tooMany := make([]mcpbroker.ProtectedProfile, mcpbroker.MaxContinuityProviders+1)
	for i := range tooMany {
		tooMany[i] = fullProfile()
		tooMany[i].Provider = fmt.Sprintf("provider-%03d", i)
	}
	atLimit := tooMany[:mcpbroker.MaxContinuityProviders]
	if _, providers, err := mcpbroker.ProtectedProfileDigest(testCallback, testIssuer, atLimit); err != nil || len(providers) != mcpbroker.MaxContinuityProviders {
		t.Fatalf("provider count at limit = (%d, %v)", len(providers), err)
	}
	for name, tc := range map[string]struct {
		callback, issuer string
		profiles         []mcpbroker.ProtectedProfile
	}{
		"empty callback":         {"", testIssuer, with(func(*mcpbroker.ProtectedProfile) {})},
		"empty derived issuer":   {testCallback, "", with(func(*mcpbroker.ProtectedProfile) {})},
		"no profiles":            {testCallback, testIssuer, nil},
		"too many providers":     {testCallback, testIssuer, tooMany},
		"missing destination":    {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.Destination = "" })},
		"missing auth mode":      {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.AuthMode = "" })},
		"empty scope":            {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.Scopes = []string{"repo", ""} })},
		"duplicate scope":        {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.Scopes = []string{"repo", "repo"} })},
		"control in destination": {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.Destination = "https://x.example/\n" })},
		"invalid utf8 in client": {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.ClientID = "c\xff" })},
		"bad provider":           {testCallback, testIssuer, with(func(p *mcpbroker.ProtectedProfile) { p.Provider = "bad\x00name" })},
	} {
		if _, _, err := mcpbroker.ProtectedProfileDigest(tc.callback, tc.issuer, tc.profiles); !errors.Is(err, mcpbroker.ErrContinuityUnavailable) {
			t.Errorf("%s: error = %v, want ErrContinuityUnavailable", name, err)
		}
	}
}
