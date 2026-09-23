package mcpbrokergrpc

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

type continuityReceiptService struct {
	stage      *continuityReceiptHandle
	recover    *continuityReceiptHandle
	mu         sync.Mutex
	commits    int
	tombs      int
	committed  bool
	tombstoned bool
}

func (s *continuityReceiptService) AttachSession(context.Context, session.SessionID) (mcpbroker.SessionHandle, mcpbroker.AttachOutcome, error) {
	return s.stage, mcpbroker.AttachCreated, nil
}
func (*continuityReceiptService) DeleteSession(context.Context, session.SessionID) (mcpbroker.DeleteOutcome, error) {
	return mcpbroker.DeleteNotFound, nil
}
func (s *continuityReceiptService) CommitCredentialCustody(context.Context, mcpbroker.CustodyAssertion) error {
	s.mu.Lock()
	s.commits++
	s.committed = true
	s.mu.Unlock()
	return nil
}
func (s *continuityReceiptService) RecoverCredentialAttachment(context.Context, mcpbroker.CustodyAssertion, string) (mcpbroker.RecoveredCredentialAttachment, error) {
	s.recover.call()
	return mcpbroker.RecoveredCredentialAttachment{Attachment: s.recover}, nil
}
func (s *continuityReceiptService) TombstoneCredentialCustody(context.Context, mcpbroker.CustodyAssertion) error {
	s.mu.Lock()
	s.tombs++
	s.tombstoned = true
	s.mu.Unlock()
	return nil
}

type continuityReceiptHandle struct {
	mu      sync.Mutex
	calls   int
	started chan struct{}
	release chan struct{}
	tools   []tool.Tool
	aborts  int
}

type oversizedContinuityTool struct{}

func (oversizedContinuityTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: "oversized", Description: strings.Repeat("x", recoverReceiptReservationBytes), Schema: []byte(`{"type":"object"}`)}
}
func (oversizedContinuityTool) ReadOnly() bool { return true }
func (oversizedContinuityTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.ToolResult{}, nil
}

