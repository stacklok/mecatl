package identitybroker

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

// testKey returns a deterministic, test-owned P-256 key derived from label.
func testKey(t *testing.T, label string) *ecdsa.PrivateKey {
	t.Helper()
	scalar := sha256.Sum256([]byte(label))
	key, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), scalar[:])
	if err != nil {
		t.Fatalf("test key: %v", err)
	}
	return key
}

func testConfig() Config {
	return Config{
		TrustDomain:        "agents.example.com",
		Audiences:          []string{"https://broker.example.com", "https://as.example.com"},
		CredentialLifetime: 5 * time.Minute,
	}
}

func digestOf(s string) Digest {
	sum := sha256.Sum256([]byte(s))
	return Digest{Algorithm: DigestSHA256, Value: hex.EncodeToString(sum[:])}
}

func testRecord(source, name string) DefinitionRecord {
	return DefinitionRecord{
		Ref: DefinitionRef{Source: source, Name: name, Revision: "3f2c9a1"},
		Provenance: Provenance{
			Profile:          "git/v1",
			Metadata:         map[string]string{"repository": "example.com/" + source, "path": name + ".md"},
			SourceDigest:     digestOf(source + "/" + name + " bytes"),
			DefinitionDigest: digestOf(source + "/" + name + " normalized"),
		},
	}
}

// opaqueSigner stands in for a non-key signer such as a KMS handle.
type opaqueSigner struct{ pub crypto.PublicKey }

func (s opaqueSigner) Public() crypto.PublicKey { return s.pub }
func (opaqueSigner) Sign(io.Reader, []byte, crypto.SignerOpts) ([]byte, error) {
	return nil, errors.New("not used")
}

func TestNewValid(t *testing.T) {
	t.Parallel()
	reviewer := testRecord("project", "code-reviewer")
	sysMain := testRecord("system", "main")
	b, err := New(testConfig(), []DefinitionRecord{reviewer, sysMain}, testKey(t, "broker"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, want := range []DefinitionRecord{reviewer, sysMain} {
		got, ok := b.Definition(want.Ref)
		if !ok || !got.equal(want) {
			t.Errorf("Definition(%+v) = %+v, %v; want %+v", want.Ref, got, ok, want)
		}
	}
	if _, ok := b.Definition(DefinitionRef{Source: "project", Name: "missing", Revision: "3f2c9a1"}); ok {
		t.Error("Definition found an unloaded reference")
	}
	if b.config.TrustDomain != "agents.example.com" || !slices.Equal(b.config.Audiences, testConfig().Audiences) || b.config.CredentialLifetime != 5*time.Minute {
		t.Errorf("retained config = %+v", b.config)
	}
}

func TestNewAcceptsEmptyDefinitionsAndOpaqueSigner(t *testing.T) {
	t.Parallel()
	signer := &opaqueSigner{pub: testKey(t, "broker").Public()}
	b, err := New(testConfig(), nil, signer)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if b.signer != crypto.Signer(signer) || len(b.definitions) != 0 {
		t.Errorf("broker = %+v", b)
	}
}

func TestNewAcceptsBoundaryInputs(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.TrustDomain = "agents_1-a." + strings.Repeat("b", 255-len("agents_1-a."))
	cfg.CredentialLifetime = MaxCredentialLifetime
	rec := testRecord("Team_A-1.x", "Code_Reviewer-2.0")
	if _, err := New(cfg, []DefinitionRecord{rec}, testKey(t, "broker")); err != nil {
		t.Fatalf("New rejected inputs at the accepted boundary: %v", err)
	}
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Config){
		"missing trust domain":       func(c *Config) { c.TrustDomain = "" },
		"uppercase trust domain":     func(c *Config) { c.TrustDomain = "Agents.example.com" },
		"trust domain with scheme":   func(c *Config) { c.TrustDomain = "spiffe://agents.example.com" },
		"trust domain too long":      func(c *Config) { c.TrustDomain = strings.Repeat("a", 256) },
		"no audiences":               func(c *Config) { c.Audiences = nil },
		"empty audience":             func(c *Config) { c.Audiences = []string{""} },
		"audience with whitespace":   func(c *Config) { c.Audiences = []string{"https://a.example.com "} },
		"audience with format char":  func(c *Config) { c.Audiences = []string{"https://a.example.com\u200b"} },
		"duplicate audience":         func(c *Config) { c.Audiences = []string{"aud", "aud"} },
		"zero lifetime":              func(c *Config) { c.CredentialLifetime = 0 },
		"negative lifetime":          func(c *Config) { c.CredentialLifetime = -time.Second },
		"lifetime above the maximum": func(c *Config) { c.CredentialLifetime = MaxCredentialLifetime + time.Second },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			mutate(&cfg)
			_, err := New(cfg, nil, testKey(t, "broker"))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestNewRejectsMissingOrUnsupportedSigner(t *testing.T) {
	t.Parallel()
	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mismatched := testKey(t, "private half")
	mismatched.PublicKey = testKey(t, "other public half").PublicKey
	tests := map[string]crypto.Signer{
		"nil":                        nil,
		"typed nil ecdsa key":        (*ecdsa.PrivateKey)(nil),
		"ecdsa P-384 key":            p384,
		"ed25519 key":                edKey,
		"nil public key":             &opaqueSigner{},
		"opaque P-384 signer":        &opaqueSigner{pub: p384.Public()},
		"opaque ed25519 signer":      &opaqueSigner{pub: edKey.Public()},
		"typed nil ecdsa public key": &opaqueSigner{pub: (*ecdsa.PublicKey)(nil)},
		"typed nil opaque signer":    (*opaqueSigner)(nil),
		"incomplete ecdsa key":       &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256()}},
		"mismatched public key":      mismatched,
	}
	for name, signer := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := New(testConfig(), nil, signer)
			if !errors.Is(err, ErrInvalidSigner) {
				t.Fatalf("New error = %v, want ErrInvalidSigner", err)
			}
		})
	}
}

