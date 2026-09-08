package mcpbrokergrpc_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	brokerv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/mcpbroker"
)

const testWait = time.Second

func TestADR_0304_RemoteAttachmentLifecycle(t *testing.T) {
	local := newFailureBroker()
	cfg := shortConfig()
	cfg.HandleIdleTimeout = 15 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "ledger"})
	if err != nil {
		t.Fatal(err)
	}
	closed := local.closedFor(session.ExternalBinding(attached.GetBinding()))
	select {
	case <-closed:
	case <-time.After(testWait):
		t.Fatal("process-owned idle attachment was not reclaimed")
	}
	_, err = server.Commit(t.Context(), &brokerv1.CommitRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation()})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("reclaimed process handle remained usable: %v", err)
	}
}

func TestInitialProductionMCPBroker_Scenario4_TransientReconnect(t *testing.T) {
	cfg := mcpbrokergrpc.DefaultConfig()
	for name, d := range map[string]time.Duration{
		"dial": cfg.DialTimeout, "rpc": cfg.RPCDeadline, "execute": cfg.ExecuteDeadline,
		"idle": cfg.HandleIdleTimeout, "sweep": cfg.SweepInterval, "cleanup": cfg.CleanupTimeout,
	} {
		if d <= 0 {
			t.Fatalf("%s deadline = %v, want positive finite value", name, d)
		}
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cfg.DialTimeout = 20 * time.Millisecond
	if _, _, err := mcpbrokergrpc.Dial(t.Context(), address, cfg, grpc.WithTransportCredentials(insecure.NewCredentials())); status.Code(err) != codes.Unavailable || errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("Dial before broker = %v, want transport Unavailable distinct from confirmed state loss", err)
	}

	local := newFailureBroker()
	brokerServer, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = brokerServer.Shutdown(context.Background()) })
	listener, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	mcpbrokergrpc.RegisterServer(server, brokerServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })

	client, conn, err := mcpbrokergrpc.Dial(t.Context(), address, cfg, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("fresh Dial after transient failure: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	first, _, err := client.AttachSession(t.Context(), "reconnect")
	if err != nil {
		t.Fatal(err)
	}
	second, outcome, err := client.AttachSession(t.Context(), "reconnect")
	if err != nil || outcome != mcpbroker.AttachReattached || first.Binding() != second.Binding() {
		t.Fatalf("same-incarnation reconnect = (%q, %q, %v), want same binding/reattached", first.Binding(), outcome, err)
	}
}

func TestSingletonBrokerRemediation_Scenario2_ProtectedContinuationNeverRebinds(t *testing.T) {
	oldBroker := newFailureBroker()
	oldConn, oldStop := failureBufServer(t, oldBroker, nil)
	defer oldStop()
	freshBroker := newFailureBroker()
	freshConn, freshStop := failureBufServer(t, freshBroker, nil)
	defer freshStop()

	switcher := &switchConn{current: oldConn}
	client, err := mcpbrokergrpc.NewClientWithConfig(switcher, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	attachment, _, err := client.AttachSession(t.Context(), "parked-continuation")
	if err != nil {
		t.Fatal(err)
	}
	binding := attachment.Binding()
	switcher.set(freshConn)
	protected := attachment.Tools()[1].(tool.AuthorizationRequester)
	if _, _, err := protected.RequestAuthorization(t.Context(), session.NewToolCall("parked", "protected", []byte(`{}`))); !errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("protected continuation after replacement = %v, want state unavailable", err)
	}
	if attachment.Binding() != binding || freshBroker.authCalls.Load() != 0 || freshBroker.executeCalls.Load() != 0 {
		t.Fatalf("parked continuation rebound on replacement: binding=%q auth=%d execute=%d", attachment.Binding(), freshBroker.authCalls.Load(), freshBroker.executeCalls.Load())
	}
}

func TestRemoteBrokerTransport_RestartBoundaryStateLoss(t *testing.T) {
	oldBroker := newFailureBroker()
	oldConn, oldStop := failureBufServer(t, oldBroker, nil)
	defer oldStop()
	newBroker := newFailureBroker()
	newConn, newStop := failureBufServer(t, newBroker, nil)
	defer newStop()

	switcher := &switchConn{current: oldConn}
	client, err := mcpbrokergrpc.NewClientWithConfig(switcher, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	attached, _, err := client.AttachSession(t.Context(), "pending")
	if err != nil {
		t.Fatal(err)
	}
	binding := attached.Binding()
	switcher.set(newConn)
	protected := attached.Tools()[1].(tool.AuthorizationRequester)
	call := session.NewToolCall("pending-call", "protected", []byte(`{}`))
	if _, _, err := protected.RequestAuthorization(t.Context(), call); !errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("authorization after restart = %v, want confirmed state loss", err)
	}
	if attached.Binding() != binding || newBroker.authCalls.Load() != 0 || newBroker.executeCalls.Load() != 0 {
		t.Fatalf("restart rebound or dispatched: binding=%q auth=%d execute=%d", attached.Binding(), newBroker.authCalls.Load(), newBroker.executeCalls.Load())
	}
	// A replacement client may enroll only before a call is parked; it is a
	// different attachment on the replacement incarnation, never a rebind of
	// the interrupted continuation above.
	freshClient, err := mcpbrokergrpc.NewClientWithConfig(newConn, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	freshAttachment, _, err := freshClient.AttachSession(t.Context(), "pre-prompt")
	if err != nil || freshAttachment.Binding() == binding {
		t.Fatalf("fresh pre-prompt enrollment = (%v, %v), want replacement attachment", freshAttachment, err)
	}
}

func TestSingletonBrokerRemediation_Scenario1_StaleIncarnationRejectedAcrossSurface(t *testing.T) {
	oldConn, oldStop := failureBufServer(t, newFailureBroker(), nil)
	defer oldStop()
	fresh := newFailureBroker()
	newConn, newStop := failureBufServer(t, fresh, nil)
	defer newStop()
	switcher := &switchConn{current: oldConn}
	client, err := mcpbrokergrpc.NewClientWithConfig(switcher, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	attached, _, err := client.AttachSession(t.Context(), "stale")
	if err != nil {
		t.Fatal(err)
	}
	binding := attached.Binding()
	auth := session.ExternalAuthorization{ID: "auth", Binding: "auth-binding", ExpiresAt: time.Now().Add(time.Hour)}
	ref := mcpbroker.WorkspaceEnrollmentRef{ID: "enrollment", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	switcher.set(newConn)

	assertStateLoss := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, mcpbroker.ErrStateUnavailable) {
			t.Errorf("%s stale operation = %v, want state unavailable", name, err)
		}
	}
	_, _, err = client.AttachSession(t.Context(), "stale-attach")
	assertStateLoss("attach", err)
	assertStateLoss("commit", attached.Commit(t.Context()))
	assertStateLoss("abort", attached.Abort(t.Context()))
	_, err = attached.Close(t.Context())
	assertStateLoss("close", err)
	protected := attached.Tools()[1].(tool.AuthorizationRequester)
	_, _, err = protected.RequestAuthorization(t.Context(), session.NewToolCall("auth-call", "protected", []byte(`{}`)))
	assertStateLoss("request authorization", err)
	assertStateLoss("abort authorization", protected.AbortAuthorization(t.Context(), auth))
	result, err := attached.Tools()[0].Execute(t.Context(), session.NewToolCall("call", "read", []byte(`{}`)), tool.Environment{})
	if err != nil || !result.IsError || result.Content != "tool temporarily unavailable" {
		t.Errorf("execute stale operation = %#v, %v, want model-visible temporary failure", result, err)
	}
	_, err = attached.PresentAuthorization(t.Context(), auth)
	assertStateLoss("present", err)
	_, err = attached.AuthorizationStatus(t.Context(), auth)
	assertStateLoss("observe authorization", err)
	_, err = attached.CancelAuthorization(t.Context(), auth)
	assertStateLoss("cancel authorization", err)
	enroller, ok := attached.(mcpbroker.WorkspaceEnrollmentAttachment)
	if !ok {
		t.Fatal("remote attachment does not preserve enrollment capability")
	}
	_, err = enroller.BeginWorkspaceEnrollment(t.Context())
	assertStateLoss("begin enrollment", err)
	_, err = enroller.ObserveWorkspaceEnrollment(t.Context(), ref)
	assertStateLoss("observe enrollment", err)
	_, err = enroller.CancelWorkspaceEnrollment(t.Context(), ref)
	assertStateLoss("cancel enrollment", err)
	_, err = client.DeleteSessionIfBinding(t.Context(), "stale", binding)
	assertStateLoss("delete", err)
	if fresh.operations() != 0 {
		t.Fatalf("fresh broker observed %d stale operations", fresh.operations())
	}
}

func TestInitialProductionMCPBroker_ServerRunDeadlineBoundsBackgroundAndHonorsEarlierCallerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name           string
		serverDeadline time.Duration
		callerDeadline time.Duration
		maximumElapsed time.Duration
	}{
		{name: "server", serverDeadline: 40 * time.Millisecond, maximumElapsed: 500 * time.Millisecond},
		{name: "caller", serverDeadline: 200 * time.Millisecond, callerDeadline: 15 * time.Millisecond, maximumElapsed: 100 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := newFailureBroker()
			local.blockExecute = make(chan struct{})
			cfg := shortConfig()
			cfg.ExecuteDeadline = tc.serverDeadline
			server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

			attached, err := server.Attach(context.Background(), &brokerv1.AttachRequest{SessionId: "bounded"})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tc.callerDeadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.callerDeadline)
				defer cancel()
			}
			started := time.Now()
			_, err = server.Execute(ctx, &brokerv1.ExecuteRequest{
				BrokerIncarnation: attached.GetBrokerIncarnation(), Handle: attached.GetHandle(),
				Name: "read", CallId: "blocked", Args: []byte(`{}`),
			})
			if status.Code(err) != codes.DeadlineExceeded {
				t.Fatalf("Run() error = %v, want DeadlineExceeded", err)
			}
			if elapsed := time.Since(started); elapsed > tc.maximumElapsed {
				t.Fatalf("Run() elapsed = %v, want no more than %v", elapsed, tc.maximumElapsed)
			}
			select {
			case <-local.executeExited:
			case <-time.After(testWait):
				t.Fatal("bounded Run did not cancel the tool execution")
			}
		})
	}
}