const receiptRecoveryReference = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func newContinuityReceiptHandle() *continuityReceiptHandle {
	return &continuityReceiptHandle{started: make(chan struct{}), release: make(chan struct{})}
}
func (h *continuityReceiptHandle) call() {
	h.mu.Lock()
	h.calls++
	calls := h.calls
	h.mu.Unlock()
	if calls == 1 {
		close(h.started)
		<-h.release
	}
}
func (h *continuityReceiptHandle) count() int                     { h.mu.Lock(); defer h.mu.Unlock(); return h.calls }
func (*continuityReceiptHandle) Binding() session.ExternalBinding { return "recovered-binding" }
func (*continuityReceiptHandle) Commit(context.Context) error     { return nil }
func (h *continuityReceiptHandle) Abort(context.Context) error {
	h.mu.Lock()
	h.aborts++
	h.mu.Unlock()
	return nil
}
func (*continuityReceiptHandle) Close(context.Context) (mcpbroker.CloseOutcome, error) {
	return mcpbroker.CloseClosed, nil
}
func (h *continuityReceiptHandle) abortCount() int { h.mu.Lock(); defer h.mu.Unlock(); return h.aborts }
func (h *continuityReceiptHandle) Tools() []tool.Tool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]tool.Tool(nil), h.tools...)
}
func (*continuityReceiptHandle) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	return "", nil
}
func (*continuityReceiptHandle) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	return "", nil
}
func (*continuityReceiptHandle) CancelAuthorization(context.Context, session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	return mcpbroker.CancelAlreadyCancelled, nil
}
func (h *continuityReceiptHandle) StageCredentialCustody(_ context.Context, _ string, _ mcpbroker.ContinuityGuard, _ mcpbroker.WorkspaceEnrollmentRef, _ time.Time) (mcpbroker.StagedCredentialCustody, error) {
	h.call()
	return mcpbroker.StagedCredentialCustody{RecoveryReference: receiptRecoveryReference, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestContinuityReceiptsUseCountCapacityAndReleaseOnExpiry(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	server.cfg.MaxReceipts = 3
	close(service.stage.release)
	close(service.recover.release)
	deadline := time.Now().Add(time.Minute)
	for i := 0; i < maxConcurrentContinuityReceipts; i++ {
		req := stageReceiptRequest(server, guard, deadline)
		req.RequestId = "stage-capacity-" + string(rune('a'+i))
		req.CompletedEnrollment.Id = req.RequestId
		if _, err := server.StageCredentialCustody(ctx, req); err != nil {
			t.Fatalf("stage %d: %v", i, err)
		}
	}
	fourth := stageReceiptRequest(server, guard, deadline)
	fourth.RequestId = "stage-capacity-overflow"
	fourth.CompletedEnrollment.Id = fourth.RequestId
	if _, err := server.StageCredentialCustody(ctx, fourth); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("overflow continuity receipt = %v, want capacity rejection", err)
	}
	if service.stage.count() != maxConcurrentContinuityReceipts {
		t.Fatalf("stage dispatches = %d, want capacity rejection before dispatch", service.stage.count())
	}

	server.mu.Lock()
	for _, receipt := range server.continuityReceipts {
		receipt.expiresAt = time.Now().Add(-time.Second)
	}
	server.sweepContinuityReceiptsLocked(time.Now())
	remaining, bytes, slots := len(server.continuityReceipts), server.continuityReceiptBytes, server.continuityReceiptSlots
	server.mu.Unlock()
	if remaining != 0 || bytes != 0 || slots != 0 {
		t.Fatalf("expired continuity accounting = %d receipts/%d bytes/%d slots", remaining, bytes, slots)
	}
}

func TestContinuityReceiptReservationsAdmitEightConcurrentRequests(t *testing.T) {
	server, _, _, _ := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	expiresAt := time.Now().Add(time.Minute)
	start := make(chan struct{})
	results := make(chan error, maxConcurrentContinuityReceipts)
	for i := 0; i < maxConcurrentContinuityReceipts; i++ {
		go func(i int) {
			<-start
			key := continuityReceiptKey{operation: "stage/v1", request: string(rune('a' + i))}
			_, leader, err := server.reserveContinuityReceipt(key, continuityDigest([]byte(key.request)), expiresAt, stageReceiptReservationBytes)
			if err == nil && !leader {
				err = status.Error(codes.Internal, "unexpected receipt follower")
			}
			results <- err
		}(i)
	}
	close(start)
	for range maxConcurrentContinuityReceipts {
		if err := <-results; err != nil {
			t.Fatalf("concurrent reservation: %v", err)
		}
	}
	if _, _, err := server.reserveContinuityReceipt(continuityReceiptKey{operation: "stage/v1", request: "overflow"}, continuityDigest([]byte("overflow")), expiresAt, stageReceiptReservationBytes); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("ninth concurrent reservation = %v, want continuity unavailable", err)
	}
}

func TestContinuityReceiptShutdownReleasesReservations(t *testing.T) {
	server, _, _, _ := newContinuityReceiptServer(t)
	expiresAt := time.Now().Add(time.Minute)
	if _, leader, err := server.reserveContinuityReceipt(continuityReceiptKey{operation: "stage/v1", request: "shutdown"}, continuityDigest([]byte("shutdown")), expiresAt, stageReceiptReservationBytes); err != nil || !leader {
		t.Fatalf("reserve continuity receipt = leader:%t err:%v", leader, err)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	server.mu.Lock()
	receipts, bytes, slots := len(server.continuityReceipts), server.continuityReceiptBytes, server.continuityReceiptSlots
	server.mu.Unlock()
	if receipts != 0 || bytes != 0 || slots != 0 {
		t.Fatalf("shutdown continuity accounting = %d receipts/%d bytes/%d slots", receipts, bytes, slots)
	}
}

func TestContinuityReceiptByteCapacityAllowsSettledRecoverResponses(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	close(service.stage.release)
	close(service.recover.release)
	deadline := time.Now().Add(time.Minute)
	for i := 0; i < maxConcurrentContinuityReceipts; i++ {
		request := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-byte-" + string(rune('a'+i)), Assertion: custodyAssertion(guard, deadline)}
		if _, err := server.RecoverCredentialAttachment(ctx, request); err != nil {
			t.Fatalf("recover %d: %v", i, err)
		}
	}
	overflow := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-slot-overflow", Assertion: custodyAssertion(guard, deadline)}
	if _, err := server.RecoverCredentialAttachment(ctx, overflow); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("slot overflow = %v, want continuity unavailable", err)
	}
	if got, want := service.recover.count(), maxConcurrentContinuityReceipts; got != want {
		t.Fatalf("recover dispatches = %d, want %d", got, want)
	}
}

