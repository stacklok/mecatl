package server

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

// custodyRecorder captures the host's enrollment choreography in order.
type custodyRecorder struct {
	mu         sync.Mutex
	events     []string
	stageIDs   []string
	stageErr   error
	commitErrs []error
	commits    int
	tombstones int
	noOffer    bool
	// failCustodySave fails every Save carrying custody once; landThenFail
	// makes that write durable before reporting the error.
	failCustodySave bool
	landThenFail    bool
	// attachErr makes expected-binding attach fail (e.g. structured instance loss).
	attachErr error
	// recoverWith supplies the fresh B2 attachment; nil makes Recover unavailable.
	recoverWith func(context.Context, session.SessionID) (brokercontract.SessionHandle, error)
	recovers    int
}

func (r *custodyRecorder) add(event string) {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
}

func (r *custodyRecorder) snapshot() ([]string, []string, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...), append([]string(nil), r.stageIDs...), r.commits
}

type custodyAttachment struct {
	*enrollmentAttachment
	rec *custodyRecorder
}

func (a *custodyAttachment) CredentialContinuity() bool {
	a.rec.mu.Lock()
	defer a.rec.mu.Unlock()
	return !a.rec.noOffer
}

func (a *custodyAttachment) StageCredentialCustody(_ context.Context, requestID string, guard brokercontract.ContinuityGuard, _ brokercontract.WorkspaceEnrollmentRef, _ time.Time) (brokercontract.StagedCredentialCustody, error) {
	a.rec.mu.Lock()
	a.rec.stageIDs = append(a.rec.stageIDs, requestID)
	a.rec.events = append(a.rec.events, "stage")
	stageErr := a.rec.stageErr
	a.rec.mu.Unlock()
	if guard.ProfileDigest != ([32]byte{}) || len(guard.Providers) != 0 {
		return brokercontract.StagedCredentialCustody{}, errors.New("host supplied broker-owned guard fields")
	}
	if stageErr != nil {
		return brokercontract.StagedCredentialCustody{}, stageErr
	}
	return brokercontract.StagedCredentialCustody{
		RecoveryReference: strings.Repeat("A", 43),
		ExpiresAt:         time.Now().Add(24 * time.Hour).UTC(),
		ProfileDigest:     [32]byte{7},
		Providers:         []string{"calendar"},
	}, nil
}

type custodyBroker struct {
	*enrollmentBroker
	rec     *custodyRecorder
	wrapped *custodyAttachment
}

func (b *custodyBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.SessionHandle, brokercontract.AttachOutcome, error) {
	if _, outcome, err := b.enrollmentBroker.AttachSession(ctx, id); err != nil {
		return nil, outcome, err
	}
	if b.wrapped == nil || b.wrapped.enrollmentAttachment != b.enrollmentBroker.attachment {
		b.wrapped = &custodyAttachment{enrollmentAttachment: b.enrollmentBroker.attachment, rec: b.rec}
	}
	return b.wrapped, brokercontract.AttachReattached, nil
}

func (b *custodyBroker) AttachSessionExpectedBinding(ctx context.Context, id session.SessionID, _ session.ExternalBinding) (brokercontract.SessionHandle, brokercontract.AttachOutcome, error) {
	b.rec.mu.Lock()
	attachErr := b.rec.attachErr
	b.rec.mu.Unlock()
	if attachErr != nil {
		return nil, "", attachErr
	}
	return b.AttachSession(ctx, id)
}

func (b *custodyBroker) CommitCredentialCustody(context.Context, brokercontract.CustodyAssertion) error {
	b.rec.mu.Lock()
	defer b.rec.mu.Unlock()
	b.rec.commits++
	b.rec.events = append(b.rec.events, "commit")
	if len(b.rec.commitErrs) != 0 {
		err := b.rec.commitErrs[0]
		b.rec.commitErrs = b.rec.commitErrs[1:]
		return err
	}
	return nil
}

