// Package conformance provides the contract tests every identity broker
// implementation, an identitybroker.Core and identitybroker.CredentialVerifier,
// passes.
//
// Importing testing from this non-test package is intentional: each
// implementation's tests call Run with a factory for fresh, empty instances.
// Most cases call the implementation the way Mecatl does, through
// identitybroker.NewInProcess, so the authentication step is always taken;
// cases about the calling workload call the Core directly. Cases are grouped by area, one
// file per area, so later work adds its cases in its own file.
package conformance

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Implementation is an identity broker under test: the Core Mecatl's calls
// reach and the CredentialVerifier the broker's own checks and AS-1 call.
type Implementation interface {
	identitybroker.Core
	identitybroker.CredentialVerifier
}

// Factory returns a fresh, empty implementation for one test, which reads
// the time from now and has loaded exactly the records Definitions returns.
// Each implementation it returns has its own signing key.
type Factory func(t *testing.T, now func() time.Time) Implementation

// The definitions the contract registers agents with, all in Definitions. An
// implementation's own tests may register with them too; nothing modifies
// them.
var (
	MainDefinition     = identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinMain, Revision: "v1"}
	ReviewerDefinition = identitybroker.DefinitionRef{Source: identitybroker.SourceGlobal, Name: "code-reviewer", Revision: "r1"}
	// DeployerDefinition's ceiling allows every github tool, merge included.
	DeployerDefinition = identitybroker.DefinitionRef{Source: identitybroker.SourceGlobal, Name: "deployer", Revision: "d1"}
	// ToolLessDefinition's ceiling grants nothing.
	ToolLessDefinition = identitybroker.DefinitionRef{Source: identitybroker.SourceGlobal, Name: "tool-less", Revision: "t1"}
	// The built-ins of helper agents that run no named definition.
	ExplorerDefinition = identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinExplorer, Revision: "v1"}
	BranchDefinition   = identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinBranch, Revision: "v1"}
	MemberDefinition   = identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinMember, Revision: "v1"}
)

// DefinitionRefs returns the references of the records Definitions returns,
// in the same order: what an implementation under test has loaded. Each call
// returns a new slice.
func DefinitionRefs() []identitybroker.DefinitionRef {
	return []identitybroker.DefinitionRef{MainDefinition, ReviewerDefinition, DeployerDefinition, ToolLessDefinition, ExplorerDefinition, BranchDefinition, MemberDefinition}
}

// ReviewerExcludedTool is the one tool the code reviewer's ceiling excludes,
// by its name on the github server.
const ReviewerExcludedTool = "pull_request_merge"

// Definitions returns the definition records an implementation under test
// loads: one for each of DefinitionRefs, the built-ins a release ships, a
// global code reviewer, deployer, and tool-less agent. Every ceiling but the
// tool-less agent's, which grants nothing, grants the github and calendar
// servers; the reviewer's excludes ReviewerExcludedTool on github. Each call
// returns new records.
func Definitions() []identitybroker.DefinitionRecord {
	record := func(ref identitybroker.DefinitionRef) identitybroker.DefinitionRecord {
		source := sha256.Sum256([]byte("source " + ref.Name))
		accepted := sha256.Sum256([]byte("definition " + ref.Name))
		github := identitybroker.CeilingServer{Server: "github"}
		if ref == ReviewerDefinition {
			github.ExceptTools = []string{ReviewerExcludedTool}
		}
		ceiling := identitybroker.NewCeiling(github, identitybroker.CeilingServer{Server: "calendar"})
		if ref == ToolLessDefinition {
			ceiling = identitybroker.Ceiling{}
		}
		return identitybroker.DefinitionRecord{Ref: ref, Provenance: identitybroker.Provenance{
			Profile:          "conformance/v1",
			SourceDigest:     identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: hex.EncodeToString(source[:])},
			DefinitionDigest: identitybroker.Digest{Algorithm: identitybroker.DigestSHA256, Value: hex.EncodeToString(accepted[:])},
		}, Ceiling: ceiling}
	}
	refs := DefinitionRefs()
	records := make([]identitybroker.DefinitionRecord, len(refs))
	for i, ref := range refs {
		records[i] = record(ref)
	}
	return records
}

// coreFactory returns an implementation on the real clock.
type coreFactory func(t *testing.T) identitybroker.Core

// serviceFactory returns an implementation as Mecatl sees it.
type serviceFactory func(t *testing.T) identitybroker.Service

// Run executes the identity broker contract against factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	core := func(t *testing.T) identitybroker.Core { return factory(t, time.Now) }
	service := func(t *testing.T) identitybroker.Service { return asMecatl(core(t)) }
	t.Run("sessions", func(t *testing.T) { runSessions(t, service) })
	t.Run("closed sessions", func(t *testing.T) { runClosedSessions(t, service) })
	t.Run("agents", func(t *testing.T) { runAgents(t, service) })
	t.Run("delegations", func(t *testing.T) { runDelegations(t, service) })
	t.Run("credentials", func(t *testing.T) { runCredentials(t, factory) })
	t.Run("credentials for an owner", func(t *testing.T) { runOwnerCredentials(t, factory) })
	t.Run("invalid requests", func(t *testing.T) { runInvalidRequests(t, service) })
	t.Run("workloads", func(t *testing.T) { runWorkloads(t, core) })
}

// asMecatl returns core behind the authentication boundary, as Mecatl calls
// it.
func asMecatl(core identitybroker.Core) identitybroker.Service {
	return identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: workloadA.Name}, core)
}