func TestOversizedRecoverReceiptAbortsUnpublishedAttachment(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	service.recover.tools = []tool.Tool{oversizedContinuityTool{}}
	close(service.stage.release)
	close(service.recover.release)
	request := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "oversized-recover", Assertion: custodyAssertion(guard, time.Now().Add(time.Minute))}
	if _, err := server.RecoverCredentialAttachment(ctx, request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("oversized recover = %v, want continuity unavailable", err)
	}
	server.mu.Lock()
	handles, owners := len(server.handles), len(server.owners)
	server.mu.Unlock()
	if handles != 1 || owners != 0 { // stage-handle is a test fixture, recovered state must be gone.
		t.Fatalf("oversized recovered state = handles:%d owners:%d, want fixture-only handle and no owner", handles, owners)
	}
	if got := service.recover.abortCount(); got != 1 {
		t.Fatalf("recovered attachment aborts = %d, want 1", got)
	}
}

func TestStageSuccessAfterCallerExpiryRemainsReplayable(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	req := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
	caller, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	result := make(chan struct {
		response *brokerv1.StageCredentialCustodyResponse
		err      error
	}, 1)
	go func() {
		response, err := server.StageCredentialCustody(caller, req)
		result <- struct {
			response *brokerv1.StageCredentialCustodyResponse
			err      error
		}{response, err}
	}()
	<-service.stage.started
	<-caller.Done()
	close(service.stage.release)
	first := <-result
	if first.err != nil || first.response.GetRecoveryReference() != receiptRecoveryReference {
		t.Fatalf("expired stage attempt = (%#v, %v), want successful staged reference", first.response, first.err)
	}
	replayed, err := server.StageCredentialCustody(context.WithoutCancel(ctx), req)
	if err != nil || replayed.GetRecoveryReference() != first.response.GetRecoveryReference() || service.stage.count() != 1 {
		t.Fatalf("stage replay = (%#v, %v), calls %d; want identical single capability result", replayed, err, service.stage.count())
	}
}

func TestRecoverExpiryBeforeRegistrationRemovesProvisionalState(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	assertion := custodyAssertion(guard, time.Now().Add(time.Minute))
	request := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-expired-before-register", Assertion: assertion}
	caller, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := server.RecoverCredentialAttachment(caller, request)
		result <- err
	}()
	<-service.recover.started
	<-caller.Done()
	close(service.recover.release)
	if err := <-result; status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("expired recovery = %v, want deadline exceeded", err)
	}
	server.mu.Lock()
	registered := len(server.handles) != 1
	owners := len(server.owners)
	server.mu.Unlock()
	if registered || owners != 0 {
		t.Fatalf("expired recovery state = handle:%t owners:%d, want no provisional state", registered, owners)
	}
}

func TestExpiredReceiptDoesNotReplayChangedDigest(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	close(service.stage.release)
	req := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
	if _, err := server.StageCredentialCustody(ctx, req); err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	server.mu.Lock()
	for key, receipt := range server.continuityReceipts {
		if key.request == req.RequestId {
			receipt.expiresAt = time.Now().Add(-time.Second)
		}
	}
	server.mu.Unlock()
	changed := protoCloneStage(req)
	changed.CompletedEnrollment.Id = "changed-after-expiry"
	if _, err := server.StageCredentialCustody(ctx, changed); err != nil {
		t.Fatalf("changed stage after expiry: %v", err)
	}
	if got := service.stage.count(); got != 2 {
		t.Fatalf("stage calls = %d, want changed request redispatched after receipt expiry", got)
	}
}

