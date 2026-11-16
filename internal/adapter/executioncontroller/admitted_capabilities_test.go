package executioncontroller

import (
	"slices"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
)

const admissionClient = "spiffe://cluster/ns/allowed"

func TestAdmitCapabilities(t *testing.T) {
	fs, cmd := CapabilityFilesystem, CapabilityForegroundCommand
	invalid, denied := executionenv.CodeInvalidArgument, executionenv.CodePermissionDenied
	cases := []struct {
		name                string
		requested, declared []string
		want                []string
		code                executionenv.ErrorCode
		message             string
	}{
		{name: "empty admits declared", want: bothCapabilities},
		{name: "explicit both", requested: []string{cmd, fs}, want: bothCapabilities},
		{name: "file-only", requested: []string{fs}, code: invalid, message: "file-only"},
		{name: "command-only", requested: []string{cmd}, code: invalid, message: "file-only"},
		{name: "outside template", requested: []string{fs, cmd}, declared: []string{fs},
			code: denied, message: `does not declare capability "foreground-command"`},
		{name: "file-only template", declared: []string{fs}, code: invalid, message: "file-only"},
		{name: "unknown", requested: []string{fs, "shell"}, code: invalid},
		{name: "duplicate", requested: []string{fs, fs}, code: invalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := admitCapabilities(tc.requested, tc.declared)
			if tc.code == "" {
				if err != nil || !slices.Equal(got, tc.want) {
					t.Fatalf("admit = %v, %v; want %v", got, err, tc.want)
				}
				return
			}
			if !codeIs(err, tc.code) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("admit = %v, %v; want %s containing %q", got, err, tc.code, tc.message)
			}
		})
	}
}

type admissionFixture struct {
	handler   *Handler
	store     *Store
	resources dynamic.ResourceInterface
	request   *executionv1.EnsureTemplateRequest
}

func newAdmissionFixture(t *testing.T, profiles *Profiles) admissionFixture {
	t.Helper()
	kinds := map[schema.GroupVersionResource]string{
		ExecutionEnvironmentGVR: "ExecutionEnvironmentList",
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), kinds)
	store := NewStore(client, "ns", profiles, nil)
	policy := ClientPolicy{MayAttestOwner: true, ExecutionTemplates: []string{"go"}}
	clients := map[string]ClientPolicy{admissionClient: policy}
	return admissionFixture{
		handler:   NewHandler(HandlerConfig{Clients: clients}, store),
		store:     store,
		resources: client.Resource(ExecutionEnvironmentGVR).Namespace("ns"),
		request: &executionv1.EnsureTemplateRequest{
			BindingId:   "binding",
			Owner:       &executionv1.Owner{Issuer: "issuer", Subject: "alice"},
			OperationId: "op",
			Template: &executionv1.TemplateSelector{
				Id: "go", Revision: profiles.defaultRevision["go"],
			},
		},
	}
}

func (f admissionFixture) ensure(
	capabilities ...string,
) (*executionv1.EnsureEnvironmentResponse, error) {
	f.request.Capabilities = capabilities
	return f.handler.EnsureTemplate(authenticatedContext(admissionClient), f.request)
}

