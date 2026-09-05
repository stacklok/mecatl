package identityissuer

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/localauthority"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLogicalAgentIdentityProjection_Scenario6_ReviewerCannotDeploy(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	verifier := testLogicalAgentVerifier(t, issuer)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	compact, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity,
		Instance: "review-42",
		Tools:    []string{"Read"},
		Source:   governance.CapabilitySet{Tools: []string{"Read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(compact)
	if err != nil {
		t.Fatal(err)
	}
	assertExactVerifiedTools(t, compact, issuer, verified)

	read, err := evaluateVerifiedTools(verified, "Read")
	if err != nil || !read.Allowed {
		t.Fatalf("verified reviewer Read = (%+v, %v), want allowed", read, err)
	}
	deploy, err := evaluateVerifiedTools(verified, "Deploy")
	if err != nil {
		t.Fatalf("verified reviewer Deploy: %v", err)
	}
	if deploy.Allowed {
		t.Fatalf("verified reviewer Deploy = %+v, want denied", deploy)
	}
}

func TestADR_0301_DeployPositiveControl(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	verifier := testLogicalAgentVerifier(t, issuer)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Deployer")
	if err != nil {
		t.Fatal(err)
	}
	compact, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity,
		Instance: "deploy-42",
		Tools:    []string{"Deploy"},
		Source:   governance.CapabilitySet{Tools: []string{"Deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := verifier.Verify(compact)
	if err != nil {
		t.Fatal(err)
	}
	assertExactVerifiedTools(t, compact, issuer, verified)
	decision, err := evaluateVerifiedTools(verified, "Deploy")
	if err != nil || !decision.Allowed {
		t.Fatalf("verified deployer Deploy = (%+v, %v), want allowed", decision, err)
	}
}

func TestADR_0301_VerifierGrantsCurrentToolsOnly(t *testing.T) {
	issuer := testLogicalAgentIssuer(t)
	verifier := testLogicalAgentVerifier(t, issuer)
	identity, err := NewLogicalAgentIdentity(issuer.TrustDomain(), DefinitionTierManaged, "Code Reviewer")
	if err != nil {
		t.Fatal(err)
	}
	readToken, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity, Instance: "review-42", Tools: []string{"Read"}, Source: governance.CapabilitySet{Tools: []string{"Read"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	deployToken, err := issuer.IssueLogicalAgent(LogicalAgentIssueRequest{
		Identity: identity, Instance: "review-42", Tools: []string{"Deploy"}, Source: governance.CapabilitySet{Tools: []string{"Deploy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	readVerified, err := verifier.Verify(readToken)
	if err != nil {
		t.Fatal(err)
	}
	deployVerified, err := verifier.Verify(deployToken)
	if err != nil {
		t.Fatal(err)
	}
	if readVerified.Subject != deployVerified.Subject {
		t.Fatalf("same logical definition subject drifted: %q != %q", readVerified.Subject, deployVerified.Subject)
	}
	if !reflect.DeepEqual(readVerified.Tools, []string{"Read"}) || !reflect.DeepEqual(deployVerified.Tools, []string{"Deploy"}) {
		t.Fatalf("independent tokens were unioned: read=%q deploy=%q", readVerified.Tools, deployVerified.Tools)
	}

	tampered := addUnsignedDeployTool(t, readToken)
	evaluations := 0
	if decision, err := verifyThenEvaluate(verifier, tampered, "Deploy", &evaluations); err == nil || decision.Allowed {
		t.Fatalf("tampered token evaluation = (%+v, %v), want verification failure", decision, err)
	}
	if evaluations != 0 {
		t.Fatalf("tampered token reached evaluator %d times", evaluations)
	}
	if decision, err := evaluateVerifiedTools(readVerified, "Deploy"); err != nil || decision.Allowed {
		t.Fatalf("prior read token deploy = (%+v, %v), want denied", decision, err)
	}
}

func TestADR_0301_NoLifecycleWiringOrPersistenceDrift(t *testing.T) {
	for _, dir := range []string{"../../internal/app", "../../internal/adapter/server", "../../engine/agent"} {
		assertNoIssuerLifecycleImport(t, dir)
	}

	s := session.New("i2-compat", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0).UTC())
	snapshot, err := sessnap.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"identityissuer", "logical_agent", "issuer", "token", "bundle"} {
		if bytes.Contains(snapshot, []byte(forbidden)) {
			t.Fatalf("session snapshot leaked I2 state %q: %s", forbidden, snapshot)
		}
	}
	event, err := json.Marshal(session.Event{Type: session.EvResult, Turn: 1, Result: &session.ResultPayload{Stop: session.StopEndTurn}})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"identityissuer", "logical_agent", "issuer", "token", "bundle"} {
		if bytes.Contains(event, []byte(forbidden)) {
			t.Fatalf("session event leaked I2 state %q: %s", forbidden, event)
		}
	}
}

func assertExactVerifiedTools(t *testing.T, compact string, issuer *Issuer, verified VerifiedLogicalAgent) {
	t.Helper()
	claim := logicalAgentTokenClaim(t, issuer, compact)
	claimTools, err := json.Marshal(claim.Tools)
	if err != nil {
		t.Fatal(err)
	}
	verifiedTools, err := json.Marshal(verified.Tools)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(claimTools, verifiedTools) {
		t.Fatalf("signed and verified tools differ: signed=%s verified=%s", claimTools, verifiedTools)
	}
	evaluatedTools, err := json.Marshal(capabilitySetFromVerified(verified).Tools)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(verifiedTools, evaluatedTools) {
		t.Fatalf("verified and evaluated tools differ: verified=%s evaluated=%s", verifiedTools, evaluatedTools)
	}
}

func capabilitySetFromVerified(verified VerifiedLogicalAgent) governance.CapabilitySet {
	return governance.CapabilitySet{Tools: append([]string(nil), verified.Tools...)}
}

func verifyThenEvaluate(verifier *LogicalAgentVerifier, compact, tool string, evaluations *int) (port.AuthorityDecision, error) {
	verified, err := verifier.Verify(compact)
	if err != nil {
		return port.AuthorityDecision{}, err
	}
	*evaluations++
	return evaluateVerifiedTools(verified, tool)
}

func evaluateVerifiedTools(verified VerifiedLogicalAgent, tool string) (port.AuthorityDecision, error) {
	return localauthority.New().AuthorizeTool(context.Background(), port.AuthorityRequest{
		CapabilitySet: capabilitySetFromVerified(verified),
		ToolName:      tool,
		Action:        tool,
		Principal: port.AuthorityPrincipal{
			Definition: verified.Subject,
			Instance:   verified.Instance,
		},
	})
}

func addUnsignedDeployTool(t *testing.T, compact string) string {
	t.Helper()
	parts := strings.Split(compact, ".")
	if len(parts) != 3 {
		t.Fatal("compact token is malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatal(err)
	}
	profile := claims[LogicalAgentClaimURI].(map[string]any)
	profile["tools"] = []string{"Deploy", "Read"}
	payload, err = json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	return strings.Join(parts, ".")
}

func assertNoIssuerLifecycleImport(t *testing.T, dir string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(body), "internal/identityissuer") {
			t.Fatalf("I2 lifecycle wiring appeared in %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan %s: %v", dir, err)
	}
}
