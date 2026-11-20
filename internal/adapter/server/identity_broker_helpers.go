package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Helper agents (subagents, parallel branches, and team members) belong to
// their root session's tree and its broker session. Each is registered when
// the engine first publishes its session, before it runs, with the agent that
// created it as its parent, and admitted again when it is resumed: see
// RegisterHelper, RegisterResumedHelper, and package childstore.

// RegisterHelper registers the helper agent of sess, a child session the
// engine is publishing, in its root session's broker session, with its
// creating agent as its parent. child names the definition the helper runs.
// An error stops the helper before it runs.
//
// The root is the run context's root session. A helper whose root holds no
// registered main agent (no identity broker, a root without an owner, or a
// team with no source session) is not registered: it runs, and its gateway
// calls fail closed. The parent is the agent of sess's parent session, or the
// root's main agent when sess records none (a member of a team created over
// the API, which the team's source session roots).
func (s *Service) RegisterHelper(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	if s.cfg.IdentityBroker == nil || s.cfg.IdentityAgents == nil {
		return nil
	}
	rootID, _ := port.RootSessionIDFromContext(ctx)
	root := string(rootID)
	if root == "" || root == string(sess.ID) {
		return nil
	}
	if _, ok := s.cfg.IdentityAgents.Held(agentcredential.Key{RootSessionID: root, AgentSessionID: root}); !ok {
		return nil
	}
	host := string(sess.ID)
	parentHost := string(sess.Relationship.ParentSessionID)
	if parentHost == "" {
		parentHost = root
	}
	// The lookup moves the parent into the root's current broker session first,
	// if the root moved since the parent registered.
	parent, err := s.cfg.IdentityAgents.Credential(ctx, agentcredential.Key{RootSessionID: root, AgentSessionID: parentHost}, false)
	if err != nil {
		return s.helperParentFailure(ctx, host, parentHost, err)
	}
	// One session tree has one owner. A helper labelled with an owner reports
	// it, so the broker refuses one labelled with another; an unlabelled one,
	// as when ownership is not enforced, reports its parent's.
	owner, ok := identityBrokerOwner(sess.Owner)
	if !ok {
		owner = parent.Owner
	}
	var admitErr error
	admit := func() error {
		admitErr = s.admitIdentityAgent(session.SessionID(root))
		return admitErr
	}
	definition := s.helperDefinition(ctx, sess, child)
	err = s.cfg.IdentityAgents.Ensure(ctx, agentcredential.Key{RootSessionID: root, AgentSessionID: host}, identitybroker.RegisterAgentRequest{
		SessionID:           parent.BrokerSessionID,
		HostSessionID:       host,
		Owner:               owner,
		Incarnation:         string(sess.Incarnation()),
		Definition:          definition,
		Parent:              parent.Agent,
		ParentHostSessionID: parentHost,
	}, admit)
	switch {
	case err == nil:
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelInfo, "registered helper agent",
			"session", host, "parent_session", parentHost, "broker_session", parent.BrokerSessionID,
			"definition", definition.Source+"/"+definition.Name)
		return nil
	case admitErr != nil:
		return admitErr
	case errors.Is(err, agentcredential.ErrDropped),
		errors.Is(err, identitybroker.ReasonError(identitybroker.ReasonSessionClosed, "")):
		// The root's broker session closed or its credentials were dropped
		// meanwhile; the root's next run entry opens a new one.
		s.logIdentityBroker(ctx, "broker session closed during helper registration", host, parent.BrokerSessionID)
		return ErrIdentityBrokerUnavailable
	case errors.Is(err, agentcredential.ErrExpired), errors.Is(err, agentcredential.ErrOtherAgent):
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker returned an unusable helper registration",
			"session", host, "broker_session", parent.BrokerSessionID, "err", err.Error())
		return fmt.Errorf("%w: identity broker returned an unusable registration", ErrInternal)
	default:
		return s.identityBrokerFailure(ctx, "register helper", host, parent.BrokerSessionID, err)
	}
}

