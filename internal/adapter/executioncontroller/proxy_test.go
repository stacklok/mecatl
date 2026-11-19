package executioncontroller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/durationpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/internal/executorrpc/commandconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

const proxyRoot = "/workspace"

type proxyOptions struct {
	mutate          func(*unstructured.Unstructured)
	clientMax       int
	maxFileBytes    int64
	maxCommandBytes int64
}

type proxyFixture struct {
	conn    *grpc.ClientConn
	files   *memfs.FileSystem
	runner  *memexecutor.Runner
	pin     ExecutorPin
	connect *recordingConnector
}

type recordingConnector struct {
	conn grpc.ClientConnInterface
	mu   sync.Mutex
	pins []ExecutorPin
}

func (c *recordingConnector) Connect(
	_ context.Context,
	pin ExecutorPin,
) (grpc.ClientConnInterface, func(), error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pins = append(c.pins, pin)
	return c.conn, func() {}, nil
}

func (c *recordingConnector) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pins)
}

func proxyRequestContext() *executorv1.RequestContext {
	return &executorv1.RequestContext{
		Environment:     &executorv1.EnvironmentRef{Id: "env", Revision: "rev"},
		Owner:           &executorv1.Owner{Issuer: scopeOwner.Issuer, Subject: scopeOwner.Subject},
		BindingId:       "binding",
		RunId:           "run",
		ClaimId:         "claim",
		Epoch:           1,
		GrantGeneration: 1,
	}
}

func proxyProfiles(opts proxyOptions) *Profiles {
	profiles := testProfiles()
	revision := profiles.defaultRevision["go"]
	profile := profiles.revisions["go"][revision]
	if opts.maxFileBytes > 0 {
		profile.Spec.MaxFileBytes = opts.maxFileBytes
	}
	if opts.maxCommandBytes > 0 {
		profile.Spec.MaxCommandBytes = opts.maxCommandBytes
	}
	profiles.revisions["go"][revision] = profile
	profiles.byName["go"] = profile
	return profiles
}

func newProxyFixture(t *testing.T, opts proxyOptions) *proxyFixture {
	t.Helper()
	pin := ExecutorPin{
		PodName:     "executor-env",
		PodUID:      "pod-1",
		Fingerprint: certificateFingerprint([]byte("executor")),
	}
	env := runFixtureEnvironment(1, 1)
	for _, prefix := range [][]string{{"spec"}, {"status", "activeRun"}} {
		client, owner := append(prefix, "clientHash"), append(prefix, "ownerHash")
		_ = unstructured.SetNestedField(env.Object, hashText(scopeCreator), client...)
		_ = unstructured.SetNestedField(env.Object, ownerHash(scopeOwner), owner...)
	}
	pod := map[string]any{"name": pin.PodName, "uid": pin.PodUID}
	_ = unstructured.SetNestedMap(env.Object, pod, "status", "pod")
	writeServerCertificatePin(env, serverCertificatePin{
		SecretName:  "executor-env-tls",
		Fingerprint: pin.Fingerprint,
		PodUID:      pin.PodUID,
	})
	if opts.mutate != nil {
		opts.mutate(env)
	}

	dir := t.TempDir()
	ca, cert, key, clientCert := rpcSecurityPKI(t, time.Now(), scopeCreator)
	writeRotationMaterial(t, dir, map[string][]byte{
		"server.crt": cert, "server.key": key, "clients.pem": ca,
	})
	manifest := securityManifest{
		Version:    1,
		Generation: 1,
		TLS: securityTLSManifest{
			CertificateFile: "server.crt",
			PrivateKeyFile:  "server.key",
			ClientCAFile:    "clients.pem",
		},
		Clients: []securityClientManifest{{
			URI:                scopeCreator,
			MayAttestOwner:     true,
			ExecutionTemplates: []string{"go"},
		}},
	}
	manifestPath := filepath.Join(dir, "manifest.json")
	writeManifest(t, manifestPath, manifest)
	kube := kubefake.NewSimpleClientset()
	manager := NewSecurityManager(manifestPath, dir, "ns", "authority", kube)
	if err := manager.Reload(t.Context()); err != nil {
		t.Fatal(err)
	}
	dynamic := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), env)
	store := NewStore(dynamic, "ns", proxyProfiles(opts), nil).WithSecurityManager(manager)
	handler := NewHandler(HandlerConfig{Security: manager}, store)

	files := memfs.NewFileSystem(proxyRoot)
	runner := memexecutor.NewRunner()
	executor := memexecutor.Dial(t, memexecutor.Executor{
		Files:    memfs.NewWorkspaceOverFileSystem(files),
		Commands: runner,
	})
	connector := &recordingConnector{conn: executor}

	clientMax := opts.clientMax
	if clientMax == 0 {
		clientMax = 1 << 10
	}
	limiter, err := NewRPCLimiter(manager, clientMax, clientMax)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(manager.TLSConfig())),
		grpc.ChainUnaryInterceptor(limiter.UnaryInterceptor),
		grpc.ChainStreamInterceptor(limiter.StreamInterceptor),
	)
	executorv1.RegisterFileServiceServer(server, NewFileProxy(handler, connector))
	executorv1.RegisterCommandServiceServer(server, NewCommandProxy(handler, connector))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	creds := credentials.NewTLS(&tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   "provider.test",
		RootCAs:      roots,
		Certificates: []tls.Certificate{clientCert},
	})
	conn, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(creds),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return &proxyFixture{conn: conn, files: files, runner: runner, pin: pin, connect: connector}
}