func TestNewRejectsInvalidDefinitions(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*DefinitionRecord){
		"missing source":           func(r *DefinitionRecord) { r.Ref.Source = "" },
		"missing name":             func(r *DefinitionRecord) { r.Ref.Name = "" },
		"missing revision":         func(r *DefinitionRecord) { r.Ref.Revision = "" },
		"name with slash":          func(r *DefinitionRecord) { r.Ref.Name = "team/reviewer" },
		"dot name":                 func(r *DefinitionRecord) { r.Ref.Name = "." },
		"dot-dot source":           func(r *DefinitionRecord) { r.Ref.Source = ".." },
		"revision with whitespace": func(r *DefinitionRecord) { r.Ref.Revision = "3f2c 9a1" },
		"missing profile":          func(r *DefinitionRecord) { r.Provenance.Profile = "" },
		"empty metadata key":       func(r *DefinitionRecord) { r.Provenance.Metadata[""] = "x" },
		"missing source digest":    func(r *DefinitionRecord) { r.Provenance.SourceDigest = Digest{} },
		"unsupported digest":       func(r *DefinitionRecord) { r.Provenance.DefinitionDigest.Algorithm = "md5" },
		"uppercase digest value": func(r *DefinitionRecord) {
			r.Provenance.SourceDigest.Value = strings.ToUpper(r.Provenance.SourceDigest.Value)
		},
		"short digest value":            func(r *DefinitionRecord) { r.Provenance.DefinitionDigest.Value = "abc" },
		"profile with zero-width space": func(r *DefinitionRecord) { r.Provenance.Profile = "git/v1\u200b" },
		"invalid UTF-8 metadata value":  func(r *DefinitionRecord) { r.Provenance.Metadata["path"] = "\xff" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			bad := testRecord("project", "code-reviewer")
			mutate(&bad)
			_, err := New(testConfig(), []DefinitionRecord{testRecord("system", "main"), bad}, testKey(t, "broker"))
			if !errors.Is(err, ErrInvalidDefinition) {
				t.Fatalf("New error = %v, want ErrInvalidDefinition", err)
			}
		})
	}
}