func (b *custodyBroker) RecoverCredentialAttachment(ctx context.Context, assertion brokercontract.CustodyAssertion, _ string) (brokercontract.RecoveredCredentialAttachment, error) {
	b.rec.mu.Lock()
	b.rec.recovers++
	b.rec.events = append(b.rec.events, "recover")
	recoverWith := b.rec.recoverWith
	b.rec.mu.Unlock()
	if recoverWith == nil {
		return brokercontract.RecoveredCredentialAttachment{}, brokercontract.ErrContinuityUnavailable
	}
	attachment, err := recoverWith(ctx, assertion.Guard.SessionID)
	if err != nil {
		return brokercontract.RecoveredCredentialAttachment{}, err
	}
	return brokercontract.RecoveredCredentialAttachment{Attachment: attachment}, nil
}

func (b *custodyBroker) TombstoneCredentialCustody(context.Context, brokercontract.CustodyAssertion) error {
	b.rec.mu.Lock()
	b.rec.tombstones++
	b.rec.events = append(b.rec.events, "tombstone")
	b.rec.mu.Unlock()
	return nil
}

type custodyRecordingStore struct {
	port.SessionStore
	rec *custodyRecorder
}

func (s custodyRecordingStore) Save(ctx context.Context, sess *session.Session) error {
	s.rec.mu.Lock()
	failCustodySave := s.rec.failCustodySave
	landThenFail := s.rec.landThenFail
	s.rec.mu.Unlock()
	if _, custody := sess.BrokerCredentialCustody(); custody && failCustodySave {
		if landThenFail {
			// The write lands but the store still reports an error (ambiguous).
			if err := s.SessionStore.Save(ctx, sess); err != nil {
				return err
			}
		}
		s.rec.add("save:failed")
		return errors.New("store unavailable")
	}
	_, pending := sess.PendingWorkspaceEnrollment()
	_, custody := sess.BrokerCredentialCustody()
	switch {
	case pending && custody:
		s.rec.add("save:pending+custody")
	case !pending && custody:
		s.rec.add("save:completed+custody")
	case pending:
		s.rec.add("save:pending")
	}
	return s.SessionStore.Save(ctx, sess)
}

type custodyFixture struct {
	svc     *Service
	broker  *custodyBroker
	rec     *custodyRecorder
	store   port.SessionStore
	session *session.Session
}

func newCustodyFixture(t *testing.T) *custodyFixture {
	t.Helper()
	rec := &custodyRecorder{}
	store := custodyRecordingStore{SessionStore: memstore.New(), rec: rec}
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { _ = runtime.Close() })
	broker := &custodyBroker{enrollmentBroker: &enrollmentBroker{Service: runtime}, rec: rec}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "workspace-enrollment-custody" }, MCPBroker: broker,
		BrokerWorkloadIdentity: &session.Principal{Issuer: "https://kubernetes.default.svc", Subject: "system:serviceaccount:agents:mecak8s"},
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			rec.add("build")
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	owned := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser})
	created, err := svc.CreateSession(owned, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return &custodyFixture{svc: svc, broker: broker, rec: rec, store: store, session: created}
}

// begin starts the enrollment and makes the broker report it connected.
func (f *custodyFixture) begin(t *testing.T) WorkspaceEnrollmentProjection {
	t.Helper()
	started, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("begin = %#v, %v", started, err)
	}
	catalogue, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}})
	if err != nil {
		t.Fatal(err)
	}
	f.broker.enrollmentBroker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: catalogue}
	f.rec.mu.Lock()
	f.rec.events = nil
	f.rec.mu.Unlock()
	return WorkspaceEnrollmentProjection{Ref: started.Ref, Status: started.Status}
}

func (f *custodyFixture) engineRegistered() bool {
	f.svc.mu.Lock()
	defer f.svc.mu.Unlock()
	_, ok := f.svc.sessionEngines[f.session.ID]
	return ok
}