func TestSingletonBrokerRemediation_Scenario1_ConcurrentCancellationAndLifecycleIsolation(t *testing.T) {
	local := newFailureBroker()
	local.blockExecute = make(chan struct{})
	cfg := shortConfig()
	cfg.HandleIdleTimeout = 200 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	cfg.CleanupTimeout = 100 * time.Millisecond
	conn, stop := failureBufServer(t, local, nil, cfg)
	defer stop()
	client, err := mcpbrokergrpc.NewClientWithConfig(conn, cfg)
	if err != nil {
		t.Fatal(err)
	}
	attached, _, err := client.AttachSession(t.Context(), "cancel-one")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	result, err := attached.Tools()[0].Execute(ctx, session.NewToolCall("blocked", "read", []byte(`{}`)), tool.Environment{})
	if err != nil || !result.IsError || !strings.Contains(result.Content, "outcome is unknown") {
		t.Fatalf("cancelled Execute = %#v, %v, want model-visible ambiguous outcome", result, err)
	}
	retryDone := make(chan error, 1)
	go func() {
		retry, retryErr := attached.Tools()[0].Execute(t.Context(), session.NewToolCall("blocked", "read", []byte(`{}`)), tool.Environment{})
		if retryErr == nil && (retry.IsError || retry.Content != "ok") {
			retryErr = fmt.Errorf("retry receipt = %#v", retry)
		}
		retryDone <- retryErr
	}()
	closeDone := make(chan error, 1)
	go func() {
		outcome, closeErr := attached.Close(t.Context())
		if closeErr == nil && outcome != mcpbroker.CloseClosed {
			closeErr = fmt.Errorf("close outcome = %q", outcome)
		}
		closeDone <- closeErr
	}()
	time.Sleep(10 * time.Millisecond)
	if got := local.closeCalls.Load(); got != 0 {
		t.Fatalf("lifecycle raced active Execute: close calls = %d", got)
	}
	close(local.blockExecute)
	if err := <-retryDone; err != nil {
		t.Fatalf("duplicate receipt after caller cancellation: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatalf("lifecycle after Execute: %v", err)
	}

	orphan, _, err := client.AttachSession(t.Context(), "orphan")
	if err != nil {
		t.Fatal(err)
	}
	orphanClosed := local.closedFor(orphan.Binding())
	select {
	case <-orphanClosed:
	case <-time.After(testWait):
		t.Fatal("orphan handle was not reclaimed within configured deadline")
	}
	if got := local.executeCalls.Load(); got != 1 {
		t.Fatalf("cancelled Execute dispatched %d times, want one isolated owner", got)
	}

	shutdownBroker := newFailureBroker()
	shutdownBroker.blockExecute = make(chan struct{})
	shutdownServer, err := mcpbrokergrpc.NewServerWithConfig(shutdownBroker, cfg)
	if err != nil {
		t.Fatal(err)
	}
	shutdownAttached, err := shutdownServer.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "shutdown-race"})
	if err != nil {
		t.Fatal(err)
	}
	executeDone := make(chan error, 1)
	go func() {
		_, executeErr := shutdownServer.Execute(context.Background(), &brokerv1.ExecuteRequest{Handle: shutdownAttached.GetHandle(), BrokerIncarnation: shutdownAttached.GetBrokerIncarnation(), Name: "read", CallId: "shutdown-call", Args: []byte(`{}`)})
		executeDone <- executeErr
	}()
	select {
	case <-time.After(testWait):
		t.Fatal("shutdown-race Execute did not start")
	case <-func() <-chan struct{} {
		started := make(chan struct{})
		go func() {
			for shutdownBroker.executeCalls.Load() == 0 {
				time.Sleep(time.Millisecond)
			}
			close(started)
		}()
		return started
	}():
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(t.Context(), testWait)
	defer shutdownCancel()
	if err := shutdownServer.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown during Execute: %v", err)
	}
	if err := <-executeDone; status.Code(err) != codes.Canceled {
		t.Fatalf("shutdown-owned Execute = %v, want cancelled terminal", err)
	}
}

