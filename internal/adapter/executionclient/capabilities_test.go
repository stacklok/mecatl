package executionclient

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/executionenv"
)

type admittedRPC struct {
	droppedCommitRPC
	reserved, attached []string
}

func (r *admittedRPC) ReserveSuccessor(
	_ context.Context, req *executionv1.ReserveSuccessorRequest, _ ...grpc.CallOption,
) (*executionv1.ReferenceReservationResponse, error) {
	r.lastSuccessor = req
	return &executionv1.ReferenceReservationResponse{
		Environment:          req.GetEnvironment(),
		AdmittedCapabilities: r.reserved,
	}, nil
}

func (r *admittedRPC) AttachEnvironment(
	_ context.Context, req *executionv1.AttachEnvironmentRequest, _ ...grpc.CallOption,
) (*executionv1.AttachEnvironmentResponse, error) {
	return &executionv1.AttachEnvironmentResponse{
		Environment:          req.GetContext().GetEnvironment(),
		Epoch:                1,
		Ready:                true,
		GrantGeneration:      1,
		AdmittedCapabilities: r.attached,
	}, nil
}

var (
	capabilityRef = session.EnvironmentRef{
		Kind:     session.EnvironmentKind("kubernetes"),
		ID:       "env",
		Revision: "rev",
	}
	capabilityOwner = &session.Principal{Issuer: "issuer", Subject: "alice"}
	bothAdmitted    = []string{"filesystem", "foreground-command"}
	successorReq    = server.PlacementSuccessorRequest{
		Ref:                  capabilityRef,
		Principal:            capabilityOwner,
		SourceBindingID:      "source",
		DestinationBindingID: "successor",
	}
)

func TestSuccessorRefusesMissingOrChangedAdmittedSet(t *testing.T) {
	for _, tc := range []struct {
		name     string
		reserved []string
		changed  bool
	}{
		{name: "missing"},
		{name: "unknown", reserved: []string{"gpu"}},
		{name: "duplicate", reserved: []string{"filesystem", "filesystem"}},
		{name: "changed", reserved: []string{"filesystem"}, changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &admittedRPC{reserved: tc.reserved, attached: bothAdmitted}
			provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
			_, err := provider.ReserveSuccessor(t.Context(), successorReq)
			changed := errors.Is(err, server.ErrPlacementChanged)
			if err == nil || rpc.aborts != 1 || tc.changed != changed {
				t.Fatalf("err=%v aborts=%d", err, rpc.aborts)
			}
		})
	}
	inherited := []string{"foreground-command", "filesystem"}
	rpc := &admittedRPC{reserved: inherited, attached: bothAdmitted}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	binding, err := provider.ReserveSuccessor(t.Context(), successorReq)
	if err != nil || binding.Environment.CommandRunner() == nil {
		t.Fatalf("inherited set refused: %v", err)
	}
}

func TestAttachEnforcesAdmittedSet(t *testing.T) {
	reattach := func(admitted []string) (server.PlacementBinding, error) {
		rpc := &admittedRPC{attached: admitted}
		provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
		return provider.Reattach(t.Context(), server.PlacementReattachRequest{
			Ref:       capabilityRef,
			Principal: capabilityOwner,
			BindingID: "binding",
		})
	}
	if _, err := reattach(nil); err == nil {
		t.Fatal("attach without an admitted set was accepted")
	}
	commandOnly := []string{"foreground-command"}
	if _, err := reattach(commandOnly); !errors.Is(err, server.ErrPlacementUnavailable) {
		t.Fatalf("command-only set: %v", err)
	}
	filesOnly, err := reattach([]string{"filesystem"})
	env := filesOnly.Environment
	if err != nil || env.Workspace() == nil || env.CommandRunner() != nil {
		t.Fatalf("filesystem-only binding: %v", err)
	}
	full, err := reattach(bothAdmitted)
	if err != nil || full.Environment.CommandRunner() == nil {
		t.Fatalf("full binding: %v", err)
	}
}

