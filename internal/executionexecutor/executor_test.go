package executionexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/executionenv"
)

func TestFileOperationsAreConfinedAndVersioned(t *testing.T) {
	root := t.TempDir()
	x, err := New(root, Limits{MaxFileBytes: 1024, MaxEntries: 100, CommandTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	ctx := context.Background()
	created, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "a/file.txt", Data: []byte("one")})
	if err != nil {
		t.Fatal(err)
	}
	read, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "a/file.txt"})
	if err != nil || string(read.Data) != "one" || read.Version != created.Version {
		t.Fatalf("read=%+v err=%v", read, err)
	}
	resolved, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileResolveAuthority, Path: "a/file.txt"})
	if err != nil || resolved.AuthorityWorkspace != root || resolved.AuthorityTarget != filepath.Join(root, "a/file.txt") {
		t.Fatalf("resolved authority=%+v err=%v", resolved.FileResponse, err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileReplace, Path: "a/file.txt", Data: []byte("two"), Version: "wrong"})
	var pe *executionenv.Error
	if !errors.As(err, &pe) || pe.Code != executionenv.CodeVersionMismatch {
		t.Fatalf("expected version mismatch: %v", err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "../../etc/passwd"})
	if err == nil {
		t.Fatal("escape accepted")
	}
	outside := filepath.Join(root, "..", "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileRead, Path: "escape"})
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("symlink escape not distinctly rejected: %v", err)
	}
	_, err = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileResolveAuthority, Path: "escape"})
	var denied *executionenv.Error
	if !errors.As(err, &denied) || denied.Code != executionenv.CodePermissionDenied {
		t.Fatalf("authority resolver accepted symlink escape: %v", err)
	}
}

func TestRenameAndCopyNeverClobber(t *testing.T) {
	x, _ := New(t.TempDir(), Limits{MaxFileBytes: 1024, MaxEntries: 100})
	defer x.Close()
	ctx := context.Background()
	_, _ = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "a", Data: []byte("a")})
	_, _ = x.Execute(ctx, executionenv.ExecutorRequest{Operation: executionenv.OpFileCreate, Path: "b", Data: []byte("b")})
	for _, op := range []executionenv.Operation{executionenv.OpFileCopy, executionenv.OpFileRename} {
		if _, err := x.Execute(ctx, executionenv.ExecutorRequest{Operation: op, Path: "a", Destination: "b"}); err == nil {
			t.Fatalf("%s clobbered", op)
		} else {
			var pe *executionenv.Error
			if !errors.As(err, &pe) || pe.Code != executionenv.CodeAlreadyExists {
				t.Fatalf("%s wrong error: %v", op, err)
			}
		}
	}
}

func TestForegroundCommandReturnsReceiptAndTimeoutTerminatesDescendants(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess regression test")
	}
	root := t.TempDir()
	ok := runExecutorHelper(t, root, executionenv.ExecutorRequest{Operation: executionenv.OpCommandStart, CommandID: "c1", Command: "printf hello", TimeoutMillis: 500})
	if ok.Command == nil || ok.Command.State != executionenv.CommandSucceeded || ok.Command.TerminalReceipt == "" || string(ok.Command.Stdout) != "hello" {
		t.Fatalf("response=%+v", ok)
	}
	timed := runExecutorHelper(t, root, executionenv.ExecutorRequest{Operation: executionenv.OpCommandStart, CommandID: "c2", Command: "sleep 10 & wait", TimeoutMillis: 20})
	if timed.Command == nil || timed.Command.State != executionenv.CommandCancelled || timed.Command.TerminalReceipt == "" {
		t.Fatalf("response=%+v", timed)
	}
}

func TestSuccessfulDetachedChildIsKilledBeforeTerminalReceipt(t *testing.T) {
	root := t.TempDir()
	result := filepath.Join(root, "result")
	response := runExecutorHelper(t, root, executionenv.ExecutorRequest{Operation: executionenv.OpCommandStart, CommandID: "detached", Command: "nohup sh -c 'sleep 0.2; echo late > result' >/dev/null 2>&1 &", TimeoutMillis: 1000})
	if response.Command == nil || response.Command.State != executionenv.CommandSucceeded || response.Command.TerminalReceipt == "" {
		t.Fatalf("response=%+v", response)
	}
	timer := time.NewTimer(400 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if _, err := os.Stat(result); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("detached writer survived terminal receipt: %v", err)
	}
}

func runExecutorHelper(t *testing.T, root string, request executionenv.ExecutorRequest) executionenv.ExecutorResponse {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestExecutorCommandHelper$")
	cmd.Env = append(os.Environ(), "MECATL_EXECUTOR_TEST_HELPER=1", "MECATL_EXECUTOR_TEST_ROOT="+root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(stdin).Encode(request); err != nil {
		t.Fatal(err)
	}
	_ = stdin.Close()
	var response executionenv.ExecutorResponse
	if err := json.NewDecoder(stdout).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("executor helper: %v", err)
	}
	return response
}

func TestExecutorCommandHelper(_ *testing.T) {
	if os.Getenv("MECATL_EXECUTOR_TEST_HELPER") != "1" {
		return
	}
	if err := EnableCommandExecution(); err != nil {
		os.Exit(2)
	}
	x, err := New(os.Getenv("MECATL_EXECUTOR_TEST_ROOT"), Limits{MaxFileBytes: 1024, MaxEntries: 100, CommandTimeout: time.Second})
	if err != nil {
		os.Exit(2)
	}
	var request executionenv.ExecutorRequest
	if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
		os.Exit(2)
	}
	response, err := x.Execute(context.Background(), request)
	if err != nil {
		os.Exit(2)
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
