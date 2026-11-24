package executioncontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

const withdrawalWait = 5 * time.Second

func (f *proxyFixture) updateEnvironment(t *testing.T, mutate func(*unstructured.Unstructured)) {
	t.Helper()
	obj, err := f.dynamic.Tracker().Get(ExecutionEnvironmentGVR, "ns", "env")
	if err != nil {
		t.Fatal(err)
	}
	env := obj.(*unstructured.Unstructured).DeepCopy()
	mutate(env)
	if err := f.dynamic.Tracker().Update(ExecutionEnvironmentGVR, env, "ns"); err != nil {
		t.Fatal(err)
	}
}

func blockedCommand(t *testing.T, f *proxyFixture) executorv1.CommandService_RunCommandClient {
	t.Helper()
	f.runner.Script("block", memexecutor.Block("ready"))
	stream := startCommand(t.Context(), t, f.conn, proxyRequestContext(), "block")
	for range 2 {
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	return stream
}

// tick runs one recheck sweep and waits for it to finish.
func (f *proxyFixture) tick(t *testing.T) {
	t.Helper()
	select {
	case f.ticks <- time.Now():
	case <-time.After(withdrawalWait):
		t.Fatal("no recheck sweep is waiting")
	}
	select {
	case <-f.swept:
	case <-time.After(withdrawalWait):
		t.Fatal("recheck sweep did not finish")
	}
}

func (r *inflightCalls) open() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, calls := range r.byEnv {
		for f := range calls {
			if f.ctx.Err() == nil {
				n++
			}
		}
	}
	return n
}

func (f *proxyFixture) requireOpen(t *testing.T, want int) {
	t.Helper()
	if got := f.handler.flights.open(); got != want {
		t.Fatalf("%d forwarded calls open, want %d", got, want)
	}
}

// requireWithdrawn runs one recheck sweep after the trigger when sweep is set.
func requireWithdrawn(
	t *testing.T,
	f *proxyFixture,
	stream executorv1.CommandService_RunCommandClient,
	sweep bool,
	want codes.Code,
) {
	t.Helper()
	ended := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		ended <- err
	}()
	if sweep {
		f.tick(t)
	}
	var err error
	select {
	case err = <-ended:
	case <-time.After(withdrawalWait):
		t.Fatal("in-flight command was not withdrawn")
	}
	if status.Code(err) != want || refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("withdrawn command = %v, want %v without NOT_FORWARDED", err, want)
	}
	deadline := time.Now().Add(withdrawalWait)
	for f.runner.Running() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("executor kept running the withdrawn command")
		}
		time.Sleep(time.Millisecond)
	}
	forwarded := f.connect.count()
	_, err = startCommand(t.Context(), t, f.conn, proxyRequestContext(), "block").Recv()
	if status.Code(err) != want || !refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("new command = %v, want %v with NOT_FORWARDED", err, want)
	}
	if f.connect.count() != forwarded {
		t.Fatal("new command reached the executor")
	}
}

func TestWithdrawalClosesInFlightCommands(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw func(*testing.T, *proxyFixture)
		want     codes.Code
	}{
		{
			name: "revocation by another replica",
			withdraw: func(t *testing.T, f *proxyFixture) {
				f.updateEnvironment(t, setField([]string{"status", "grantGeneration"}, int64(2)))
			},
			want: codes.Unauthenticated,
		},
		{
			name: "environment deleted",
			withdraw: func(t *testing.T, f *proxyFixture) {
				err := f.dynamic.Tracker().Delete(ExecutionEnvironmentGVR, "ns", "env")
				if err != nil {
					t.Fatal(err)
				}
			},
			want: codes.NotFound,
		},
		{
			name: "lapsed run claim",
			withdraw: func(t *testing.T, f *proxyFixture) {
				lapsed := time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
				expiry := []string{"status", "activeRun", "expiresAt"}
				f.updateEnvironment(t, setField(expiry, lapsed))
			},
			want: codes.Unauthenticated,
		},
		{
			name: "caller policy change",
			withdraw: func(t *testing.T, f *proxyFixture) {
				f.manifest.Generation++
				f.manifest.Clients[0].ExecutionTemplates = nil
				writeManifest(t, f.manifestPath, f.manifest)
				if err := f.security.Reload(t.Context()); err != nil {
					t.Fatal(err)
				}
			},
			want: codes.Unavailable,
		},
		{
			name: "caller trust removed",
			withdraw: func(t *testing.T, f *proxyFixture) {
				f.manifest.Generation++
				f.manifest.Clients[0].URI = "spiffe://mecatl.test/other"
				writeManifest(t, f.manifestPath, f.manifest)
				if err := f.security.Reload(t.Context()); err != nil {
					t.Fatal(err)
				}
			},
			want: codes.Unauthenticated,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{manualTicks: true})
			stream := blockedCommand(t, f)
			f.tick(t)
			f.requireOpen(t, 1)
			tc.withdraw(t, f)
			requireWithdrawn(t, f, stream, true, tc.want)
		})
	}
}

