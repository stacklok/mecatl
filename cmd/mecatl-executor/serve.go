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

func runServeMode(ctx context.Context, args []string) error {
	if args[0] != serveMode {
		return errors.New("unknown mode " + args[0])
	}
	flags := flag.NewFlagSet(serveMode, flag.ContinueOnError)
	listen := flags.String("listen", "", "TCP address to serve the executor gRPC services on")
	maxFileBytes := flags.Int(
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
	if *maxFileBytes <= 0 || *maxFileBytes > executionenv.MaxFileBytes {
		return fmt.Errorf("serve: -max-file-bytes must be in 1..%d", executionenv.MaxFileBytes)
	}
	if runtime.GOOS != "linux" {
		return errors.New("serve requires Linux")
	}
	if err := executionexecutor.EnableCommandExecution(); err != nil {
		return err
	}

	files, err := newFileServer(workspaceRoot, *maxFileBytes)
	if err != nil {
		return err
	}

	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		return err
	}

	supervisor := executionexecutor.NewCommandSupervisor(
		[]string{"/proc/self/exe", superviseMode},
		[]string{},
		workspaceRoot,
	)
	commands, err := commandserver.New(supervisor,
		commandserver.WithMaxTimeout(commandTimeout),
		commandserver.WithMaxOutputBytes(executionenv.MaxFileBytes),
		commandserver.WithMaxCommandBytes(executionenv.MaxCommandBytes))
	if err != nil {
		return err
	}

	go supervisor.ReapOrphans(ctx, orphanReapInterval)
	srv := grpc.NewServer(grpc.WaitForHandlers(true))
	executorv1.RegisterCommandServiceServer(srv, commands)
	executorv1.RegisterFileServiceServer(srv, files)
	go func() {
		<-ctx.Done()
		srv.Stop()
	}()

	return srv.Serve(lis)
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
