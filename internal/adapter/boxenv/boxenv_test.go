package boxenv

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type fakeBoxAPI struct {
	t            *testing.T
	mu           sync.Mutex
	files        map[string][]byte
	state        string
	createCalls  int
	resumeCalls  int
	commandCalls int
	lastCreate   createBoxRequest
	lastCommand  commandRequest
}

func newFakeBoxAPI(t *testing.T) (*fakeBoxAPI, *httptest.Server) {
	t.Helper()
	fake := &fakeBoxAPI{t: t, files: make(map[string][]byte), state: "ready"}
	testServer := httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	t.Cleanup(testServer.Close)
	return fake, testServer
}

func (f *fakeBoxAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
		f.t.Errorf("Authorization = %q, want bearer test key", got)
		writeJSON(w, http.StatusUnauthorized, map[string]any{"code": "unauthorized", "message": "bad auth"})
		return
	}

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/box/v1/boxes":
		var in createBoxRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Errorf("decode create: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_request"})
			return
		}
		f.mu.Lock()
		f.createCalls++
		f.lastCreate = in
		state := f.state
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"box": map[string]any{"id": "box-1", "state": state}})

	case r.Method == http.MethodGet && r.URL.Path == "/api/box/v1/boxes/box-1":
		f.mu.Lock()
		state := f.state
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"box": map[string]any{"id": "box-1", "state": state}})

	case r.Method == http.MethodPost && r.URL.Path == "/api/box/v1/boxes/box-1/resume":
		f.mu.Lock()
		f.resumeCalls++
		f.state = "ready"
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodPost && r.URL.Path == "/api/box/v1/boxes/box-1/stop":
		f.mu.Lock()
		f.state = "stopped"
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	case r.Method == http.MethodPost && r.URL.Path == "/api/box/v1/boxes/box-1/commands":
		var in commandRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Errorf("decode command: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_request"})
			return
		}
		f.mu.Lock()
		f.commandCalls++
		f.lastCommand = in
		f.mu.Unlock()

		result := commandResult{}
		switch {
		case in.Command == "pwd":
			result.Stdout = "/home/oai/share/workspace\n"
		case in.Command == "find . -type f -print0":
			f.mu.Lock()
			for name := range f.files {
				if strings.HasPrefix(name, boxWorkspace+"/") {
					result.Stdout += "./" + strings.TrimPrefix(name, boxWorkspace+"/") + "\x00"
				}
			}
			f.mu.Unlock()
		case strings.Contains(in.Command, "stat -c"):
			result.Stdout = "f\n644\n5\n123\n"
		}
		writeJSON(w, http.StatusOK, map[string]any{"result": result})

	case r.Method == http.MethodGet && r.URL.Path == "/api/box/v1/boxes/box-1/files":
		name := r.URL.Query().Get("path")
		f.mu.Lock()
		data, ok := f.files[name]
		f.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"code": "file_not_found", "message": "missing"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"content": base64.StdEncoding.EncodeToString(data)})

	case r.Method == http.MethodPut && r.URL.Path == "/api/box/v1/boxes/box-1/files":
		var in struct {
			Path     string `json:"path"`
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			f.t.Errorf("decode write: %v", err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_request"})
			return
		}
		data, err := base64.StdEncoding.DecodeString(in.Content)
		if err != nil || in.Encoding != "base64" {
			f.t.Errorf("invalid file write encoding=%q err=%v", in.Encoding, err)
			writeJSON(w, http.StatusBadRequest, map[string]any{"code": "bad_encoding"})
			return
		}
		f.mu.Lock()
		f.files[in.Path] = data
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})

	default:
		f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.String())
		writeJSON(w, http.StatusNotFound, map[string]any{"code": "not_found", "message": "unexpected"})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newTestProvider(t *testing.T, endpoint string) *Provider {
	t.Helper()
	provider, err := NewProvider(Config{
		APIKey:       "test-key",
		BaseURL:      endpoint + "/api/box/v1",
		Scope:        "test",
		TTLSeconds:   123,
		ReadyTimeout: time.Second,
		PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	return provider
}

func TestProviderBindDefaultCreatesIsolatedBox(t *testing.T) {
	fake, endpoint := newFakeBoxAPI(t)
	provider := newTestProvider(t, endpoint.URL)
	binding, err := provider.Bind(t.Context(), server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if binding.Ref.Kind != Kind || binding.Ref.ID != "box-1" || binding.Ref.Revision != providerVersion {
		t.Fatalf("Ref = %#v", binding.Ref)
	}
	if binding.Environment.Ref() != binding.Ref || binding.Environment.Workspace().Root() != workspaceRoot {
		t.Fatalf("binding/env affinity mismatch: ref=%#v root=%q", binding.Environment.Ref(), binding.Environment.Workspace().Root())
	}
	runner := binding.Environment.CommandRunner()
	bound, ok := runner.(interface{ BoundWorkspaceRoot() string })
	if !ok || bound.BoundWorkspaceRoot() != workspaceRoot {
		t.Fatalf("runner does not report bound root %q", workspaceRoot)
	}
	result, err := runner.Run(t.Context(), "pwd")
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Run = %#v, %v", result, err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", fake.createCalls)
	}
	if !fake.lastCreate.NoEnv || fake.lastCreate.TTLSeconds != 123 {
		t.Fatalf("create request = %#v, want noEnv=true ttl=123", fake.lastCreate)
	}
	if fake.lastCommand.Cwd != boxWorkspace {
		t.Fatalf("Shell cwd = %q, want %q", fake.lastCommand.Cwd, boxWorkspace)
	}
}

func TestProviderReattachResumesExactBoxWithoutCreating(t *testing.T) {
	fake, endpoint := newFakeBoxAPI(t)
	fake.state = "stopped"
	provider := newTestProvider(t, endpoint.URL)
	ref := sessionRef("box-1")
	binding, err := provider.Reattach(t.Context(), server.PlacementReattachRequest{Ref: ref, Scope: "test"})
	if err != nil {
		t.Fatalf("Reattach: %v", err)
	}
	if binding.Ref != ref {
		t.Fatalf("Ref = %#v, want %#v", binding.Ref, ref)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.createCalls != 0 || fake.resumeCalls != 1 {
		t.Fatalf("create=%d resume=%d, want 0/1", fake.createCalls, fake.resumeCalls)
	}
}

func TestWorkspaceVersionedMutationAndGrep(t *testing.T) {
	fake, endpoint := newFakeBoxAPI(t)
	provider := newTestProvider(t, endpoint.URL)
	binding, err := provider.Bind(t.Context(), server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	ws := binding.Environment.Workspace()

	created, err := ws.CreateFile(t.Context(), "dir/a.txt", []byte("hello\nworld\n"))
	if err != nil {
		t.Fatalf("CreateFile: %v", err)
	}
	data, readVersion, err := ws.ReadVersion(t.Context(), "dir/a.txt")
	if err != nil || string(data) != "hello\nworld\n" || !created.Equal(readVersion) {
		t.Fatalf("ReadVersion data=%q created/read equal=%v err=%v", data, created.Equal(readVersion), err)
	}

	matches, err := ws.Grep(t.Context(), "hello", "**/*.txt")
	if err != nil || len(matches) != 1 || matches[0].Path != "dir/a.txt" || matches[0].Line != 1 {
		t.Fatalf("Grep = %#v, %v", matches, err)
	}

	fake.mu.Lock()
	fake.files["workspace/dir/a.txt"] = []byte("changed elsewhere")
	fake.mu.Unlock()
	_, err = ws.ReplaceFile(t.Context(), "dir/a.txt", readVersion, []byte("new"))
	var mismatch *tool.VersionMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("ReplaceFile error = %v, want VersionMismatchError", err)
	}

	if _, err := ws.CreateFile(t.Context(), "../escape", []byte("x")); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("CreateFile escape = %v, want ErrPathEscape", err)
	}
	if _, err := ws.Read(t.Context(), "/etc/passwd"); !errors.Is(err, ErrPathEscape) {
		t.Fatalf("Read escape = %v, want ErrPathEscape", err)
	}
}

func TestAPIOkFalseIsFailure(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "code": "denied", "message": "no"})
	}))
	defer testServer.Close()
	base, err := url.Parse(testServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &apiClient{base: base, apiKey: "test-key", http: testServer.Client()}
	if err := client.request(context.Background(), http.MethodGet, "/x", nil, nil, nil); err == nil {
		t.Fatal("request returned nil error for ok=false")
	}
}

func TestReadMissingWrapsFSNotExist(t *testing.T) {
	_, endpoint := newFakeBoxAPI(t)
	provider := newTestProvider(t, endpoint.URL)
	ws := &Workspace{client: provider.client, boxID: "box-1"}
	_, err := ws.Read(t.Context(), "missing.txt")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read missing = %v, want fs.ErrNotExist", err)
	}
}

func TestReadMissingENOENTWrapsFSNotExist(t *testing.T) {
	testServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"code":    "box_direct_failed",
			"message": "ENOENT: no such file or directory",
		})
	}))
	defer testServer.Close()

	provider := newTestProvider(t, testServer.URL)
	ws := &Workspace{client: provider.client, boxID: "box-1"}
	_, err := ws.Read(t.Context(), "missing.txt")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read missing = %v, want fs.ErrNotExist", err)
	}
}

func sessionRef(id string) session.EnvironmentRef {
	return session.EnvironmentRef{Kind: Kind, ID: id, Revision: providerVersion}
}