func TestWorkspaceEnrollmentCustodyFollowsNormativeOrder(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	connected, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("connect = %#v, %v", connected, err)
	}
	events, _, _ := f.rec.snapshot()
	want := []string{"stage", "save:pending+custody", "commit", "build", "save:completed+custody"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if !f.engineRegistered() {
		t.Fatal("no engine registered after durable completion")
	}
	loaded, err := f.store.Load(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	custody, ok := loaded.BrokerCredentialCustody()
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending || !ok || custody.ProfileDigest() != ([32]byte{7}) || strings.Join(custody.Providers(), ",") != "calendar" {
		t.Fatalf("persisted custody = %+v, %v", custody, ok)
	}
}

func TestWorkspaceEnrollmentCustodyCommitFailureBlocksCompletionAndRetriesOnlyCommit(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.commitErrs = []error{brokercontract.ErrContinuityUnavailable}
	f.rec.mu.Unlock()
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("completion succeeded although custody Commit failed")
	}
	loaded, err := f.store.Load(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); !pending {
		t.Fatal("failed Commit completed the enrollment")
	}
	if _, custody := loaded.BrokerCredentialCustody(); !custody {
		t.Fatal("staged custody was not persisted before Commit")
	}
	if events, _, _ := f.rec.snapshot(); strings.Contains(strings.Join(events, ","), "build") {
		t.Fatalf("enrollment engine built although custody Commit failed: %v", events)
	}

	if connected, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("retry = %#v, %v", connected, err)
	}
	events, stageIDs, commits := f.rec.snapshot()
	if len(stageIDs) != 1 || commits != 2 {
		t.Fatalf("stage calls = %v, commits = %d; want one Stage and a retried Commit", stageIDs, commits)
	}
	if strings.Count(strings.Join(events, ","), "stage") != 1 {
		t.Fatalf("retry staged new custody: %v", events)
	}
}

func TestWorkspaceEnrollmentStageFailureRetriesSameRequestID(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.stageErr = brokercontract.ErrContinuityUnavailable
	f.rec.mu.Unlock()
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("completion succeeded although Stage failed")
	}
	loaded, _ := f.store.Load(t.Context(), f.session.ID)
	if _, custody := loaded.BrokerCredentialCustody(); custody {
		t.Fatal("failed Stage persisted custody")
	}
	f.rec.mu.Lock()
	f.rec.stageErr = nil
	f.rec.mu.Unlock()
	if connected, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("retry = %#v, %v", connected, err)
	}
	_, stageIDs, _ := f.rec.snapshot()
	if len(stageIDs) != 2 || stageIDs[0] != stageIDs[1] || stageIDs[0] == "" {
		t.Fatalf("stage request ids = %v, want one stable id reused", stageIDs)
	}
}

func TestWorkspaceEnrollmentCustodyPartitionChangeFailsBeforeCommit(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.commitErrs = []error{brokercontract.ErrContinuityUnavailable}
	f.rec.mu.Unlock()
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("first completion unexpectedly succeeded")
	}
	f.svc.cfg.BrokerWorkloadIdentity = &session.Principal{Issuer: "https://kubernetes.default.svc", Subject: "system:serviceaccount:agents:other"}
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("custody committed after the host workload identity changed")
	}
	if _, _, commits := f.rec.snapshot(); commits != 1 {
		t.Fatalf("commits = %d, want the partition mismatch to fail before any Commit RPC", commits)
	}
}

func TestWorkspaceEnrollmentOfferedContinuityFailsClosedWithoutInputs(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.svc.cfg.BrokerWorkloadIdentity = nil
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("enrollment completed without custody although the broker offered continuity")
	}
	loaded, _ := f.store.Load(t.Context(), f.session.ID)
	if _, pending := loaded.PendingWorkspaceEnrollment(); !pending {
		t.Fatal("fail-closed enrollment cleared the pending gate")
	}
	if _, stageIDs, _ := f.rec.snapshot(); len(stageIDs) != 0 {
		t.Fatalf("staged without a workload identity: %v", stageIDs)
	}
}

