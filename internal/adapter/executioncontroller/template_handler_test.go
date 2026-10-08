package executioncontroller

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
)

func TestAuthenticatedTemplateCatalogAndBinding(t *testing.T) {
	spec := testProfiles().byName["go"].Spec
	revision := fixtureRevision(t, spec)
	registry, _ := templateFixture(t, map[string]TemplateRevision{revision: {Execution: spec, Display: TemplateDisplay{Name: "Docs"}}}, revision)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	store := NewStore(dyn, "ns", registry, nil)
	allowed := "spiffe://cluster/ns/allowed"
	denied := "spiffe://cluster/ns/denied"
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{allowed: {MayAttestOwner: true, ExecutionTemplates: []string{"go"}}, denied: {MayAttestOwner: true}}}, store)
	list, err := h.ListExecutionTemplates(authenticatedContext(allowed), &executionv1.ListExecutionTemplatesRequest{})
	if err != nil || len(list.Items) != 1 || list.Items[0].Template.Revision != revision || list.InventoryRevision == "" {
		t.Fatalf("authorized inventory: %+v, %v", list, err)
	}
	hidden, err := h.ListExecutionTemplates(authenticatedContext(denied), &executionv1.ListExecutionTemplatesRequest{})
	if err != nil || len(hidden.Items) != 0 || hidden.InventoryRevision == list.InventoryRevision {
		t.Fatalf("hidden inventory: %+v, %v", hidden, err)
	}
	selector := &executionv1.TemplateSelector{Id: "go", Revision: revision}
	unknown := &executionv1.TemplateSelector{Id: "unknown", Revision: revision}
	for _, sel := range []*executionv1.TemplateSelector{selector, unknown} {
		if _, err := h.ValidateTemplate(authenticatedContext(denied), &executionv1.ValidateTemplateRequest{Template: sel}); status.Code(err) != codes.NotFound {
			t.Fatalf("hidden selector %q code=%v", sel.Id, status.Code(err))
		}
	}
	q := &executionv1.EnsureTemplateRequest{BindingId: "binding", Template: selector, Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, OperationId: "op"}
	_, deniedErr := h.EnsureTemplate(authenticatedContext(denied), q)
	if status.Code(deniedErr) != codes.NotFound {
		t.Fatalf("unauthorized bind code=%v", status.Code(deniedErr))
	}
	if _, err := h.EnsureTemplate(authenticatedContext(allowed), q); err != nil {
		t.Fatal(err)
	}
	q.Template = unknown
	if _, err := h.EnsureTemplate(authenticatedContext(allowed), q); status.Code(err) != codes.NotFound || status.Convert(err).Message() != status.Convert(deniedErr).Message() {
		t.Fatalf("unknown and unauthorized IDs have different errors: %v / %v", err, deniedErr)
	}
	if _, err := h.EnsureTemplate(authenticatedContext(allowed), &executionv1.EnsureTemplateRequest{Template: &executionv1.TemplateSelector{Id: "go"}, BindingId: "other", Owner: q.Owner, OperationId: "op"}); status.Code(err) != codes.NotFound {
		t.Fatalf("revisionless selection bypassed policy: %v", err)
	}
}
