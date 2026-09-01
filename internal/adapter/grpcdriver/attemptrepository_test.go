package grpcdriver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	driverv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/driver/v1"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

type attemptRepositoryStub struct {
	learning.AttemptRepository
	create func(context.Context, learning.AttemptPartition, learning.AttemptCreate) (learning.AttemptRecord, error)
}

func (s attemptRepositoryStub) Create(ctx context.Context, partition learning.AttemptPartition, create learning.AttemptCreate) (learning.AttemptRecord, error) {
	return s.create(ctx, partition, create)
}

func TestAttemptRepositoryDriverSmoke(t *testing.T) {
	t.Parallel()

	partition, err := learning.DeriveAttemptPartition("caller")
	if err != nil {
		t.Fatal(err)
	}
	digest := learning.CanonicalDigest("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	source := learning.AttemptSource{SessionID: session.SessionID("session-1"), RunID: "run-1", CanonicalDigest: digest}
	prompt := learning.CurrentPromptBinding{Ordinal: 3, Digest: digest, Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, prompt)
	if err != nil {
		t.Fatal(err)
	}
	create := learning.AttemptCreate{ID: "attempt-0123456789abcdef", Provenance: provenance}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	want := learning.AttemptRecord{
		ID: create.ID, Version: "opaque-v1", State: learning.AttemptQueued,
		Provenance: provenance, AttemptGeneration: 1, CreatedAt: now, UpdatedAt: now,
	}

	backend := attemptRepositoryStub{create: func(_ context.Context, gotPartition learning.AttemptPartition, gotCreate learning.AttemptCreate) (learning.AttemptRecord, error) {
		if gotPartition != partition || gotCreate != create {
			t.Fatalf("Create inputs = (%q, %+v), want (%q, %+v)", gotPartition, gotCreate, partition, create)
		}
		return want, nil
	}}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterAttemptRepositoryServiceServer(gs, NewAttemptRepositoryServer(backend))
	})
	got, err := NewAttemptRepository(conn).Create(context.Background(), partition, create)
	if err != nil {
		t.Fatalf("Create over wire: %v", err)
	}
	if got != want {
		t.Fatalf("Create over wire = %+v, want %+v", got, want)
	}
}