func (f admissionFixture) setRecorded(t *testing.T, name string, recorded []string) {
	t.Helper()
	o, err := f.resources.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unstructured.RemoveNestedField(o.Object, "spec", admittedCapabilitiesField)
	if recorded != nil {
		err := unstructured.SetNestedStringSlice(
			o.Object,
			recorded,
			"spec",
			admittedCapabilitiesField,
		)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.resources.Update(t.Context(), o, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
}

func wantRefusal(t *testing.T, err error, code codes.Code, message string) {
	t.Helper()
	if st := status.Convert(err); st.Code() != code || !strings.Contains(st.Message(), message) {
		t.Fatalf("refusal = %v; want %v containing %q", err, code, message)
	}
}

func TestEnsureTemplateAdmitsAndRecordsCapabilities(t *testing.T) {
	f := newAdmissionFixture(t, testProfiles())
	_, err := f.ensure(CapabilityFilesystem)
	wantRefusal(t, err, codes.InvalidArgument, "file-only sessions are not supported yet")
	if list, _ := f.resources.List(t.Context(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatalf("refused request created %d environments", len(list.Items))
	}

	ensured, err := f.ensure()
	if err != nil || !slices.Equal(ensured.GetAdmittedCapabilities(), bothCapabilities) {
		t.Fatalf("ensure = %v, %v", ensured, err)
	}
	name := ensured.GetEnvironment().GetId()
	env, err := f.resources.Get(t.Context(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recorded, _, _ := unstructured.NestedStringSlice(env.Object, "spec", admittedCapabilitiesField)
	if !slices.Equal(recorded, bothCapabilities) {
		t.Fatalf("recorded = %v", recorded)
	}
	again, err := f.ensure(CapabilityForegroundCommand, CapabilityFilesystem)
	if err != nil || again.GetEnvironment().GetId() != name {
		t.Fatalf("retry with the same set = %v, %v", again, err)
	}
	_, err = f.ensure(CapabilityFilesystem)
	wantRefusal(t, err, codes.InvalidArgument, "file-only")

	f.setRecorded(t, name, []string{CapabilityFilesystem})
	_, err = f.ensure()
	wantRefusal(t, err, codes.Aborted, "different execution capabilities")
}

func TestEnsureTemplateRefusesCapabilityOutsideTemplate(t *testing.T) {
	profiles := testProfiles()
	revision := profiles.defaultRevision["go"]
	fileOnly := profiles.revisions["go"][revision]
	fileOnly.Spec.Capabilities = []string{CapabilityFilesystem}
	profiles.revisions["go"][revision] = fileOnly
	f := newAdmissionFixture(t, profiles)
	_, err := f.ensure(CapabilityFilesystem, CapabilityForegroundCommand)
	wantRefusal(t, err, codes.PermissionDenied, `does not declare capability "foreground-command"`)
}

func TestAdmittedCapabilitiesReturnedOnReattach(t *testing.T) {
	f := newAdmissionFixture(t, testProfiles())
	ensured, err := f.ensure()
	if err != nil {
		t.Fatal(err)
	}
	ref := refFromProto(ensured.GetEnvironment())
	owner := ownerHash(executionenv.Owner{Issuer: "issuer", Subject: "alice"})
	err = f.store.CommitReference(t.Context(), ref, admissionClient, owner, "binding", "op")
	if err != nil {
		t.Fatal(err)
	}
	ready := func(o *unstructured.Unstructured) error {
		setConditionObject(o, "Ready", true, "Ready", "fixture ready")
		return nil
	}
	if err := f.store.retryUpdateStatus(t.Context(), ref.ID, ready); err != nil {
		t.Fatal(err)
	}
	f.setRecorded(t, ref.ID, []string{CapabilityFilesystem})
	attached, err := f.store.Attach(t.Context(), ref, admissionClient, owner, "binding")
	got := attachResponse(attached).GetAdmittedCapabilities()
	if err != nil || !slices.Equal(got, []string{CapabilityFilesystem}) {
		t.Fatalf("attach admitted = %v, %v", got, err)
	}
}

func TestPreAdmissionEnvironmentReportsTemplateCapabilities(t *testing.T) {
	env := lifecycleEnvironment(time.Now())
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(client, "ns", testProfiles(), nil)
	ref := executionenv.EnvironmentRef{ID: "env", Revision: "rev"}
	attached, err := store.Attach(t.Context(), ref, "client", "owner", "source")
	if err != nil || !slices.Equal(attached.AdmittedCapabilities, bothCapabilities) {
		t.Fatalf("attach admitted = %v, %v", attached.AdmittedCapabilities, err)
	}
	reserved, err := store.ReserveSuccessor(
		t.Context(),
		ref,
		"client",
		"owner",
		"source",
		"next",
		"fork",
	)
	if err != nil || !slices.Equal(reserved, bothCapabilities) {
		t.Fatalf("successor admitted = %v, %v", reserved, err)
	}

	f := newAdmissionFixture(t, testProfiles())
	ensured, err := f.ensure()
	if err != nil {
		t.Fatal(err)
	}
	f.setRecorded(t, ensured.GetEnvironment().GetId(), nil)
	again, err := f.ensure()
	if err != nil || !slices.Equal(again.GetAdmittedCapabilities(), bothCapabilities) {
		t.Fatalf("retry of a pre-admission environment = %v, %v", again, err)
	}
}