func (f *proxyFixture) revokeEnvironment(ctx context.Context) error {
	owner := &executionv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject}
	resp, err := executionv1.NewExecutionProviderServiceClient(f.conn).RevokeEnvironment(
		ctx,
		&executionv1.RevokeEnvironmentRequest{
			Environment:             &executionv1.EnvironmentRef{Id: "env", Revision: "rev"},
			Owner:                   owner,
			ExpectedGrantGeneration: 1,
			OperationId:             "revoke",
		},
	)
	if err == nil && resp.GetGrantGeneration() != 2 {
		err = fmt.Errorf("RevokeEnvironment granted generation %d", resp.GetGrantGeneration())
	}
	return err
}

func (f *proxyFixture) revoke(t *testing.T) {
	t.Helper()
	if err := f.revokeEnvironment(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestRevocationHereClosesCallsWithoutARecheck(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{administrator: true, manualTicks: true})
	stream := blockedCommand(t, f)
	f.revoke(t)
	requireWithdrawn(t, f, stream, false, codes.Unauthenticated)
}

func TestRevocationDuringAdmissionRefusesTheCall(t *testing.T) {
	for name, dialErr := range map[string]error{
		"dial succeeds": nil,
		"dial fails":    errors.New("dial failed"),
	} {
		t.Run(name, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{administrator: true, manualTicks: true})
			revoked := make(chan error, 1)
			f.connect.before = func() error {
				f.connect.before = nil
				revoked <- f.revokeEnvironment(t.Context())
				return dialErr
			}
			f.runner.Script("block", memexecutor.Block("ready"))
			_, err := startCommand(t.Context(), t, f.conn, proxyRequestContext(), "block").Recv()
			if err := <-revoked; err != nil {
				t.Fatal(err)
			}
			if status.Code(err) != codes.Unauthenticated ||
				!refusedBeforeForwarding(status.Convert(err)) {
				t.Fatalf("revoked during admission = %v, want Unauthenticated, NOT_FORWARDED", err)
			}
			if f.runner.Running() != 0 {
				t.Fatal("command revoked during admission reached the executor")
			}
		})
	}
}

func TestRecheckToleratesTransientUnreadiness(t *testing.T) {
	for reason, sweeps := range map[string]int{
		"ExecutorUnavailable": 2,
		"PVCUnavailable":      2,
		"Reconciled":          2,
		"InvalidTemplate":     1,
	} {
		t.Run(reason, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{manualTicks: true})
			stream := blockedCommand(t, f)
			f.updateEnvironment(t, func(o *unstructured.Unstructured) {
				setConditionObject(o, "Ready", false, reason, "")
			})
			for range sweeps - 1 {
				f.tick(t)
				f.requireOpen(t, 1)
			}
			requireWithdrawn(t, f, stream, true, codes.Unavailable)
		})
	}
}

func TestPolicyChangeDuringUnreadinessClosesAtOnce(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{manualTicks: true})
	stream := blockedCommand(t, f)
	f.updateEnvironment(t, func(o *unstructured.Unstructured) {
		setConditionObject(o, "Ready", false, "ExecutorUnavailable", "")
	})
	f.manifest.Generation++
	f.manifest.Clients[0].ExecutionTemplates = nil
	writeManifest(t, f.manifestPath, f.manifest)
	if err := f.security.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	requireWithdrawn(t, f, stream, true, codes.Unavailable)
}

