package boxenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderLifecycleAndWorkspace(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoxAPI(t)
	provider, err := New(Config{
		APIKey:       "test-key",
		BaseURL:      fake.server.URL,
		HTTPClient:   fake.server.Client(),
		Scope:        "test",
		TTLSeconds:   60,
		ReadyTimeout: 2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider.client.poll = time.Millisecond

	ctx := context.Background()
	binding, err := provider.Bind(ctx, server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Ref.Kind != Kind || !binding.Ref.Valid() {
		t.Fatalf("ref = %+v", binding.Ref)
	}
	if got := fake.lastCreateNoEnv(); !got {
		t.Fatal("Box create did not force noEnv=true")
	}

	ws := binding.Environment.Workspace()
	if ws == nil {
		t.Fatal("nil workspace")
	}
	runner := binding.Environment.CommandRunner()
	if runner == nil {
		t.Fatal("nil runner")
	}
	bound, ok := runner.(interface{ BoundWorkspaceRoot() string })
	if !ok {
		t.Fatal("runner does not expose its bound workspace root")
	}
	if bound.BoundWorkspaceRoot() != ws.Root() {
		t.Fatalf("runner/workspace affinity mismatch: runner=%q workspace=%q", bound.BoundWorkspaceRoot(), ws.Root())
	}

	v1, err := ws.CreateFile(ctx, "dir/a.txt", []byte("one\ntwo\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, readVersion, err := ws.ReadVersion(ctx, "dir/a.txt")
	if err != nil || string(data) != "one\ntwo\n" || !v1.Equal(readVersion) {
		t.Fatalf("read = %q, versionEqual=%v, err=%v", data, v1.Equal(readVersion), err)
	}
	if _, err := ws.ReplaceFile(ctx, "dir/a.txt", tool.NewFileVersion("stale"), []byte("bad")); err == nil {
		t.Fatal("stale replace succeeded")
	} else {
		var mismatch *tool.VersionMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("stale replace error = %T %v", err, err)
		}
	}
	v2, err := ws.ReplaceFile(ctx, "dir/a.txt", readVersion, []byte("two\nneedle\n"))
	if err != nil || v2.Equal(readVersion) {
		t.Fatalf("replace version=%v err=%v", v2, err)
	}

	if _, err := ws.CreateFile(ctx, "dir/a.txt", []byte("duplicate")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate create = %v", err)
	}
	if _, err := ws.Read(ctx, "../escape"); err == nil {
		t.Fatal("path escape accepted")
	}

	ns, ok := ws.(tool.WorkspaceNamespace)
	if !ok {
		t.Fatal("Box workspace does not expose namespace operations")
	}
	if _, err := ns.CopyFile(ctx, "dir/a.txt", "dir/b.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := ns.ReadDir(ctx, "dir")
	if err != nil || len(entries) != 2 {
		t.Fatalf("readdir = %+v, err=%v", entries, err)
	}
	matches, err := ws.Grep(ctx, "needle", "**/*.txt")
	if err != nil || len(matches) != 2 {
		t.Fatalf("grep = %+v, err=%v", matches, err)
	}
	paths, err := ws.Glob(ctx, "**/*.txt")
	if err != nil || len(paths) != 2 {
		t.Fatalf("glob = %+v, err=%v", paths, err)
	}
	if err := ns.Rename(ctx, "dir/b.txt", "dir/c.txt"); err != nil {
		t.Fatal(err)
	}
	if err := ns.Remove(ctx, "dir/c.txt"); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Run(ctx, "printf shell > shell.txt"); err != nil {
		t.Fatal(err)
	}
	if got, err := ws.Read(ctx, "shell.txt"); err != nil || string(got) != "shell" {
		t.Fatalf("shell/read affinity = %q, %v", got, err)
	}

	if binding.Close == nil {
		t.Fatal("binding has no provisional close")
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	if state := fake.state(binding.Ref.ID); state != "archived" {
		t.Fatalf("state after close = %q", state)
	}

	rebound, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: binding.Ref, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if state := fake.state(binding.Ref.ID); state != "idle" {
		t.Fatalf("state after reattach = %q", state)
	}
	if got, err := rebound.Environment.Workspace().Read(ctx, "shell.txt"); err != nil || string(got) != "shell" {
		t.Fatalf("reattached content = %q, %v", got, err)
	}

	stale := binding.Ref
	stale.Revision = "old"
	if _, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: stale, Scope: "test"}); !errors.Is(err, server.ErrPlacementStale) {
		t.Fatalf("stale reattach = %v", err)
	}
}

func TestConcurrentReplaceHasOneWinner(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoxAPI(t)
	provider, err := New(Config{APIKey: "test-key", BaseURL: fake.server.URL, HTTPClient: fake.server.Client(), Scope: "test", TTLSeconds: 60, ReadyTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	provider.client.poll = time.Millisecond
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binding.Close() }()
	ws := binding.Environment.Workspace()
	v, err := ws.CreateFile(context.Background(), "race.txt", []byte("base"))
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, text := range []string{"left", "right"} {
		text := text
		go func() {
			<-start
			_, err := ws.ReplaceFile(context.Background(), "race.txt", v, []byte(text))
			results <- err
		}()
	}
	close(start)
	var success, conflict int
	for range 2 {
		err := <-results
		if err == nil {
			success++
			continue
		}
		var mismatch *tool.VersionMismatchError
		if errors.As(err, &mismatch) {
			conflict++
		} else {
			t.Fatalf("unexpected replace error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestNoFSAndConfigurationGuards(t *testing.T) {
	fake := newFakeBoxAPI(t)
	provider, err := New(Config{APIKey: "test-key", BaseURL: fake.server.URL, HTTPClient: fake.server.Client(), Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.NoFSPlacement(), Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil || binding.Ref.Kind != session.EnvKindNoFS || binding.Environment.CommandRunner() != nil {
		t.Fatalf("no-fs binding=%+v err=%v", binding.Ref, err)
	}
	if fake.createCount() != 0 {
		t.Fatal("no-fs binding provisioned a Box")
	}

	if _, err := New(Config{Scope: "test"}); err == nil {
		t.Fatal("missing API key accepted")
	}
	if _, err := New(Config{APIKey: "secret", Scope: "test", Workdir: "../escape"}); err == nil {
		t.Fatal("escaping workdir accepted")
	}
}

func requireLocalHelper(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("local Box helper contract fixture requires a POSIX host")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to exercise the same helper used inside Box")
	}
}

type fakeBoxAPI struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	next    int
	boxes   map[string]*fakeBox
	creates []createBoxRequest
}

type fakeBox struct {
	state string
	root  string
}

func newFakeBoxAPI(t *testing.T) *fakeBoxAPI {
	t.Helper()
	f := &fakeBoxAPI{t: t, boxes: map[string]*fakeBox{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeBoxAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/boxes" && r.Method == http.MethodPost {
		var req createBoxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		root := f.t.TempDir()
		f.mu.Lock()
		f.next++
		id := fmt.Sprintf("box-%d", f.next)
		f.boxes[id] = &fakeBox{state: "idle", root: root}
		f.creates = append(f.creates, req)
		f.mu.Unlock()
		writeJSON(w, http.StatusAccepted, boxEnvelope{Box: boxState{ID: id, State: "idle"}})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 2 || parts[0] != "boxes" {
		http.NotFound(w, r)
		return
	}
	id := parts[1]
	f.mu.Lock()
	box := f.boxes[id]
	f.mu.Unlock()
	if box == nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodGet {
		f.mu.Lock()
		state := box.state
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, boxEnvelope{Box: boxState{ID: id, State: state}})
		return
	}
	if len(parts) != 3 || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	switch parts[2] {
	case "stop":
		f.mu.Lock()
		box.state = "archived"
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	case "resume":
		var req resumeBoxRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.NoEnv {
			http.Error(w, "resume must preserve noEnv", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		box.state = "idle"
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	case "commands":
		var req commandRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad command", http.StatusBadRequest)
			return
		}
		cwd := box.root
		if req.CWD != "" && req.CWD != "." {
			cwd = filepath.Join(box.root, filepath.FromSlash(req.CWD))
		}
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			http.Error(w, "mkdir", http.StatusInternalServerError)
			return
		}
		cmd := exec.CommandContext(r.Context(), "/bin/sh", "-c", req.Command)
		cmd.Dir = cwd
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		exit := 0
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				exit = exitErr.ExitCode()
			} else {
				exit = 1
			}
		}
		writeJSON(w, http.StatusOK, commandResponse{Success: exit == 0, ExitCode: exit, Stdout: stdout.String(), Stderr: stderr.String()})
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (f *fakeBoxAPI) lastCreateNoEnv() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates) > 0 && f.creates[len(f.creates)-1].NoEnv
}

func (f *fakeBoxAPI) createCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.creates)
}

func (f *fakeBoxAPI) state(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if box := f.boxes[id]; box != nil {
		return box.state
	}
	return ""
}
