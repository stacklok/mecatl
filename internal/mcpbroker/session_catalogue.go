package mcpbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

// Catalogue seals a broker authority snapshot; neither its slice nor tool specs can be mutated through accessors.
type Catalogue interface {
	Connection() ConnectionRef
	Ref() session.BrokerCatalogueRef
	Tools() []tool.Tool
	ToolNames() []string
	Valid() bool
	brokerCatalogue()
}

type brokerCatalogue struct {
	connection ConnectionRef
	ref        session.BrokerCatalogueRef
	tools      []tool.Tool
	names      []string
}

// NewCatalogue freezes executable descriptors, retaining read-only, serial,
// authorization, disclosure, and execution-metadata behavior. It does not grant authority.
func NewCatalogue(ref session.BrokerCatalogueRef, connection ConnectionRef, tools []tool.Tool) (Catalogue, error) {
	if !validBrokerRef(string(ref)) || (connection != "" && !validBrokerRef(string(connection))) || (len(tools) > 0 && connection == "") || len(tools) > 1024 {
		return nil, ErrInvalidWorkspaceCatalogue
	}
	frozen := make([]tool.Tool, 0, len(tools))
	names := make([]string, 0, len(tools))
	total := len(ref)
	for _, candidate := range tools {
		if nilTool(candidate) {
			return nil, ErrInvalidWorkspaceCatalogue
		}
		if _, planOnly := candidate.(tool.PlanOnly); planOnly {
			return nil, ErrInvalidWorkspaceCatalogue
		}
		spec := cloneToolSpec(candidate.Spec())
		schema := bytes.TrimSpace(spec.Schema)
		if slices.Contains(names, spec.Name) || !session.ValidWorkspaceEnrollmentToolNames([]string{spec.Name}) || !utf8.ValidString(spec.Description) || len(spec.Schema) > 256*1024 || len(schema) == 0 || schema[0] != '{' || !json.Valid(schema) {
			return nil, ErrInvalidWorkspaceCatalogue
		}
		total += len(spec.Name) + len(spec.Description) + len(spec.Schema)
		if total > 2*1024*1024 {
			return nil, ErrInvalidWorkspaceCatalogue
		}
		advertised := cloneToolSpec(spec)
		if d, ok := candidate.(tool.Disclosable); ok {
			advertised = cloneToolSpec(d.Advertised())
			if advertised.Name != spec.Name {
				return nil, ErrInvalidWorkspaceCatalogue
			}
		}
		names = append(names, spec.Name)
		base := catalogueTool{frozenTool: frozenTool{Tool: candidate, spec: spec}, advertised: advertised, readOnly: candidate.ReadOnly()}
		_, serial := candidate.(tool.DispatchSerial)
		if requester, ok := candidate.(tool.AuthorizationRequester); ok {
			authorized := catalogueAuthorizationTool{catalogueTool: base, requester: requester}
			if serial {
				frozen = append(frozen, &catalogueAuthorizationSerialTool{catalogueAuthorizationTool: authorized})
			} else {
				frozen = append(frozen, &authorized)
			}
		} else if serial {
			frozen = append(frozen, &catalogueSerialTool{catalogueTool: base})
		} else {
			frozen = append(frozen, &base)
		}
	}
	return &brokerCatalogue{ref: ref, connection: connection, tools: frozen, names: names}, nil
}

func (*brokerCatalogue) brokerCatalogue()                  {}
func (c *brokerCatalogue) Valid() bool                     { return c != nil }
func (c *brokerCatalogue) Connection() ConnectionRef       { return c.connection }
func (c *brokerCatalogue) Ref() session.BrokerCatalogueRef { return c.ref }
func (c *brokerCatalogue) Tools() []tool.Tool              { return append([]tool.Tool(nil), c.tools...) }
func (c *brokerCatalogue) ToolNames() []string             { return append([]string(nil), c.names...) }

// Keep the session snapshot's frozen projection separate from the donor workspace
// catalogue, whose disclosure and read-only behavior continues to delegate live.
type catalogueTool struct {
	frozenTool
	advertised tool.ToolSpec
	readOnly   bool
}

func (t *catalogueTool) BrokerInvocationDisposition(err error) session.BrokerAttemptDisposition {
	if b, ok := t.Tool.(interface {
		BrokerInvocationDisposition(error) session.BrokerAttemptDisposition
	}); ok {
		return b.BrokerInvocationDisposition(err)
	}
	return session.BrokerAttemptUnknown
}
func (t *catalogueTool) InspectBrokerAttempt(ctx context.Context, attempt session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	if b, ok := t.Tool.(tool.BrokerAttemptControl); ok {
		return b.InspectBrokerAttempt(ctx, attempt)
	}
	return tool.BrokerAttemptStatus{}, ErrStateUnavailable
}
func (t *catalogueTool) AcknowledgeBrokerAttempt(ctx context.Context, attempt session.BrokerAttempt) (tool.BrokerAttemptStatus, error) {
	if b, ok := t.Tool.(tool.BrokerAttemptControl); ok {
		return b.AcknowledgeBrokerAttempt(ctx, attempt)
	}
	return tool.BrokerAttemptStatus{}, ErrStateUnavailable
}

func (t *catalogueTool) ReadOnly() bool            { return t.readOnly }
func (t *catalogueTool) Advertised() tool.ToolSpec { return cloneToolSpec(t.advertised) }

type catalogueAuthorizationTool struct {
	catalogueTool
	requester tool.AuthorizationRequester
}

func (t *catalogueAuthorizationTool) RequestAuthorization(ctx context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return t.requester.RequestAuthorization(ctx, call)
}

func (t *catalogueAuthorizationTool) AbortAuthorization(ctx context.Context, authorization session.ExternalAuthorization) error {
	return t.requester.AbortAuthorization(ctx, authorization)
}

type catalogueSerialTool struct{ catalogueTool }

func (*catalogueSerialTool) DispatchSerialTool() {}

type catalogueAuthorizationSerialTool struct{ catalogueAuthorizationTool }

func (*catalogueAuthorizationSerialTool) DispatchSerialTool() {}