var (
	workloadA = identitybroker.Workload{Name: "system:serviceaccount:mecatl:mecak8s"}
	workloadB = identitybroker.Workload{Name: "system:serviceaccount:other:intruder"}
)

var (
	alice = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	bob   = identitybroker.Owner{Issuer: "https://login.example.com", Subject: "bob-117"}
)

func openRequest(host string, owner identitybroker.Owner) identitybroker.OpenSessionRequest {
	return identitybroker.OpenSessionRequest{HostSessionID: host, Incarnation: incarnation(host), Owner: owner}
}

func findRequest(host string) identitybroker.FindSessionRequest {
	return identitybroker.FindSessionRequest{RootHostSessionID: host}
}

// closeHostRequest closes host's broker session, as on the deletion of the
// incarnation openRequest opens.
func closeHostRequest(host string) identitybroker.CloseSessionRequest {
	return identitybroker.CloseSessionRequest{RootHostSessionID: host, RootHostIncarnation: incarnation(host), Reason: identitybroker.ReasonSessionClosed}
}

// incarnation returns a well-formed minted incarnation derived from label, so
// cases can name agents readably and the same label gives the same agent.
func incarnation(label string) string {
	sum := sha256.Sum256([]byte(label))
	enc := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	return "inc_" + enc[:25] + "a"
}

// registerRequest registers s's main agent, whose incarnation is its host
// session's.
func registerRequest(s identitybroker.Session) identitybroker.RegisterAgentRequest {
	return identitybroker.RegisterAgentRequest{
		SessionID:     s.ID,
		HostSessionID: s.HostSessionID,
		Owner:         s.Owner,
		Incarnation:   s.Incarnation,
		Definition:    MainDefinition,
	}
}

// childRequest registers a child of parent, which runs in s's root host
// session, with its own host session and an incarnation derived from label.
func childRequest(s identitybroker.Session, parent identitybroker.AgentRef, label string) identitybroker.RegisterAgentRequest {
	req := registerRequest(s)
	req.HostSessionID = s.HostSessionID + "-" + label
	req.Incarnation = incarnation(label)
	req.Parent = parent
	req.ParentHostSessionID = s.HostSessionID
	return req
}

// callReportRequest is a well-formed report of a dispatched attempt in
// session sessionID, which no implementation issued.
func callReportRequest(sessionID string) identitybroker.ReportCallRequest {
	return identitybroker.ReportCallRequest{
		SessionID: sessionID, OperationID: "op-1", AttemptID: "att_conformance",
		Dispatch: identitybroker.DispatchSuccess, Outcome: identitybroker.ObservedUnknown,
	}
}

func authorizeRequest(sessionID string) identitybroker.AuthorizeCallRequest {
	return identitybroker.AuthorizeCallRequest{
		SessionID:       sessionID,
		Credential:      identitybroker.NewToken("credential"),
		OperationID:     "op-1",
		RunID:           "R42",
		RunOwner:        alice,
		Method:          identitybroker.MethodToolsCall,
		Server:          "github",
		Tool:            "github.pull_request_read",
		ArgumentsSHA256: strings.Repeat("ab", 32),
	}
}

// delegationRequest is a well-formed report in sessionID naming agents the
// session need not hold.
func delegationRequest(sessionID string) identitybroker.ReportDelegationRequest {
	return identitybroker.ReportDelegationRequest{
		SessionID: sessionID, DelegationID: "D42",
		DelegatingRun:   delegatingRun,
		DelegatingAgent: identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_aaaaaaaaaaaaaaaaaaaaaaaaaa"},
		ReceivingRun:    receivingRun,
		ReceivingAgent:  identitybroker.AgentRef{Subject: "spiffe://agents.example.com/agent/ag_bbbbbbbbbbbbbbbbbbbbbbbbbb"},
		Owner:           alice,
	}
}

func mustOpen(t *testing.T, svc identitybroker.Service, req identitybroker.OpenSessionRequest) identitybroker.Session {
	t.Helper()
	s, err := svc.OpenSession(context.Background(), req)
	if err != nil {
		t.Fatalf("OpenSession(%s): %v", req.HostSessionID, err)
	}
	return s
}

func mustClose(t *testing.T, svc identitybroker.Service, id string) {
	t.Helper()
	err := svc.CloseSession(context.Background(), identitybroker.CloseSessionRequest{SessionID: id, Reason: identitybroker.ReasonSessionClosed})
	if err != nil {
		t.Fatalf("CloseSession(%s): %v", id, err)
	}
}

// sameError fails unless a and b are both errors with the same text, so a
// caller cannot tell the two situations apart.
func sameError(t *testing.T, call string, a, b error) {
	t.Helper()
	if a == nil || b == nil || a.Error() != b.Error() {
		t.Fatalf("%s: errors %v and %v differ, so the two situations can be told apart", call, a, b)
	}
}

// wantError fails unless err is an *identitybroker.Error with code and reason.
func wantError(t *testing.T, call string, err error, code identitybroker.Code, reason identitybroker.Reason) {
	t.Helper()
	var got *identitybroker.Error
	if !errors.As(err, &got) {
		t.Fatalf("%s: error = %v, want *identitybroker.Error %s %s", call, err, code, reason)
	}
	if got.Code != code || got.Reason != reason {
		t.Fatalf("%s: error = %s %q, want %s %q", call, got.Code, got.Reason, code, reason)
	}
}
