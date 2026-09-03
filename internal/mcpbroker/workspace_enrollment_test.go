package mcpbroker_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

type enrollmentTool struct {
	spec       tool.ToolSpec
	laterSpec  *tool.ToolSpec
	specCalls  int
	executions int
}

func (t *enrollmentTool) Spec() tool.ToolSpec {
	t.specCalls++
	if t.specCalls > 1 && t.laterSpec != nil {
		return *t.laterSpec
	}
	return t.spec
}
func (*enrollmentTool) ReadOnly() bool { return true }
func (t *enrollmentTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	t.executions++
	return session.NewToolResult("call", "ok"), nil
}

type enrollmentAuthorizationTool struct{ enrollmentTool }

func (*enrollmentAuthorizationTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	return session.ExternalAuthorization{}, false, nil
}
func (*enrollmentAuthorizationTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

type enrollmentAttachmentFake struct{}

var _ mcpbroker.WorkspaceEnrollmentAttachment = enrollmentAttachmentFake{}

func (enrollmentAttachmentFake) BeginWorkspaceEnrollment(context.Context) (mcpbroker.WorkspaceEnrollmentPresentation, error) {
	return mcpbroker.WorkspaceEnrollmentPresentation{}, nil
}
func (enrollmentAttachmentFake) ObserveWorkspaceEnrollment(context.Context, mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return mcpbroker.WorkspaceEnrollmentResult{}, nil
}
func (enrollmentAttachmentFake) CancelWorkspaceEnrollment(context.Context, mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	return mcpbroker.WorkspaceEnrollmentResult{}, nil
}

func enrollmentRef() mcpbroker.WorkspaceEnrollmentRef {
	return mcpbroker.WorkspaceEnrollmentRef{
		ID:               "enrollment-1",
		RequiredServices: 2,
		ExpiresAt:        time.Unix(100, 0).UTC(),
	}
}

func newCatalogue(t *testing.T, ref mcpbroker.WorkspaceEnrollmentRef, tools ...tool.Tool) mcpbroker.WorkspaceCatalogue {
	t.Helper()
	catalogue, err := mcpbroker.NewWorkspaceCatalogue(ref, tools)
	if err != nil {
		t.Fatalf("NewWorkspaceCatalogue: %v", err)
	}
	return catalogue
}

func TestWorkspaceEnrollmentResultShapes(t *testing.T) {
	ref := enrollmentRef()
	catalogue := newCatalogue(t, ref, &enrollmentTool{spec: tool.ToolSpec{Name: "mcp__github__issues"}})
	otherRef := ref
	otherRef.ID = "other"
	otherCatalogue := newCatalogue(t, otherRef, &enrollmentTool{spec: tool.ToolSpec{Name: "other"}})
	otherServicesRef := ref
	otherServicesRef.RequiredServices++
	otherServicesCatalogue := newCatalogue(t, otherServicesRef, &enrollmentTool{spec: tool.ToolSpec{Name: "other-services"}})
	otherExpiryRef := ref
	otherExpiryRef.ExpiresAt = otherExpiryRef.ExpiresAt.Add(time.Second)
	otherExpiryCatalogue := newCatalogue(t, otherExpiryRef, &enrollmentTool{spec: tool.ToolSpec{Name: "other-expiry"}})
	tests := []struct {
		name   string
		result mcpbroker.WorkspaceEnrollmentResult
		valid  bool
	}{
		{"pending", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentPending}, true},
		{"connected with catalogue", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: catalogue}, true},
		{"connected without catalogue", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected}, false},
		{"pending with catalogue", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentPending, Catalogue: catalogue}, false},
		{"terminal with catalogue", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentDenied, Catalogue: catalogue}, false},
		{"unknown status", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: "forged"}, false},
		{"mismatched catalogue ID", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: otherCatalogue}, false},
		{"mismatched catalogue required services", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: otherServicesCatalogue}, false},
		{"mismatched catalogue expiry", mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: otherExpiryCatalogue}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.result.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v", got, tc.valid)
			}
		})
	}
}