func TestWorkspaceEnrollmentWithoutContinuityOfferKeepsLegacyPath(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.noOffer = true
	f.rec.mu.Unlock()
	if connected, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("legacy connect = %#v, %v", connected, err)
	}
	loaded, _ := f.store.Load(t.Context(), f.session.ID)
	if _, custody := loaded.BrokerCredentialCustody(); custody {
		t.Fatal("custody created although the broker did not offer continuity")
	}
	if _, stageIDs, commits := f.rec.snapshot(); len(stageIDs) != 0 || commits != 0 {
		t.Fatalf("stage=%v commits=%d on the legacy path", stageIDs, commits)
	}
}

func TestWorkspaceEnrollmentDefiniteCustodySaveFailureTombstones(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.failCustodySave = true
	f.rec.mu.Unlock()
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("completion succeeded although the custody save failed")
	}
	events, _, commits := f.rec.snapshot()
	if commits != 0 || !strings.Contains(strings.Join(events, ","), "save:failed,tombstone") {
		t.Fatalf("events = %v, commits = %d; want Tombstone and no Commit", events, commits)
	}
}

func TestWorkspaceEnrollmentAmbiguousCustodySaveContinuesWhenDurable(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.failCustodySave, f.rec.landThenFail = true, true
	f.rec.mu.Unlock()
	// The ambiguous save landed, so Commit proceeds; the final completion save
	// also carries custody and is failed by this fixture, so the call errors
	// after Commit without ever tombstoning the durable custody.
	_, _ = f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID)
	events, _, commits := f.rec.snapshot()
	joined := strings.Join(events, ",")
	if commits != 1 || strings.Contains(joined, "tombstone") {
		t.Fatalf("events = %v, commits = %d; want Commit and no Tombstone", events, commits)
	}
}

var errInstanceLost = errors.Join(brokercontract.ErrStateUnavailable, brokercontract.ErrBrokerIncarnationLost)

// replacementBroker returns attachments from a second, independent runtime, so
// every recovered binding carries a fresh process prefix like a real B2.
func replacementBroker(t *testing.T) func(context.Context, session.SessionID) (brokercontract.SessionHandle, error) {
	t.Helper()
	b2 := testBrokerRuntime(t)
	t.Cleanup(func() { _ = b2.Close() })
	return func(ctx context.Context, id session.SessionID) (brokercontract.SessionHandle, error) {
		handle, _, err := b2.AttachSession(ctx, id)
		return handle, err
	}
}

// pendingCustodySession leaves the fixture session durably pending+custody by
// failing the first custody Commit, the state a B1 loss before Commit leaves.
func (f *custodyFixture) pendingCustodySession(t *testing.T) {
	t.Helper()
	f.begin(t)
	f.rec.mu.Lock()
	f.rec.commitErrs = []error{brokercontract.ErrContinuityUnavailable}
	f.rec.mu.Unlock()
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("first completion unexpectedly succeeded")
	}
	f.svc.closeSessionLocal(f.session.ID)
}