func TestLifecycleOperationDuringUnreadinessClosesAtOnce(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{manualTicks: true})
	stream := blockedCommand(t, f)
	f.updateEnvironment(t, func(o *unstructured.Unstructured) {
		setConditionObject(o, "Ready", false, "Reconciled", "")
		setField([]string{"status", "lifecycleOperation", "id"}, "replace")(o)
	})
	requireWithdrawn(t, f, stream, true, codes.Unavailable)
}

// stallingConn reports each executor stream it opens and stops it after its
// first message until the forwarded call's context ends.
type stallingConn struct {
	grpc.ClientConnInterface
	opened  chan struct{}
	stalled chan struct{}
}

func (c *stallingConn) NewStream(
	ctx context.Context,
	desc *grpc.StreamDesc,
	method string,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	s, err := c.ClientConnInterface.NewStream(ctx, desc, method, opts...)
	if err != nil {
		return nil, err
	}
	c.opened <- struct{}{}
	return &stallingStream{ClientStream: s, ctx: ctx, stalled: c.stalled}, nil
}

type stallingStream struct {
	grpc.ClientStream
	ctx      context.Context
	stalled  chan struct{}
	received bool
}

func (s *stallingStream) RecvMsg(m any) error {
	if s.received {
		s.stalled <- struct{}{}
		<-s.ctx.Done()
		return status.FromContextError(s.ctx.Err()).Err()
	}
	s.received = true
	return s.ClientStream.RecvMsg(m)
}

func (f *proxyFixture) stall() *stallingConn {
	c := &stallingConn{
		ClientConnInterface: f.connect.conn,
		opened:              make(chan struct{}, 1),
		stalled:             make(chan struct{}, 1),
	}
	f.connect.conn = c
	return c
}

func await(t *testing.T, signal <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(withdrawalWait):
		t.Fatal(what)
	}
}

func requireWithdrawnStream(t *testing.T, err error) {
	t.Helper()
	if status.Code(err) != codes.Unauthenticated || refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("withdrawn stream = %v, want Unauthenticated without NOT_FORWARDED", err)
	}
}

func TestWithdrawalClosesInFlightFileStreams(t *testing.T) {
	revoke := func(t *testing.T, f *proxyFixture) {
		f.updateEnvironment(t, setField([]string{"status", "grantGeneration"}, int64(2)))
		f.tick(t)
	}
	t.Run("read", func(t *testing.T) {
		f := newProxyFixture(t, proxyOptions{manualTicks: true})
		if err := f.files.Write(t.Context(), "a.txt", []byte("a")); err != nil {
			t.Fatal(err)
		}
		conn := f.stall()
		read, err := executorv1.NewFileServiceClient(f.conn).ReadFile(
			t.Context(),
			&executorv1.ReadFileRequest{Context: proxyRequestContext(), Path: "a.txt"},
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := read.Recv(); err != nil {
			t.Fatal(err)
		}
		await(t, conn.stalled, "ReadFile never waited on the executor")
		revoke(t, f)
		_, err = read.Recv()
		requireWithdrawnStream(t, err)
	})
	t.Run("upload", func(t *testing.T) {
		f := newProxyFixture(t, proxyOptions{manualTicks: true})
		conn := f.stall()
		up, err := executorv1.NewFileServiceClient(f.conn).CreateFile(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		first := &executorv1.CreateFileRequest{
			Context: proxyRequestContext(),
			Path:    "new.txt",
			Data:    []byte("a"),
		}
		if err := up.Send(first); err != nil {
			t.Fatal(err)
		}
		await(t, conn.opened, "the upload never reached the executor")
		revoke(t, f)
		_ = up.Send(&executorv1.CreateFileRequest{Data: []byte("b")})
		_, err = up.CloseAndRecv()
		requireWithdrawnStream(t, err)
		if _, err := f.files.Read(t.Context(), "new.txt"); err == nil {
			t.Fatal("withdrawn upload wrote its file")
		}
	})
}

func TestTemplateRevocationDeletesTheExecutorAndClosesCalls(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{
		manualTicks: true,
		mutate:      func(env *unstructured.Unstructured) { env.SetUID("env-uid") },
	})
	stream := blockedCommand(t, f)
	f.tick(t)
	f.requireOpen(t, 1)
	executor := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      f.pin.PodName,
			Namespace: "ns",
			UID:       "pod-1",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: ExecutionEnvironmentGVR.GroupVersion().String(),
				Kind:       "ExecutionEnvironment",
				Name:       "env",
				UID:        "env-uid",
				Controller: new(true),
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	kube := kubefake.NewSimpleClientset(executor)
	// A replica started with the revised templates file reconciles the environment.
	revoked := proxyProfiles(proxyOptions{})
	revoked.eligibility["go"][revoked.defaultRevision["go"]] = TemplatePolicy{Revoked: true}
	r := NewReconciler(f.dynamic, kube, "ns", revoked)
	t.Cleanup(r.queue.ShutDown)
	if err := r.Reconcile(t.Context(), "env"); err != nil {
		t.Fatal(err)
	}
	_, err := kube.CoreV1().Pods("ns").Get(t.Context(), f.pin.PodName, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("revoked template kept its executor Pod: %v", err)
	}
	requireWithdrawn(t, f, stream, true, codes.Unavailable)
}

func TestWithdrawalClosesInFlightFileCalls(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{manualTicks: true})
	if err := f.files.Write(t.Context(), "a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	gated := &gatedConn{
		ClientConnInterface: f.connect.conn,
		entered:             make(chan struct{}, 1),
		gate:                make(chan struct{}),
	}
	f.connect.conn = gated
	done := make(chan error, 1)
	go func() {
		_, err := executorv1.NewFileServiceClient(f.conn).StatFile(
			t.Context(),
			&executorv1.StatFileRequest{Context: proxyRequestContext(), Path: "a.txt"},
		)
		done <- err
	}()
	<-gated.entered
	f.updateEnvironment(t, setField([]string{"status", "grantGeneration"}, int64(2)))
	f.tick(t)
	select {
	case err := <-done:
		if status.Code(err) != codes.Unauthenticated {
			t.Fatalf("withdrawn StatFile = %v", err)
		}
	case <-time.After(withdrawalWait):
		t.Fatal("in-flight file call was not withdrawn")
	}
}