// TestDefinitionLookupMatchesWholeReference proves reference equality through
// the broker: every field takes part, comparison is exact, and several
// revisions of one definition coexist.
func TestDefinitionLookupMatchesWholeReference(t *testing.T) {
	t.Parallel()
	rev1 := testRecord("project", "code-reviewer")
	rev2 := testRecord("project", "code-reviewer")
	rev2.Ref.Revision = "8b41d07"
	rev2.Provenance.SourceDigest = digestOf("revision 2 bytes")
	rev2.Provenance.DefinitionDigest = digestOf("revision 2 normalized")
	b, err := New(testConfig(), []DefinitionRecord{rev1, rev2}, testKey(t, "broker"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, want := range []DefinitionRecord{rev1, rev2} {
		lookup := DefinitionRef{Source: want.Ref.Source, Name: want.Ref.Name, Revision: want.Ref.Revision}
		if got, ok := b.Definition(lookup); !ok || !got.equal(want) {
			t.Errorf("Definition(%+v) = %+v, %v; want %+v", lookup, got, ok, want)
		}
	}
	for _, miss := range []DefinitionRef{
		{Source: "admin", Name: "code-reviewer", Revision: rev1.Ref.Revision},
		{Source: "project", Name: "Code-Reviewer", Revision: rev1.Ref.Revision},
		{Source: "project", Name: "code-reviewer", Revision: "3F2C9A1"},
	} {
		if _, ok := b.Definition(miss); ok {
			t.Errorf("Definition(%+v) matched a different reference", miss)
		}
	}
}

func TestNewCollapsesIdenticalDuplicates(t *testing.T) {
	t.Parallel()
	first := testRecord("project", "code-reviewer")
	second := testRecord("project", "code-reviewer") // equal values, distinct metadata map
	b, err := New(testConfig(), []DefinitionRecord{first, second}, testKey(t, "broker"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(b.definitions) != 1 {
		t.Fatalf("retained %d records, want 1", len(b.definitions))
	}

	noMeta, emptyMeta := first, first
	noMeta.Provenance.Metadata, emptyMeta.Provenance.Metadata = nil, map[string]string{}
	if _, err := New(testConfig(), []DefinitionRecord{noMeta, emptyMeta}, testKey(t, "broker")); err != nil {
		t.Fatalf("nil and empty metadata should be identical: %v", err)
	}
}

func TestNewRejectsConflictingDuplicates(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*DefinitionRecord){
		"source digest":     func(r *DefinitionRecord) { r.Provenance.SourceDigest = digestOf("modified bytes") },
		"definition digest": func(r *DefinitionRecord) { r.Provenance.DefinitionDigest = digestOf("other normalized") },
		"profile":           func(r *DefinitionRecord) { r.Provenance.Profile = "git/v2" },
		"metadata value":    func(r *DefinitionRecord) { r.Provenance.Metadata["path"] = "other.md" },
		"extra metadata":    func(r *DefinitionRecord) { r.Provenance.Metadata["branch"] = "main" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			conflict := testRecord("project", "code-reviewer")
			mutate(&conflict)
			_, err := New(testConfig(), []DefinitionRecord{testRecord("project", "code-reviewer"), conflict}, testKey(t, "broker"))
			if !errors.Is(err, ErrConflictingDefinition) {
				t.Fatalf("New error = %v, want ErrConflictingDefinition", err)
			}
		})
	}
}

func TestSameNameFromDifferentSourcesStaysDistinct(t *testing.T) {
	t.Parallel()
	project := testRecord("project", "code-reviewer")
	admin := testRecord("admin", "code-reviewer")
	b, err := New(testConfig(), []DefinitionRecord{project, admin}, testKey(t, "broker"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, want := range []DefinitionRecord{project, admin} {
		got, ok := b.Definition(want.Ref)
		if !ok || !got.equal(want) {
			t.Errorf("Definition(%+v) = %+v, %v; want %+v", want.Ref, got, ok, want)
		}
	}
}

func TestCallerMutationsDoNotAlterRetainedState(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	defs := []DefinitionRecord{testRecord("project", "code-reviewer")}
	key := testKey(t, "broker")
	wantPub := testKey(t, "broker").Public().(*ecdsa.PublicKey) // independent of key's memory
	ref := defs[0].Ref

	b, err := New(cfg, defs, key)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cfg.Audiences[0] = "https://attacker.example.com"
	cfg.TrustDomain = "other.example.com"
	defs[0].Provenance.Metadata["path"] = "attacker.md"
	defs[0].Provenance.SourceDigest = digestOf("swapped")
	defs[0] = testRecord("admin", "deployer")
	*key = *testKey(t, "replacement")

	if !slices.Equal(b.config.Audiences, testConfig().Audiences) || b.config.TrustDomain != "agents.example.com" {
		t.Errorf("retained config changed: %+v", b.config)
	}
	got, ok := b.Definition(ref)
	if !ok || !got.equal(testRecord("project", "code-reviewer")) {
		t.Errorf("retained definition changed: %+v, %v", got, ok)
	}
	if gotPub := b.signer.Public().(*ecdsa.PublicKey); !gotPub.Equal(wantPub) {
		t.Error("retained signing key changed after the caller replaced its key")
	}
}

func TestDefinitionLookupReturnsIndependentCopy(t *testing.T) {
	t.Parallel()
	want := testRecord("project", "code-reviewer")
	b, err := New(testConfig(), []DefinitionRecord{want}, testKey(t, "broker"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	got, _ := b.Definition(want.Ref)
	got.Provenance.Metadata["path"] = "attacker.md"
	got.Provenance.Metadata["extra"] = "x"
	got.Provenance.Profile = "other/v1"

	again, ok := b.Definition(want.Ref)
	if !ok || !again.equal(want) {
		t.Errorf("lookup mutation reached the broker: %+v", again)
	}
}