func TestWorkspaceCatalogueIsImmutableFrozenSnapshot(t *testing.T) {
	ref := enrollmentRef()
	schema := json.RawMessage(`{"type":"object"}`)
	source := &enrollmentTool{
		spec:      tool.ToolSpec{Name: "provider/tool name", Description: "original", Schema: schema},
		laterSpec: &tool.ToolSpec{Name: "stateful-change", Description: "changed"},
	}
	input := []tool.Tool{source}
	catalogue := newCatalogue(t, ref, input...)
	result := mcpbroker.WorkspaceEnrollmentResult{
		Ref: ref, Status: mcpbroker.WorkspaceEnrollmentConnected, Catalogue: catalogue,
	}
	if source.specCalls != 1 {
		t.Fatalf("constructor called Spec %d times, want once", source.specCalls)
	}

	input[0] = &enrollmentTool{spec: tool.ToolSpec{Name: "replacement"}}
	source.spec.Name = "stateful-change"
	source.spec.Description = "changed"
	schema[2] = 'X'

	first := catalogue.Tools()
	first[0] = nil
	second := catalogue.Tools()
	gotSpec := second[0].Spec()
	if gotSpec.Name != "provider/tool name" || gotSpec.Description != "original" || string(gotSpec.Schema) != `{"type":"object"}` {
		t.Fatalf("frozen spec = %+v", gotSpec)
	}
	gotSpec.Schema[0] = 'X'
	if got := second[0].Spec(); string(got.Schema) != `{"type":"object"}` {
		t.Fatalf("Spec accessor aliases schema: %q", got.Schema)
	}
	if !result.Valid() {
		t.Fatal("source mutation invalidated connected result")
	}
	if source.specCalls != 1 {
		t.Fatalf("accessors or result validation recalled stateful Spec: %d calls", source.specCalls)
	}

	names := catalogue.ToolNames()
	names[0] = "mutated"
	if got := catalogue.ToolNames(); !slices.Equal(got, []string{"provider/tool name"}) {
		t.Fatalf("ToolNames = %q", got)
	}
	if got := second[0].Spec().Name; got != catalogue.ToolNames()[0] {
		t.Fatalf("executable name %q differs from authority name %q", got, catalogue.ToolNames()[0])
	}
	if _, err := second[0].Execute(context.Background(), session.ToolCall{}, tool.Environment{}); err != nil || source.executions != 1 {
		t.Fatalf("wrapped execution = %v, executions %d", err, source.executions)
	}
}

func TestWorkspaceCataloguePreservesAuthorizationCapability(t *testing.T) {
	source := &enrollmentAuthorizationTool{enrollmentTool: enrollmentTool{spec: tool.ToolSpec{Name: "protected"}}}
	catalogue := newCatalogue(t, enrollmentRef(), source)
	if _, ok := catalogue.Tools()[0].(tool.AuthorizationRequester); !ok {
		t.Fatal("frozen wrapper dropped AuthorizationRequester")
	}
}

func TestNewWorkspaceCatalogueRejectsInvalidTools(t *testing.T) {
	var typedNil *enrollmentTool
	for _, tc := range []struct {
		name  string
		tools []tool.Tool
	}{
		{"nil", []tool.Tool{nil}},
		{"typed nil", []tool.Tool{typedNil}},
		{"empty name", []tool.Tool{&enrollmentTool{}}},
		{"control name", []tool.Tool{&enrollmentTool{spec: tool.ToolSpec{Name: "bad\nname"}}}},
		{"invalid UTF-8", []tool.Tool{&enrollmentTool{spec: tool.ToolSpec{Name: string([]byte{0xff})}}}},
		{"duplicate", []tool.Tool{&enrollmentTool{spec: tool.ToolSpec{Name: "same"}}, &enrollmentTool{spec: tool.ToolSpec{Name: "same"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogue, err := mcpbroker.NewWorkspaceCatalogue(enrollmentRef(), tc.tools)
			if !errors.Is(err, mcpbroker.ErrInvalidWorkspaceCatalogue) || catalogue != nil {
				t.Fatalf("NewWorkspaceCatalogue = (%v, %v), want nil invalid-catalogue error", catalogue, err)
			}
		})
	}
}

func TestNewWorkspaceCatalogueShortCircuitsEnrollmentBounds(t *testing.T) {
	for _, tc := range []struct {
		name          string
		tools         []tool.Tool
		specCallsWant int
	}{
		{"tool count", numberedCatalogueTools(258, 4), 257},
		{"aggregate name bytes", numberedCatalogueTools(130, 256), 129},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogue, err := mcpbroker.NewWorkspaceCatalogue(enrollmentRef(), tc.tools)
			if !errors.Is(err, mcpbroker.ErrInvalidWorkspaceCatalogue) || catalogue != nil {
				t.Fatalf("NewWorkspaceCatalogue = (%v, %v), want nil invalid-catalogue error", catalogue, err)
			}
			for i, candidate := range tc.tools {
				calls := candidate.(*enrollmentTool).specCalls
				if i < tc.specCallsWant && calls != 1 {
					t.Fatalf("tool %d Spec calls = %d, want 1 before rejection", i, calls)
				}
				if i >= tc.specCallsWant && calls != 0 {
					t.Fatalf("tool %d Spec calls = %d, want 0 after rejection", i, calls)
				}
			}
		})
	}
}

