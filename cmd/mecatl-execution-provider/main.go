// Command mecatl-execution-provider runs the authenticated Kubernetes execution provider.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	executionv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/execution/v1"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/executioncontroller"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
	"github.com/stacklok/mecatl/internal/executionenv"
)

func main() {
	if err := run(); err != nil {
		slog.Error("execution provider stopped", "error", err)
		os.Exit(1)
	}
}
func run() error { //nolint:gocyclo // Startup validation and owned-resource shutdown stay in one composition root.
	var addr, healthAddr, namespace, profilesPath, manifestPath, keyDirectory, authorityConfigMap string
	var reloadInterval time.Duration
	var maxConcurrentStreams, maxConcurrentRPCs, maxConcurrentRPCsPerClient int
	flag.StringVar(&addr, "listen", ":8443", "gRPC listen address")
	flag.StringVar(&namespace, "namespace", "", "managed Kubernetes namespace")
	flag.StringVar(&profilesPath, "profiles", "/etc/mecatl-execution/profiles.yaml", "strict operator profile file")
	flag.StringVar(&healthAddr, "health-listen", ":8081", "operational HTTP health listen address; empty disables")
	flag.StringVar(&manifestPath, "grant-keyring-manifest", "/etc/mecatl-execution/security/manifest.json", "versioned security manifest")
	flag.StringVar(&keyDirectory, "grant-key-directory", "/etc/mecatl-execution/security", "projected security key and TLS directory")
	flag.StringVar(&authorityConfigMap, "security-authority-configmap", "", "provider-owned security generation high-water ConfigMap")
	flag.DurationVar(&reloadInterval, "security-reload-interval", 2*time.Second, "security material reload interval")
	flag.IntVar(&maxConcurrentStreams, "max-concurrent-streams", 64, "maximum concurrent HTTP/2 streams per connection")
	flag.IntVar(&maxConcurrentRPCs, "max-concurrent-rpcs", 128, "maximum active provider RPCs")
	flag.IntVar(&maxConcurrentRPCsPerClient, "max-concurrent-rpcs-per-client", 32, "maximum active provider RPCs per authorized client")
	flag.Parse()
	if namespace == "" || authorityConfigMap == "" || reloadInterval <= 0 || reloadInterval > time.Minute || maxConcurrentStreams < 1 || maxConcurrentStreams > 1024 || maxConcurrentRPCs < 1 || maxConcurrentRPCs > 4096 || maxConcurrentRPCsPerClient < 1 || maxConcurrentRPCsPerClient > maxConcurrentRPCs {
		return errors.New("required identity, security reload interval, or RPC concurrency bounds are invalid")
	}
	profiles, err := executioncontroller.LoadProfiles(profilesPath)
	if err != nil {
		return err
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("build in-cluster Kubernetes config: %w", err)
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	security := executioncontroller.NewSecurityManager(manifestPath, keyDirectory, namespace, authorityConfigMap, kube)
	if err := security.Reload(context.Background()); err != nil {
		return fmt.Errorf("load security material: %w", err)
	}
	podexec := executioncontroller.NewPodExecutor(cfg, kube, namespace)
	store := executioncontroller.NewStore(dyn, namespace, profiles, podexec).WithKubeClient(kube)
	reconciler := executioncontroller.NewReconciler(dyn, kube, namespace, profiles)
	handler := executioncontroller.NewHandler(executioncontroller.HandlerConfig{Security: security, Ready: func() bool { return reconciler.Ready() && security.Ready() }, Diagnostics: slogdiag.New(os.Stderr, true, port.LevelInfo)}, store)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	go security.Run(ctx, reloadInterval)
	if err := reconciler.Initialize(ctx); err != nil {
		return fmt.Errorf("initialize controller: %w", err)
	}
	controllerErr := make(chan error, 1)
	go func() { controllerErr <- reconciler.Run(ctx) }()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	limiter, err := executioncontroller.NewRPCLimiter(security, maxConcurrentRPCs, maxConcurrentRPCsPerClient)
	if err != nil {
		return err
	}
	server := grpc.NewServer(
		grpc.Creds(credentials.NewTLS(security.TLSConfig())),
		grpc.ChainUnaryInterceptor(limiter.UnaryInterceptor),
		grpc.MaxRecvMsgSize(executionenv.MaxMessageBytes),
		grpc.MaxSendMsgSize(executionenv.MaxMessageBytes),
		grpc.MaxConcurrentStreams(uint32(maxConcurrentStreams)), //nolint:gosec // validated to 1..1024 above.
		grpc.KeepaliveParams(keepalive.ServerParameters{MaxConnectionAge: 30 * time.Minute, MaxConnectionAgeGrace: 2 * time.Minute, Time: 2 * time.Minute, Timeout: 20 * time.Second}),
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 30 * time.Second, PermitWithoutStream: false}),
	)
	executionv1.RegisterExecutionProviderServiceServer(server, handler)
	var healthServer *http.Server
	if healthAddr != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
			if !reconciler.Ready() {
				http.Error(w, "not ready: "+reconciler.ReadinessReason(), http.StatusServiceUnavailable)
				return
			}
			readyCtx, stop := context.WithTimeout(r.Context(), 2*time.Second)
			defer stop()
			if !security.CheckReady(readyCtx) {
				http.Error(w, "not ready: security-authority-or-expiry", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		healthServer = &http.Server{Addr: healthAddr, Handler: mux, ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second}
		go func() {
			if err := healthServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				cancel()
			}
		}()
	}
	defer func() {
		if healthServer != nil {
			shutdown, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			_ = healthServer.Shutdown(shutdown)
		}
	}()
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(ln) }()
	select {
	case err := <-controllerErr:
		cancel()
		server.GracefulStop()
		if err != nil {
			return fmt.Errorf("controller: %w", err)
		}
		return nil
	case err := <-serveErr:
		cancel()
		if err != nil {
			return err
		}
		return nil
	case <-ctx.Done():
		done := make(chan struct{})
		go func() { server.GracefulStop(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			server.Stop()
		}
		return nil
	}
}
