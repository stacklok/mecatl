package identityissuer

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stacklok/mecatl/engine/governance"
)

func TestADR_0252_ContainedToolProjectionSucceeds(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	source := governance.CapabilitySet{Tools: []string{"Read", "Write", "mcp__github__get_issue"}, RemainingDelegationDepth: 1, FileSystem: true}
	for _, requested := range [][]string{{"Read", "Write"}, {"Write", "Read"}, {}} {
		token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: requested, Source: source})
		if err != nil {
			t.Fatalf("IssueLogicalAgent(%q): %v", requested, err)
		}
		claim := logicalAgentTokenClaim(t, issuer, token)
		want := append([]string(nil), requested...)
		if len(want) == 2 && want[0] == "Write" {
			want[0], want[1] = want[1], want[0]
		}
		if !reflect.DeepEqual(claim.Tools, want) {
			t.Fatalf("tools = %#v, want %#v", claim.Tools, want)
		}
	}
}

func TestADR_0252_LogicalAgentProjectionNeverWidens(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	request := LogicalAgentIssueRequest{Identity: identity, Tools: []string{"Read", "Write"}, Source: governance.CapabilitySet{Tools: []string{"Read"}}}
	if token, err := issuer.IssueLogicalAgent(request); err == nil || token != "" {
		t.Fatalf("widening issue = %q, %v; want no token and error", token, err)
	}
	request.Source.Tools = append(request.Source.Tools, "Write")
	if token, err := issuer.IssueLogicalAgent(request); err != nil || token == "" {
		t.Fatalf("positive control issue = %q, %v", token, err)
	}
}

func TestLogicalAgentIdentityProjection_Scenario2_ToolNamesStayExact(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	exact := []string{"Read", "read", " Read", "Read ", "café", "cafe\u0301", "mcp__github__get_issue", "mcp__github__get_issue_extra"}
	source := governance.CapabilitySet{Tools: append([]string(nil), exact...)}
	for _, name := range exact {
		token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: []string{name}, Source: source})
		if err != nil || token == "" {
			t.Fatalf("exact tool %q issue = %q, %v", name, token, err)
		}
	}
	for _, name := range []string{"READ", "Read\t", "cafÉ", "mcp__github__get", "mcp__github__get_issue/child", "mcp_resource__github"} {
		token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: []string{name}, Source: source})
		if err == nil || token != "" {
			t.Fatalf("non-exact tool %q issue = %q, %v; want no token and error", name, token, err)
		}
	}
}

func TestADR_0252_ContainmentOracleCorpus(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	source := governance.CapabilitySet{Tools: []string{"Read", " read", "café", "cafe\u0301", "mcp__github__get_issue"}, RemainingDelegationDepth: 1}
	for _, requested := range [][]string{{}, {"Read"}, {"Read", "mcp__github__get_issue"}, {"mcp__github__get_issue", "Read"}, {" read"}, {"café"}, {"cafe\u0301"}, {"read"}, {"mcp__github__get"}} {
		want := source.Contains(governance.CapabilitySet{Tools: requested})
		token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Tools: requested, Source: source})
		if got := err == nil && token != ""; got != want {
			t.Fatalf("requested=%q issue=%t, want source.Contains=%t (token=%q, err=%v)", requested, got, want, token, err)
		}
	}
}

func TestADR_0252_IssuerUsesGovernanceContainment(t *testing.T) {
	parsed, err := parser.ParseFile(token.NewFileSet(), "logical_agent_issue.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	usesGovernance := false
	for _, declaration := range parsed.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "IssueLogicalAgent" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if ok && selector.Sel.Name == "Contains" {
				usesGovernance = true
			}
			return true
		})
	}
	if !usesGovernance {
		t.Fatal("production typed issuance no longer calls CapabilitySet.Contains")
	}
	body, err := os.ReadFile("logical_agent_issue.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("engine/governance")) {
		t.Fatal("production typed issuance no longer depends on governance containment")
	}
	for _, forbidden := range []string{"containsTools", "subset", "containsRequested"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("production typed issuance contains second subset policy %q", forbidden)
		}
	}
}

func TestLogicalAgentIdentityProjection_Scenario3_TypedIssue(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierProject, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Instance: "session-42", Tools: []string{"Write", "Read"}, Source: governance.CapabilitySet{Tools: []string{"Read", "Write"}}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := jwt.NewParser(jwt.WithoutClaimsValidation()).Parse(token, func(parsed *jwt.Token) (any, error) { return issuer.PublicKey(), nil })
	if err != nil || !parsed.Valid || parsed.Method.Alg() != "ES256" {
		t.Fatalf("typed token validation = %v, valid=%t alg=%v", err, parsed != nil && parsed.Valid, parsed.Method)
	}
	claims := parsed.Claims.(jwt.MapClaims)
	if got := claims["iss"]; got != "spiffe://"+issuer.TrustDomain() {
		t.Fatalf("iss = %q", got)
	}
	if got := claims["sub"]; got != identity.Subject {
		t.Fatalf("sub = %q, want %q", got, identity.Subject)
	}
	if got := claims["aud"]; !reflect.DeepEqual(got, []any{testConfig().Audience}) {
		t.Fatalf("aud = %#v", got)
	}
	if got := parsed.Header["kid"]; got != issuer.ActiveKID() {
		t.Fatalf("kid = %q, want %q", got, issuer.ActiveKID())
	}
	issuedAt, issuedOK := claims["iat"].(float64)
	expiresAt, expiresOK := claims["exp"].(float64)
	if !issuedOK || !expiresOK || time.Duration(expiresAt-issuedAt)*time.Second != testConfig().TokenTTL {
		t.Fatalf("issued/expires = %#v/%#v, want fixed TTL %s", claims["iat"], claims["exp"], testConfig().TokenTTL)
	}
	wantClaim := LogicalAgentClaim{DefinitionTier: DefinitionTierProject, DefinitionName: "Code Reviewer", Instance: "session-42", Tools: []string{"Read", "Write"}}
	if got := logicalAgentTokenClaim(t, issuer, token); !reflect.DeepEqual(got, wantClaim) {
		t.Fatalf("typed claim = %#v, want %#v", got, wantClaim)
	}
}