func TestEnsureRefusesMissingAdmittedSet(t *testing.T) {
	missing := &executionv1.EnsureEnvironmentResponse{
		Environment: &executionv1.EnvironmentRef{Id: "env", Revision: "rev"},
		Ready:       true,
	}
	if _, err := ensureFromProto(missing); err == nil {
		t.Fatal("ensure without an admitted set was accepted")
	}
	got, err := ensureFromProto(&executionv1.EnsureEnvironmentResponse{
		AdmittedCapabilities: executionenv.TemplateCapabilities(),
	})
	if err != nil || len(got.AdmittedCapabilities) != 2 {
		t.Fatalf("ensure = %+v, %v", got, err)
	}
}

type ensuringRPC struct {
	admittedRPC
	requested []string
	err       error
}

func (r *ensuringRPC) EnsureTemplate(
	_ context.Context, req *executionv1.EnsureTemplateRequest, _ ...grpc.CallOption,
) (*executionv1.EnsureEnvironmentResponse, error) {
	r.requested = req.GetCapabilities()
	if r.err != nil {
		return nil, r.err
	}
	return &executionv1.EnsureEnvironmentResponse{
		Environment:          &executionv1.EnvironmentRef{Id: "env", Revision: "rev"},
		Epoch:                1,
		Ready:                true,
		GrantGeneration:      1,
		AdmittedCapabilities: r.attached,
	}, nil
}

func providerStatus(
	t *testing.T,
	grpcCode codes.Code,
	code executionenv.ErrorCode,
	message string,
) error {
	t.Helper()
	detail := &executionv1.ErrorDetail{Code: string(code)}
	st, err := status.New(grpcCode, message).WithDetails(detail)
	if err != nil {
		t.Fatal(err)
	}
	return st.Err()
}

func TestBindForwardsCapabilitiesAndKeepsAdmissionRefusals(t *testing.T) {
	const reason = `execution template does not declare capability "foreground-command"`
	denied := func(message string) error {
		return providerStatus(t, codes.PermissionDenied, executionenv.CodePermissionDenied, message)
	}
	invalidCode := executionenv.CodeInvalidArgument
	invalid := providerStatus(t, codes.InvalidArgument, invalidCode, "file-only")
	refused, notFound := server.ErrExecutionCapabilityRefused, server.ErrPlacementNotFound
	for _, tc := range []struct {
		name    string
		err     error
		want    error
		message string
	}{
		{name: "admitted"},
		{name: "refusal", err: denied(reason), want: refused, message: reason},
		{name: "file-only", err: invalid, want: refused, message: "file-only"},
		{name: "generic denial", err: denied(genericProviderMessage), want: notFound},
		{name: "unsafe text", err: denied("bad\x00text"), want: notFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &ensuringRPC{admittedRPC: admittedRPC{attached: bothAdmitted}, err: tc.err}
			provider := &Provider{client: &Client{rpc: rpc}, profile: "coding", revision: "rev"}
			binding, err := provider.Bind(t.Context(), server.PlacementBindRequest{
				Selector:     server.DefaultPlacement(),
				Principal:    capabilityOwner,
				BindingID:    "binding",
				Capabilities: bothAdmitted,
			})
			if !slices.Equal(rpc.requested, bothAdmitted) {
				t.Fatalf("requested = %v", rpc.requested)
			}
			if tc.want == nil {
				if err != nil || !slices.Equal(binding.AdmittedCapabilities, bothAdmitted) {
					t.Fatalf("binding = %+v, %v", binding.AdmittedCapabilities, err)
				}
				return
			}
			if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("err = %v, want %v containing %q", err, tc.want, tc.message)
			}
		})
	}
}

func TestSuccessorRefusesUnadmittedSourceCapabilities(t *testing.T) {
	rpc := &admittedRPC{reserved: []string{"filesystem"}, attached: bothAdmitted}
	provider := &Provider{client: &Client{rpc: rpc}, profile: "coding"}
	req := successorReq
	req.Capabilities = bothAdmitted
	_, err := provider.ReserveSuccessor(t.Context(), req)
	if !errors.Is(err, server.ErrExecutionCapabilityRefused) || rpc.aborts != 1 {
		t.Fatalf("err = %v, aborts = %d", err, rpc.aborts)
	}
}
