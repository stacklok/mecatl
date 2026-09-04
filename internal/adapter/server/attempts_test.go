package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memattempt"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func attemptTestContext(subject string) context.Context {
	return session.WithPrincipal(context.Background(), &session.Principal{Issuer: "https://issuer.example", Subject: subject})
}

func createAttemptFixture(t *testing.T, repository learning.AttemptRepository, subject, sourceSuffix string) learning.AttemptRecord {
	t.Helper()
	return createAttemptFixtureForPrincipal(t, repository, &session.Principal{Issuer: "https://issuer.example", Subject: subject}, sourceSuffix)
}

func createAttemptFixtureForPrincipal(t *testing.T, repository learning.AttemptRepository, principal *session.Principal, sourceSuffix string) learning.AttemptRecord {
	t.Helper()
	caller := reflectionPrincipal(principal)
	partition, err := learning.DeriveAttemptPartition(caller)
	if err != nil {
		t.Fatal(err)
	}
	source := learning.AttemptSource{
		SessionID:       session.SessionID("private-session-" + sourceSuffix),
		RunID:           learning.DurableRunID("private-run-" + sourceSuffix),
		CanonicalDigest: learning.CanonicalDigest(strings.Repeat("a", 64)),
	}
	prompt := learning.CurrentPromptBinding{Ordinal: 0, Digest: learning.CanonicalDigest(strings.Repeat("b", 64)), Origin: learning.PromptOriginCurrentPrincipal}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, source, prompt)
	if err != nil {
		t.Fatal(err)
	}
	id, err := learning.DeterministicAttemptID(caller, source)
	if err != nil {
		t.Fatal(err)
	}
	record, err := repository.Create(context.Background(), partition, learning.AttemptCreate{ID: id, Provenance: provenance})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestADR_0259_AttemptControlsAreNonDisclosingBeforeSideEffects(t *testing.T) {
	repository := memattempt.New(wallclock.Clock{})
	first := createAttemptFixture(t, repository, "alice", "one")
	_ = createAttemptFixture(t, repository, "alice", "two")
	var ownerResolved atomic.Bool
	ordered := &ownerFirstAttemptRepository{AttemptRepository: repository, ownerResolved: &ownerResolved}
	diag := &attemptRecordingDiagnostics{}
	svc := &Service{cfg: Config{
		Attempts: ordered, OwnershipEnforced: true, Diagnostics: diag,
		AttemptPrincipal: func(principal *session.Principal) string {
			ownerResolved.Store(true)
			return reflectionPrincipal(principal)
		},
	}}

	alice := attemptTestContext("alice")
	bob := attemptTestContext("bob")
	owned, err := svc.GetLearningAttempt(alice, string(first.ID))
	if err != nil || owned.GetId() != string(first.ID) {
		t.Fatalf("owner GetLearningAttempt() = %#v, %v", owned, err)
	}
	page1, err := svc.ListLearningAttempts(alice, "", "", 1)
	if err != nil || len(page1.GetAttempts()) != 1 || page1.GetNextCursor() == "" {
		t.Fatalf("owner first page = %#v, %v", page1, err)
	}
	page2, err := svc.ListLearningAttempts(alice, "", page1.GetNextCursor(), 1)
	if err != nil || len(page2.GetAttempts()) != 1 || page2.GetAttempts()[0].GetId() == page1.GetAttempts()[0].GetId() {
		t.Fatalf("owner second page = %#v, %v", page2, err)
	}

	_, foreignErr := svc.GetLearningAttempt(bob, string(first.ID))
	_, missingErr := svc.GetLearningAttempt(bob, "attempt-missing")
	if !errors.Is(foreignErr, ErrNotFound) || !errors.Is(missingErr, ErrNotFound) || foreignErr.Error() != missingErr.Error() {
		t.Fatalf("foreign/missing errors = %v / %v; want identical absence", foreignErr, missingErr)
	}
	foreignPage, err := svc.ListLearningAttempts(bob, "", "", 1)
	if err != nil || len(foreignPage.GetAttempts()) != 0 || foreignPage.GetNextCursor() != "" {
		t.Fatalf("foreign list disclosed owner metadata: %#v, %v", foreignPage, err)
	}
	if len(diag.entries) != 0 {
		t.Fatalf("absence emitted diagnostics: %v", diag.entries)
	}

	grpcEndpoint := &HarnessServer{svc: svc}
	if _, err = grpcEndpoint.GetLearningAttempt(bob, &mecatlv1.GetLearningAttemptRequest{Id: string(first.ID)}); status.Code(err) != codes.NotFound {
		t.Fatalf("gRPC foreign get code = %v, want NotFound", status.Code(err))
	}
	if _, err = grpcEndpoint.GetLearningAttempt(bob, &mecatlv1.GetLearningAttemptRequest{Id: "attempt-missing"}); status.Code(err) != codes.NotFound {
		t.Fatalf("gRPC missing get code = %v, want NotFound", status.Code(err))
	}

	httpEndpoint := NewHTTPHandler(svc)
	foreignHTTP := httptest.NewRecorder()
	httpEndpoint.ServeHTTP(foreignHTTP, httptest.NewRequest(http.MethodGet, "/v1/learning/attempts/"+string(first.ID), nil).WithContext(bob))
	missingHTTP := httptest.NewRecorder()
	httpEndpoint.ServeHTTP(missingHTTP, httptest.NewRequest(http.MethodGet, "/v1/learning/attempts/attempt-missing", nil).WithContext(bob))
	if foreignHTTP.Code != http.StatusNotFound || missingHTTP.Code != http.StatusNotFound || foreignHTTP.Body.String() != missingHTTP.Body.String() {
		t.Fatalf("HTTP foreign/missing = (%d, %q) / (%d, %q)", foreignHTTP.Code, foreignHTTP.Body.String(), missingHTTP.Code, missingHTTP.Body.String())
	}
}