func TestAttemptWatchIsDeferred(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locating attempt repository protocol")
	}
	protocolPath := filepath.Join(filepath.Dir(file), "..", "..", "..", "contracts", "proto", "mecatl", "driver", "v1", "attempt_repository.proto")
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(protocol), "ADR-0250 watches session events only") {
		t.Fatal("attempt repository protocol does not document that ADR-0250 watches session events only")
	}

	wantRPCs := []string{
		"CreateAttempt", "GetAttempt", "ListAttempts", "AcquireAttemptClaim", "RenewAttemptClaim", "CheckpointAttempt",
		"ReleaseAttemptClaim", "FinalizeAttempt", "RetryAttempt", "AbandonAttempt", "DeleteAttempt", "DeleteTerminalAttemptsBefore",
	}
	var gotRPCs []string
	for _, line := range strings.Split(string(protocol), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "rpc ") {
			gotRPCs = append(gotRPCs, strings.Fields(line)[1][:strings.IndexByte(strings.Fields(line)[1], '(')])
		}
	}
	if !reflect.DeepEqual(gotRPCs, wantRPCs) {
		t.Fatalf("attempt repository RPCs = %v, want lifecycle operations only %v", gotRPCs, wantRPCs)
	}

	capabilityType := reflect.TypeOf(LearningRepositoryCapabilities{})
	wantCapabilities := []string{"AttemptRepository", "ProposalRepository", "SkillRepository", "ValidatedSkillActivation", "AutomaticAdmissionLedger", "OwnershipMode", "CallerInfrastructureRPCsSeparated"}
	if capabilityType.NumField() != len(wantCapabilities) {
		t.Fatalf("learning repository capabilities have %d fields, want %d", capabilityType.NumField(), len(wantCapabilities))
	}
	for i, want := range wantCapabilities {
		if got := capabilityType.Field(i).Name; got != want {
			t.Errorf("learning repository capability field[%d] = %q, want %q", i, got, want)
		}
	}
	wantWireCapabilities := []string{"attempt_repository", "proposal_repository", "skill_repository", "validated_skill_activation", "ownership_mode", "caller_infrastructure_rpcs_separated", "automatic_admission_ledger"}
	wireFields := (&driverv1.LearningRepositoryCapabilitiesResponse{}).ProtoReflect().Descriptor().Fields()
	if wireFields.Len() != len(wantWireCapabilities) {
		t.Fatalf("learning repository wire capabilities have %d fields, want %d", wireFields.Len(), len(wantWireCapabilities))
	}
	for i, want := range wantWireCapabilities {
		if got := string(wireFields.Get(i).Name()); got != want {
			t.Errorf("learning repository wire capability field[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestAttemptRepositoryDriverDoesNotExposeBackendErrors(t *testing.T) {
	t.Parallel()

	partition, err := learning.DeriveAttemptPartition("safe-error-caller")
	if err != nil {
		t.Fatal(err)
	}
	digest := learning.CanonicalDigest("1111111111111111111111111111111111111111111111111111111111111111")
	source := learning.AttemptSource{SessionID: "session-safe-error", RunID: "run-safe-error", CanonicalDigest: digest}
	prompt := learning.CurrentPromptBinding{Ordinal: 1, Digest: digest, Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, prompt)
	if err != nil {
		t.Fatal(err)
	}
	create := learning.AttemptCreate{ID: "attempt-1111111111111111", Provenance: provenance}
	backend := attemptRepositoryStub{create: func(context.Context, learning.AttemptPartition, learning.AttemptCreate) (learning.AttemptRecord, error) {
		return learning.AttemptRecord{}, errors.New("database password hunter2")
	}}
	conn := dialBufconn(t, func(gs *grpc.Server) {
		driverv1.RegisterAttemptRepositoryServiceServer(gs, NewAttemptRepositoryServer(backend))
	})
	_, err = NewAttemptRepository(conn).Create(context.Background(), partition, create)
	if status.Code(err) != codes.Internal {
		t.Fatalf("Create error code = %v, want Internal", status.Code(err))
	}
	if got, want := status.Convert(err).Message(), "attempt repository driver request failed"; got != want {
		t.Fatalf("Create error message = %q, want bounded safe message %q", got, want)
	}
}

func TestAttemptRepositoryDriverMapsSafeTypedErrors(t *testing.T) {
	t.Parallel()

	partition, err := learning.DeriveAttemptPartition("error-caller")
	if err != nil {
		t.Fatal(err)
	}
	digest := learning.CanonicalDigest("abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	source := learning.AttemptSource{SessionID: "session-errors", RunID: "run-errors", CanonicalDigest: digest}
	prompt := learning.CurrentPromptBinding{Ordinal: 1, Digest: digest, Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHard, source, prompt)
	if err != nil {
		t.Fatal(err)
	}
	create := learning.AttemptCreate{ID: "attempt-abcdef0123456789", Provenance: provenance}

	cases := []error{
		learning.ErrInvalidAttempt,
		learning.ErrAttemptNotFound,
		learning.ErrAttemptCreateConflict,
		learning.ErrAttemptVersionConflict,
		learning.ErrAttemptTransition,
		learning.ErrAttemptClaimConflict,
		learning.ErrAttemptClaimLost,
	}
	for _, sentinel := range cases {
		sentinel := sentinel
		t.Run(sentinel.Error(), func(t *testing.T) {
			backend := attemptRepositoryStub{create: func(context.Context, learning.AttemptPartition, learning.AttemptCreate) (learning.AttemptRecord, error) {
				return learning.AttemptRecord{}, sentinel
			}}
			conn := dialBufconn(t, func(gs *grpc.Server) {
				driverv1.RegisterAttemptRepositoryServiceServer(gs, NewAttemptRepositoryServer(backend))
			})
			_, err := NewAttemptRepository(conn).Create(context.Background(), partition, create)
			if !errors.Is(err, sentinel) {
				t.Fatalf("Create error = %v, want errors.Is(_, %v)", err, sentinel)
			}
			if err != nil && err.Error() != sentinel.Error() {
				t.Fatalf("Create error text = %q, want bounded safe text %q", err, sentinel)
			}
		})
	}
}
