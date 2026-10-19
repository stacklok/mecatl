package executioncontroller

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
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

func TestEnsureTemplateStatusConflictExhaustionPreservesWireCodeAndSafeDiagnostic(t *testing.T) {
	const sentinel = "PRIVATE-RESOURCE-NAME-TOKEN"
	profiles := testProfiles()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	attempts := 0
	dyn.PrependReactor("update", ExecutionEnvironmentGVR.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetSubresource() != "status" {
			return false, nil, nil
		}
		attempts++
		return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: ExecutionEnvironmentGVR.Resource}, sentinel, errors.New(sentinel))
	})
	diagnostics := &handlerDiagnosticRecorder{}
	const clientID = "spiffe://cluster/ns/allowed"
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{clientID: {MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}, Diagnostics: diagnostics}, NewStore(dyn, "ns", profiles, nil))
	_, err := h.EnsureTemplate(authenticatedContext(clientID), &executionv1.EnsureTemplateRequest{
		BindingId: "binding", Template: &executionv1.TemplateSelector{Id: "go", Revision: profiles.defaultRevision["go"]},
		Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, OperationId: "op",
	})
	st := status.Convert(err)
	if attempts < 2 || st.Code() != codes.Aborted || st.Message() != "execution provider request failed" {
		t.Fatalf("status retries=%d, code=%v, message=%q", attempts, st.Code(), st.Message())
	}
	if details := st.Details(); len(details) != 1 || details[0].(*executionv1.ErrorDetail).Code != string(executionenv.CodeConflict) || details[0].(*executionv1.ErrorDetail).Retryable {
		t.Fatalf("status conflict detail=%v", details)
	}
	if len(diagnostics.records) != 1 || fmt.Sprint(diagnostics.records[0].attrs) != "[operation ensure_template reason conflict]" || strings.Contains(fmt.Sprint(diagnostics.records), sentinel) {
		t.Fatalf("unsafe or missing ensure diagnostic: %+v", diagnostics.records)
	}
}

func TestEnsureTemplateUnknownBackendErrorDiagnosticIsBounded(t *testing.T) {
	const sentinel = "PRIVATE-BACKEND-ERROR"
	profiles := testProfiles()
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	dyn.PrependReactor("create", ExecutionEnvironmentGVR.Resource, func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(sentinel)
	})
	diagnostics := &handlerDiagnosticRecorder{}
	const clientID = "spiffe://cluster/ns/allowed"
	h := NewHandler(HandlerConfig{Clients: map[string]ClientPolicy{clientID: {MayAttestOwner: true, ExecutionTemplates: []string{"go"}}}, Diagnostics: diagnostics}, NewStore(dyn, "ns", profiles, nil))
	_, err := h.EnsureTemplate(authenticatedContext(clientID), &executionv1.EnsureTemplateRequest{
		BindingId: "binding", Template: &executionv1.TemplateSelector{Id: "go", Revision: profiles.defaultRevision["go"]},
		Owner: &executionv1.Owner{Issuer: "issuer", Subject: "alice"}, OperationId: "op",
	})
	if status.Code(err) != codes.Internal || len(diagnostics.records) != 1 || fmt.Sprint(diagnostics.records[0].attrs) != "[operation ensure_template reason other]" || strings.Contains(fmt.Sprint(diagnostics.records), sentinel) {
		t.Fatalf("unsafe or missing backend failure: code=%v, records=%+v", status.Code(err), diagnostics.records)
	}
}