func TestADR_0259_AttemptControlsRequirePrivateOwnerBinding(t *testing.T) {
	repository := memattempt.New(wallclock.Clock{})
	failed := createAttemptFixture(t, repository, "alice", "failed")
	now := failed.CreatedAt.Add(time.Second)
	alicePartition, err := learning.DeriveAttemptPartition(reflectionPrincipal(&session.Principal{Issuer: "https://issuer.example", Subject: "alice"}))
	if err != nil {
		t.Fatal(err)
	}
	running, claim, err := repository.AcquireClaim(context.Background(), alicePartition, failed.ID, failed.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	failed, err = repository.Finalize(context.Background(), alicePartition, failed.ID, running.Version, claim, learning.AttemptFinalization{
		State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: learning.FailureUnavailable,
	})
	if err != nil {
		t.Fatal(err)
	}

	var ownerResolved atomic.Bool
	ordered := &ownerFirstAttemptRepository{AttemptRepository: repository, ownerResolved: &ownerResolved}
	svc := &Service{cfg: Config{
		Attempts: ordered, OwnershipEnforced: true, Now: func() time.Time { return now },
		AttemptPrincipal: func(principal *session.Principal) string {
			ownerResolved.Store(true)
			return reflectionPrincipal(principal)
		},
	}}

	before, found, err := repository.Get(context.Background(), alicePartition, failed.ID)
	if err != nil || !found {
		t.Fatalf("Get failed attempt = %#v, %v", before, err)
	}
	bob := attemptTestContext("bob")
	if _, err = svc.RetryLearningAttempt(bob, string(failed.ID), string(failed.Version)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign retry error = %v, want absence", err)
	}
	if _, err = svc.AbandonLearningAttempt(bob, string(failed.ID), string(failed.Version)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign abandon error = %v, want absence", err)
	}
	system := session.WithPrincipal(context.Background(), &session.Principal{Issuer: "mecatl:internal", Subject: "scheduler", GrantType: session.GrantTypeSystem})
	if _, err = svc.RetryLearningAttempt(system, string(failed.ID), string(failed.Version)); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("system retry error = %v, want failed precondition", err)
	}
	if _, err = svc.AbandonLearningAttempt(system, string(failed.ID), string(failed.Version)); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("system abandon error = %v, want failed precondition", err)
	}
	if _, err = svc.RetryLearningAttempt(context.Background(), string(failed.ID), string(failed.Version)); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("identity-free retry error = %v, want failed precondition", err)
	}
	if _, err = svc.AbandonLearningAttempt(context.Background(), string(failed.ID), string(failed.Version)); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("identity-free abandon error = %v, want failed precondition", err)
	}
	afterRejected, _, err := repository.Get(context.Background(), alicePartition, failed.ID)
	if err != nil || afterRejected != before {
		t.Fatalf("rejected controls changed attempt: before=%#v after=%#v err=%v", before, afterRejected, err)
	}

	alice := attemptTestContext("alice")
	if _, err = svc.RetryLearningAttempt(alice, string(failed.ID), "stale-version"); !errors.Is(err, ErrAttemptVersionConflict) {
		t.Fatalf("stale retry error = %v, want version conflict", err)
	}
	afterStale, _, _ := repository.Get(context.Background(), alicePartition, failed.ID)
	if afterStale != before {
		t.Fatalf("stale retry changed attempt: before=%#v after=%#v", before, afterStale)
	}
	retried, err := svc.RetryLearningAttempt(alice, string(failed.ID), string(failed.Version))
	if err != nil || retried.GetState() != string(learning.AttemptQueued) || retried.GetAttemptGeneration() != uint64(failed.AttemptGeneration+1) {
		t.Fatalf("retry result = %#v, %v", retried, err)
	}

	terminal := createAttemptFixture(t, repository, "alice", "terminal")
	abandoned, err := svc.AbandonLearningAttempt(alice, string(terminal.ID), string(terminal.Version))
	if err != nil || abandoned.GetState() != string(learning.AttemptAbandoned) {
		t.Fatalf("abandon result = %#v, %v", abandoned, err)
	}
	if _, err = svc.RetryLearningAttempt(alice, string(terminal.ID), abandoned.GetVersion()); !errors.Is(err, ErrAttemptTerminalConflict) {
		t.Fatalf("terminal retry error = %v, want terminal conflict", err)
	}
	terminalAfter, _, _ := repository.Get(context.Background(), alicePartition, terminal.ID)
	if terminalAfter.Version != learning.AttemptVersion(abandoned.GetVersion()) || terminalAfter.State != learning.AttemptAbandoned {
		t.Fatalf("terminal conflict changed attempt: %#v", terminalAfter)
	}

	claimed := createAttemptFixture(t, repository, "alice", "claimed")
	claimed, _, err = repository.AcquireClaim(context.Background(), alicePartition, claimed.ID, claimed.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AbandonLearningAttempt(alice, string(claimed.ID), string(claimed.Version)); !errors.Is(err, ErrAttemptLiveClaimConflict) {
		t.Fatalf("live-claim abandon error = %v, want live claim conflict", err)
	}
	claimedAfter, _, _ := repository.Get(context.Background(), alicePartition, claimed.ID)
	if claimedAfter != claimed {
		t.Fatalf("live-claim conflict changed attempt: before=%#v after=%#v", claimed, claimedAfter)
	}

	ownerlessPartition, err := learning.DeriveAttemptPartition(reflectionPrincipal(nil))
	if err != nil {
		t.Fatal(err)
	}
	ownerlessFailed := createAttemptFixtureForPrincipal(t, repository, nil, "ownerless-failed")
	ownerlessRunning, ownerlessClaim, err := repository.AcquireClaim(context.Background(), ownerlessPartition, ownerlessFailed.ID, ownerlessFailed.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ownerlessFailed, err = repository.Finalize(context.Background(), ownerlessPartition, ownerlessFailed.ID, ownerlessRunning.Version, ownerlessClaim, learning.AttemptFinalization{
		State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: learning.FailureUnavailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerlessControls := &Service{cfg: Config{Attempts: repository, Now: func() time.Time { return now }}}
	ownerlessRetried, err := ownerlessControls.RetryLearningAttempt(context.Background(), string(ownerlessFailed.ID), string(ownerlessFailed.Version))
	if err != nil || ownerlessRetried.GetState() != string(learning.AttemptQueued) {
		t.Fatalf("ownerless retry = %#v, %v", ownerlessRetried, err)
	}
	if _, err = ownerlessControls.RetryLearningAttempt(system, string(ownerlessFailed.ID), ownerlessRetried.GetVersion()); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("ownerless system retry error = %v, want failed precondition", err)
	}
	ownerlessAbandoned, err := ownerlessControls.AbandonLearningAttempt(context.Background(), string(ownerlessFailed.ID), ownerlessRetried.GetVersion())
	if err != nil || ownerlessAbandoned.GetState() != string(learning.AttemptAbandoned) {
		t.Fatalf("ownerless abandon = %#v, %v", ownerlessAbandoned, err)
	}

	grpcFailed := createFailedAttemptFixture(t, repository, "alice", "grpc")
	grpcResponse, err := (&HarnessServer{svc: svc}).RetryLearningAttempt(alice, &mecatlv1.MutateLearningAttemptRequest{
		Id: string(grpcFailed.ID), ExpectedVersion: string(grpcFailed.Version),
	})
	if err != nil || grpcResponse.GetAttempt().GetState() != string(learning.AttemptQueued) {
		t.Fatalf("gRPC retry = %#v, %v", grpcResponse, err)
	}

	httpQueued := createAttemptFixture(t, repository, "alice", "http")
	httpResponse := httptest.NewRecorder()
	httpRequest := httptest.NewRequest(http.MethodPost, "/v1/learning/attempts/"+string(httpQueued.ID)+"/abandon", strings.NewReader(`{"expected_version":"`+string(httpQueued.Version)+`"}`)).WithContext(alice)
	NewHTTPHandler(svc).ServeHTTP(httpResponse, httpRequest)
	if httpResponse.Code != http.StatusOK {
		t.Fatalf("HTTP abandon status = %d, body=%s", httpResponse.Code, httpResponse.Body.String())
	}
	var abandonedResponse mecatlv1.MutateLearningAttemptResponse
	if err = json.Unmarshal(httpResponse.Body.Bytes(), &abandonedResponse); err != nil || abandonedResponse.GetAttempt().GetState() != string(learning.AttemptAbandoned) {
		t.Fatalf("HTTP abandon = %#v, %v", abandonedResponse.GetAttempt(), err)
	}
}

func createFailedAttemptFixture(t *testing.T, repository learning.AttemptRepository, subject, suffix string) learning.AttemptRecord {
	t.Helper()
	record := createAttemptFixture(t, repository, subject, suffix)
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(&session.Principal{Issuer: "https://issuer.example", Subject: subject}))
	if err != nil {
		t.Fatal(err)
	}
	running, claim, err := repository.AcquireClaim(context.Background(), partition, record.ID, record.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	record, err = repository.Finalize(context.Background(), partition, record.ID, running.Version, claim, learning.AttemptFinalization{
		State: learning.AttemptFailed, Outcome: learning.AttemptOutcomeFailed, FailureCode: learning.FailureUnavailable,
	})
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func TestADR_0259_AttemptAPIIsContentFree(t *testing.T) {
	repository := memattempt.New(wallclock.Clock{})
	record := createAttemptFixture(t, repository, "alice", "secret-path-token")
	caller := reflectionPrincipal(&session.Principal{Issuer: "https://issuer.example", Subject: "alice"})
	partition, err := learning.DeriveAttemptPartition(caller)
	if err != nil {
		t.Fatal(err)
	}
	running, claim, err := repository.AcquireClaim(context.Background(), partition, record.ID, record.Version, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	running, err = repository.Checkpoint(context.Background(), partition, record.ID, running.Version, claim, learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointProposalLinked, ProposalID: "proposal-safe-link"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.Checkpoint(context.Background(), partition, record.ID, running.Version, claim, learning.AttemptCheckpoint{Stage: learning.AttemptCheckpointSkillLinked, ProposalID: "proposal-safe-link", SkillID: "skill-safe-link"})
	if err != nil {
		t.Fatal(err)
	}

	svc := &Service{cfg: Config{Attempts: repository, OwnershipEnforced: true}}
	got, err := svc.GetLearningAttempt(attemptTestContext("alice"), string(record.ID))
	if err != nil {
		t.Fatal(err)
	}
	assertAttemptProjectionShape(t, got)
	wire, err := protojson.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-session", "private-run", strings.Repeat("a", 64), strings.Repeat("b", 64), "issuer.example", "alice", "secret-path-token"} {
		if strings.Contains(string(wire), forbidden) {
			t.Fatalf("attempt projection leaked %q: %s", forbidden, wire)
		}
	}
	if got.GetProposalId() != "proposal-safe-link" || got.GetSkillId() != "skill-safe-link" {
		t.Fatalf("authorized links = (%q, %q)", got.GetProposalId(), got.GetSkillId())
	}
	assertProtoStringsValid(t, got.ProtoReflect())

	grpcResponse, err := (&HarnessServer{svc: svc}).GetLearningAttempt(attemptTestContext("alice"), &mecatlv1.GetLearningAttemptRequest{Id: string(record.ID)})
	if err != nil || grpcResponse.GetAttempt().GetId() != string(record.ID) {
		t.Fatalf("gRPC attempt projection = %#v, %v", grpcResponse, err)
	}
	listResponse, err := (&HarnessServer{svc: svc}).ListLearningAttempts(attemptTestContext("alice"), &mecatlv1.ListLearningAttemptsRequest{State: string(learning.AttemptRunning), Limit: 1})
	if err != nil || len(listResponse.GetAttempts()) != 1 {
		t.Fatalf("gRPC attempt page = %#v, %v", listResponse, err)
	}

	response := &mecatlv1.ListLearningAttemptsResponse{Attempts: []*mecatlv1.LearningAttempt{got}, NextCursor: string(record.ID)}
	fields := protoFieldNames(response.ProtoReflect().Descriptor())
	if !reflect.DeepEqual(fields, []string{"attempts", "next_cursor"}) {
		t.Fatalf("list projection fields = %v; content/count/watch fields are forbidden", fields)
	}
	assertProtoStringsValid(t, response.ProtoReflect())

	encoded, err := json.Marshal(response)
	if err != nil || !utf8.Valid(encoded) {
		t.Fatalf("HTTP JSON is not valid UTF-8: %q, %v", encoded, err)
	}

	httpResponse := httptest.NewRecorder()
	NewHTTPHandler(svc).ServeHTTP(httpResponse, httptest.NewRequest(http.MethodGet, "/v1/learning/attempts/"+string(record.ID), nil).WithContext(attemptTestContext("alice")))
	if httpResponse.Code != http.StatusOK || !utf8.Valid(httpResponse.Body.Bytes()) {
		t.Fatalf("HTTP attempt projection = %d, %q", httpResponse.Code, httpResponse.Body.Bytes())
	}
	for _, forbidden := range []string{"private-session", "private-run", "secret-path-token", "issuer.example"} {
		if strings.Contains(httpResponse.Body.String(), forbidden) {
			t.Fatalf("HTTP attempt projection leaked %q: %s", forbidden, httpResponse.Body.String())
		}
	}
}

func assertAttemptProjectionShape(t *testing.T, attempt *mecatlv1.LearningAttempt) {
	t.Helper()
	got := protoFieldNames(attempt.ProtoReflect().Descriptor())
	want := []string{"id", "version", "state", "outcome", "failure_code", "attempt_generation", "claim_generation", "claim_expires_at", "checkpoint_stage", "proposal_id", "skill_id", "created_at", "updated_at"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("attempt projection fields = %v, want safe closed metadata %v", got, want)
	}
}

func protoFieldNames(descriptor protoreflect.MessageDescriptor) []string {
	fields := descriptor.Fields()
	out := make([]string, fields.Len())
	for i := 0; i < fields.Len(); i++ {
		out[i] = string(fields.Get(i).Name())
	}
	return out
}

func assertProtoStringsValid(t *testing.T, message protoreflect.Message) {
	t.Helper()
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if field.Message() != nil {
					assertProtoStringsValid(t, list.Get(i).Message())
				} else if field.Kind() == protoreflect.StringKind && !utf8.ValidString(list.Get(i).String()) {
					t.Errorf("invalid UTF-8 at %s", field.FullName())
				}
			}
		} else if field.Message() != nil {
			assertProtoStringsValid(t, value.Message())
		} else if field.Kind() == protoreflect.StringKind && !utf8.ValidString(value.String()) {
			t.Errorf("invalid UTF-8 at %s", field.FullName())
		}
		return true
	})
}