// RegisterResumedHelper admits the helper of sess, a child session the engine
// is resuming. A helper whose credential is still held keeps it, moved into
// its root's current broker session if the root moved. One with none, as after
// its root's credentials were dropped or a refused registration, registers as
// RegisterHelper registers a new one, so a helper that cannot register is not
// resumed. A resumed subagent runs the default explorer, so it registers as
// system/explorer; the broker refuses that for an agent it recorded with
// another definition.
//
// Shortcut: the harness resumes every subagent on the default explorer, so a named subagent whose credential is gone cannot be resumed; removed by #2548.
func (s *Service) RegisterResumedHelper(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	if s.cfg.IdentityBroker == nil || s.cfg.IdentityAgents == nil {
		return nil
	}
	rootID, _ := port.RootSessionIDFromContext(ctx)
	root := string(rootID)
	if root == "" || root == string(sess.ID) {
		return nil
	}
	key := agentcredential.Key{RootSessionID: root, AgentSessionID: string(sess.ID)}
	if _, held := s.cfg.IdentityAgents.Held(key); !held {
		return s.RegisterHelper(ctx, sess, child)
	}
	if _, err := s.cfg.IdentityAgents.Credential(ctx, key, false); err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker resumed helper has no usable credential",
			"session", string(sess.ID), "err", err.Error())
		if errors.Is(err, agentcredential.ErrNoCredential) {
			// Dropped between the two lookups: the root's next run entry
			// opens its broker session again.
			return ErrIdentityBrokerUnavailable
		}
		return s.credentialLookupFailure(ctx, string(sess.ID), err)
	}
	return nil
}

// helperParentFailure maps a failed lookup of a helper's parent credential.
// A parent with no credential is an internal fault; other failures map as
// credentialLookupFailure maps them.
func (s *Service) helperParentFailure(ctx context.Context, host, parentHost string, err error) error {
	s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker helper's parent has no usable credential",
		"session", host, "parent_session", parentHost, "err", err.Error())
	if errors.Is(err, agentcredential.ErrNoCredential) {
		return fmt.Errorf("%w: the helper's parent agent is not registered", ErrInternal)
	}
	return s.credentialLookupFailure(ctx, host, err)
}

// credentialLookupFailure maps a held credential's failed renewal or move: a
// credential dropped meanwhile, like an unavailable broker, is retryable; one
// the broker answered unusably is an internal fault; any other broker error
// maps as a registration's does.
func (s *Service) credentialLookupFailure(ctx context.Context, host string, err error) error {
	switch {
	case errors.Is(err, agentcredential.ErrDropped):
		return ErrIdentityBrokerUnavailable
	case errors.Is(err, agentcredential.ErrExpired), errors.Is(err, agentcredential.ErrOtherAgent):
		return fmt.Errorf("%w: identity broker returned an unusable registration", ErrInternal)
	default:
		return s.identityBrokerFailure(ctx, "register helper", host, "", err)
	}
}

// helperAdmitter admits the helpers of a service's child stores.
type helperAdmitter struct{ s *Service }

func (a helperAdmitter) AdmitChild(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	return a.s.RegisterHelper(ctx, sess, child)
}

func (a helperAdmitter) AdmitResumedChild(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) error {
	return a.s.RegisterResumedHelper(ctx, sess, child)
}

// helperDefinition is the reference of the definition sess's helper runs (see
// helperDefinitionRef), logging when the broker loaded none, so the refusal
// that follows can be told apart from an authorization refusal.
func (s *Service) helperDefinition(ctx context.Context, sess *session.Session, child port.ChildSessionInfo) identitybroker.DefinitionRef {
	ref, loaded := helperDefinitionRef(s.cfg.IdentityDefinitions, sess.Kind, child.DefinitionName)
	if !loaded {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker loaded no definition the helper agent runs; the broker refuses its registration",
			"session", string(sess.ID), "definition", child.DefinitionName)
	}
	return ref
}