func TestSingletonBrokerRemediation_Scenario1_LifecycleReceiptsAreAbsoluteAndReplayable(t *testing.T) {
	local := newFailureBroker()
	cfg := shortConfig()
	cfg.HandleIdleTimeout = 30 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	closed, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "close-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	closeReq := &brokerv1.CloseRequest{Handle: closed.GetHandle(), BrokerIncarnation: closed.GetBrokerIncarnation()}
	for i := 0; i < 2; i++ {
		response, closeErr := server.Close(t.Context(), closeReq)
		if closeErr != nil || response.GetOutcome() != string(mcpbroker.CloseClosed) {
			t.Fatalf("Close retry %d = %#v, %v, want original closed receipt", i, response, closeErr)
		}
	}
	if got := local.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", got)
	}
	if _, err := server.Abort(t.Context(), &brokerv1.AbortRequest{Handle: closeReq.GetHandle(), BrokerIncarnation: closeReq.GetBrokerIncarnation()}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("Abort after Close = %v, want state unavailable", err)
	}

	aborted, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "abort-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	abortReq := &brokerv1.AbortRequest{Handle: aborted.GetHandle(), BrokerIncarnation: aborted.GetBrokerIncarnation()}
	for i := 0; i < 2; i++ {
		if _, abortErr := server.Abort(t.Context(), abortReq); abortErr != nil {
			t.Fatalf("Abort retry %d: %v", i, abortErr)
		}
	}
	if got := local.abortCalls.Load(); got != 1 {
		t.Fatalf("underlying Abort calls = %d, want 1", got)
	}

	time.Sleep(4 * cfg.HandleIdleTimeout)
	if _, err = server.Close(t.Context(), closeReq); status.Code(err) != codes.FailedPrecondition || !hasBrokerReason(err, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_STATE_UNAVAILABLE) {
		t.Fatalf("Close after receipt expiry = %v, want structured state unavailable", err)
	}
	if got := local.closeCalls.Load(); got != 2 { // one Close plus the Abort implementation's local Close.
		t.Fatalf("terminal receipt was closed again during expiry: calls = %d, want 2", got)
	}
}

func TestInitialProductionMCPBroker_ConcurrentCloseRetriesShareOneReceipt(t *testing.T) {
	local := newFailureBroker()
	server, err := mcpbrokergrpc.NewServerWithConfig(local, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "concurrent-close"})
	if err != nil {
		t.Fatal(err)
	}
	req := &brokerv1.CloseRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation()}

	const callers = 16
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, closeErr := server.Close(t.Context(), req)
			if closeErr == nil && response.GetOutcome() != string(mcpbroker.CloseClosed) {
				closeErr = fmt.Errorf("outcome = %q, want closed", response.GetOutcome())
			}
			errs <- closeErr
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := local.closeCalls.Load(); got != 1 {
		t.Fatalf("underlying Close calls = %d, want 1", got)
	}
}

