package mcpbroker_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

func brokerRef() session.BrokerCatalogueRef {
	return session.BrokerCatalogueRef(base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
}

type sessionCatalogueTool struct {
	spec, advertised tool.ToolSpec
	writable         bool
	specCalls        int
	call             session.ToolCall
	authorization    session.ExternalAuthorization
}

func (t *sessionCatalogueTool) Spec() tool.ToolSpec       { t.specCalls++; return t.spec }
func (t *sessionCatalogueTool) ReadOnly() bool            { return !t.writable }
func (t *sessionCatalogueTool) Advertised() tool.ToolSpec { return t.advertised }
func (t *sessionCatalogueTool) Execute(_ context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.call = call
	return session.NewToolResult(call.ID, "delegated"), nil
}
func (t *sessionCatalogueTool) ExecutionMetadata(call session.ToolCall) (mcpbroker.ExecutionMetadata, bool) {
	t.call = call
	return mcpbroker.ExecutionMetadata{Backend: "fixture", OutboundCredentialKind: mcpbroker.OutboundCredentialNone}, true
}

type sessionCatalogueAuthorizationTool struct{ *sessionCatalogueTool }

func (t *sessionCatalogueAuthorizationTool) RequestAuthorization(_ context.Context, call session.ToolCall) (session.ExternalAuthorization, bool, error) {
	t.call = call
	return t.authorization, true, nil
}
func (t *sessionCatalogueAuthorizationTool) AbortAuthorization(_ context.Context, authorization session.ExternalAuthorization) error {
	t.authorization = authorization
	return nil
}

type sessionCatalogueSerialTool struct{ *sessionCatalogueTool }

func (*sessionCatalogueSerialTool) DispatchSerialTool() {}

type sessionCatalogueAuthorizationSerialTool struct {
	*sessionCatalogueAuthorizationTool
}

func (*sessionCatalogueAuthorizationSerialTool) DispatchSerialTool() {}

func TestBrokerCatalogueFreezesDescriptorsAndPreservesDelegation(t *testing.T) {
	for _, auth := range []bool{false, true} {
		for _, serial := range []bool{false, true} {
			t.Run(fmt.Sprintf("authorization=%v/serial=%v", auth, serial), func(t *testing.T) {
				source := &sessionCatalogueTool{
					spec:          tool.ToolSpec{Name: "search", Description: "full", Schema: []byte(`{"type":"object"}`)},
					advertised:    tool.ToolSpec{Name: "search", Description: "preview", Schema: []byte(`{}`)},
					authorization: session.ExternalAuthorization{ID: "authorization"},
				}
				var candidate tool.Tool = source
				if auth {
					a := &sessionCatalogueAuthorizationTool{source}
					candidate = a
					if serial {
						candidate = &sessionCatalogueAuthorizationSerialTool{a}
					}
				} else if serial {
					candidate = &sessionCatalogueSerialTool{source}
				}
				input := []tool.Tool{candidate}
				catalogue, err := mcpbroker.NewCatalogue(brokerRef(), input)
				if err != nil {
					t.Fatal(err)
				}
				if !catalogue.Valid() || catalogue.Ref() != brokerRef() {
					t.Fatal("invalid catalogue identity")
				}
				input[0] = nil
				source.spec.Name, source.spec.Description = "changed", "changed"
				source.spec.Schema[0] = 'x'
				source.advertised.Name, source.advertised.Description = "other", "changed"
				source.advertised.Schema[0] = 'x'
				source.writable = true
				tools := catalogue.Tools()
				tools[0] = nil
				names := catalogue.ToolNames()
				names[0] = "changed"
				wrapped := catalogue.Tools()[0]
				spec := wrapped.Spec()
				if spec.Name != "search" || spec.Description != "full" || string(spec.Schema) != `{"type":"object"}` || !wrapped.ReadOnly() || catalogue.ToolNames()[0] != "search" {
					t.Fatalf("snapshot changed: %+v", spec)
				}
				spec.Schema[0] = 'y'
				if wrapped.Spec().Schema[0] != '{' || source.specCalls != 1 {
					t.Fatal("spec was not frozen once")
				}
				projection := wrapped.(tool.Disclosable).Advertised()
				if projection.Name != "search" || projection.Description != "preview" || string(projection.Schema) != `{}` {
					t.Fatalf("projection changed: %+v", projection)
				}
				projection.Schema[0] = 'y'
				if wrapped.(tool.Disclosable).Advertised().Schema[0] != '{' {
					t.Fatal("projection aliases returned schema")
				}
				if _, got := wrapped.(tool.DispatchSerial); got != serial {
					t.Fatalf("serial=%v want %v", got, serial)
				}
				requester, got := wrapped.(tool.AuthorizationRequester)
				if got != auth {
					t.Fatalf("authorization=%v want %v", got, auth)
				}
				call := session.NewToolCall("call", "search", []byte(" {\"query\":\"test\"} \n"))
				result, err := wrapped.Execute(t.Context(), call, tool.Environment{})
				if err != nil || result.CallID != call.ID || result.Content != "delegated" || !reflect.DeepEqual(source.call, call) {
					t.Fatalf("execute delegation: %+v %v", result, err)
				}
				metadata, ok := wrapped.(mcpbroker.ExecutionMetadataProvider).ExecutionMetadata(call)
				if !ok || metadata.Backend != "fixture" || metadata.OutboundCredentialKind != mcpbroker.OutboundCredentialNone || !reflect.DeepEqual(source.call, call) {
					t.Fatalf("metadata delegation: %+v", metadata)
				}
				if auth {
					a, required, err := requester.RequestAuthorization(t.Context(), call)
					if err != nil || !required || a.ID != "authorization" || !reflect.DeepEqual(source.call, call) {
						t.Fatalf("authorization delegation: %+v %v", a, err)
					}
					a.ID = "cancelled"
					if err := requester.AbortAuthorization(t.Context(), a); err != nil || source.authorization.ID != "cancelled" {
						t.Fatalf("abort delegation: %v", err)
					}
				}
			})
		}
	}
}

func TestBrokerCataloguePreservesWritableMarker(t *testing.T) {
	spec := tool.ToolSpec{Name: "mutate", Schema: []byte(`{}`)}
	source := &sessionCatalogueTool{spec: spec, advertised: spec, writable: true}
	catalogue, err := mcpbroker.NewCatalogue(brokerRef(), []tool.Tool{source})
	if err != nil {
		t.Fatal(err)
	}
	source.writable = false
	if catalogue.Tools()[0].ReadOnly() {
		t.Fatal("writable tool became read-only after source mutation")
	}
}

func TestBrokerCatalogueRejectsMalformedDescriptors(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec tool.ToolSpec
	}{
		{"empty name", tool.ToolSpec{Schema: []byte(`{}`)}},
		{"name bound", tool.ToolSpec{Name: strings.Repeat("x", 257), Schema: []byte(`{}`)}},
		{"invalid name", tool.ToolSpec{Name: string([]byte{0xff}), Schema: []byte(`{}`)}},
		{"empty schema", tool.ToolSpec{Name: "search"}},
		{"schema not object", tool.ToolSpec{Name: "search", Schema: []byte(`[]`)}},
		{"malformed schema", tool.ToolSpec{Name: "search", Schema: []byte(`{"bad":`)}},
		{"invalid description", tool.ToolSpec{Name: "search", Description: string([]byte{0xff}), Schema: []byte(`{}`)}},
		{"schema whitespace bound", tool.ToolSpec{Name: "search", Schema: append([]byte(`{}`), bytes.Repeat([]byte(" "), 256*1024)...)}},
		{"schema bound", tool.ToolSpec{Name: "search", Schema: []byte(`{"x":"` + strings.Repeat("a", 256*1024) + `"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := mcpbroker.NewCatalogue(brokerRef(), []tool.Tool{&enrollmentTool{spec: tc.spec}}); err == nil {
				t.Fatal("accepted malformed descriptor")
			}
		})
	}
}

func TestBrokerCatalogueRejectsInvalidSourcesAndBounds(t *testing.T) {
	source := &enrollmentTool{spec: tool.ToolSpec{Name: "search", Schema: []byte(`{}`)}}
	var nilTool *enrollmentTool
	for name, tools := range map[string][]tool.Tool{
		"nil": {nil}, "typed nil": {nilTool}, "duplicate": {source, source},
		"mismatched projection": {&sessionCatalogueTool{spec: source.spec, advertised: tool.ToolSpec{Name: "different"}}},
		"plan only":             {&enrollmentPlanOnlyTool{enrollmentTool: *source}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := mcpbroker.NewCatalogue(brokerRef(), tools); err == nil {
				t.Fatal("accepted invalid tools")
			}
		})
	}
	for _, ref := range []session.BrokerCatalogueRef{"", "invalid", session.BrokerCatalogueRef(strings.Repeat("A", 42) + "B"), session.BrokerCatalogueRef(strings.Repeat("A", 44))} {
		if _, err := mcpbroker.NewCatalogue(ref, nil); err == nil {
			t.Fatalf("accepted invalid reference %q", ref)
		}
	}
	tools := make([]tool.Tool, 1024)
	for i := range tools {
		tools[i] = &enrollmentTool{spec: tool.ToolSpec{Name: fmt.Sprintf("tool%d", i), Schema: []byte(`{}`)}}
	}
	if _, err := mcpbroker.NewCatalogue(brokerRef(), tools); err != nil {
		t.Fatalf("at tool limit: %v", err)
	}
	if _, err := mcpbroker.NewCatalogue(brokerRef(), append(tools, source)); err == nil {
		t.Fatal("accepted too many tools")
	}
	// Ref, name, schema and description jointly consume the catalogue byte limit.
	atLimit := &enrollmentTool{spec: tool.ToolSpec{Name: "search", Schema: []byte(`{}`), Description: strings.Repeat("x", 2*1024*1024-len(brokerRef())-len("search")-2)}}
	if _, err := mcpbroker.NewCatalogue(brokerRef(), []tool.Tool{atLimit}); err != nil {
		t.Fatalf("at byte limit: %v", err)
	}
	atLimit.spec.Description += "x"
	if _, err := mcpbroker.NewCatalogue(brokerRef(), []tool.Tool{atLimit}); err == nil {
		t.Fatal("accepted oversized catalogue")
	}
	catalogue, err := mcpbroker.NewCatalogue(brokerRef(), []tool.Tool{source})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalogue.Tools()[0].(mcpbroker.ExecutionMetadataProvider).ExecutionMetadata(session.ToolCall{}); ok {
		t.Fatal("invented execution metadata")
	}
	if got := catalogue.Tools()[0].(tool.Disclosable).Advertised(); got.Name != "search" || string(got.Schema) != `{}` {
		t.Fatalf("default projection: %+v", got)
	}
}