// helperDefinitionRef is the reference of the definition a helper of kind
// runs, from loaded, the references the broker loaded. A named definition is
// looked up as identityAgentDefinition looks up a session's. A helper that
// runs no named definition has a built-in: system/explorer for a subagent,
// system/branch for a parallel branch, and system/member for a team member.
//
// Shortcut: a helper's definition is known by name only, so this sends the revision the broker loaded, not the one the helper runs; removed by #2548.
func helperDefinitionRef(loaded []identitybroker.DefinitionRef, kind session.SessionKind, name string) (identitybroker.DefinitionRef, bool) {
	builtin := identitybroker.BuiltinExplorer
	switch kind {
	case session.SessionKindParallelBranch:
		builtin = identitybroker.BuiltinBranch
	case session.SessionKindTeamMember:
		builtin = identitybroker.BuiltinMember
	}
	return loadedDefinitionRef(loaded, name, builtin)
}

// gatewayRouted is a broker session tool that can say whether its calls take
// the identity broker's gateway, presenting the acting agent's own
// credential, and give the tool a helper agent uses for it.
type gatewayRouted interface {
	HelperGatewayTool() (tool.Tool, bool)
}

// HelperGatewayTools returns the tools helper agents get for sessionTools:
// one for each broker tool whose calls take the identity broker's gateway,
// which presents the acting agent's own credential. Any other broker route
// acts as the session, with its shared grant, so a helper never gets one.
func HelperGatewayTools(sessionTools []tool.Tool) []tool.Tool {
	var out []tool.Tool
	for _, t := range sessionTools {
		if g, ok := t.(gatewayRouted); ok {
			if helperTool, ok := g.HelperGatewayTool(); ok {
				out = append(out, helperTool)
			}
		}
	}
	return out
}

// teamHelperTools returns the gateway tools the generic members of a team
// rooted in root get, limited to those root's bound authority allows, and
// their names. It needs the root's broker attachment resident in this
// process, as it is once the root has run here, and a root with a bound
// authority; otherwise members get none and their gateway calls fail closed.
// A root it cannot load fails the team's start, which can then be retried,
// rather than starting members without the tools they should have.
func (s *Service) teamHelperTools(ctx context.Context, root session.SessionID) ([]tool.Tool, []string, error) {
	if s.cfg.IdentityBroker == nil || s.cfg.MemberEngineWithHelperTools == nil || root == "" {
		return nil, nil, nil
	}
	s.mu.Lock()
	attachment := s.brokerAttachments[root]
	s.mu.Unlock()
	if attachment == nil {
		return nil, nil, nil
	}
	sess, err := s.cfg.Store.Load(ctx, root)
	if err != nil {
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "team root session could not be loaded for its gateway tools",
			"session", string(root), "err", err.Error(), "class", port.ClassifySessionLoadFailure(err).String())
		if errors.Is(err, port.ErrSessionNotFound) {
			return nil, nil, fmt.Errorf("%w: team root session %q", ErrNotFound, root)
		}
		return nil, nil, fmt.Errorf("%w: load team root session for its gateway tools: %v", ErrInternal, err)
	}
	authority, bound := sess.BoundAuthority()
	if !bound {
		return nil, nil, nil
	}
	var tools []tool.Tool
	var names []string
	for _, t := range HelperGatewayTools(attachment.Tools()) {
		name := t.Spec().Name
		if !authority.CapabilitySet.AllowsTool(name) {
			continue
		}
		tools = append(tools, t)
		names = append(names, name)
	}
	return tools, names, nil
}

// withHelperToolNames returns authority with names added to its tools, so a
// team member's authority allows the gateway tools its root allows.
func withHelperToolNames(authority session.Authority, names []string) session.Authority {
	if len(names) == 0 {
		return authority
	}
	tools := slices.Clone(authority.CapabilitySet.Tools)
	for _, name := range names {
		if !slices.Contains(tools, name) {
			tools = append(tools, name)
		}
	}
	slices.Sort(tools)
	authority.CapabilitySet.Tools = tools
	return authority
}