func TestBrokerRecoveryCompletesPendingCustodySessionWithFreshBinding(t *testing.T) {
	f := newCustodyFixture(t)
	f.pendingCustodySession(t)
	before, _ := f.store.Load(t.Context(), f.session.ID)
	f.rec.mu.Lock()
	f.rec.attachErr, f.rec.recoverWith, f.rec.events = errInstanceLost, replacementBroker(t), nil
	f.rec.mu.Unlock()

	connected, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID)
	if err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("recovery connect = %#v, %v", connected, err)
	}
	events, stageIDs, _ := f.rec.snapshot()
	joined := strings.Join(events, ",")
	if !strings.Contains(joined, "commit,recover") || len(stageIDs) != 1 {
		t.Fatalf("events = %v stage=%v; want still-staged custody committed before Recover and no new Stage", events, stageIDs)
	}
	after, err := f.store.Load(t.Context(), f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := after.PendingWorkspaceEnrollment(); pending {
		t.Fatal("recovered pending enrollment was not completed")
	}
	if after.ExternalBinding == "" || after.ExternalBinding == before.ExternalBinding {
		t.Fatalf("binding = %q (was %q); want the fresh B2 binding persisted", after.ExternalBinding, before.ExternalBinding)
	}
	if _, custody := after.BrokerCredentialCustody(); !custody {
		t.Fatal("recovery dropped custody")
	}
}

func TestBrokerRecoveryAdoptsFreshBindingForCompletedSession(t *testing.T) {
	f := newCustodyFixture(t)
	f.begin(t)
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err != nil {
		t.Fatalf("initial enrollment: %v", err)
	}
	f.svc.closeSessionLocal(f.session.ID)
	before, _ := f.store.Load(t.Context(), f.session.ID)
	oldBinding := before.ExternalBinding
	oldTools := strings.Join(before.Authority.CapabilitySet.Tools, ",")
	f.rec.mu.Lock()
	f.rec.attachErr, f.rec.recoverWith = errInstanceLost, replacementBroker(t)
	f.rec.mu.Unlock()

	sel := ProviderSelector{ProviderID: before.ProviderID, ModelID: before.ModelID, ReasoningEffort: before.ReasoningEffort}
	if _, err := f.svc.buildAndRegisterSessionEngine(t.Context(), before, sel, profileForSession(before), before.Mode, true); err != nil {
		t.Fatalf("run-entry engine build after broker loss: %v", err)
	}
	after, _ := f.store.Load(t.Context(), f.session.ID)
	if after.ExternalBinding == "" || after.ExternalBinding == oldBinding {
		t.Fatalf("binding = %q, want a fresh B2 binding replacing %q", after.ExternalBinding, oldBinding)
	}
	if got := strings.Join(after.Authority.CapabilitySet.Tools, ","); got != oldTools {
		t.Fatalf("adopted tools = %v, want %v", got, oldTools)
	}
}

func TestBrokerRecoveryOnlyAfterStructuredInstanceLoss(t *testing.T) {
	f := newCustodyFixture(t)
	f.pendingCustodySession(t)
	f.rec.mu.Lock()
	f.rec.attachErr, f.rec.recoverWith = brokercontract.ErrStateUnavailable, replacementBroker(t)
	f.rec.mu.Unlock()
	if connected, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil && connected.Status == brokercontract.WorkspaceEnrollmentConnected {
		t.Fatal("generic broker unavailability recovered the session")
	}
	f.rec.mu.Lock()
	recovers := f.rec.recovers
	f.rec.mu.Unlock()
	if recovers != 0 {
		t.Fatalf("Recover called %d times without structured instance loss", recovers)
	}
}

func TestBrokerRecoveryFailureLeavesDurableStateUntouched(t *testing.T) {
	f := newCustodyFixture(t)
	f.pendingCustodySession(t)
	before, _ := f.store.Load(t.Context(), f.session.ID)
	f.rec.mu.Lock()
	f.rec.attachErr, f.rec.recoverWith = errInstanceLost, nil
	f.rec.mu.Unlock()
	if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.session.ID); err == nil {
		t.Fatal("recovery succeeded although Recover failed")
	}
	after, _ := f.store.Load(t.Context(), f.session.ID)
	if _, pending := after.PendingWorkspaceEnrollment(); !pending || after.ExternalBinding != before.ExternalBinding {
		t.Fatalf("failed recovery changed durable state: pending=%v binding %q -> %q", pending, before.ExternalBinding, after.ExternalBinding)
	}
}
