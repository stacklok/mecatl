package fileserver_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	executorv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/executor/v1"
	"github.com/stacklok/mecatl/engine/adapter/fsconformance"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/executorrpc/executorclient"
	"github.com/stacklok/mecatl/internal/executorrpc/fileconformance"
	"github.com/stacklok/mecatl/internal/executorrpc/fileserver"
)

const (
	root            = "/workspace"
	limitExceeded   = executorv1.ErrorReason_ERROR_REASON_LIMIT_EXCEEDED
	versionMismatch = executorv1.ErrorReason_ERROR_REASON_VERSION_MISMATCH
)

func newServer(
	t *testing.T,
	ws fileserver.Workspace,
	opts ...fileserver.Option,
) *fileserver.Server {
	t.Helper()
	opts = append(opts, fileserver.WithPathEscapeErrors(memfs.ErrPathEscape))
	srv, err := fileserver.New(ws, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func dial(
	t *testing.T,
	opts ...fileserver.Option,
) (*executorclient.Workspace, executorv1.FileServiceClient, *memfs.FileSystem) {
	t.Helper()
	ws, conn, backend := dialConn(t, opts...)
	return ws, executorv1.NewFileServiceClient(conn), backend
}

func dialConn(
	t *testing.T,
	opts ...fileserver.Option,
) (*executorclient.Workspace, *grpc.ClientConn, *memfs.FileSystem) {
	t.Helper()
	backend := memfs.NewFileSystem(root)
	conn := fileconformance.NewClient(
		t,
		newServer(t, memfs.NewWorkspaceOverFileSystem(backend), opts...),
	)
	return fileconformance.NewWorkspace(conn, root), conn, backend
}

func TestConformanceOverMemfs(t *testing.T) {
	fileconformance.Run(t, func(t *testing.T) fileconformance.Target {
		_, conn, backend := dialConn(t)
		return fileconformance.Target{
			Conn: conn,
			Root: root,
			External: fsconformance.ExternalAccess{
				Read: func(p string) ([]byte, error) {
					return backend.Read(context.Background(), p)
				},
				Write: func(p string, data []byte) error {
					return backend.Write(context.Background(), p, data)
				},
			},
		}
	})
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	if _, err := fileserver.New(nil); err == nil {
		t.Fatal("New accepted a nil workspace")
	}
	ws := memfs.NewWorkspace(root)
	for _, opt := range []fileserver.Option{
		fileserver.WithMaxFileBytes(0),
		fileserver.WithMaxResults(-1),
		fileserver.WithMaxPathBytes(0),
	} {
		if _, err := fileserver.New(ws, opt); err == nil {
			t.Fatal("New accepted a non-positive limit")
		}
	}
}

func wantReason(t *testing.T, err error, code codes.Code, reason executorv1.ErrorReason) {
	t.Helper()
	if status.Code(err) != code || fileconformance.Reason(err) != reason {
		t.Fatalf("err = %v (reason %v), want %v %v", err, fileconformance.Reason(err), code, reason)
	}
}

func TestFileSizeIsBoundedByMaxFileBytes(t *testing.T) {
	ctx := context.Background()
	ws, client, backend := dial(t, fileserver.WithMaxFileBytes(8))

	_, err := ws.CreateFile(ctx, "big.txt", []byte("123456789"))
	wantReason(t, unwrapStatus(err), codes.ResourceExhausted, limitExceeded)
	if _, err := backend.Read(ctx, "big.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("oversized create wrote the file: %v", err)
	}
	v, err := ws.CreateFile(ctx, "fit.txt", []byte("12345678"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ws.ReplaceFile(ctx, "fit.txt", v, []byte("123456789"))
	wantReason(t, unwrapStatus(err), codes.ResourceExhausted, limitExceeded)

	if err := backend.Write(ctx, "seeded.txt", []byte("123456789")); err != nil {
		t.Fatal(err)
	}
	stream, err := client.ReadFile(ctx, &executorv1.ReadFileRequest{Path: "seeded.txt"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = stream.Recv()
	wantReason(t, err, codes.ResourceExhausted, limitExceeded)
}

func unwrapStatus(err error) error {
	for e := err; e != nil; e = errors.Unwrap(e) {
		if _, ok := status.FromError(e); ok {
			return e
		}
	}
	return err
}

func TestLargeFilesTravelInChunks(t *testing.T) {
	ctx := context.Background()
	ws, client, _ := dial(t)
	content := bytes.Repeat([]byte("abcdefgh"), (fileconformance.ChunkBytes+5)/8+1)
	if _, err := ws.CreateFile(ctx, "large.bin", content); err != nil {
		t.Fatal(err)
	}
	stream, err := client.ReadFile(ctx, &executorv1.ReadFileRequest{Path: "large.bin"})
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	var messages int
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatal(err)
		}
		messages++
		got = append(got, msg.GetData()...)
		if msg.GetVersion() != nil {
			break
		}
	}
	if messages != 2 || !bytes.Equal(got, content) {
		t.Fatalf("read %d bytes in %d messages, want %d in 2", len(got), messages, len(content))
	}
}

func TestStreamEndedBeforeCloseWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ws, client, _ := dial(t)
	stream, err := client.CreateFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	msg := &executorv1.CreateFileRequest{Path: "a.txt", Data: []byte("x")}
	if err := stream.Send(msg); err != nil {
		t.Fatal(err)
	}
	cancel()
	if _, err := ws.CreateFile(context.Background(), "a.txt", []byte("winner")); err != nil {
		t.Fatalf("create after abandoned stream: %v", err)
	}
	if got, err := ws.Read(context.Background(), "a.txt"); err != nil || string(got) != "winner" {
		t.Fatalf("content %q err %v", got, err)
	}
}

func TestRequestValidation(t *testing.T) {
	ctx := context.Background()
	ws, client, _ := dial(t, fileserver.WithMaxPathBytes(16), fileserver.WithMaxResults(1))

	_, err := ws.Stat(ctx, "../outside")
	wantReason(t, err, codes.PermissionDenied, executorv1.ErrorReason_ERROR_REASON_PATH_ESCAPE)
	_, err = ws.Stat(ctx, strings.Repeat("a", 17))
	wantReason(t, err, codes.ResourceExhausted, limitExceeded)

	for _, p := range []string{"a.txt", "b.txt"} {
		if _, err := ws.CreateFile(ctx, p, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, err = ws.Glob(ctx, "*.txt")
	wantReason(t, err, codes.ResourceExhausted, limitExceeded)

	stream, err := client.ReplaceFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(&executorv1.ReplaceFileRequest{Path: "a.txt"}); err != nil {
		t.Fatal(err)
	}
	_, err = stream.CloseAndRecv()
	wantReason(t, err, codes.FailedPrecondition, versionMismatch)

	empty, err := client.CreateFile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := empty.CloseAndRecv(); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty stream err = %v, want InvalidArgument", err)
	}
}

// racyWorkspace checks and writes non-atomically, so only the server's
// serialization keeps concurrent creates and replacements correct.
type racyWorkspace struct {
	*memfs.Workspace
	mu       sync.Mutex
	inflight map[string]int
	peak     int
	hold     chan struct{}
	entered  chan struct{}
}

func newRacy() *racyWorkspace {
	return &racyWorkspace{Workspace: memfs.NewWorkspace(root), inflight: map[string]int{}}
}

func (r *racyWorkspace) enter(p string) func() {
	r.mu.Lock()
	r.inflight[p]++
	r.peak = max(r.peak, r.inflight[p])
	r.mu.Unlock()
	if p == "held.txt" && r.hold != nil {
		r.entered <- struct{}{}
		<-r.hold
	}
	return func() {
		r.mu.Lock()
		r.inflight[p]--
		r.mu.Unlock()
	}
}

func (r *racyWorkspace) CreateFile(
	ctx context.Context,
	p string,
	data []byte,
) (tool.FileVersion, error) {
	defer r.enter(p)()
	if _, err := r.Stat(ctx, p); err == nil {
		return tool.FileVersion{}, fs.ErrExist
	}
	return r.write(ctx, p, data)
}

func (r *racyWorkspace) ReplaceFile(
	ctx context.Context,
	p string,
	old tool.FileVersion,
	data []byte,
) (tool.FileVersion, error) {
	defer r.enter(p)()
	_, current, err := r.ReadVersion(ctx, p)
	if err != nil {
		return tool.FileVersion{}, err
	}
	if !current.Equal(old) {
		return tool.FileVersion{}, &tool.VersionMismatchError{Path: p}
	}
	return r.write(ctx, p, data)
}

func (r *racyWorkspace) write(
	ctx context.Context,
	p string,
	data []byte,
) (tool.FileVersion, error) {
	time.Sleep(time.Millisecond)
	if err := r.Write(ctx, p, data); err != nil {
		return tool.FileVersion{}, err
	}
	_, v, err := r.ReadVersion(ctx, p)
	return v, err
}

func TestConcurrentWritesSerializePerPath(t *testing.T) {
	ctx := context.Background()
	racy := newRacy()
	ws := fileconformance.NewWorkspace(fileconformance.NewClient(t, newServer(t, racy)), root)
	const writers = 16
	spellings := [2]string{"shared.txt", "./shared.txt"}

	outcomes := func(call func(i int) error) (wins, conflicts int) {
		errs := make([]error, writers)
		var wg sync.WaitGroup
		for i := range writers {
			wg.Go(func() { errs[i] = call(i) })
		}
		wg.Wait()
		for _, err := range errs {
			var mismatch *tool.VersionMismatchError
			switch {
			case err == nil:
				wins++
			case errors.Is(err, fs.ErrExist), errors.As(err, &mismatch):
				conflicts++
			default:
				t.Fatalf("unexpected error: %v", err)
			}
		}
		return wins, conflicts
	}

	wins, conflicts := outcomes(func(i int) error {
		_, err := ws.CreateFile(ctx, spellings[i%2], []byte{byte('a' + i)})
		return err
	})
	if wins != 1 || conflicts != writers-1 {
		t.Fatalf("creates: %d wins, %d conflicts", wins, conflicts)
	}
	_, v, err := ws.ReadVersion(ctx, "shared.txt")
	if err != nil {
		t.Fatal(err)
	}
	wins, conflicts = outcomes(func(i int) error {
		_, err := ws.ReplaceFile(ctx, spellings[i%2], v, []byte{byte('A' + i)})
		return err
	})
	if wins != 1 || conflicts != writers-1 {
		t.Fatalf("replacements: %d wins, %d conflicts", wins, conflicts)
	}
	racy.mu.Lock()
	defer racy.mu.Unlock()
	if racy.peak != 1 {
		t.Fatalf("peak concurrent writes on one spelling = %d, want 1", racy.peak)
	}
}

func TestWritesToDifferentPathsDoNotSerialize(t *testing.T) {
	ctx := context.Background()
	racy := newRacy()
	racy.hold, racy.entered = make(chan struct{}), make(chan struct{})
	ws := fileconformance.NewWorkspace(fileconformance.NewClient(t, newServer(t, racy)), root)

	held := make(chan error, 1)
	go func() {
		_, err := ws.CreateFile(ctx, "held.txt", []byte("x"))
		held <- err
	}()
	<-racy.entered
	otherCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := ws.CreateFile(otherCtx, "other.txt", []byte("y")); err != nil {
		t.Fatalf("create on another path while one is held: %v", err)
	}
	close(racy.hold)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
}
