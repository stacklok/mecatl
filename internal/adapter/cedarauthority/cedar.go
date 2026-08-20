// Package cedarauthority provides the optional Cedar-backed authority evaluator.
package cedarauthority

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	cedar "github.com/cedar-policy/cedar-go"

	"github.com/stacklok/mecatl/engine/port"
)

// DefaultPolicy is the static shipped policy. The carried capability set is
// checked before Cedar evaluates this broad permit, so this policy cannot grant
// a capability the caller did not carry.
var DefaultPolicy = []byte(`permit(principal, action, resource);`)

var definitionGroupGrant = regexp.MustCompile(`(?is)permit\b[^;]*\bDefinition::`)

// Evaluator evaluates one immutable, startup-loaded policy set. Request entities
// are constructed per authorization; no policy or entity is registered globally.
type Evaluator struct {
	policies *cedar.PolicySet
}

var _ port.AuthorityEvaluator = (*Evaluator)(nil)
var _ port.AuthorityOwnerRequirement = (*Evaluator)(nil)

// RequiresOwnerIdentity reports that Cedar policies receive owner entities and
// therefore cannot safely evaluate an ownerless request.
func (*Evaluator) RequiresOwnerIdentity() bool { return true }

// New parses an operator-owned static policy set. Definition-group permits are
// rejected because instance membership in a definition would widen authority.
func New(policy []byte) (*Evaluator, error) {
	if len(strings.TrimSpace(string(policy))) == 0 {
		return nil, fmt.Errorf("cedar authority policy is empty")
	}
	if definitionGroupGrant.Match(policy) {
		return nil, fmt.Errorf("cedar authority policy grants through a definition group")
	}
	policies, err := cedar.NewPolicySetFromBytes("authority.cedar", policy)
	if err != nil {
		return nil, fmt.Errorf("load Cedar authority policy: %w", err)
	}
	return &Evaluator{policies: policies}, nil
}

// AuthorizeTool first enforces the carried capability set, then permits Cedar to
// add only denials such as a workspace path boundary.
func (e *Evaluator) AuthorizeTool(_ context.Context, request port.AuthorityRequest) (port.AuthorityDecision, error) {
	if malformed(request) {
		return port.AuthorityDecision{Reason: "authority request is malformed"}, nil
	}
	if !request.CapabilitySet.AllowsTool(request.ToolName) {
		return port.AuthorityDecision{Reason: "tool is absent from the capability set"}, nil
	}
	if e == nil || e.policies == nil {
		return port.AuthorityDecision{}, fmt.Errorf("cedar authority evaluator is unavailable")
	}

	decision, diagnostics := cedar.Authorize(e.policies, entities(request), cedar.Request{
		Principal: cedar.NewEntityUID("Instance", cedar.String(request.Principal.Instance)),
		Action:    cedar.NewEntityUID("Tool", cedar.String(request.Action)),
		Resource:  cedar.NewEntityUID("Resource", cedar.String(resourceID(request))),
		Context:   cedar.NewRecord(cedar.RecordMap{}),
	})
	if len(diagnostics.Errors) != 0 {
		return port.AuthorityDecision{}, fmt.Errorf("evaluate Cedar authority policy: %s", diagnostics.Errors[0])
	}
	if decision != cedar.Allow {
		return port.AuthorityDecision{Reason: "denied by Cedar authority policy"}, nil
	}
	return port.AuthorityDecision{Allowed: true}, nil
}

func malformed(request port.AuthorityRequest) bool {
	return request.ToolName == "" || request.Action == "" || request.Principal.Definition == "" || request.Principal.Instance == "" || request.Principal.OwnerIssuer == "" || request.Principal.OwnerSubject == ""
}

func entities(request port.AuthorityRequest) cedar.EntityMap {
	instance := cedar.NewEntityUID("Instance", cedar.String(request.Principal.Instance))
	definition := cedar.NewEntityUID("Definition", cedar.String(request.Principal.Definition))
	ownerIssuer := cedar.NewEntityUID("OwnerIssuer", cedar.String(request.Principal.OwnerIssuer))
	ownerSubject := cedar.NewEntityUID("OwnerSubject", cedar.String(request.Principal.OwnerSubject))
	resource := cedar.NewEntityUID("Resource", cedar.String(resourceID(request)))
	path := ""
	workspace := ""
	kind := ""
	if request.Resource != nil {
		path = request.Resource.Path
		workspace = request.Resource.Workspace
		kind = string(request.Resource.Kind)
	}
	return cedar.EntityMap{
		instance:     {UID: instance, Parents: cedar.NewEntityUIDSet(definition), Attributes: cedar.NewRecord(cedar.RecordMap{"owner_issuer": ownerIssuer, "owner_subject": ownerSubject})},
		definition:   {UID: definition, Parents: cedar.NewEntityUIDSet(), Attributes: cedar.NewRecord(cedar.RecordMap{})},
		ownerIssuer:  {UID: ownerIssuer, Parents: cedar.NewEntityUIDSet(), Attributes: cedar.NewRecord(cedar.RecordMap{})},
		ownerSubject: {UID: ownerSubject, Parents: cedar.NewEntityUIDSet(), Attributes: cedar.NewRecord(cedar.RecordMap{})},
		resource:     {UID: resource, Parents: cedar.NewEntityUIDSet(), Attributes: cedar.NewRecord(cedar.RecordMap{"path": cedar.String(path), "workspace": cedar.String(workspace), "kind": cedar.String(kind)})},
	}
}

func resourceID(request port.AuthorityRequest) string {
	if request.Resource == nil {
		return "none"
	}
	return string(request.Resource.Kind) + ":" + request.Resource.Workspace + ":" + request.Resource.Path
}