type ownerFirstAttemptRepository struct {
	learning.AttemptRepository
	ownerResolved *atomic.Bool
}

func (r *ownerFirstAttemptRepository) Get(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID) (learning.AttemptRecord, bool, error) {
	if !r.ownerResolved.Swap(false) {
		return learning.AttemptRecord{}, false, errors.New("attempt repository reached before private owner binding")
	}
	return r.AttemptRepository.Get(ctx, partition, id)
}

func (r *ownerFirstAttemptRepository) List(ctx context.Context, partition learning.AttemptPartition, query learning.AttemptList) (learning.AttemptPage, error) {
	if !r.ownerResolved.Swap(false) {
		return learning.AttemptPage{}, errors.New("attempt repository reached before private owner binding")
	}
	return r.AttemptRepository.List(ctx, partition, query)
}

func (r *ownerFirstAttemptRepository) Retry(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion) (learning.AttemptRecord, error) {
	if !r.ownerResolved.Swap(false) {
		return learning.AttemptRecord{}, errors.New("attempt repository reached before private owner binding")
	}
	return r.AttemptRepository.Retry(ctx, partition, id, expected)
}

func (r *ownerFirstAttemptRepository) Abandon(ctx context.Context, partition learning.AttemptPartition, id learning.AttemptID, expected learning.AttemptVersion) (learning.AttemptRecord, error) {
	if !r.ownerResolved.Swap(false) {
		return learning.AttemptRecord{}, errors.New("attempt repository reached before private owner binding")
	}
	return r.AttemptRepository.Abandon(ctx, partition, id, expected)
}

type attemptRecordingDiagnostics struct{ entries []string }

func (d *attemptRecordingDiagnostics) Log(_ context.Context, _ port.Level, message string, _ ...any) {
	d.entries = append(d.entries, message)
}

func (d *attemptRecordingDiagnostics) With(...any) port.Diagnostics { return d }

var _ port.Diagnostics = (*attemptRecordingDiagnostics)(nil)
