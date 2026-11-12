package main

import (
	"context"
	"errors"
	"flag"
	"net"
	"runtime"
	"time"

	"google.golang.org/grpc"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executionexecutor"
	"github.com/stacklok/mecatl/internal/executorrpc/commandserver"
)

const orphanReapInterval = time.Second

func serveCommand(ctx context.Context, args []string) error {
	if args[0] != serveMode {
		return errors.New("unknown mode " + args[0])
	}
	flags := flag.NewFlagSet(serveMode, flag.ContinueOnError)
	listen := flags.String("listen", "", "TCP address to serve the executor gRPC services on")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *listen == "" {
		return errors.New("serve: -listen is required")
	}
	if runtime.GOOS != "linux" {
		return errors.New("serve requires Linux")
	}
	if err := executionexecutor.EnableCommandExecution(); err != nil {
		return err
	}
	lis, err := (&net.ListenConfig{}).Listen(ctx, "tcp", *listen)
	if err != nil {
		return err
	}
	supervisor := executionexecutor.NewCommandSupervisor(
		[]string{"/proc/self/exe", superviseMode}, []string{}, workspaceRoot)
	go supervisor.ReapOrphans(ctx, orphanReapInterval)
	srv := grpc.NewServer(grpc.WaitForHandlers(true))
	commands := commandserver.New(supervisor, commandserver.Options{
		MaxTimeout:      commandTimeout,
		MaxOutputBytes:  executionenv.MaxFileBytes,
		MaxCommandBytes: executionenv.MaxCommandBytes,
	})
	executorv1.RegisterCommandServiceServer(srv, commands)
	go func() {
		<-ctx.Done()
		srv.Stop()
	}()
	return srv.Serve(lis)
}