func TestInitialProductionMCPBroker_TerminalLifecycleReceiptErrorSemantics(t *testing.T) {
	t.Run("close terminal outcome survives returned error", func(t *testing.T) {
		local := newFailureBroker()
		local.closeOutcome = mcpbroker.CloseClosed
		local.closeErr = context.DeadlineExceeded
		server, err := mcpbrokergrpc.NewServerWithConfig(local, shortConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
		attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "close-error-receipt"})
		if err != nil {
			t.Fatal(err)
		}
		req := &brokerv1.CloseRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation()}
		if _, err := server.Close(t.Context(), req); status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("first Close = %v, want deadline", err)
		}
		local.closeErr = nil
		response, err := server.Close(t.Context(), req)
		if err != nil || response.GetOutcome() != string(mcpbroker.CloseClosed) {
			t.Fatalf("Close retry = %#v, %v, want retained closed receipt", response, err)
		}
		if got := local.closeCalls.Load(); got != 1 {
			t.Fatalf("underlying Close calls = %d, want 1", got)
		}
	})

	t.Run("failed abort remains retryable", func(t *testing.T) {
		local := newFailureBroker()
		local.abortErr = errors.New("abort failed")
		server, err := mcpbrokergrpc.NewServerWithConfig(local, shortConfig())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
		attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "abort-error-retry"})
		if err != nil {
			t.Fatal(err)
		}
		req := &brokerv1.AbortRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation()}
		if _, err := server.Abort(t.Context(), req); err == nil {
			t.Fatal("first Abort succeeded, want injected failure")
		}
		local.abortErr = nil
		if _, err := server.Abort(t.Context(), req); err != nil {
			t.Fatalf("Abort retry: %v", err)
		}
		if _, err := server.Abort(t.Context(), req); err != nil {
			t.Fatalf("Abort receipt retry: %v", err)
		}
		if got := local.abortCalls.Load(); got != 2 {
			t.Fatalf("underlying Abort calls = %d, want failed attempt plus one success", got)
		}
	})
}

func TestInvariant_initial_broker_makes_no_distributed_claims(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	paths := []string{
		filepath.Join(root, "internal", "adapter", "mcpbrokergrpc"),
		filepath.Join(root, "contracts", "proto", "mecatl", "broker"),
		filepath.Join(root, "deploy", "helm", "mecabroker"),
	}
	for _, base := range paths {
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() {
				return nil
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			lower := strings.ToLower(string(body))
			for _, claim := range []string{
				"replicas are " + "interchangeable",
				"callback failover " + "is supported",
				"restart" + "-durable",
				"exactly-once " + "external effects",
				"high availability " + "is provided",
			} {
				if strings.Contains(lower, claim) {
					t.Errorf("%s makes forbidden distributed claim %q", path, claim)
				}
			}
			return nil
		})
	}
}

func TestInitialProductionMCPBroker_Scenario4_AmbiguousToolResultIsNotReplayed(t *testing.T) {
	local := newFailureBroker()
	var lose atomic.Bool
	var runHandlers atomic.Int32
	lose.Store(true)
	interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if strings.HasSuffix(info.FullMethod, "/Execute") {
			runHandlers.Add(1)
		}
		response, err := handler(ctx, req)
		if strings.HasSuffix(info.FullMethod, "/Execute") && err == nil && lose.CompareAndSwap(true, false) {
			return nil, status.Error(codes.Aborted, "response lost after dispatch")
		}
		return response, err
	}
	conn, stop := failureBufServer(t, local, interceptor)
	defer stop()
	client, err := mcpbrokergrpc.NewClientWithConfig(conn, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	attached, _, err := client.AttachSession(t.Context(), "ambiguous")
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("call-1", "read", []byte(`{}`))
	result, err := attached.Tools()[0].Execute(t.Context(), call, tool.Environment{})
	if err != nil || !result.IsError || result.CallID != call.ID ||
		!strings.Contains(result.Content, "operation may already have completed") ||
		!strings.Contains(result.Content, "Do not automatically repeat it") ||
		!strings.Contains(result.Content, "Reconcile through a safe status/read path first") {
		t.Fatalf("lost response = %#v, %v, want actionable model-visible ambiguity", result, err)
	}
	if got, handlers := local.executeCalls.Load(), runHandlers.Load(); got != 1 || handlers != 1 {
		t.Fatalf("possibly dispatched Execute invoked proxy=%d handler=%d times, want 1 each", got, handlers)
	}
	second := session.NewToolCall("call-2", "read", []byte(`{}`))
	result, err = attached.Tools()[0].Execute(t.Context(), second, tool.Environment{})
	if err != nil || result.IsError || local.executeCalls.Load() != 2 || runHandlers.Load() != 2 {
		t.Fatalf("later ordinary action = %#v, %v, proxy=%d handlers=%d", result, err, local.executeCalls.Load(), runHandlers.Load())
	}
}