func TestFileProxyConformance(t *testing.T) {
	fileconformance.Run(t, func(t *testing.T) fileconformance.Target {
		f := newProxyFixture(t, proxyOptions{maxFileBytes: 64 << 20})
		return fileconformance.Target{
			Conn: f.conn,
			Root: proxyRoot,
			External: fsconformance.ExternalAccess{
				Read: func(p string) ([]byte, error) {
					return f.files.Read(context.Background(), p)
				},
				Write: func(p string, data []byte) error {
					return f.files.Write(context.Background(), p, data)
				},
			},
			Context: func(context.Context) (*executorv1.RequestContext, error) {
				return proxyRequestContext(), nil
			},
		}
	})
}

func TestCommandProxyConformance(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{})
	f.runner.Script("succeed", memexecutor.Exit("out", "err", 0))
	f.runner.Script("fail", memexecutor.Exit("", "", 3))
	f.runner.Script("block", memexecutor.Block("ready"))
	f.runner.Script("survivor", memexecutor.OutcomeUnknown(memexecutor.Exit("", "", 0)))
	client := executorv1.NewCommandServiceClient(f.conn)
	commandconformance.Run(t, commandconformance.Harness{
		Client:         func(*testing.T) executorv1.CommandServiceClient { return client },
		Succeed:        "succeed",
		Fail:           "fail",
		Block:          "block",
		BlockWithChild: "block",
		ChildGone: func(ctx context.Context) error {
			for f.runner.Running() != 0 {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				time.Sleep(time.Millisecond)
			}
			return nil
		},
		Survivor: "survivor",
		Context:  proxyRequestContext,
	})
}