func TestContinuityReceiptCapacityIsPartitionedByWorkload(t *testing.T) {
	server, _, _, _ := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	expiresAt := time.Now().Add(time.Minute)
	var first, second [32]byte
	second[0] = 1
	for i := 0; i < maxConcurrentContinuityReceipts; i++ {
		key := continuityReceiptKey{workload: first, operation: "stage/v1", request: string(rune('a' + i))}
		if _, leader, err := server.reserveContinuityReceipt(key, continuityDigest([]byte(key.request)), expiresAt, stageReceiptReservationBytes); err != nil || !leader {
			t.Fatalf("first workload reservation %d = leader:%t err:%v", i, leader, err)
		}
	}
	key := continuityReceiptKey{workload: second, operation: "stage/v1", request: "independent-workload"}
	if _, leader, err := server.reserveContinuityReceipt(key, continuityDigest([]byte(key.request)), expiresAt, stageReceiptReservationBytes); err != nil || !leader {
		t.Fatalf("second workload reservation = leader:%t err:%v, want independent capacity", leader, err)
	}
}

func TestPreSideEffectStageFailureDoesNotReserveReceipt(t *testing.T) {
	server, ctx, guard, _ := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	req := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
	req.Handle = "missing-handle"
	if _, err := server.StageCredentialCustody(ctx, req); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("missing handle = %v, want failed precondition", err)
	}
	server.mu.Lock()
	receipts, slots, bytes := len(server.continuityReceipts), server.continuityReceiptSlots, server.continuityReceiptBytes
	server.mu.Unlock()
	if receipts != 0 || slots != 0 || bytes != 0 {
		t.Fatalf("pre-side-effect failure retained %d receipts/%d slots/%d bytes", receipts, slots, bytes)
	}
}

func TestRetryablePreSideEffectFailureReleasesContinuityReceipt(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	caller, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := server.StageCredentialCustody(caller, stageReceiptRequest(server, guard, time.Now().Add(time.Minute))); status.Code(err) != codes.Canceled {
		t.Fatalf("cancelled stage = %v, want cancelled", err)
	}
	if got := service.stage.count(); got != 0 {
		t.Fatalf("stage calls = %d, want no side effect", got)
	}
	server.mu.Lock()
	receipts, slots, bytes := len(server.continuityReceipts), server.continuityReceiptSlots, server.continuityReceiptBytes
	server.mu.Unlock()
	if receipts != 0 || slots != 0 || bytes != 0 {
		t.Fatalf("retryable failure retained %d receipts/%d slots/%d bytes", receipts, slots, bytes)
	}
}

func TestStageCredentialCustodyRetryReturnsSameReference(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	deadline := time.Now().Add(time.Minute)
	req := stageReceiptRequest(server, guard, deadline)
	results := make(chan *brokerv1.StageCredentialCustodyResponse, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() { response, err := server.StageCredentialCustody(ctx, req); results <- response; errs <- err }()
	}
	<-service.stage.started
	close(service.stage.release)
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatalf("first StageCredentialCustody: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second StageCredentialCustody: %v", err)
	}
	if service.stage.count() != 1 || first.GetRecoveryReference() != second.GetRecoveryReference() {
		t.Fatalf("stage calls/results = %d, %q/%q; want one identical replay", service.stage.count(), first.GetRecoveryReference(), second.GetRecoveryReference())
	}
	changed := protoCloneStage(req)
	changed.CompletedEnrollment.Id = "different-enrollment"
	if _, err := server.StageCredentialCustody(ctx, changed); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("changed Stage request = %v, want generic continuity unavailable", err)
	}
}

func TestRecoverCredentialAttachmentRetryReturnsSameProvisionalHandle(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	assertion := custodyAssertion(guard, time.Now().Add(time.Minute))
	req := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-retry", Assertion: assertion}
	results := make(chan *brokerv1.RecoverCredentialAttachmentResponse, 2)
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			response, err := server.RecoverCredentialAttachment(ctx, req)
			results <- response
			errs <- err
		}()
	}
	<-service.recover.started
	close(service.recover.release)
	first, second := <-results, <-results
	if err := <-errs; err != nil {
		t.Fatalf("first RecoverCredentialAttachment: %v", err)
	}
	if err := <-errs; err != nil {
		t.Fatalf("second RecoverCredentialAttachment: %v", err)
	}
	if service.recover.count() != 1 || first.GetHandle() != second.GetHandle() {
		t.Fatalf("recover calls/results = %d, %q/%q; want one identical replay", service.recover.count(), first.GetHandle(), second.GetHandle())
	}
	changed := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: req.RequestId, Assertion: custodyAssertion(guard, time.Now().Add(90*time.Second))}
	if _, err := server.RecoverCredentialAttachment(ctx, changed); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("changed Recover request = %v, want generic continuity unavailable", err)
	}
}