func TestPolicyRecheckRunsAtTheConfiguredInterval(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{recheck: 20 * time.Millisecond})
	stream := blockedCommand(t, f)
	f.updateEnvironment(t, setField([]string{"status", "grantGeneration"}, int64(2)))
	requireWithdrawn(t, f, stream, false, codes.Unauthenticated)
}

func TestHandlerDefaultsThePolicyRecheckInterval(t *testing.T) {
	h := NewHandler(HandlerConfig{}, nil)
	if h.cfg.PolicyRecheckInterval != 10*time.Second {
		t.Fatalf("default recheck interval = %v", h.cfg.PolicyRecheckInterval)
	}
}

func TestRevocationWithdrawsOnlyEarlierGenerations(t *testing.T) {
	r := newInflightCalls()
	ctxs := map[uint64]context.Context{}
	for _, generation := range []uint64{1, 2} {
		ctx, cancel := context.WithCancelCause(t.Context())
		defer cancel(nil)
		r.add("env", &flight{generation: generation, withdraw: cancel})
		ctxs[generation] = ctx
	}
	other, cancelOther := context.WithCancelCause(t.Context())
	defer cancelOther(nil)
	r.add("other", &flight{generation: 1, withdraw: cancelOther})
	if closed := r.withdrawBelow("env", 2, errRunClaimNotCurrent); closed != 1 {
		t.Fatalf("closed %d calls, want 1", closed)
	}
	if withdrawnOr(ctxs[1], context.Canceled) != errRunClaimNotCurrent {
		t.Fatal("earlier generation kept running or lost its cause")
	}
	if ctxs[2].Err() != nil || other.Err() != nil {
		t.Fatal("revocation closed a current or unrelated call")
	}
}

func TestStoppedCallsLeaveTheRegistry(t *testing.T) {
	r := newInflightCalls()
	f := &flight{withdraw: func(error) {}}
	r.add("env", f)
	r.remove("env", f)
	if len(r.byEnv) != 0 {
		t.Fatalf("registry retained %v", r.byEnv)
	}
}