func TestInitialProductionMCPBroker_DefinitiveSessionLossIsModelVisible(t *testing.T) {
	local := newFailureBroker()
	conn, stop := failureBufServer(t, local, nil)
	defer stop()
	client, err := mcpbrokergrpc.NewClientWithConfig(conn, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	attached, _, err := client.AttachSession(t.Context(), "lost-session")
	if err != nil {
		t.Fatal(err)
	}
	remote := attached.Tools()[0]
	if _, err := attached.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("call-1", remote.Spec().Name, []byte(`{}`))
	result, err := remote.Execute(t.Context(), call, tool.Environment{})
	if err != nil || !result.IsError || result.CallID != call.ID || result.Content != "tool temporarily unavailable" {
		t.Fatalf("Execute after session loss = %#v, %v, want fixed model-visible temporary failure", result, err)
	}
	if got := local.executeCalls.Load(); got != 0 {
		t.Fatalf("underlying Execute calls = %d, want 0", got)
	}
}

func TestSingletonBrokerRemediation_Scenario1_DispatchClassificationIsConservative(t *testing.T) {
	for _, code := range []codes.Code{codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal} {
		t.Run(code.String(), func(t *testing.T) {
			local := newFailureBroker()
			interceptor := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				response, err := handler(ctx, req)
				if strings.HasSuffix(info.FullMethod, "/Execute") && err == nil {
					return nil, status.Error(code, "response lost after dispatch")
				}
				return response, err
			}
			conn, stop := failureBufServer(t, local, interceptor)
			defer stop()
			client, err := mcpbrokergrpc.NewClientWithConfig(conn, shortConfig())
			if err != nil {
				t.Fatal(err)
			}
			attached, _, err := client.AttachSession(t.Context(), session.SessionID("ambiguous-"+code.String()))
			if err != nil {
				t.Fatal(err)
			}
			call := session.NewToolCall("call-1", "read", []byte(`{}`))
			result, err := attached.Tools()[0].Execute(t.Context(), call, tool.Environment{})
			if err != nil || !result.IsError || result.CallID != call.ID || result.Content != "remote tool outcome is unknown because the broker response was lost; the operation may already have completed. Do not automatically repeat it. Reconcile through a safe status/read path first; if unavailable, report the uncertainty and seek operator direction." {
				t.Fatalf("Execute = %#v, %v, want fixed ambiguous result", result, err)
			}
			if got := local.executeCalls.Load(); got != 1 {
				t.Fatalf("underlying Execute calls = %d, want 1", got)
			}
		})
	}
	for _, test := range []struct {
		name     string
		method   string
		ordinary bool
	}{
		{name: "recognized exact method", method: brokerv1.BrokerService_Execute_FullMethodName, ordinary: true},
		{name: "wrong method is ambiguous", method: "/mecatl.broker.v1.BrokerService/Commit"},
		{name: "absent proof is ambiguous"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := mcpbrokergrpc.NewClient(dispatchProofConn{method: test.method})
			attached, _, err := client.AttachSession(t.Context(), "proof")
			if err != nil {
				t.Fatal(err)
			}
			call := session.NewToolCall("proof-call", "read", []byte(`{}`))
			result, err := attached.Tools()[0].Execute(t.Context(), call, tool.Environment{})
			if test.ordinary {
				if err == nil || result.CallID != "" {
					t.Fatalf("recognized proof = %#v, %v, want ordinary RPC error", result, err)
				}
			} else if err != nil || !result.IsError || result.Content != ambiguousOutcomeText {
				t.Fatalf("unrecognized proof = %#v, %v, want correlated ambiguity", result, err)
			}
		})
	}
	client := mcpbrokergrpc.NewClient(dispatchProofConn{method: brokerv1.BrokerService_Execute_FullMethodName, reason: brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED})
	attached, _, err := client.AttachSession(t.Context(), "capacity-proof")
	if err != nil {
		t.Fatal(err)
	}
	call := session.NewToolCall("capacity-call", "read", []byte(`{}`))
	result, err := attached.Tools()[0].Execute(t.Context(), call, tool.Environment{})
	if !errors.Is(err, mcpbroker.ErrCapacity) || result.CallID != "" {
		t.Fatalf("pre-dispatch capacity = %#v, %v, want ordinary capacity error", result, err)
	}
}

const ambiguousOutcomeText = "remote tool outcome is unknown because the broker response was lost; the operation may already have completed. Do not automatically repeat it. Reconcile through a safe status/read path first; if unavailable, report the uncertainty and seek operator direction."

type dispatchProofConn struct {
	method string
	reason brokerv1.BrokerErrorReason
}

func (c dispatchProofConn) Invoke(_ context.Context, method string, _, reply any, _ ...grpc.CallOption) error {
	if strings.HasSuffix(method, "/Attach") {
		*reply.(*brokerv1.AttachResponse) = brokerv1.AttachResponse{Binding: "binding", Handle: "handle", Outcome: string(mcpbroker.AttachCreated), BrokerIncarnation: "incarnation", Tools: []*brokerv1.ToolDescriptor{{Name: "read", Description: "read", Schema: []byte(`{"type":"object"}`)}}}
		return nil
	}
	st := status.New(codes.InvalidArgument, "rejected")
	if c.method == "" {
		return st.Err()
	}
	reason := c.reason
	if reason == brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_UNSPECIFIED {
		reason = brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_DISPATCH_NOT_STARTED
	}
	withDetail, err := st.WithDetails(&brokerv1.BrokerErrorDetail{Reason: reason, DispatchMethod: c.method})
	if err != nil {
		return err
	}
	return withDetail.Err()
}

func (dispatchProofConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("unexpected stream")
}

func TestSingletonBrokerRemediation_Scenario2_BoundedAdmissionAcrossBrokerRegistries(t *testing.T) {
	t.Run("attachment handles", func(t *testing.T) {
		cfg := shortConfig()
		cfg.MaxHandles = 1
		server, err := mcpbrokergrpc.NewServerWithConfig(newFailureBroker(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Shutdown(context.Background())
		if _, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "handle-one"}); err != nil {
			t.Fatal(err)
		}
		if _, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "handle-two"}); status.Code(err) != codes.ResourceExhausted || !hasBrokerReason(err, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED) {
			t.Fatalf("handle capacity = %v, want structured capacity", err)
		}
	})

	t.Run("execute receipts", func(t *testing.T) {
		cfg := shortConfig()
		cfg.MaxReceipts = 1
		local := newFailureBroker()
		server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Shutdown(context.Background())
		attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "receipt-bound"})
		if err != nil {
			t.Fatal(err)
		}
		execute := func(call string) error {
			_, executeErr := server.Execute(t.Context(), &brokerv1.ExecuteRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation(), Name: "read", CallId: call, Args: []byte(`{}`)})
			return executeErr
		}
		if err := execute("first"); err != nil {
			t.Fatal(err)
		}
		if err := execute("second"); status.Code(err) != codes.ResourceExhausted || !hasBrokerReason(err, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED) {
			t.Fatalf("receipt capacity = %v, want structured capacity", err)
		}
		if local.executeCalls.Load() != 1 {
			t.Fatalf("receipt rejection dispatched %d calls, want 1", local.executeCalls.Load())
		}
	})

	t.Run("pending lifecycle controls", func(t *testing.T) {
		cfg := shortConfig()
		cfg.MaxPendingControls = 1
		cfg.RPCDeadline = time.Second
		local := newFailureBroker()
		local.blockClose = make(chan struct{})
		local.closeEntered = make(chan struct{}, 1)
		server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Shutdown(context.Background())
		first, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "control-one"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "control-two"})
		if err != nil {
			t.Fatal(err)
		}
		firstDone := make(chan error, 1)
		go func() {
			_, closeErr := server.Close(context.Background(), &brokerv1.CloseRequest{Handle: first.GetHandle(), BrokerIncarnation: first.GetBrokerIncarnation()})
			firstDone <- closeErr
		}()
		select {
		case <-local.closeEntered:
		case <-time.After(time.Second):
			t.Fatal("first lifecycle control did not enter")
		}
		_, err = server.Close(t.Context(), &brokerv1.CloseRequest{Handle: second.GetHandle(), BrokerIncarnation: second.GetBrokerIncarnation()})
		if status.Code(err) != codes.ResourceExhausted || !hasBrokerReason(err, brokerv1.BrokerErrorReason_BROKER_ERROR_REASON_CAPACITY_REACHED) {
			t.Fatalf("control capacity = %v, want structured capacity", err)
		}
		close(local.blockClose)
		if err := <-firstDone; err != nil {
			t.Fatalf("first lifecycle control: %v", err)
		}
	})
}