func TestCredentialContinuityMalformedRequestRejectedBeforeStateAccess(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	if _, err := server.RecoverCredentialAttachment(ctx, &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "malformed"}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed recovery = %v, want invalid argument", err)
	}
	if _, err := server.StageCredentialCustody(ctx, &brokerv1.StageCredentialCustodyRequest{RequestId: "\x00", Guard: continuityGuardToWire(guard)}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("malformed stage = %v, want invalid argument", err)
	}
	if service.recover.count() != 0 || service.stage.count() != 0 {
		t.Fatalf("capability calls = recover:%d stage:%d, want none", service.recover.count(), service.stage.count())
	}
}

func TestCredentialContinuityWrongWorkloadRejectedBeforeCustodyLookup(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	guard.WorkloadPartition[0] ^= 0xff
	if _, err := server.RecoverCredentialAttachment(ctx, &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "wrong-workload", Assertion: custodyAssertion(guard, time.Now().Add(time.Minute))}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("wrong workload = %v, want permission denied", err)
	}
	if service.recover.count() != 0 {
		t.Fatalf("recover calls = %d, want no state access", service.recover.count())
	}
}

func TestStageCredentialCustodyChangedRequestIDContentRejected(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	req := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
	result := make(chan error, 1)
	go func() { _, err := server.StageCredentialCustody(ctx, req); result <- err }()
	<-service.stage.started
	close(service.stage.release)
	if err := <-result; err != nil {
		t.Fatalf("seed stage: %v", err)
	}
	changed := protoCloneStage(req)
	changed.CompletedEnrollment.Id = "different-enrollment"
	if _, err := server.StageCredentialCustody(ctx, changed); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("changed stage request = %v, want failed precondition", err)
	}
}

func TestRecoverCredentialAttachmentChangedRequestRejected(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	assertion := custodyAssertion(guard, time.Now().Add(time.Minute))
	req := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-retry", Assertion: assertion}
	result := make(chan error, 1)
	go func() { _, err := server.RecoverCredentialAttachment(ctx, req); result <- err }()
	<-service.recover.started
	close(service.recover.release)
	if err := <-result; err != nil {
		t.Fatalf("seed recover: %v", err)
	}
	changed := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: req.RequestId, Assertion: custodyAssertion(guard, time.Now().Add(90*time.Second))}
	if _, err := server.RecoverCredentialAttachment(ctx, changed); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("changed recover request = %v, want failed precondition", err)
	}
}

func TestCommitCredentialCustodyExactRetryIsIdempotent(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	req := &brokerv1.CommitCredentialCustodyRequest{Assertion: custodyAssertion(guard, time.Now().Add(time.Minute))}
	for range 2 {
		if _, err := server.CommitCredentialCustody(ctx, req); err != nil {
			t.Fatalf("commit retry: %v", err)
		}
	}
	service.mu.Lock()
	calls, committed := service.commits, service.committed
	service.mu.Unlock()
	if calls != 2 || !committed {
		t.Fatalf("commit calls/state = %d/%t, want two exact attempts and one committed state", calls, committed)
	}
}

func TestTombstoneCredentialCustodyExactRetryIsIdempotent(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	req := &brokerv1.TombstoneCredentialCustodyRequest{Assertion: custodyAssertion(guard, time.Now().Add(time.Minute))}
	for range 2 {
		if _, err := server.TombstoneCredentialCustody(ctx, req); err != nil {
			t.Fatalf("tombstone retry: %v", err)
		}
	}
	service.mu.Lock()
	calls, tombstoned := service.tombs, service.tombstoned
	service.mu.Unlock()
	if calls != 2 || !tombstoned {
		t.Fatalf("tombstone calls/state = %d/%t, want two exact attempts and one tombstoned state", calls, tombstoned)
	}
}

