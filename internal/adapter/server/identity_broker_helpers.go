package server

import (
	"context"
	"errors"
	"fmt"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/agentcredential"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Helper agents (subagents, parallel branches, and team members) belong to
// their root session's tree and its broker session. Each is registered when
// the engine first publishes its session, before it runs, with the agent that
// created it as its parent: see RegisterHelper and package childstore.

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
		s.cfg.Diagnostics.Log(context.WithoutCancel(ctx), port.LevelWarn, "identity broker helper has no registered parent",
			"session", host, "parent_session", parentHost, "err", err.Error())
		if errors.Is(err, agentcredential.ErrNoCredential) {
			return fmt.Errorf("%w: the helper's parent agent is not registered", ErrInternal)
		}
		return ErrIdentityBrokerUnavailable
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