func startCommand(
	ctx context.Context,
	t *testing.T,
	conn grpc.ClientConnInterface,
	rc *executorv1.RequestContext,
	command string,
) executorv1.CommandService_RunCommandClient {
	t.Helper()
	stream, err := executorv1.NewCommandServiceClient(conn).RunCommand(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = stream.Send(&executorv1.RunCommandRequest{
		Context: rc,
		Action: &executorv1.RunCommandRequest_Start{
			Start: &executorv1.StartCommand{Command: command},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func setField(path []string, value any) func(*unstructured.Unstructured) {
	return func(env *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(env.Object, value, path...)
	}
}

func TestProxyRefusesBeforeForwarding(t *testing.T) {
	admitted := func(names ...any) func(*unstructured.Unstructured) {
		return setField([]string{"spec", admittedCapabilitiesField}, names)
	}
	for _, tc := range []struct {
		name         string
		mutate       func(*unstructured.Unstructured)
		request      func(*executorv1.RequestContext) *executorv1.RequestContext
		wantFiles    codes.Code
		wantCommands codes.Code
	}{
		{name: "current", wantFiles: codes.OK, wantCommands: codes.OK},
		{
			name: "stale epoch",
			request: func(r *executorv1.RequestContext) *executorv1.RequestContext {
				r.Epoch++
				return r
			},
			wantFiles: codes.Aborted, wantCommands: codes.Aborted,
		},
		{
			name: "stale grant generation",
			request: func(r *executorv1.RequestContext) *executorv1.RequestContext {
				r.GrantGeneration++
				return r
			},
			wantFiles: codes.Aborted, wantCommands: codes.Aborted,
		},
		{
			name:      "revoked grant generation",
			mutate:    setField([]string{"status", "grantGeneration"}, int64(2)),
			wantFiles: codes.Aborted, wantCommands: codes.Aborted,
		},
		{
			name: "lapsed claim",
			mutate: setField(
				[]string{"status", "activeRun", "expiresAt"},
				time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano),
			),
			wantFiles: codes.Aborted, wantCommands: codes.Aborted,
		},
		{
			name:      "commands not admitted",
			mutate:    admitted("filesystem"),
			wantFiles: codes.OK, wantCommands: codes.PermissionDenied,
		},
		{
			name:      "files not admitted",
			mutate:    admitted("foreground-command"),
			wantFiles: codes.PermissionDenied, wantCommands: codes.OK,
		},
		{
			name:      "missing request context",
			request:   func(*executorv1.RequestContext) *executorv1.RequestContext { return nil },
			wantFiles: codes.PermissionDenied, wantCommands: codes.PermissionDenied,
		},
		{
			name: "request context without claim",
			request: func(r *executorv1.RequestContext) *executorv1.RequestContext {
				r.ClaimId = ""
				return r
			},
			wantFiles: codes.PermissionDenied, wantCommands: codes.PermissionDenied,
		},
		{
			name: "request context without owner",
			request: func(r *executorv1.RequestContext) *executorv1.RequestContext {
				r.Owner = nil
				return r
			},
			wantFiles: codes.PermissionDenied, wantCommands: codes.PermissionDenied,
		},
		{
			name:      "executor Pod replaced",
			mutate:    setField([]string{"status", "pod", "uid"}, "pod-2"),
			wantFiles: codes.Unavailable, wantCommands: codes.Unavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProxyFixture(t, proxyOptions{mutate: tc.mutate})
			if err := f.files.Write(t.Context(), "a.txt", []byte("a")); err != nil {
				t.Fatal(err)
			}
			f.runner.Script("true", memexecutor.Exit("", "", 0))
			rc := proxyRequestContext()
			if tc.request != nil {
				rc = tc.request(rc)
			}
			_, fileErr := executorv1.NewFileServiceClient(f.conn).StatFile(
				t.Context(),
				&executorv1.StatFileRequest{Context: rc, Path: "a.txt"},
			)
			if status.Code(fileErr) != tc.wantFiles {
				t.Fatalf("StatFile = %v, want %v", fileErr, tc.wantFiles)
			}
			_, commandErr := startCommand(t.Context(), t, f.conn, rc, "true").Recv()
			if status.Code(commandErr) != tc.wantCommands {
				t.Fatalf("RunCommand = %v, want %v", commandErr, tc.wantCommands)
			}
			errs := []error{fileErr, commandErr}
			forwarded := 0
			for _, code := range []codes.Code{tc.wantFiles, tc.wantCommands} {
				if code == codes.OK {
					forwarded++
				}
			}
			for _, err := range errs {
				if err != nil && !refusedBeforeForwarding(status.Convert(err)) {
					t.Fatalf("refusal %v lacks NOT_FORWARDED", err)
				}
			}
			if f.connect.count() != forwarded {
				t.Fatalf("forwarded %d calls, want %d", f.connect.count(), forwarded)
			}
			for _, pin := range f.connect.pins {
				if pin != f.pin {
					t.Fatalf("forwarded to %+v, want %+v", pin, f.pin)
				}
			}
		})
	}
}

func TestCommandProxyBoundsTheStart(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{maxCommandBytes: 4})
	_, err := startCommand(t.Context(), t, f.conn, proxyRequestContext(), "sleep 1").Recv()
	if status.Code(err) != codes.ResourceExhausted ||
		!refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("oversized command = %v", err)
	}
	stream, err := executorv1.NewCommandServiceClient(f.conn).RunCommand(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&executorv1.RunCommandRequest{
		Context: proxyRequestContext(),
		Action: &executorv1.RunCommandRequest_Start{Start: &executorv1.StartCommand{
			Command: "true",
			Timeout: durationpb.New(2 * time.Minute),
		}},
	})
	_, err = stream.Recv()
	if status.Code(err) != codes.InvalidArgument || !refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("timeout over the template limit = %v", err)
	}
	if f.connect.count() != 0 {
		t.Fatal("refused command was forwarded")
	}
}

func TestCommandProxyClipsOutputMidStream(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{maxCommandBytes: 8})
	next := make(chan struct{})
	f.runner.Script("chatty", memexecutor.Stream(next, "12345", "67890", "abc"))
	stream := startCommand(t.Context(), t, f.conn, proxyRequestContext(), "chatty")
	if msg, err := stream.Recv(); err != nil || msg.GetStarted() == nil {
		t.Fatalf("first event = %v, %v", msg, err)
	}
	for _, want := range []string{"12345", "678"} {
		next <- struct{}{}
		msg, err := stream.Recv()
		if err != nil || string(msg.GetStdout()) != want {
			t.Fatalf("stdout = %q, %v; want %q", msg.GetStdout(), err, want)
		}
	}
	next <- struct{}{}
	msg, err := stream.Recv()
	if err != nil || msg.GetResult() == nil {
		t.Fatalf("event after the limit = %v, %v; want the result", msg, err)
	}
	result := msg.GetResult()
	if !result.GetOutputTruncated() ||
		result.GetState() != executorv1.CommandState_COMMAND_STATE_SUCCEEDED {
		t.Fatalf("result = %v", result)
	}
}