func numberedCatalogueTools(count, width int) []tool.Tool {
	tools := make([]tool.Tool, count)
	for i := range tools {
		suffix := strconv.Itoa(i)
		tools[i] = &enrollmentTool{spec: tool.ToolSpec{Name: strings.Repeat("x", width-len(suffix)) + suffix}}
	}
	return tools
}

func TestWorkspaceEnrollmentPresentationURLValidationAndIsolation(t *testing.T) {
	valid := mcpbroker.WorkspaceEnrollmentPresentation{
		Ref: enrollmentRef(),
		URL: "https://authorize.example/enrollment-1",
	}
	if !valid.Valid() {
		t.Fatal("valid presentation rejected")
	}
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"relative", "/authorize/enrollment-1"},
		{"hostless", "https:/authorize/enrollment-1"},
		{"javascript", "javascript:alert(1)"},
		{"file", "file:///authorize/enrollment-1"},
		{"other scheme", "ftp://authorize.example/enrollment-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			presentation := valid
			presentation.URL = tc.url
			if presentation.Valid() {
				t.Fatalf("Valid accepted %q", tc.url)
			}
		})
	}

	for _, typ := range []reflect.Type{
		reflect.TypeOf(mcpbroker.WorkspaceEnrollmentRef{}),
		reflect.TypeOf((*mcpbroker.WorkspaceCatalogue)(nil)).Elem(),
		reflect.TypeOf(mcpbroker.WorkspaceEnrollmentResult{}),
	} {
		if typ.Kind() == reflect.Struct {
			for i := 0; i < typ.NumField(); i++ {
				if strings.Contains(strings.ToLower(typ.Field(i).Name), "url") {
					t.Fatalf("durable %s field %q carries URL-shaped data", typ.Name(), typ.Field(i).Name)
				}
			}
		}
		for i := 0; i < typ.NumMethod(); i++ {
			if strings.Contains(strings.ToLower(typ.Method(i).Name), "url") {
				t.Fatalf("durable %s accessor %q exposes URL-shaped data", typ.Name(), typ.Method(i).Name)
			}
		}
	}
}

func TestWorkspaceEnrollmentInterfaceHasNoClientAuthoredResolution(t *testing.T) {
	typ := reflect.TypeOf((*mcpbroker.WorkspaceEnrollmentAttachment)(nil)).Elem()
	contextType := reflect.TypeOf((*context.Context)(nil)).Elem()
	refType := reflect.TypeOf(mcpbroker.WorkspaceEnrollmentRef{})
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		wantInputs := []reflect.Type{contextType}
		if method.Name != "BeginWorkspaceEnrollment" {
			wantInputs = append(wantInputs, refType)
		}
		if method.Type.NumIn() != len(wantInputs) {
			t.Fatalf("%s accepts %d inputs, want only context and optional ref", method.Name, method.Type.NumIn())
		}
		for j, want := range wantInputs {
			if got := method.Type.In(j); got != want {
				t.Fatalf("%s input %d = %s, want %s", method.Name, j, got, want)
			}
		}
	}
}