func TestADR_0252_FreshRandomJWTID(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	request := LogicalAgentIssueRequest{Identity: identity, Source: governance.CapabilitySet{}}
	firstRandom, err := issuer.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	secondRandom, err := issuer.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	if logicalAgentJWTID(t, issuer, firstRandom) == logicalAgentJWTID(t, issuer, secondRandom) {
		t.Fatal("fresh random mints reused jti")
	}

	issuer.random = bytes.NewReader(bytes.Repeat([]byte{0x41}, 32))
	first, err := issuer.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	firstID := logicalAgentJWTID(t, issuer, first)
	secondID := logicalAgentJWTID(t, issuer, second)
	want := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 16))
	if firstID != want || secondID != want || len(firstID) != 22 || strings.Contains(firstID, "=") {
		t.Fatalf("jti values = %q, %q; want unpadded 16-byte base64url %q", firstID, secondID, want)
	}
	issuer.random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))
	third, err := issuer.IssueLogicalAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	if logicalAgentJWTID(t, issuer, third) == firstID {
		t.Fatal("distinct random mints reused jti")
	}
}

func TestADR_0252_IssueFailsBeforeReturningCredential(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		request LogicalAgentIssueRequest
		random  *bytes.Reader
	}{
		{"random failure", LogicalAgentIssueRequest{Identity: identity, Source: governance.CapabilitySet{}}, bytes.NewReader(nil)},
		{"invalid claim", LogicalAgentIssueRequest{Identity: identity, Tools: []string{"bad\nname"}, Source: governance.CapabilitySet{Tools: []string{"bad\nname"}}}, nil},
		{"containment failure", LogicalAgentIssueRequest{Identity: identity, Tools: []string{"Write"}, Source: governance.CapabilitySet{}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer.random = tc.random
			token, err := issuer.IssueLogicalAgent(tc.request)
			if err == nil || token != "" {
				t.Fatalf("issue = %q, %v; want no token and error", token, err)
			}
		})
	}
	issuer.random = bytes.NewReader(bytes.Repeat([]byte{0x42}, 16))
	issuer.maxTokenBytes = 1
	if token, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{Identity: identity, Source: governance.CapabilitySet{}}); err == nil || token != "" {
		t.Fatalf("oversized compact issue = %q, %v; want no token and error", token, err)
	}
}

func TestADR_0252_LogicalAgentIssuerIsNotArbitrarySigner(t *testing.T) {
	typ := reflect.TypeOf((*Host)(nil))
	method, ok := typ.MethodByName("IssueLogicalAgent")
	if !ok {
		t.Fatal("Host lacks typed logical-agent issue operation")
	}
	if method.Type.NumIn() != 2 || method.Type.In(1) != reflect.TypeOf(LogicalAgentIssueRequest{}) || method.Type.NumOut() != 2 || method.Type.Out(0).Kind() != reflect.String || !method.Type.Out(1).Implements(reflect.TypeOf((*error)(nil)).Elem()) {
		t.Fatalf("Host IssueLogicalAgent signature = %s; want typed request -> (string, error)", method.Type)
	}
	for _, name := range []string{"IssueJWTSubject", "Sign", "PrivateKey"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Fatalf("Host exposes arbitrary signing method %s", name)
		}
	}
}

func testLogicalAgentIssuer(t *testing.T) *Issuer {
	t.Helper()
	issuer, err := Load(testConfig(), testManifest("active", true), func(string) ([]byte, error) { return testPKCS8(t), nil })
	if err != nil {
		t.Fatal(err)
	}
	issuer.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	return issuer
}

func logicalAgentTokenClaim(t *testing.T, issuer *Issuer, compact string) LogicalAgentClaim {
	t.Helper()
	parsed, err := jwt.NewParser(jwt.WithoutClaimsValidation()).Parse(compact, func(parsed *jwt.Token) (any, error) { return issuer.PublicKey(), nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse token: %v", err)
	}
	value, ok := parsed.Claims.(jwt.MapClaims)[LogicalAgentClaimURI]
	if !ok {
		t.Fatal("logical-agent claim missing")
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := ParseLogicalAgentClaim([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func logicalAgentJWTID(t *testing.T, issuer *Issuer, compact string) string {
	t.Helper()
	parsed, err := jwt.NewParser(jwt.WithoutClaimsValidation()).Parse(compact, func(parsed *jwt.Token) (any, error) { return issuer.PublicKey(), nil })
	if err != nil || !parsed.Valid {
		t.Fatalf("parse token: %v", err)
	}
	id, ok := parsed.Claims.(jwt.MapClaims)["jti"].(string)
	if !ok {
		t.Fatal("jti missing")
	}
	return id
}