func TestSingletonBrokerRemediation_OwnershipOutlivesExpiredHandles(t *testing.T) {
	cfg := shortConfig()
	cfg.HandleIdleTimeout = 20 * time.Millisecond
	cfg.OwnerRetention = 100 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	local := newFailureBroker()
	server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })

	ownerCtx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "workload-a"})
	first, err := server.Attach(ownerCtx, &brokerv1.AttachRequest{SessionId: "retained-owner"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-local.closedFor(session.ExternalBinding(first.GetBinding())):
	case <-time.After(testWait):
		t.Fatal("idle attachment was not reclaimed")
	}

	otherCtx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "workload-b"})
	if _, err := server.Attach(otherCtx, &brokerv1.AttachRequest{SessionId: "retained-owner"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("other workload attach after handle expiry = %v, want PermissionDenied", err)
	}
	deadline := time.Now().Add(testWait)
	for {
		reattached, attachErr := server.Attach(otherCtx, &brokerv1.AttachRequest{SessionId: "retained-owner"})
		if attachErr == nil {
			if reattached.GetBinding() == first.GetBinding() || reattached.GetOutcome() != string(mcpbroker.AttachCreated) {
				t.Fatalf("post-retention attach = binding %q outcome %q, want fresh logical state", reattached.GetBinding(), reattached.GetOutcome())
			}
			break
		}
		if status.Code(attachErr) != codes.PermissionDenied && status.Code(attachErr) != codes.Unavailable {
			t.Fatalf("post-retention attach = %v", attachErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("retired logical state did not release workload ownership")
		}
		time.Sleep(cfg.SweepInterval)
	}
}

func TestOwnerRetirementRetriesDeletionBeforeReleasingCapacity(t *testing.T) {
	cfg := shortConfig()
	cfg.MaxOwners = 1
	cfg.HandleIdleTimeout = 15 * time.Millisecond
	cfg.OwnerRetention = 25 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	local := newFailureBroker()
	local.deleteFails.Store(true)
	server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	ownerCtx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner-a"})
	if _, err := server.Attach(ownerCtx, &brokerv1.AttachRequest{SessionId: "retirement-retry"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(testWait)
	for local.deleteCalls.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("owner retirement deletion was not attempted")
		}
		time.Sleep(cfg.SweepInterval)
	}
	if _, err := server.Attach(ownerCtx, &brokerv1.AttachRequest{SessionId: "capacity-waiter"}); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("capacity during failed retirement = %v, want ResourceExhausted", err)
	}
	otherCtx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "owner-b"})
	if _, err := server.Attach(otherCtx, &brokerv1.AttachRequest{SessionId: "retirement-retry"}); status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.Unavailable {
		t.Fatalf("takeover during failed retirement = %v", err)
	}
	failedAttempts := local.deleteCalls.Load()
	local.deleteFails.Store(false)
	for {
		attached, attachErr := server.Attach(ownerCtx, &brokerv1.AttachRequest{SessionId: "capacity-waiter"})
		if attachErr == nil {
			if attached.GetOutcome() != string(mcpbroker.AttachCreated) {
				t.Fatalf("capacity waiter outcome = %q", attached.GetOutcome())
			}
			break
		}
		if status.Code(attachErr) != codes.ResourceExhausted {
			t.Fatalf("capacity retry = %v", attachErr)
		}
		if time.Now().After(deadline) {
			t.Fatal("successful deletion did not release capacity")
		}
		time.Sleep(cfg.SweepInterval)
	}
	if local.deleteCalls.Load() <= failedAttempts {
		t.Fatal("owner retirement was not retried after deletion failure")
	}
}

func TestSingletonBrokerRemediation_Scenario2_RetentionAndOwnership(t *testing.T) {
	cfg := shortConfig()
	cfg.HandleIdleTimeout = 25 * time.Millisecond
	cfg.SweepInterval = 5 * time.Millisecond
	cfg.CleanupTimeout = time.Second
	local := newFailureBroker()
	local.blockExecute = make(chan struct{})
	server, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	attached, err := server.Attach(t.Context(), &brokerv1.AttachRequest{SessionId: "owned-active"})
	if err != nil {
		t.Fatal(err)
	}
	executeDone := make(chan error, 1)
	go func() {
		_, executeErr := server.Execute(context.Background(), &brokerv1.ExecuteRequest{Handle: attached.GetHandle(), BrokerIncarnation: attached.GetBrokerIncarnation(), Name: "read", CallId: "active", Args: []byte(`{}`)})
		executeDone <- executeErr
	}()
	deadline := time.Now().Add(time.Second)
	for local.executeCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if local.executeCalls.Load() != 1 {
		t.Fatal("active Execute did not start")
	}
	// Absolute idle retention must not reclaim active authority.
	time.Sleep(2 * cfg.HandleIdleTimeout)
	if local.closeCalls.Load() != 0 {
		t.Fatal("active attachment was reclaimed by idle retention")
	}
	if err := server.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-executeDone; status.Code(err) != codes.Canceled {
		t.Fatalf("joined Execute = %v, want cancellation", err)
	}
	if local.closeCalls.Load() != 1 {
		t.Fatalf("owned attachment closes = %d, want exactly 1", local.closeCalls.Load())
	}
	if err := server.Shutdown(t.Context()); err != nil || local.closeCalls.Load() != 1 {
		t.Fatalf("idempotent shutdown = %v, closes=%d", err, local.closeCalls.Load())
	}
}

