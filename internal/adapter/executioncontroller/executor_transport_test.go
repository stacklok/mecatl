package executioncontroller

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executionenv"
	"github.com/stacklok/mecatl/internal/executorrpc/memexecutor"
)

var transportRoute = ExecutorRoute{
	MaxFileBytes:       1 << 20,
	MaxCommandBytes:    1 << 10,
	MaxCommandDuration: time.Minute,
}

func memTransport(t *testing.T, e memexecutor.Executor) *ServerTransport {
	t.Helper()
	conn := memexecutor.Dial(t, e)
	return NewServerTransport(func(ExecutorPin) (grpc.ClientConnInterface, func(), error) {
		return conn, func() {}, nil
	})
}

func requireCode(t *testing.T, err error, code executionenv.ErrorCode) {
	t.Helper()
	var controlled *executionenv.Error
	if !errors.As(err, &controlled) || controlled.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestServerTransportFileOperations(t *testing.T) {
	x := memTransport(t, memexecutor.Executor{Files: memfs.NewWorkspace("/workspace")})
	do := func(q executionenv.ExecutorRequest) (executionenv.FileResponse, error) {
		resp, err := x.Execute(t.Context(), transportRoute, q)
		return resp.FileResponse, err
	}
	created, err := do(executionenv.ExecutorRequest{
		Operation: executionenv.OpFileCreate,
		Path:      "a.txt",
		Data:      []byte("one\n"),
	})
	if err != nil || created.Version == "" {
		t.Fatalf("create=%+v err=%v", created, err)
	}
	read, err := do(executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "a.txt"})
	if err != nil || string(read.Data) != "one\n" || read.Version != created.Version {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	_, err = do(executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "a.txt"})
	requireCode(t, err, executionenv.CodeAlreadyExists)
	stale, _ := tool.EncodeFileVersion(tool.NewFileVersion("stale"))
	_, err = do(executionenv.ExecutorRequest{
		Operation: executionenv.OpFileReplace,
		Path:      "a.txt",
		Version:   stale,
		Data:      []byte("two\n"),
	})
	requireCode(t, err, executionenv.CodeVersionMismatch)
	_, err = do(executionenv.ExecutorRequest{
		Operation: executionenv.OpFileReplace,
		Path:      "a.txt",
		Version:   created.Version,
		Data:      []byte("two\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = do(executionenv.ExecutorRequest{
		Operation:   executionenv.OpFileCopy,
		Path:        "a.txt",
		Destination: "b.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	listed, err := do(executionenv.ExecutorRequest{Operation: executionenv.OpFileList, Path: "."})
	if err != nil || len(listed.Entries) != 2 {
		t.Fatalf("list=%+v err=%v", listed, err)
	}
	_, err = do(executionenv.ExecutorRequest{
		Operation: executionenv.OpFileList,
		Path:      ".",
		Limit:     1,
	})
	requireCode(t, err, executionenv.CodeResourceExhausted)
	grep, err := do(executionenv.ExecutorRequest{
		Operation: executionenv.OpFileGrep,
		Pattern:   "two",
	})
	if err != nil || len(grep.Matches) != 2 {
		t.Fatalf("grep=%+v err=%v", grep, err)
	}
	_, err = do(executionenv.ExecutorRequest{Operation: executionenv.OpFileRemove, Path: "b.txt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = do(executionenv.ExecutorRequest{Operation: executionenv.OpFileStat, Path: "b.txt"})
	requireCode(t, err, executionenv.CodeNotFound)
	_, err = do(executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "../escape"})
	requireCode(t, err, executionenv.CodePermissionDenied)
}

func TestServerTransportCommands(t *testing.T) {
	runner := memexecutor.NewRunner()
	runner.Script("ok", memexecutor.Exit("out", "err", 0))
	runner.Script("fail", memexecutor.Exit("", "", 3))
	runner.Script("block", memexecutor.Block("ready"))
	runner.Script("unknown", memexecutor.OutcomeUnknown(memexecutor.Exit("", "", 0)))
	runner.Script("loud", memexecutor.Exit(string(make([]byte, 2<<10)), "", 0))
	x := memTransport(t, memexecutor.Executor{Commands: runner})
	type result = *executionenv.CommandStatusResponse
	run := func(command string, timeout time.Duration) (result, error) {
		resp, err := x.Execute(t.Context(), transportRoute, executionenv.ExecutorRequest{
			Operation:     executionenv.OpCommandStart,
			Command:       command,
			CommandID:     "id",
			TimeoutMillis: timeout.Milliseconds(),
		})
		return resp.Command, err
	}
	ok, err := run("ok", time.Minute)
	if err != nil || ok.State != executionenv.CommandSucceeded || string(ok.Stdout) != "out" ||
		string(ok.Stderr) != "err" || ok.TerminalReceipt == "" {
		t.Fatalf("ok=%+v err=%v", ok, err)
	}
	failed, err := run("fail", time.Minute)
	if err != nil || failed.State != executionenv.CommandFailed || failed.ExitCode != 3 {
		t.Fatalf("fail=%+v err=%v", failed, err)
	}
	timedOut, err := run("block", 50*time.Millisecond)
	if err != nil || timedOut.State != executionenv.CommandCancelled {
		t.Fatalf("block=%+v err=%v", timedOut, err)
	}
	_, err = run("unknown", time.Minute)
	requireCode(t, err, executionenv.CodeFenceUnknown)
	loud, err := run("loud", time.Minute)
	if err != nil || len(loud.Stdout) <= int(transportRoute.MaxCommandBytes) {
		t.Fatalf("over-limit output must exceed the bound for the store to refuse: %v", err)
	}
}

func TestServerTransportRefusesBeforeForwarding(t *testing.T) {
	filesOnly := memTransport(t, memexecutor.Executor{Files: memfs.NewWorkspace("/workspace")})
	_, err := filesOnly.Execute(t.Context(), transportRoute, executionenv.ExecutorRequest{
		Operation:     executionenv.OpCommandStart,
		Command:       "true",
		TimeoutMillis: 1000,
	})
	requireCode(t, err, executionenv.CodeNotReady)
	unreachable := NewServerTransport(func(ExecutorPin) (grpc.ClientConnInterface, func(), error) {
		return nil, nil, errors.New("dial failed")
	})
	_, err = unreachable.Execute(context.Background(), transportRoute, executionenv.ExecutorRequest{
		Operation: executionenv.OpFileRead,
		Path:      "a.txt",
	})
	requireCode(t, err, executionenv.CodeNotReady)
}