func TestRunCommandHoldsPerClientCapacity(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{clientMax: 1})
	f.runner.Script("block", memexecutor.Block("ready"))
	ctx, cancel := context.WithCancel(t.Context())
	stream := startCommand(ctx, t, f.conn, proxyRequestContext(), "block")
	for range 2 {
		if _, err := stream.Recv(); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.files.Write(t.Context(), "a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	files := executorv1.NewFileServiceClient(f.conn)
	stat := &executorv1.StatFileRequest{Context: proxyRequestContext(), Path: "a.txt"}
	if _, err := files.StatFile(t.Context(), stat); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("StatFile during a command = %v", err)
	}
	_, err := startCommand(t.Context(), t, f.conn, proxyRequestContext(), "block").Recv()
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("second RunCommand = %v", err)
	}
	cancel()
	for {
		if _, err := stream.Recv(); err != nil {
			break
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := files.StatFile(t.Context(), stat)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("capacity was not released after the stream ended")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFileProxyEnforcesTheTemplateFileLimit(t *testing.T) {
	f := newProxyFixture(t, proxyOptions{maxFileBytes: 4})
	if err := f.files.Write(t.Context(), "big.txt", []byte("12345")); err != nil {
		t.Fatal(err)
	}
	ws := executorclient.NewWorkspace(
		f.conn,
		proxyRoot,
		func(context.Context) (*executorv1.RequestContext, error) {
			return proxyRequestContext(), nil
		},
	)
	_, err := ws.Read(t.Context(), "big.txt")
	if status.Code(err) != codes.ResourceExhausted ||
		fileconformance.Reason(err) != executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED {
		t.Fatalf("oversized read = %v", err)
	}
	if _, err := ws.CreateFile(t.Context(), "new.txt", []byte("12345")); err == nil {
		t.Fatal("oversized create succeeded")
	}
	if _, err := f.files.Read(t.Context(), "new.txt"); err == nil {
		t.Fatal("oversized create reached the workspace")
	}
	if _, err := ws.CreateFile(t.Context(), "ok.txt", []byte("1234")); err != nil {
		t.Fatalf("create within the limit = %v", err)
	}
}

func TestExecutorConnectorFollowsThePinnedPod(t *testing.T) {
	pki := newPlatformPKI(t, testProviderClientURI)
	cfg, fingerprint := executorServerTLS(t, "env", pki)
	files := memfs.NewWorkspace(proxyRoot)
	if err := files.Write(t.Context(), "a.txt", []byte("a")); err != nil {
		t.Fatal(err)
	}
	srv, err := fileserver.New(files, fileserver.WithPathEscapeErrors(memfs.ErrPathEscape))
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
	executorv1.RegisterFileServiceServer(server, srv)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	kube := kubefake.NewSimpleClientset(executorPodAt("executor-env", "pod-1", "127.0.0.1"))
	connector := NewExecutorDialer(kube, "ns", pki.clientDir, testProviderClientURI).
		Connector(listener.Addr().(*net.TCPAddr).Port)
	pin := ExecutorPin{PodName: "executor-env", PodUID: "pod-1", Fingerprint: fingerprint}
	stat := func() error {
		conn, release, err := connector.Connect(t.Context(), pin)
		if err != nil {
			return err
		}
		defer release()
		_, err = executorv1.NewFileServiceClient(conn).StatFile(
			t.Context(),
			&executorv1.StatFileRequest{Path: "a.txt"},
		)
		return err
	}
	if err := stat(); err != nil {
		t.Fatalf("pinned executor = %v", err)
	}
	pods := kube.CoreV1().Pods("ns")
	if err := pods.Delete(t.Context(), "executor-env", metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	replacement := executorPodAt("executor-env", "pod-2", "127.0.0.1")
	if _, err := pods.Create(t.Context(), replacement, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := stat(); status.Code(err) != codes.Unavailable {
		t.Fatalf("replaced Pod at the same address = %v", err)
	}
}

func TestExecutorErrorDropsNotForwarded(t *testing.T) {
	claimed := notForwarded(status.Error(codes.PermissionDenied, "denied"))
	err := executorError(claimed)
	if status.Code(err) != codes.PermissionDenied || refusedBeforeForwarding(status.Convert(err)) {
		t.Fatalf("executorError(%v) = %v", claimed, err)
	}
	limit := limitExceeded("too big")
	got := executorError(limit)
	if reasonOf(got) != executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED {
		t.Fatalf("executorError(%v) = %v", limit, got)
	}
}

func reasonOf(err error) executorv1.ErrorReason {
	for _, d := range status.Convert(err).Details() {
		if detail, ok := d.(*executorv1.ErrorDetail); ok {
			return detail.GetReason()
		}
	}
	return executorv1.ErrorReason_ERROR_REASON_UNSPECIFIED
}