func TestSingletonBrokerRemediation_Scenario2_FreshClientPrePromptRecovery(t *testing.T) {
	oldConn, oldStop := failureBufServer(t, newFailureBroker(), nil)
	defer oldStop()
	fresh := newFailureBroker()
	newConn, newStop := failureBufServer(t, fresh, nil)
	defer newStop()
	switcher := &switchConn{current: oldConn}
	oldClient, err := mcpbrokergrpc.NewClientWithConfig(switcher, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	oldAttachment, _, err := oldClient.AttachSession(t.Context(), "reenroll")
	if err != nil {
		t.Fatal(err)
	}
	oldEnroller := oldAttachment.(mcpbroker.WorkspaceEnrollmentAttachment)
	oldPresentation, err := oldEnroller.BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	switcher.set(newConn)
	if _, err := oldEnroller.ObserveWorkspaceEnrollment(t.Context(), oldPresentation.Ref); !errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("stale outer correlation observe = %v, want state loss", err)
	}
	protected := oldAttachment.Tools()[1].(tool.AuthorizationRequester)
	if _, _, err := protected.RequestAuthorization(t.Context(), session.NewToolCall("protected", "protected", []byte(`{}`))); !errors.Is(err, mcpbroker.ErrStateUnavailable) {
		t.Fatalf("live protected authorization rebound: %v", err)
	}
	freshClient, err := mcpbrokergrpc.NewClientWithConfig(newConn, shortConfig())
	if err != nil {
		t.Fatal(err)
	}
	freshAttachment, _, err := freshClient.AttachSession(t.Context(), "reenroll")
	if err != nil {
		t.Fatal(err)
	}
	freshPresentation, err := freshAttachment.(mcpbroker.WorkspaceEnrollmentAttachment).BeginWorkspaceEnrollment(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if freshPresentation.Ref.ID == oldPresentation.Ref.ID || fresh.beginCalls.Load() != 1 || fresh.authCalls.Load() != 0 {
		t.Fatalf("fresh enrollment did not replace only outer correlation: old=%q fresh=%q begin=%d auth=%d", oldPresentation.Ref.ID, freshPresentation.Ref.ID, fresh.beginCalls.Load(), fresh.authCalls.Load())
	}
}

func shortConfig() mcpbrokergrpc.Config {
	cfg := mcpbrokergrpc.DefaultConfig()
	cfg.DialTimeout = 100 * time.Millisecond
	cfg.RPCDeadline = 200 * time.Millisecond
	cfg.ExecuteDeadline = 200 * time.Millisecond
	cfg.HandleIdleTimeout = time.Second
	cfg.SweepInterval = 10 * time.Millisecond
	cfg.CleanupTimeout = 100 * time.Millisecond
	return cfg
}

type switchConn struct {
	mu      sync.RWMutex
	current grpc.ClientConnInterface
}

func (c *switchConn) set(conn grpc.ClientConnInterface) { c.mu.Lock(); c.current = conn; c.mu.Unlock() }
func (c *switchConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	c.mu.RLock()
	current := c.current
	c.mu.RUnlock()
	return current.Invoke(ctx, method, args, reply, opts...)
}
func (c *switchConn) NewStream(ctx context.Context, desc *grpc.StreamDesc, method string, opts ...grpc.CallOption) (grpc.ClientStream, error) {
	c.mu.RLock()
	current := c.current
	c.mu.RUnlock()
	return current.NewStream(ctx, desc, method, opts...)
}

func failureBufServer(t *testing.T, local mcpbroker.Service, interceptor grpc.UnaryServerInterceptor, configs ...mcpbrokergrpc.Config) (*grpc.ClientConn, func()) {
	t.Helper()
	cfg := shortConfig()
	if len(configs) > 0 {
		cfg = configs[0]
	}
	brokerServer, err := mcpbrokergrpc.NewServerWithConfig(local, cfg)
	if err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	var options []grpc.ServerOption
	if interceptor != nil {
		options = append(options, grpc.UnaryInterceptor(interceptor))
	}
	server := grpc.NewServer(options...)
	mcpbrokergrpc.RegisterServer(server, brokerServer)
	go func() { _ = server.Serve(listener) }()
	conn, err := grpc.NewClient("passthrough:///broker", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	stop := func() {
		_ = brokerServer.Shutdown(context.Background())
		server.Stop()
		_ = conn.Close()
		_ = listener.Close()
	}
	return conn, stop
}

var failureBrokerSerial atomic.Int32

type failureBroker struct {
	mu            sync.Mutex
	serial        int32
	sessions      map[session.SessionID]*failureAttachment
	next          int
	authCalls     atomic.Int32
	executeCalls  atomic.Int32
	beginCalls    atomic.Int32
	abortCalls    atomic.Int32
	closeCalls    atomic.Int32
	abortErr      error
	closeOutcome  mcpbroker.CloseOutcome
	closeErr      error
	blockExecute  chan struct{}
	executeExited chan struct{}
	blockClose    chan struct{}
	closeEntered  chan struct{}
	deleteCalls   atomic.Int32
	deleteFails   atomic.Bool
}

func newFailureBroker() *failureBroker {
	return &failureBroker{serial: failureBrokerSerial.Add(1), sessions: make(map[session.SessionID]*failureAttachment), executeExited: make(chan struct{}, 1)}
}
func (b *failureBroker) AttachSession(_ context.Context, id session.SessionID) (mcpbroker.Attachment, mcpbroker.AttachOutcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if existing := b.sessions[id]; existing != nil {
		return existing.peer(), mcpbroker.AttachReattached, nil
	}
	b.next++
	a := &failureAttachment{
		broker:       b,
		binding:      session.ExternalBinding(fmt.Sprintf("binding-%d-%d", b.serial, b.next)),
		enrollmentID: session.WorkspaceEnrollmentID(fmt.Sprintf("enrollment-%d-%d", b.serial, b.next)),
		closed:       make(chan struct{}),
	}
	b.sessions[id] = a
	return a.peer(), mcpbroker.AttachCreated, nil
}
func (b *failureBroker) DeleteSession(_ context.Context, id session.SessionID) (mcpbroker.DeleteOutcome, error) {
	b.deleteCalls.Add(1)
	if b.deleteFails.Load() {
		return "", errors.New("delete unavailable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.sessions[id] == nil {
		return mcpbroker.DeleteNotFound, nil
	}
	delete(b.sessions, id)
	return mcpbroker.DeleteDeleted, nil
}
func (b *failureBroker) DeleteSessionIfBinding(_ context.Context, id session.SessionID, binding session.ExternalBinding) (mcpbroker.DeleteOutcome, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	a := b.sessions[id]
	if a == nil {
		return mcpbroker.DeleteNotFound, nil
	}
	if a.binding != binding {
		return "", mcpbroker.ErrStateUnavailable
	}
	delete(b.sessions, id)
	return mcpbroker.DeleteDeleted, nil
}
func (b *failureBroker) operations() int32 {
	return b.authCalls.Load() + b.executeCalls.Load() + b.beginCalls.Load()
}
func (b *failureBroker) closedFor(binding session.ExternalBinding) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.sessions {
		if a.binding == binding {
			return a.closed
		}
	}
	ch := make(chan struct{})
	close(ch)
	return ch
}

type failureAttachment struct {
	broker       *failureBroker
	binding      session.ExternalBinding
	enrollmentID session.WorkspaceEnrollmentID
	closed       chan struct{}
	closeOnce    *sync.Once
}

func (a *failureAttachment) peer() *failureAttachment {
	if a.closeOnce == nil {
		a.closeOnce = &sync.Once{}
	}
	return &failureAttachment{broker: a.broker, binding: a.binding, enrollmentID: a.enrollmentID, closed: a.closed, closeOnce: a.closeOnce}
}
func (a *failureAttachment) Binding() session.ExternalBinding { return a.binding }
func (*failureAttachment) Commit(context.Context) error       { return nil }
func (a *failureAttachment) Abort(ctx context.Context) error {
	a.broker.abortCalls.Add(1)
	if a.broker.abortErr != nil {
		return a.broker.abortErr
	}
	_, err := a.Close(ctx)
	return err
}
func (a *failureAttachment) Close(ctx context.Context) (mcpbroker.CloseOutcome, error) {
	a.broker.closeCalls.Add(1)
	if a.broker.blockClose != nil {
		if a.broker.closeEntered != nil {
			select {
			case a.broker.closeEntered <- struct{}{}:
			default:
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-a.broker.blockClose:
		}
	}
	closed := false
	a.closeOnce.Do(func() { close(a.closed); closed = true })
	if a.broker.closeOutcome != "" {
		return a.broker.closeOutcome, a.broker.closeErr
	}
	if closed {
		return mcpbroker.CloseClosed, nil
	}
	return mcpbroker.CloseAlreadyClosed, nil
}
func (a *failureAttachment) Tools() []tool.Tool {
	return []tool.Tool{&failureTool{a: a, name: "read"}, &failureTool{a: a, name: "protected", protected: true}}
}
func (*failureAttachment) PresentAuthorization(context.Context, session.ExternalAuthorization) (string, error) {
	return "https://broker.invalid/present", nil
}
func (*failureAttachment) AuthorizationStatus(context.Context, session.ExternalAuthorization) (session.AuthorizationStatus, error) {
	return session.AuthorizationPending, nil
}
func (*failureAttachment) CancelAuthorization(context.Context, session.ExternalAuthorization) (mcpbroker.CancelOutcome, error) {
	return mcpbroker.CancelCancelled, nil
}
func (a *failureAttachment) BeginWorkspaceEnrollment(context.Context) (mcpbroker.WorkspaceEnrollmentPresentation, error) {
	a.broker.beginCalls.Add(1)
	ref := mcpbroker.WorkspaceEnrollmentRef{ID: a.enrollmentID, RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	return mcpbroker.WorkspaceEnrollmentPresentation{Ref: ref, URL: "https://broker.invalid/enroll"}, nil
}
func (a *failureAttachment) ObserveWorkspaceEnrollment(context.Context, mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	ref := mcpbroker.WorkspaceEnrollmentRef{ID: a.enrollmentID, RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	return mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentPending}, nil
}
func (a *failureAttachment) CancelWorkspaceEnrollment(context.Context, mcpbroker.WorkspaceEnrollmentRef) (mcpbroker.WorkspaceEnrollmentResult, error) {
	ref := mcpbroker.WorkspaceEnrollmentRef{ID: a.enrollmentID, RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
	return mcpbroker.WorkspaceEnrollmentResult{Ref: ref, Status: mcpbroker.WorkspaceEnrollmentCancelled}, nil
}

type failureTool struct {
	a         *failureAttachment
	name      string
	protected bool
}

func (t *failureTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Description: t.name, Schema: []byte(`{"type":"object"}`)}
}
func (*failureTool) ReadOnly() bool { return true }
func (t *failureTool) Execute(ctx context.Context, call session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	t.a.broker.executeCalls.Add(1)
	if t.a.broker.blockExecute != nil {
		select {
		case <-ctx.Done():
			select {
			case t.a.broker.executeExited <- struct{}{}:
			default:
			}
			return session.ToolResult{}, ctx.Err()
		case <-t.a.broker.blockExecute:
		}
	}
	return session.NewToolResult(call.ID, "ok"), nil
}
func (t *failureTool) RequestAuthorization(context.Context, session.ToolCall) (session.ExternalAuthorization, bool, error) {
	t.a.broker.authCalls.Add(1)
	return session.ExternalAuthorization{ID: "auth", Binding: "auth-binding", ExpiresAt: time.Now().Add(time.Hour)}, true, nil
}
func (*failureTool) AbortAuthorization(context.Context, session.ExternalAuthorization) error {
	return nil
}

var _ mcpbroker.BindingSessionDeleter = (*failureBroker)(nil)
var _ mcpbroker.WorkspaceEnrollmentAttachment = (*failureAttachment)(nil)