func TestCredentialContinuityGuardMismatchIsGeneric(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	guard.WorkloadPartition[0] ^= 0xff
	_, err := server.RecoverCredentialAttachment(ctx, &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "guard-mismatch", Assertion: custodyAssertion(guard, time.Now().Add(time.Minute))})
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "broker session is not available" {
		t.Fatalf("guard mismatch = %v", err)
	}
	if service.recover.count() != 0 {
		t.Fatalf("guard mismatch accessed custody %d times", service.recover.count())
	}
}

func TestBrokerCredentialContinuity_Scenario2_RejectsInvalidContinuityGuard(t *testing.T) {
	server, ctx, _, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	_, err := server.RecoverCredentialAttachment(ctx, &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "invalid-guard", Assertion: &brokerv1.CustodyAssertion{}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid guard = %v", err)
	}
	if service.recover.count() != 0 {
		t.Fatalf("invalid guard accessed custody %d times", service.recover.count())
	}
}

func TestBrokerCredentialContinuity_Scenario2_HostAssertionBoundary(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	guard.WorkloadPartition[0] ^= 0xff
	_, err := server.StageCredentialCustody(ctx, stageReceiptRequest(server, guard, time.Now().Add(time.Minute)))
	if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != "broker session is not available" {
		t.Fatalf("host assertion failure = %v", err)
	}
	if service.stage.count() != 0 {
		t.Fatalf("host assertion reached custody stage %d times", service.stage.count())
	}
}

func newContinuityReceiptServer(t *testing.T) (*Server, context.Context, mcpbroker.ContinuityGuard, *continuityReceiptService) {
	t.Helper()
	principal := &session.Principal{Issuer: "issuer", Subject: "workload", GrantType: session.GrantTypeClientCredentials}
	ctx := session.WithPrincipal(t.Context(), principal)
	workload, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionWorkload, principal)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := mcpbroker.ContinuityPrincipalPartition(mcpbroker.ContinuityPartitionOwner, principal)
	if err != nil {
		t.Fatal(err)
	}
	guard := mcpbroker.ContinuityGuard{SessionID: "continuity-session", SessionIncarnation: session.NewIncarnationID(), OwnerPartition: owner, WorkloadPartition: workload, Providers: []string{"provider"}}
	guard.ProfileDigest[0] = 1
	service := &continuityReceiptService{stage: newContinuityReceiptHandle(), recover: newContinuityReceiptHandle()}
	server, err := NewServer(service, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	server.handles["stage-handle"] = &serverHandle{sessionHandle: service.stage, principal: principal, logicalID: guard.SessionID, binding: string(service.stage.Binding()), expiresAt: time.Now().Add(time.Minute), changed: make(chan struct{}), receipts: make(map[session.ToolCallID]*executeReceipt)}
	return server, ctx, guard, service
}

func stageReceiptRequest(server *Server, guard mcpbroker.ContinuityGuard, deadline time.Time) *brokerv1.StageCredentialCustodyRequest {
	return &brokerv1.StageCredentialCustodyRequest{RequestId: "stage-retry", BrokerIncarnation: server.instanceID, Handle: "stage-handle", Guard: continuityGuardToWire(guard), CompletedEnrollment: &brokerv1.WorkspaceRef{Id: "enrollment", RequiredServices: 1, ExpiresAt: timestamppb.New(deadline)}, AttemptDeadline: timestamppb.New(deadline)}
}
func custodyAssertion(guard mcpbroker.ContinuityGuard, deadline time.Time) *brokerv1.CustodyAssertion {
	return &brokerv1.CustodyAssertion{Guard: continuityGuardToWire(guard), RecoveryReference: receiptRecoveryReference, AttemptDeadline: timestamppb.New(deadline)}
}
func protoCloneStage(in *brokerv1.StageCredentialCustodyRequest) *brokerv1.StageCredentialCustodyRequest {
	out := *in
	ref := *in.CompletedEnrollment
	out.CompletedEnrollment = &ref
	return &out
}
