package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"runtime"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executionexecutor"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
)

const orphanReapInterval = time.Second

type serveConfig struct {
	files        bool
	commands     bool
	maxFileBytes int
	keepalive    executionenv.ExecutorKeepalive
}

type serveFlags struct {
	listen       string
	healthListen string
	tlsDir       string
	trustDir     string
	cfg          serveConfig
}

func runServeMode(ctx context.Context, args []string) error {
	if args[0] != serveMode {
		return errors.New("unknown mode " + args[0])
	}
	sf, err := parseServeFlags(args[1:])
	if err != nil {
		return err
	}
	tlsConfig, err := executionexecutor.ServerTLSConfig(sf.tlsDir, sf.trustDir)
	if err != nil {
		return fmt.Errorf("serve: %w", err)
	}
	if runtime.GOOS != "linux" {
		return errors.New("serve requires Linux")
	}

	srv, err := newExecutorServer(ctx, workspaceRoot, sf.cfg, credentials.NewTLS(tlsConfig))
	if err != nil {
		return err
	}
	servers := []*grpc.Server{srv}
	listens := []string{sf.listen}
	if sf.healthListen != "" {
		servers = append(servers, newHealthServer(ctx, workspaceRoot, sf.cfg))
		listens = append(listens, sf.healthListen)
	}
	return serveAll(ctx, servers, listens)
}

// serveAll serves each server on its address until ctx ends or one fails.
func serveAll(ctx context.Context, servers []*grpc.Server, addrs []string) error {
	errs := make(chan error, len(servers))
	defer func() {
		for _, srv := range servers {
			srv.Stop()
		}
	}()
	for i, srv := range servers {
		lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", addrs[i])
		if err != nil {
			return err
		}
		go func() { errs <- srv.Serve(lis) }()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-errs:
		return err
	}
}

// newHealthServer serves only gRPC health, without TLS, for the kubelet's
// readiness probe; the status reflects whether root accepts writes.
func newHealthServer(ctx context.Context, root string, cfg serveConfig) *grpc.Server {
	var services []string
	if cfg.files {
		services = append(services, executorv1.FileService_ServiceDesc.ServiceName)
	}
	if cfg.commands {
		services = append(services, executorv1.CommandService_ServiceDesc.ServiceName)
	}
	health := executionexecutor.NewWorkspaceHealth(root, services...)
	go health.Run(ctx)
	srv := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	healthpb.RegisterHealthServer(srv, health)
	return srv
}

func parseServeFlags(args []string) (serveFlags, error) {
	flags := flag.NewFlagSet(serveMode, flag.ContinueOnError)
	sf := serveFlags{cfg: serveConfig{keepalive: executionenv.DefaultExecutorKeepalive()}}
	cfg := &sf.cfg
	flags.StringVar(&sf.listen, "listen", "", "TCP address to serve the executor gRPC services on")
	flags.StringVar(
		&sf.healthListen,
		executionenv.HealthListenFlag,
		"",
		"TCP address to serve plaintext gRPC health on, reporting whether /workspace is writable",
	)
	flags.BoolVar(&cfg.files, "files", false, "serve the Files service")
	flags.BoolVar(&cfg.commands, "commands", false, "serve the Commands service")
	flags.IntVar(
		&cfg.maxFileBytes,
		"max-file-bytes",
		executionenv.MaxFileBytes,
		"largest file the Files service reads or writes, from the template",
	)
	flags.DurationVar(
		&cfg.keepalive.Interval,
		executionenv.KeepaliveIntervalFlag,
		cfg.keepalive.Interval,
		"idle time before pinging the provider",
	)
	flags.DurationVar(
		&cfg.keepalive.Timeout,
		executionenv.KeepaliveTimeoutFlag,
		cfg.keepalive.Timeout,
		"wait for a ping reply before closing the connection and cancelling its calls",
	)
	flags.StringVar(
		&sf.tlsDir,
		"tls-dir",
		executionenv.ExecutorServerTLSDir,
		"directory holding the server tls.crt and tls.key",
	)
	flags.StringVar(
		&sf.trustDir,
		"provider-trust-dir",
		executionenv.ExecutorProviderTrustDir,
		"directory holding the provider's ca.crt and client-uri",
	)
	if err := flags.Parse(args); err != nil {
		return serveFlags{}, err
	}
	if sf.listen == "" {
		return serveFlags{}, errors.New("serve: -listen is required")
	}
	if !cfg.files && !cfg.commands {
		return serveFlags{}, errors.New("serve: enable at least one of -files and -commands")
	}
	if cfg.maxFileBytes <= 0 || cfg.maxFileBytes > executionenv.MaxFileBytes {
		return serveFlags{}, fmt.Errorf(
			"serve: -max-file-bytes must be in 1..%d",
			executionenv.MaxFileBytes,
		)
	}
	if err := cfg.keepalive.Validate(); err != nil {
		return serveFlags{}, fmt.Errorf(
			"serve: -%s, -%s: %w",
			executionenv.KeepaliveIntervalFlag,
			executionenv.KeepaliveTimeoutFlag,
			err,
		)
	}
	return sf, nil
}

// newExecutorServer registers only the enabled services; with Commands
// disabled nothing starts a process.
func newExecutorServer(
	ctx context.Context,
	root string,
	cfg serveConfig,
	creds credentials.TransportCredentials,
) (*grpc.Server, error) {
	opts := append(
		[]grpc.ServerOption{grpc.Creds(creds), grpc.WaitForHandlers(true)},
		executionexecutor.ServerKeepalive(cfg.keepalive)...,
	)
	srv := grpc.NewServer(opts...)
	if cfg.files {
		files, err := newFileServer(root, cfg.maxFileBytes)
		if err != nil {
			return nil, err
		}
		executorv1.RegisterFileServiceServer(srv, files)
	}
	if cfg.commands {
		commands, supervisor, err := newCommandServer(root)
		if err != nil {
			return nil, err
		}
		executorv1.RegisterCommandServiceServer(srv, commands)
		go supervisor.ReapOrphans(ctx, orphanReapInterval)
	}
	return srv, nil
}

func newCommandServer(
	root string,
) (*commandserver.Server, *executionexecutor.CommandSupervisor, error) {
	if err := executionexecutor.EnableCommandExecution(); err != nil {
		return nil, nil, err
	}
	supervisor := executionexecutor.NewCommandSupervisor(
		[]string{"/proc/self/exe", superviseMode},
		[]string{},
		root,
	)
	commands, err := commandserver.New(
		supervisor,
		commandserver.WithMaxTimeout(commandTimeout),
		commandserver.WithMaxOutputBytes(executionenv.MaxFileBytes),
		commandserver.WithMaxCommandBytes(executionenv.MaxCommandBytes),
	)
	if err != nil {
		return nil, nil, err
	}
	return commands, supervisor, nil
}

func newFileServer(root string, maxFileBytes int) (*fileserver.Server, error) {
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		return nil, err
	}
	return fileserver.New(
		ws,
		fileserver.WithMaxFileBytes(maxFileBytes),
		fileserver.WithMaxResults(executionenv.MaxListEntries),
		fileserver.WithMaxPathBytes(executionenv.MaxPathBytes),
		fileserver.WithPathEscapeErrors(osfs.ErrPathEscape),
		fileserver.WithStorageFullErrors(syscall.ENOSPC, syscall.EDQUOT),
	)
}
