package port

import (
	"context"

	"github.com/stacklok/mecatl/engine/governance"
)

// AuthorityPrincipal identifies the authority under which a tool is requested.
// Definition is the resolved agent-definition identity, Instance identifies this
// concrete run, and OwnerIssuer and OwnerSubject are the exact verified caller
// identity pair that owns it. They are distinct from the capability set and
// intentionally contain no credentials.
type AuthorityPrincipal struct {
	Definition   string
	Instance     string
	OwnerIssuer  string
	OwnerSubject string
}

// AuthorityResourceKind identifies the non-secret target class in an authority
// request. It is provider-neutral: evaluators cannot depend on a tool's raw
// argument format to identify a resource.
type AuthorityResourceKind string

const (
	// AuthorityResourceWorkspaceFile is a normalized local target associated with
	// the executing session's workspace.
	AuthorityResourceWorkspaceFile AuthorityResourceKind = "workspace_file"
)

// AuthorityResource is a normalized, non-secret target derived at the execution
// boundary. Path is a clean absolute local path and Workspace identifies the
// current session workspace that supplied it; neither is raw tool-call JSON nor
// an adapter-specific resource identifier.
type AuthorityResource struct {
	Kind      AuthorityResourceKind
	Path      string
	Workspace string
}

// AuthorityRequest is the provider-neutral input to AuthorityEvaluator. The
// carried capability set is the complete local authority fact; evaluators do not
// look it up from mutable state. ToolName selects the carried capability while
// Action identifies the operation the caller requested. They differ for derived
// capabilities such as a named MCP server's resources. DelegationDepth provides
// additional action context; Principal carries attribution without credentials or
// raw tool arguments. Resource is present only when the execution boundary can
// derive an unambiguous non-secret local target.
type AuthorityRequest struct {
	CapabilitySet   governance.CapabilitySet
	ToolName        string
	Action          string
	DelegationDepth int
	Principal       AuthorityPrincipal
	Resource        *AuthorityResource
}

// AuthorityDecision is an evaluator's authorization result. A denied decision
// is a successful evaluation; an unavailable evaluator returns an error so the
// call site can fail closed and report that condition separately.
type AuthorityDecision struct {
	Allowed bool
	Reason  string
}

// AuthorityOwnerRequirement is an optional AuthorityEvaluator capability for
// evaluators whose policy cannot safely authorize an ownerless request. Evaluators
// that do not implement it must accept an absent owner identity when their other
// request requirements are met.
type AuthorityOwnerRequirement interface {
	RequiresOwnerIdentity() bool
}

// AuthorityEvaluator authorizes one tool execution against a carried capability
// set. Implementations may only tighten the set's authority; they must not use
// external policy to grant a tool the set omits.
type AuthorityEvaluator interface {
	AuthorizeTool(context.Context, AuthorityRequest) (AuthorityDecision, error)
}
