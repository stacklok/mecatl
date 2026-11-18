package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"runtime"
	"time"

	"google.golang.org/grpc"

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
}

func runServeMode(ctx context.Context, args []string) error {
	if args[0] != serveMode {
		return errors.New("unknown mode " + args[0])
	}
	flags := flag.NewFlagSet(serveMode, flag.ContinueOnError)
	listen := flags.String("listen", "", "TCP address to serve the executor gRPC services on")
	var cfg serveConfig
	flags.BoolVar(&cfg.files, "files", false, "serve the Files service")
	flags.BoolVar(&cfg.commands, "commands", false, "serve the Commands service")
	flags.IntVar(
		&cfg.maxFileBytes,
		"max-file-bytes",
		executionenv.MaxFileBytes,
		"largest file the Files service reads or writes, from the template",
	)
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *listen == "" {
		return errors.New("serve: -listen is required")
	}
	if !cfg.files && !cfg.commands {
		return errors.New("serve: enable at least one of -files and -commands")
	}
	if cfg.maxFileBytes <= 0 || cfg.maxFileBytes > executionenv.MaxFileBytes {
		return fmt.Errorf("serve: -max-file-bytes must be in 1..%d", executionenv.MaxFileBytes)
	}
	if runtime.GOOS != "linux" {
		return errors.New("serve requires Linux")
	}

	srv, err := newExecutorServer(ctx, workspaceRoot, cfg)
	if err != nil {
		return err
	}
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		return err
	}
	go func() {
		<-ctx.Done()
		srv.Stop()
	}()
	return srv.Serve(lis)
}

// newExecutorServer registers only the enabled services; with Commands
// disabled nothing starts a process.
func newExecutorServer(ctx context.Context, root string, cfg serveConfig) (*grpc.Server, error) {
	srv := grpc.NewServer(grpc.WaitForHandlers(true))
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
	)
}