// Reactors must be added before calls run: the fake client does not lock them.
func failGets(
	reactors interface {
		PrependReactor(verb string, resource string, reaction k8stesting.ReactionFunc)
	},
	resource string,
	fail func(n int64) bool,
) *atomic.Bool {
	var armed atomic.Bool
	var gets atomic.Int64
	reactors.PrependReactor(
		"get",
		resource,
		func(k8stesting.Action) (bool, runtime.Object, error) {
			if armed.Load() && fail(gets.Add(1)) {
				return true, nil, errors.New("api server unavailable")
			}
			return false, nil, nil
		},
	)
	return &armed
}

func always(int64) bool { return true }

func TestRecheckToleratesOneUnconfirmedRefusal(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(*proxyFixture) *atomic.Bool
		want codes.Code
	}{
		{
			name: "environment unreadable",
			fail: func(f *proxyFixture) *atomic.Bool {
				return failGets(f.dynamic, "executionenvironments", always)
			},
			want: codes.NotFound,
		},
		{
			name: "security authority unreachable",
			fail: func(f *proxyFixture) *atomic.Bool {
				return failGets(f.kube, "configmaps", always)
			},
			want: codes.Unavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{manualTicks: true})
			armed := tc.fail(f)
			stream := blockedCommand(t, f)
			armed.Store(true)
			f.tick(t)
			f.requireOpen(t, 1)
			requireWithdrawn(t, f, stream, true, tc.want)
		})
	}
}

func TestReadableRecheckResetsTheTolerance(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{manualTicks: true})
	armed := failGets(f.dynamic, "executionenvironments", func(n int64) bool { return n%2 == 1 })
	blockedCommand(t, f)
	armed.Store(true)
	for range 5 {
		f.tick(t)
		f.requireOpen(t, 1)
	}
}

func TestRecheckReadsEachEnvironmentAndTheAuthorityOncePerSweep(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{manualTicks: true})
	var environments, authority atomic.Int64
	count := func(n *atomic.Int64) k8stesting.ReactionFunc {
		return func(k8stesting.Action) (bool, runtime.Object, error) {
			n.Add(1)
			return false, nil, nil
		}
	}
	f.dynamic.PrependReactor("get", "executionenvironments", count(&environments))
	f.kube.PrependReactor("get", "configmaps", count(&authority))
	const commands = 20
	for range commands {
		blockedCommand(t, f)
	}
	if err := f.files.Write(t.Context(), "a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	gated := &gatedConn{
		ClientConnInterface: f.connect.conn,
		entered:             make(chan struct{}, 1),
		gate:                make(chan struct{}),
	}
	f.connect.conn = gated
	t.Cleanup(func() { close(gated.gate) })
	go func() {
		_, _ = executorv1.NewFileServiceClient(f.conn).StatFile(
			t.Context(),
			&executorv1.StatFileRequest{Context: proxyRequestContext(), Path: "a.txt"},
		)
	}()
	<-gated.entered
	environments.Store(0)
	authority.Store(0)
	f.tick(t)
	f.requireOpen(t, commands+1)
	if environments.Load() != 1 || authority.Load() != 1 {
		t.Fatalf(
			"sweep read the environment %d and the authority %d times, want once each",
			environments.Load(),
			authority.Load(),
		)
	}
}

func TestRecheckKeySeparatesPresentedChains(t *testing.T) {
	withChain := func(raws ...string) context.Context {
		var chain []*x509.Certificate
		for _, raw := range raws {
			chain = append(chain, &x509.Certificate{Raw: []byte(raw)})
		}
		state := tls.ConnectionState{PeerCertificates: chain}
		return peer.NewContext(t.Context(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: state}})
	}
	rc := proxyRequestContext()
	base := recheckKey(withChain("leaf", "intermediate-a"), "files", rc)
	for name, other := range map[string]string{
		"other intermediate": recheckKey(withChain("leaf", "intermediate-b"), "files", rc),
		"no intermediate":    recheckKey(withChain("leaf"), "files", rc),
		"shifted boundary":   recheckKey(withChain("leafintermediate-a"), "files", rc),
		"other capability":   recheckKey(withChain("leaf", "intermediate-a"), "commands", rc),
	} {
		if other == base {
			t.Errorf("%s shares a recheck group with the original chain", name)
		}
	}
	if recheckKey(withChain("leaf", "intermediate-a"), "files", rc) != base {
		t.Error("identical calls fall into different recheck groups")
	}
}
