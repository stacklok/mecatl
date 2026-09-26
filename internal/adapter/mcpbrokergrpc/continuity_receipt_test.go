package mcpbrokergrpc

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

type continuityReceiptService struct {
	stage      *continuityReceiptHandle
	recover    *continuityReceiptHandle
	recoverErr error
	mu         sync.Mutex
	commits    int
	tombs      int
	committed  bool
	tombstoned bool
}

func (s *continuityReceiptService) AttachSession(context.Context, session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
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
func (s *continuityReceiptService) RecoverCredentialAttachment(ctx context.Context, _ mcpbroker.CustodyAssertion, _ string) (mcpbroker.RecoveredCredentialAttachment, error) {
	s.recover.captureContext(ctx)
	s.recover.call()
	if s.recoverErr != nil {
		return mcpbroker.RecoveredCredentialAttachment{}, s.recoverErr
	}
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
	mu           sync.Mutex
	calls        int
	started      chan struct{}
	release      chan struct{}
	operationCtx chan context.Context
	tools        []tool.Tool
	aborts       int
	continuity   bool
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

type continuityFixedClock struct {
	now time.Time
}

func (c *continuityFixedClock) Now() time.Time { return c.now }

func newContinuityReceiptHandle() *continuityReceiptHandle {
	return &continuityReceiptHandle{started: make(chan struct{}), release: make(chan struct{})}
}
func (h *continuityReceiptHandle) captureContext(ctx context.Context) {
	if h.operationCtx != nil {
		h.operationCtx <- ctx
	}
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
func (h *continuityReceiptHandle) CredentialContinuity() bool     { return h.continuity }
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
func (h *continuityReceiptHandle) RefreshGrantedAuthorizationCatalogue(context.Context, session.ExternalAuthorization) ([]tool.Tool, error) {
	return h.Tools(), nil
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
func (h *continuityReceiptHandle) StageCredentialCustody(ctx context.Context, _ string, _ mcpbroker.ContinuityGuard, _ mcpbroker.WorkspaceEnrollmentRef, _ time.Time) (mcpbroker.StagedCredentialCustody, error) {
	h.captureContext(ctx)
	h.call()
	var digest [32]byte
	digest[0] = 1
	return mcpbroker.StagedCredentialCustody{RecoveryReference: receiptRecoveryReference, ExpiresAt: time.Now().Add(time.Minute), ProfileDigest: digest, Providers: []string{"provider"}}, nil
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
	if _, err := server.StageCredentialCustody(ctx, fourth); !isCapacityReason(err) {
		t.Fatalf("overflow continuity receipt = %v, want structured capacity rejection", err)
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

func TestRecoverCredentialAttachmentMaxHandlesReturnsCapacityReason(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	server.cfg.MaxHandles = 0
	close(service.recover.release)
	request := &brokerv1.RecoverCredentialAttachmentRequest{
		RequestId: "recover-max-handles",
		Assertion: custodyAssertion(guard, time.Now().Add(time.Minute)),
	}
	if _, err := server.RecoverCredentialAttachment(ctx, request); !isCapacityReason(err) {
		t.Fatalf("Recover at MaxHandles = %v, want structured capacity reason", err)
	}
	if service.recover.count() != 1 || service.recover.abortCount() != 1 {
		t.Fatalf("recover calls/aborts = %d/%d, want one recovery and cleanup", service.recover.count(), service.recover.abortCount())
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
	if _, _, err := server.reserveContinuityReceipt(continuityReceiptKey{operation: "stage/v1", request: "overflow"}, continuityDigest([]byte("overflow")), expiresAt, stageReceiptReservationBytes); !isCapacityReason(err) {
		t.Fatalf("ninth concurrent reservation = %v, want structured capacity reason", err)
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
	if _, err := server.RecoverCredentialAttachment(ctx, overflow); !isCapacityReason(err) {
		t.Fatalf("slot overflow = %v, want structured capacity reason", err)
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
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(service.recover.release) }) }
	defer func() { release(); _ = server.Shutdown(context.Background()) }()
	service.recover.operationCtx = make(chan context.Context, 1)
	assertion := custodyAssertion(guard, time.Now().Add(100*time.Millisecond))
	request := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-expired-before-register", Assertion: assertion}
	result := make(chan error, 1)
	go func() {
		_, err := server.RecoverCredentialAttachment(ctx, request)
		result <- err
	}()
	operationCtx := <-service.recover.operationCtx
	<-service.recover.started
	select {
	case <-operationCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("recovery operation did not stop at the attempt deadline")
	}
	release()
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

func TestBrokerCredentialContinuity_IncarnationBoundaries(t *testing.T) {
	t.Run("stage rejects stale or absent incarnation before custody work", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			incarnation string
		}{
			{name: "stale", incarnation: "stale-incarnation"},
			{name: "absent", incarnation: ""},
		} {
			t.Run(tc.name, func(t *testing.T) {
				server, ctx, guard, service := newContinuityReceiptServer(t)
				defer func() { _ = server.Shutdown(context.Background()) }()
				close(service.stage.release)
				req := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
				req.BrokerIncarnation = tc.incarnation
				if _, err := server.StageCredentialCustody(ctx, req); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("stage with %s incarnation = %v, want FailedPrecondition", tc.name, err)
				}
				if calls := service.stage.count(); calls != 0 {
					t.Fatalf("stage reached custody %d times, want none", calls)
				}
				server.mu.Lock()
				receipts, slots, bytes := len(server.continuityReceipts), server.continuityReceiptSlots, server.continuityReceiptBytes
				server.mu.Unlock()
				if receipts != 0 || slots != 0 || bytes != 0 {
					t.Fatalf("incarnation rejection retained %d receipts/%d slots/%d bytes", receipts, slots, bytes)
				}
			})
		}
	})

	for _, operation := range []string{"commit", "tombstone", "recover"} {
		t.Run(operation+" accepts replacement incarnation", func(t *testing.T) {
			originalServer, ctx, guard, service := newContinuityReceiptServer(t)
			defer func() { _ = originalServer.Shutdown(context.Background()) }()
			replacementServer, err := NewServer(service, DefaultConfig())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = replacementServer.Shutdown(context.Background()) }()
			if originalServer.instanceID == replacementServer.instanceID {
				t.Fatal("replacement server reused the original incarnation")
			}
			server := replacementServer
			replacementIncarnation := server.instanceID
			assertion := custodyAssertion(guard, time.Now().Add(time.Minute))

			switch operation {
			case "commit":
				if _, err := server.CommitCredentialCustody(ctx, &brokerv1.CommitCredentialCustodyRequest{Assertion: assertion}); err != nil {
					t.Fatalf("commit under replacement incarnation: %v", err)
				}
				service.mu.Lock()
				calls := service.commits
				service.mu.Unlock()
				if calls != 1 {
					t.Fatalf("commit custody calls = %d, want 1", calls)
				}
			case "tombstone":
				if _, err := server.TombstoneCredentialCustody(ctx, &brokerv1.TombstoneCredentialCustodyRequest{Assertion: assertion}); err != nil {
					t.Fatalf("tombstone under replacement incarnation: %v", err)
				}
				service.mu.Lock()
				calls := service.tombs
				service.mu.Unlock()
				if calls != 1 {
					t.Fatalf("tombstone custody calls = %d, want 1", calls)
				}
			case "recover":
				close(service.recover.release)
				response, err := server.RecoverCredentialAttachment(ctx, &brokerv1.RecoverCredentialAttachmentRequest{
					RequestId: "replacement-recover",
					Assertion: assertion,
				})
				if err != nil {
					t.Fatalf("recover under replacement incarnation: %v", err)
				}
				if response.GetBrokerIncarnation() != replacementIncarnation {
					t.Fatalf("recovered broker incarnation = %q, want replacement %q", response.GetBrokerIncarnation(), replacementIncarnation)
				}
				if calls := service.recover.count(); calls != 1 {
					t.Fatalf("recover custody calls = %d, want 1", calls)
				}
			}
		})
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
	if _, err := server.StageCredentialCustody(ctx, changed); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("changed Stage request = %v, want InvalidArgument", err)
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
	if _, err := server.RecoverCredentialAttachment(ctx, changed); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("changed Recover request = %v, want InvalidArgument", err)
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
	if _, err := server.StageCredentialCustody(ctx, changed); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("changed stage request = %v, want InvalidArgument", err)
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
	if _, err := server.RecoverCredentialAttachment(ctx, changed); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("changed recover request = %v, want InvalidArgument", err)
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

func TestContinuityRPCDeadlineValidationUsesServerClock(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	clock := continuityFixedClock{now: time.Now().Add(30 * time.Second)}
	server.WithClock(&clock)
	deadline := clock.Now().Add(-time.Second)
	stageReq := stageReceiptRequest(server, guard, deadline)
	assertion := custodyAssertion(guard, deadline)

	for _, tc := range []struct {
		name string
		call func() error
	}{
		{name: "stage", call: func() error { _, err := server.StageCredentialCustody(ctx, stageReq); return err }},
		{name: "commit", call: func() error {
			_, err := server.CommitCredentialCustody(ctx, &brokerv1.CommitCredentialCustodyRequest{Assertion: assertion})
			return err
		}},
		{name: "recover", call: func() error {
			_, err := server.RecoverCredentialAttachment(ctx, &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "expired-recover", Assertion: assertion})
			return err
		}},
		{name: "tombstone", call: func() error {
			_, err := server.TombstoneCredentialCustody(ctx, &brokerv1.TombstoneCredentialCustodyRequest{Assertion: assertion})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); status.Code(err) != codes.InvalidArgument {
				t.Fatalf("expired attempt = %v, want InvalidArgument", err)
			}
		})
	}
	if service.stage.count() != 0 || service.recover.count() != 0 {
		t.Fatalf("expired assertions reached custody: stage=%d recover=%d", service.stage.count(), service.recover.count())
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.commits != 0 || service.tombs != 0 {
		t.Fatalf("expired assertions reached custody: commits=%d tombstones=%d", service.commits, service.tombs)
	}
}

func TestContinuityReceiptExpiryUsesServerClock(t *testing.T) {
	server, _, _, _ := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	clock := continuityFixedClock{now: time.Now().Add(30 * time.Second)}
	server.WithClock(&clock)
	key := continuityReceiptKey{operation: "stage/v1", request: "clock-expiry"}
	digest := continuityDigest([]byte("original"))
	expiresAt := clock.Now().Add(time.Minute)
	receipt, leader, err := server.reserveContinuityReceipt(key, digest, expiresAt, stageReceiptReservationBytes)
	if err != nil || !leader {
		t.Fatalf("initial receipt reservation = leader:%t err:%v", leader, err)
	}

	clock.now = expiresAt.Add(time.Second)
	server.mu.Lock()
	server.sweepContinuityReceiptsLocked(clock.Now())
	server.mu.Unlock()
	server.finishContinuityReceipt(key, receipt, nil, nil, continuityUnavailable(), expiresAt)
	server.mu.Lock()
	_, retained := server.continuityReceipts[key]
	server.mu.Unlock()
	if retained {
		t.Fatal("expired receipt remained after settlement using the injected clock")
	}

	clock.now = time.Now().Add(30 * time.Second)
	expiresAt = clock.Now().Add(time.Minute)
	_, leader, err = server.reserveContinuityReceipt(key, digest, expiresAt, stageReceiptReservationBytes)
	if err != nil || !leader {
		t.Fatalf("receipt re-reservation = leader:%t err:%v", leader, err)
	}
	clock.now = expiresAt.Add(time.Second)
	_, leader, err = server.reserveContinuityReceipt(key, continuityDigest([]byte("changed")), expiresAt.Add(time.Minute), stageReceiptReservationBytes)
	if err != nil || !leader {
		t.Fatalf("changed request after expiry = leader:%t err:%v, want a fresh reservation", leader, err)
	}
}

func TestRecoveredHandleIdleExpiryUsesServerClock(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	clock := continuityFixedClock{now: time.Now().Add(30 * time.Second)}
	server.WithClock(&clock)
	close(service.recover.release)
	request := &brokerv1.RecoverCredentialAttachmentRequest{
		RequestId: "recover-clock-expiry",
		Assertion: custodyAssertion(guard, clock.Now().Add(time.Minute)),
	}
	response, err := server.RecoverCredentialAttachment(ctx, request)
	if err != nil {
		t.Fatalf("recover attachment: %v", err)
	}
	server.mu.Lock()
	expiresAt := server.handles[response.GetHandle()].expiresAt
	server.mu.Unlock()
	if want := clock.Now().Add(server.cfg.HandleIdleTimeout); !expiresAt.Equal(want) {
		t.Fatalf("recovered handle expires at %v, want %v from server clock", expiresAt, want)
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

func TestRecoverCredentialAttachmentCapacityErrorIsStructured(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	defer func() { _ = server.Shutdown(context.Background()) }()
	service.recoverErr = mcpbroker.ErrCapacity
	close(service.recover.release)
	request := &brokerv1.RecoverCredentialAttachmentRequest{
		RequestId: "recover-capacity",
		Assertion: custodyAssertion(guard, time.Now().Add(time.Minute)),
	}
	if _, err := server.RecoverCredentialAttachment(ctx, request); !isCapacityReason(err) {
		t.Fatalf("Recover capacity error = %v, want CAPACITY_REACHED with empty dispatch method", err)
	}
}

func TestContinuityClientRejectsMalformedCapacityReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		code   codes.Code
		method string
		valid  bool
	}{
		{name: "expected tuple", code: codes.ResourceExhausted, valid: true},
		{name: "wrong status", code: codes.FailedPrecondition},
		{name: "execute dispatch method", code: codes.ResourceExhausted, method: executeMethod},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := status.New(tc.code, "capacity").WithDetails(&brokerv1.BrokerErrorDetail{
				Reason:         brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED,
				DispatchMethod: tc.method,
			})
			if err != nil {
				t.Fatal(err)
			}
			mapped := continuityClientError(response.Err())
			if tc.valid {
				if mapped != mcpbroker.ErrCapacity {
					t.Fatalf("continuity capacity mapping = %v, want ErrCapacity", mapped)
				}
			} else if mapped == nil || mapped == mcpbroker.ErrCapacity {
				t.Fatalf("malformed continuity capacity mapping = %v, want protocol error", mapped)
			}
		})
	}
}

func TestStructuredCapacityAndRevocationReasonsRoundTrip(t *testing.T) {
	capacity := brokerStatus(mcpbroker.ErrCapacity)
	if !isCapacityReason(capacity) {
		t.Fatalf("capacity status = %v, want CAPACITY_REACHED with empty dispatch method", capacity)
	}
	revoked := brokerStatus(mcpbroker.ErrContinuityRevoked)
	if status.Code(revoked) != codes.FailedPrecondition {
		t.Fatalf("revocation status code = %s, want FailedPrecondition", status.Code(revoked))
	}
	reason, method, ok, err := brokerReason(revoked)
	if err != nil || !ok || method != "" || reason != brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CONTINUITY_REVOKED {
		t.Fatalf("revocation detail = %v/%q/%t/%v", reason, method, ok, err)
	}
	if mapped := clientError(revoked); mapped != mcpbroker.ErrContinuityRevoked {
		t.Fatalf("client revocation mapping = %v, want ErrContinuityRevoked", mapped)
	}
}

func isCapacityReason(err error) bool {
	reason, method, ok, protocolErr := brokerReason(err)
	return status.Code(err) == codes.ResourceExhausted && protocolErr == nil && ok &&
		reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED && method == ""
}

func TestRecoveredAttachmentReportsRealCredentialContinuity(t *testing.T) {
	for _, tc := range []struct {
		name  string
		offer bool
	}{
		{name: "offered", offer: true},
		{name: "not offered", offer: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, ctx, guard, service := newContinuityReceiptServer(t)
			defer func() { _ = server.Shutdown(context.Background()) }()
			service.recover.continuity = tc.offer
			close(service.recover.release)

			client := NewClient(continuityRecoveryConn{server: server, serverContext: ctx})
			recovered, err := client.RecoverCredentialAttachment(ctx, mcpbroker.CustodyAssertion{
				Guard: guard, RecoveryReference: receiptRecoveryReference, AttemptDeadline: time.Now().Add(time.Minute),
			}, "recover-continuity-offer")
			if err != nil {
				t.Fatalf("RecoverCredentialAttachment: %v", err)
			}
			advertiser, ok := recovered.Attachment.(mcpbroker.CredentialContinuityAdvertiser)
			if !ok || advertiser.CredentialContinuity() != tc.offer {
				t.Fatalf("recovered client continuity = %v, want %v", ok && advertiser.CredentialContinuity(), tc.offer)
			}
		})
	}
}

type continuityRecoveryConn struct {
	server        *Server
	serverContext context.Context
}

func (c continuityRecoveryConn) Invoke(_ context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	if method != "/mecatl.broker.v1.BrokerService/RecoverCredentialAttachment" {
		return errors.New("unexpected RPC method")
	}
	request, ok := args.(*brokerv1.RecoverCredentialAttachmentRequest)
	if !ok {
		return errors.New("unexpected RecoverCredentialAttachment request type")
	}
	response, err := c.server.RecoverCredentialAttachment(c.serverContext, request)
	if err != nil {
		return err
	}
	encoded, err := proto.Marshal(response)
	if err != nil {
		return err
	}
	return proto.Unmarshal(encoded, reply.(proto.Message))
}

func (continuityRecoveryConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
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
	return &brokerv1.StageCredentialCustodyRequest{RequestId: "stage-retry", BrokerIncarnation: server.instanceID, Handle: "stage-handle", Guard: stageGuardToWire(guard), CompletedEnrollment: &brokerv1.WorkspaceRef{Id: "enrollment", RequiredServices: 1, ExpiresAt: timestamppb.New(deadline)}, AttemptDeadline: timestamppb.New(deadline)}
}
func custodyAssertion(guard mcpbroker.ContinuityGuard, deadline time.Time) *brokerv1.CustodyAssertion {
	return &brokerv1.CustodyAssertion{Guard: continuityGuardToWire(guard), RecoveryReference: receiptRecoveryReference, AttemptDeadline: timestamppb.New(deadline)}
}
func protoCloneStage(in *brokerv1.StageCredentialCustodyRequest) *brokerv1.StageCredentialCustodyRequest {
	return proto.Clone(in).(*brokerv1.StageCredentialCustodyRequest)
}

func TestBrokerCredentialContinuity_LeaderCancellationDoesNotPoisonReceipt(t *testing.T) {
	t.Run("stage", func(t *testing.T) {
		server, ctx, guard, service := newContinuityReceiptServer(t)
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(service.stage.release) }) }
		defer func() { release(); _ = server.Shutdown(context.Background()) }()
		service.stage.operationCtx = make(chan context.Context, 1)
		request := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
		leaderCtx, cancelLeader := context.WithCancel(ctx)
		defer cancelLeader()
		type result struct {
			response *brokerv1.StageCredentialCustodyResponse
			err      error
		}
		leaderResult := make(chan result, 1)
		go func() {
			response, err := server.StageCredentialCustody(leaderCtx, request)
			leaderResult <- result{response: response, err: err}
		}()
		operationCtx := <-service.stage.operationCtx
		<-service.stage.started

		waiterStarted := make(chan struct{})
		waiterResult := make(chan result, 1)
		go func() {
			close(waiterStarted)
			response, err := server.StageCredentialCustody(ctx, request)
			waiterResult <- result{response: response, err: err}
		}()
		<-waiterStarted
		cancelLeader()
		<-leaderCtx.Done()
		if err := operationCtx.Err(); err != nil {
			t.Fatalf("leader cancellation reached staged operation: %v", err)
		}
		release()
		for name, result := range map[string]<-chan result{"leader": leaderResult, "waiter": waiterResult} {
			got := <-result
			if got.err != nil || got.response.GetRecoveryReference() != receiptRecoveryReference {
				t.Fatalf("%s stage result = (%#v, %v), want retained custody result", name, got.response, got.err)
			}
		}
		replayed, err := server.StageCredentialCustody(ctx, request)
		if err != nil || replayed.GetRecoveryReference() != receiptRecoveryReference || service.stage.count() != 1 {
			t.Fatalf("stage retry = (%#v, %v), dispatches %d; want same single result", replayed, err, service.stage.count())
		}
	})

	t.Run("recover", func(t *testing.T) {
		server, ctx, guard, service := newContinuityReceiptServer(t)
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(service.recover.release) }) }
		defer func() { release(); _ = server.Shutdown(context.Background()) }()
		service.recover.operationCtx = make(chan context.Context, 1)
		request := &brokerv1.RecoverCredentialAttachmentRequest{RequestId: "recover-cancel", Assertion: custodyAssertion(guard, time.Now().Add(time.Minute))}
		leaderCtx, cancelLeader := context.WithCancel(ctx)
		defer cancelLeader()
		type result struct {
			response *brokerv1.RecoverCredentialAttachmentResponse
			err      error
		}
		leaderResult := make(chan result, 1)
		go func() {
			response, err := server.RecoverCredentialAttachment(leaderCtx, request)
			leaderResult <- result{response: response, err: err}
		}()
		operationCtx := <-service.recover.operationCtx
		<-service.recover.started

		waiterStarted := make(chan struct{})
		waiterResult := make(chan result, 1)
		go func() {
			close(waiterStarted)
			response, err := server.RecoverCredentialAttachment(ctx, request)
			waiterResult <- result{response: response, err: err}
		}()
		<-waiterStarted
		cancelLeader()
		<-leaderCtx.Done()
		if err := operationCtx.Err(); err != nil {
			t.Fatalf("leader cancellation reached recovered operation: %v", err)
		}
		release()
		var handle string
		for name, result := range map[string]<-chan result{"leader": leaderResult, "waiter": waiterResult} {
			got := <-result
			if got.err != nil || got.response.GetHandle() == "" {
				t.Fatalf("%s recover result = (%#v, %v), want published attachment", name, got.response, got.err)
			}
			if handle != "" && got.response.GetHandle() != handle {
				t.Fatalf("%s recover handle = %q, want replayed handle %q", name, got.response.GetHandle(), handle)
			}
			handle = got.response.GetHandle()
		}
		replayed, err := server.RecoverCredentialAttachment(ctx, request)
		if err != nil || replayed.GetHandle() != handle || service.recover.count() != 1 {
			t.Fatalf("recover retry = (%#v, %v), dispatches %d; want same single handle", replayed, err, service.recover.count())
		}
	})
}

func TestBrokerCredentialContinuity_ShutdownCancelsAndJoinsLeader(t *testing.T) {
	server, ctx, guard, service := newContinuityReceiptServer(t)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(service.stage.release) }) }
	defer func() { release(); _ = server.Shutdown(context.Background()) }()
	service.stage.operationCtx = make(chan context.Context, 1)
	request := stageReceiptRequest(server, guard, time.Now().Add(time.Minute))
	stageResult := make(chan error, 1)
	go func() {
		_, err := server.StageCredentialCustody(ctx, request)
		stageResult <- err
	}()
	operationCtx := <-service.stage.operationCtx
	<-service.stage.started

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShutdown()
	shutdownResult := make(chan error, 1)
	go func() { shutdownResult <- server.Shutdown(shutdownCtx) }()
	select {
	case <-operationCtx.Done():
	case <-shutdownCtx.Done():
		t.Fatal("server shutdown did not cancel the continuity operation")
	}
	select {
	case err := <-shutdownResult:
		t.Fatalf("shutdown returned before the in-flight leader finished: %v", err)
	default:
	}
	release()
	if err := <-stageResult; status.Code(err) != codes.Canceled {
		t.Fatalf("stage during shutdown = %v, want canceled", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}
