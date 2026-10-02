package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// tui_multi_session_test.go holds the Scenario 1 and 2 proofs of the
// multi-session mecatui acceptance plan (docs/acceptance/tui-multi-session.md,
// ADR 0374 Decisions 1-3). Every test runs real git in t.TempDir() offline.

func tmsGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// tmsRepo is a committed repository with a sibling worktree on branch feature.
func tmsRepo(t *testing.T) (base, sibling string) {
	t.Helper()
	base = t.TempDir()
	wsInitRepo(t, base)
	wtWriteFile(t, filepath.Join(base, "README"), "base\n")
	wsCommit(t, base, "init")
	sibling = filepath.Join(t.TempDir(), "feature")
	wsRunGit(t, base, "worktree", "add", "-b", "feature", sibling)
	return base, sibling
}

func tmsResolved(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("resolve %s: %v", path, err)
	}
	return resolved
}

func tmsWorkspace(root string) tool.Workspace {
	ws, err := osfs.NewWorkspace(root)
	if err != nil {
		return nil
	}
	return ws
}

// tmsProvider is the trusted local provider with a real lister and a creator
// whose managed root is a private temp directory.
func tmsProvider(t *testing.T, root string) *localPlacementProvider {
	t.Helper()
	issuer, err := server.NewWorktreeSelectorIssuer(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Workspace: root, Shell: "/bin/sh", TrustProject: true}
	return &localPlacementProvider{
		scope: "test", root: root, workspace: tmsWorkspace,
		worktrees: buildWorktreeLister(cfg), selectors: issuer,
		creator: &worktreeCreator{
			root: root, managedRoot: filepath.Join(t.TempDir(), "state", "mecatl", "worktrees", "repo-00000000"),
			newName: randomManagedWorktreeName, git: runHardenedGit,
		},
	}
}

func tmsSiblingRef(t *testing.T, provider *localPlacementProvider, sibling, revision string) session.EnvironmentRef {
	t.Helper()
	choice, ok := provider.listedWorktree(context.Background(), sibling)
	if !ok {
		t.Fatalf("sibling %s is not listed", sibling)
	}
	return session.EnvironmentRef{Kind: session.EnvKindLocal, ID: choice.Path, Revision: revision}
}

// tmsSiblingSelector picks the sibling worktree: git lists the main worktree
// first, and the display sanitizer drops slash-bearing branch labels.
func tmsSiblingSelector(t *testing.T, choices []server.ScopedWorktree) string {
	t.Helper()
	if len(choices) != 2 || choices[1].Selector == "" {
		t.Fatalf("worktree choices = %+v, want main + sibling", choices)
	}
	return choices[1].Selector
}

func tmsBranches(t *testing.T, repo string) string {
	t.Helper()
	return tmsGitOutput(t, repo, "branch", "--list", "mecatl/*")
}

func tmsManagedEntries(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return entries
}

func tmsBuild(t *testing.T, cfg Config) *Built {
	t.Helper()
	cfg.UseMock = true
	cfg.NoSoul = true
	if cfg.MockProvider == nil {
		cfg.MockProvider = mockllm.New(mockllm.TextTurn("ok"), mockllm.TextTurn("ok"), mockllm.TextTurn("ok"))
	}
	if cfg.MemoryDir == "" {
		cfg.MemoryDir = t.TempDir()
	}
	built, err := buildIsolated(t, context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(built.Close)
	return built
}

// --- Scenario 1 -------------------------------------------------------------

func TestTUIMultiSession_Scenario1_WorktreeReattachSurvivesCommit(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	base, sibling := tmsRepo(t)
	built := tmsBuild(t, Config{Workspace: base, Shell: "/bin/sh", TrustProject: true})
	svc := built.Service

	source, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	choices, err := svc.ListWorktreesForSession(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	selector := tmsSiblingSelector(t, choices)
	moved, err := svc.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{Selector: selector, SelectorPresent: true})
	if err != nil {
		t.Fatalf("ClearSession onto worktree: %v", err)
	}
	loaded, err := svc.GetSession(ctx, moved)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.EnvironmentRef.Revision != localWorktreePlacementRevision {
		t.Fatalf("worktree ref revision = %q, want %q", loaded.EnvironmentRef.Revision, localWorktreePlacementRevision)
	}

	wtWriteFile(t, filepath.Join(sibling, "work.txt"), "committed\n")
	wsCommit(t, sibling, "work in the worktree")

	run, err := svc.StartRun(ctx, moved, "after the commit")
	if err != nil {
		t.Fatalf("StartRun after commit: %v", err)
	}
	for _, event := range runEvents(run) {
		if event.Type == session.EvResult && event.Result != nil && event.Result.Error != "" {
			t.Fatalf("run after commit failed: %s", event.Result.Error)
		}
	}
	if _, err := svc.ListWorktreesForSession(ctx, moved); err != nil {
		t.Fatalf("ListWorktrees after commit: %v", err)
	}
	if _, err := svc.ListCommandsForSession(ctx, moved); err != nil {
		t.Fatalf("ListCommands after commit: %v", err)
	}
}

func TestTUIMultiSession_Scenario1_RemovedWorktreeFailsClosed(t *testing.T) {
	base, sibling := tmsRepo(t)
	provider := tmsProvider(t, base)
	ref := tmsSiblingRef(t, provider, sibling, localWorktreePlacementRevision)
	wsRunGit(t, base, "worktree", "remove", "--force", sibling)

	workspaceCalls := 0
	provider.workspace = func(root string) tool.Workspace {
		workspaceCalls++
		return tmsWorkspace(root)
	}
	if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: ref, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("removed worktree reattach = %v, want ErrPlacementNotFound", err)
	}
	if workspaceCalls != 0 {
		t.Fatalf("workspace constructed %d times before the fail-closed decision", workspaceCalls)
	}

	// The same refusal surfaces as failed precondition at run entry.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	base2, sibling2 := tmsRepo(t)
	built := tmsBuild(t, Config{Workspace: base2, Shell: "/bin/sh", TrustProject: true})
	ctx := context.Background()
	source, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	choices, err := built.Service.ListWorktreesForSession(ctx, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	selector := tmsSiblingSelector(t, choices)
	moved, err := built.Service.ClearSessionSuccessor(ctx, source.ID, server.SuccessorPlacement{Selector: selector, SelectorPresent: true})
	if err != nil {
		t.Fatal(err)
	}
	built.Service.CloseSession(moved)
	wsRunGit(t, base2, "worktree", "remove", "--force", sibling2)
	// Today's fail-closed reattach classification (placement_selector_not_found)
	// is unchanged; it is decided before any workspace is constructed.
	if _, err := built.Service.StartRun(ctx, moved, "after removal"); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("run in a removed worktree = %v, want ErrPlacementNotFound", err)
	}
	if _, err := built.Service.ListWorktreesForSession(ctx, moved); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("ListWorktrees from a removed worktree = %v, want ErrPlacementNotFound", err)
	}
}

func TestTUIMultiSession_Scenario1_LegacyRevisionReattaches(t *testing.T) {
	base, sibling := tmsRepo(t)
	provider := tmsProvider(t, base)
	head := tmsGitOutput(t, sibling, "rev-parse", "HEAD")
	legacy := tmsSiblingRef(t, provider, sibling, head)

	// A commit moves HEAD away from the persisted legacy revision.
	wtWriteFile(t, filepath.Join(sibling, "next.txt"), "next\n")
	wsCommit(t, sibling, "next")
	rebound, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: legacy, Scope: "test"})
	if err != nil {
		t.Fatalf("legacy hex revision reattach: %v", err)
	}
	if rebound.Ref != legacy {
		t.Fatalf("reattach ref = %+v, want exact persisted %+v", rebound.Ref, legacy)
	}
	if rebound.Metadata.Revision == head {
		t.Fatal("display revision did not follow the current listing")
	}
	sha256Legacy := legacy
	sha256Legacy.Revision = strings.Repeat("a", 64)
	if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: sha256Legacy, Scope: "test"}); err != nil {
		t.Fatalf("legacy sha-256 revision reattach: %v", err)
	}

	// The configured-root ref still requires its exact revision.
	root := configuredLocalPlacementRef(base)
	if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: root, Scope: "test"}); err != nil {
		t.Fatalf("configured root reattach: %v", err)
	}
	stale := root
	stale.Revision = head
	if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: stale, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("configured root with a different revision = %v, want ErrPlacementNotFound", err)
	}
}

func TestTUIMultiSession_Scenario1_MalformedRevisionFailsClosed(t *testing.T) {
	base, sibling := tmsRepo(t)
	provider := tmsProvider(t, base)
	head := tmsGitOutput(t, sibling, "rev-parse", "HEAD")
	for _, revision := range []string{"", "worktree-v2", "configured-v1", strings.ToUpper(head), head[:39], head + "0", strings.Repeat("g", 40), "HEAD"} {
		ref := tmsSiblingRef(t, provider, sibling, revision)
		if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: ref, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
			t.Fatalf("revision %q reattach = %v, want ErrPlacementNotFound", revision, err)
		}
	}
}

// --- Scenario 2 -------------------------------------------------------------

type tmsCreateResponse struct {
	SessionID string `json:"session_id"`
	Placement struct {
		Label  string `json:"label"`
		Branch string `json:"branch"`
	} `json:"placement"`
}

func TestTUIMultiSession_Scenario2_CreateSessionInNewWorktree(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	base, _ := tmsRepo(t)
	built := tmsBuild(t, Config{Workspace: base, Shell: "/bin/sh", TrustProject: true})
	if !built.Service.CompatibilityInfo(context.Background()).GetCapabilities().GetCreateWorktrees() {
		t.Fatal("create_worktrees capability is false on a trusted local deployment")
	}

	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{"new_worktree":true}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /v1/sessions = %d: %s", resp.StatusCode, raw)
	}
	var created tmsCreateResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}

	managed := managedWorktreeRoot(state, base)
	entries := tmsManagedEntries(t, managed)
	if len(entries) != 1 || !validManagedWorktreeName(entries[0].Name()) {
		t.Fatalf("managed root entries = %v, want one mecatl-<hex8> worktree", entries)
	}
	name := entries[0].Name()
	// The label names the new worktree; the slash-bearing branch is dropped by
	// the pinned display sanitizer (TestInvariant_placement_display_rejects_path_like_metadata).
	if created.Placement.Label != name {
		t.Fatalf("placement metadata = %+v, want label %q", created.Placement, name)
	}
	if strings.Contains(string(raw), managed) || strings.Contains(string(raw), tmsResolved(t, managed)) || strings.Contains(string(raw), state) || strings.Contains(string(raw), base) {
		t.Fatalf("create response carries a server path: %s", raw)
	}
	if got := tmsGitOutput(t, filepath.Join(managed, name), "rev-parse", "--abbrev-ref", "HEAD"); got != "mecatl/"+name {
		t.Fatalf("new worktree branch = %q", got)
	}
	if tmsGitOutput(t, filepath.Join(managed, name), "rev-parse", "HEAD") != tmsGitOutput(t, base, "rev-parse", "HEAD") {
		t.Fatal("new worktree was not created from the configured root's HEAD")
	}
	sess, err := built.Service.GetSession(context.Background(), session.SessionID(created.SessionID))
	if err != nil {
		t.Fatal(err)
	}
	if sess.EnvironmentRef.Revision != localWorktreePlacementRevision || tmsResolved(t, sess.EnvironmentRef.ID) != tmsResolved(t, filepath.Join(managed, name)) {
		t.Fatalf("session ref = %+v, want the new worktree with %q", sess.EnvironmentRef, localWorktreePlacementRevision)
	}
	run, err := built.Service.StartRun(context.Background(), sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun in new worktree: %v", err)
	}
	_ = runEvents(run)
}

func TestTUIMultiSession_Scenario2_NewWorktreeGate(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		cfg  func(base string) Config
	}{
		{"untrusted", func(base string) Config { return Config{Workspace: base, Shell: "/bin/sh"} }},
		{"no shell", func(base string) Config { return Config{Workspace: base, TrustProject: true} }},
		{"non-local default", func(base string) Config {
			ref := session.EnvironmentRef{Kind: "remote", ID: "opaque", Revision: "r1"}
			return Config{Workspace: base, Shell: "/bin/sh", TrustProject: true, PlacementScope: "remote-a", PlacementProvider: &compositionPlacementProvider{binding: server.PlacementBinding{
				Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/remote"), memledger.New(), nil),
			}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("XDG_STATE_HOME", state)
			base, _ := tmsRepo(t)
			storeDir := t.TempDir()
			cfg := tc.cfg(base)
			cfg.StoreDir = storeDir
			built := tmsBuild(t, cfg)
			if built.Service.CompatibilityInfo(ctx).GetCapabilities().GetCreateWorktrees() {
				t.Fatal("create_worktrees advertised by a closed gate")
			}
			before := tmsManagedEntries(t, storeDir)
			_, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithNewWorktree())
			if !errors.Is(err, server.ErrFailedPrecondition) {
				t.Fatalf("new worktree create = %v, want failed precondition", err)
			}
			if after := tmsManagedEntries(t, storeDir); len(after) != len(before) {
				t.Fatalf("store entries %d -> %d after refused create", len(before), len(after))
			}
			if _, err := os.Stat(filepath.Join(state, "mecatl", "worktrees")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("managed worktree root touched: %v", err)
			}
			if branches := tmsBranches(t, base); branches != "" {
				t.Fatalf("branches created: %s", branches)
			}
		})
	}

	t.Run("no-fs profile", func(t *testing.T) {
		state := t.TempDir()
		t.Setenv("XDG_STATE_HOME", state)
		base, _ := tmsRepo(t)
		built := tmsBuild(t, Config{Workspace: base, Shell: "/bin/sh", TrustProject: true})
		if !built.Service.CompatibilityInfo(ctx).GetCapabilities().GetCreateWorktrees() {
			t.Fatal("create_worktrees false on an admitted deployment")
		}
		_, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS, server.WithNewWorktree())
		if !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("no-fs new worktree create = %v, want failed precondition", err)
		}
		// Both wire transports thread the field to the same refusal.
		_, err = server.NewHarnessServer(built.Service).CreateSession(ctx, &mecatlv1.CreateSessionRequest{Profile: "no-fs", NewWorktree: true})
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("gRPC no-fs new_worktree = %v, want FAILED_PRECONDITION", err)
		}
		srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
		defer srv.Close()
		resp, err := http.Post(srv.URL+"/v1/sessions", "application/json", strings.NewReader(`{"profile":"no-fs","new_worktree":true}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusPreconditionFailed {
			t.Fatalf("HTTP no-fs new_worktree = %d, want 412", resp.StatusCode)
		}
		if _, err := os.Stat(filepath.Join(state, "mecatl", "worktrees")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed worktree root touched: %v", err)
		}
		if branches := tmsBranches(t, base); branches != "" {
			t.Fatalf("branches created: %s", branches)
		}
	})

	t.Run("profiled remote provider refuses", func(t *testing.T) {
		base, _ := tmsRepo(t)
		profiled := &profilePlacementProvider{remote: &compositionPlacementProvider{}, local: tmsProvider(t, base)}
		if _, ok := any(profiled).(server.PlacementWorktreeCreator); ok {
			t.Fatal("remote-default provider advertises worktree creation")
		}
		_, err := profiled.Bind(ctx, server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true})
		if !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("profiled Bind = %v, want failed precondition", err)
		}
		if remote := profiled.remote.(*compositionPlacementProvider); len(remote.calls) != 0 {
			t.Fatal("remote provider was asked to bind a new worktree")
		}
	})
}

func TestTUIMultiSession_Scenario2_CreateRunsNoHooks(t *testing.T) {
	base, _ := tmsRepo(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(base, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Prove the hook is live for an unhardened git, so the absence below means
	// the hardened environment suppressed it.
	probe := filepath.Join(t.TempDir(), "probe")
	wsRunGit(t, base, "worktree", "add", "--detach", probe)
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("fixture hook did not fire for plain git: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}

	provider := tmsProvider(t, base)
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true})
	if err != nil {
		t.Fatalf("new worktree bind: %v", err)
	}
	if binding.Environment.Workspace() == nil {
		t.Fatal("new worktree binding has no workspace")
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-checkout hook ran during server worktree creation: %v", err)
	}
}

type tmsFailingSaveStore struct{ port.SessionStore }

func (tmsFailingSaveStore) Save(context.Context, *session.Session) error {
	return errors.New("store unavailable")
}

func TestTUIMultiSession_Scenario2_FailedCreateCleansUp(t *testing.T) {
	assertGone := func(t *testing.T, provider *localPlacementProvider, base string) {
		t.Helper()
		if entries := tmsManagedEntries(t, provider.creator.managedRoot); len(entries) != 0 {
			t.Fatalf("partial worktree left behind: %v", entries)
		}
		if branches := tmsBranches(t, base); branches != "" {
			t.Fatalf("new branch left behind: %s", branches)
		}
		if listed := tmsGitOutput(t, base, "worktree", "list", "--porcelain"); strings.Contains(listed, "mecatl-") {
			t.Fatalf("worktree still registered:\n%s", listed)
		}
	}

	t.Run("listing fails after add", func(t *testing.T) {
		base, _ := tmsRepo(t)
		provider := tmsProvider(t, base)
		provider.worktrees = &scriptedWorktreeLister{} // returns nothing after the add
		_, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true})
		if err == nil {
			t.Fatal("bind succeeded without a listed worktree")
		}
		assertGone(t, provider, base)
	})

	t.Run("workspace construction fails after add", func(t *testing.T) {
		base, _ := tmsRepo(t)
		provider := tmsProvider(t, base)
		provider.workspace = func(string) tool.Workspace { return nil }
		if _, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true}); err == nil {
			t.Fatal("bind succeeded without a workspace")
		}
		assertGone(t, provider, base)
	})

	t.Run("session persistence fails after bind", func(t *testing.T) {
		base, _ := tmsRepo(t)
		provider := tmsProvider(t, base)
		store := memstore.New()
		svc, err := newTestServerService(server.Config{
			Engine: noopEngine(), Store: tmsFailingSaveStore{SessionStore: store},
			PlacementProvider: provider, PlacementScope: "test", SharedEngineRoot: base,
		})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		if !svc.CompatibilityInfo(context.Background()).GetCapabilities().GetCreateWorktrees() {
			t.Fatal("create_worktrees false for an admitting provider")
		}
		created, err := svc.CreateSessionWithProfile(context.Background(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithNewWorktree())
		if err == nil {
			t.Fatalf("create persisted despite the failing store: %+v", created)
		}
		assertGone(t, provider, base)
	})
}

func TestTUIMultiSession_Scenario2_ConcurrentAndCollidingCreates(t *testing.T) {
	bind := func(provider *localPlacementProvider) (server.PlacementBinding, error) {
		return provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true})
	}

	t.Run("concurrent creates are distinct", func(t *testing.T) {
		base, _ := tmsRepo(t)
		provider := tmsProvider(t, base)
		const n = 4
		var wg sync.WaitGroup
		refs := make([]session.EnvironmentRef, n)
		errs := make([]error, n)
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				binding, err := bind(provider)
				refs[i], errs[i] = binding.Ref, err
			}()
		}
		wg.Wait()
		seen := map[string]bool{}
		for i := range n {
			if errs[i] != nil {
				t.Fatalf("create %d: %v", i, errs[i])
			}
			if seen[refs[i].ID] {
				t.Fatalf("two creates bound %s", refs[i].ID)
			}
			seen[refs[i].ID] = true
		}
		if got := strings.Count(tmsBranches(t, base), "mecatl/"); got != n {
			t.Fatalf("branches = %d, want %d", got, n)
		}
	})

	t.Run("colliding names are retried", func(t *testing.T) {
		base, _ := tmsRepo(t)
		provider := tmsProvider(t, base)
		// A kept branch and an existing path both collide; the third name is free.
		wsRunGit(t, base, "branch", "mecatl/mecatl-0000000a")
		if err := os.MkdirAll(filepath.Join(provider.creator.managedRoot, "mecatl-0000000b"), 0o700); err != nil {
			t.Fatal(err)
		}
		names := []string{"mecatl-0000000a", "mecatl-0000000b", "mecatl-0000000c"}
		var calls int
		provider.creator.newName = func() (string, error) {
			name := names[calls]
			calls++
			return name, nil
		}
		binding, err := bind(provider)
		if err != nil {
			t.Fatalf("create with collisions: %v", err)
		}
		if calls != 3 || filepath.Base(binding.Ref.ID) != "mecatl-0000000c" || binding.Metadata.Branch != "mecatl/mecatl-0000000c" || binding.Metadata.Label != "mecatl-0000000c" {
			t.Fatalf("calls=%d ref=%+v metadata=%+v", calls, binding.Ref, binding.Metadata)
		}

		provider.creator.newName = func() (string, error) { return "mecatl-0000000a", nil }
		if _, err := bind(provider); err == nil {
			t.Fatal("an always-colliding generator did not give up")
		}
	})

	t.Run("unborn head fails precondition", func(t *testing.T) {
		base := t.TempDir()
		wsInitRepo(t, base)
		provider := tmsProvider(t, base)
		_, err := bind(provider)
		if !errors.Is(err, server.ErrWorktreeCreationUnavailable) || !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("unborn HEAD bind = %v, want failed precondition", err)
		}
		if entries := tmsManagedEntries(t, provider.creator.managedRoot); len(entries) != 0 {
			t.Fatalf("entries created: %v", entries)
		}
		// The service maps it to FAILED_PRECONDITION through the sanitizer.
		binder, err := server.NewPlacementBinder(provider)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := binder.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true}); !errors.Is(err, server.ErrFailedPrecondition) {
			t.Fatalf("binder unborn HEAD = %v, want failed precondition", err)
		}
	})
}

// TestTUIMultiSession_Scenario2_FreshEnrollmentProvenance proves a worktree
// session is created through the ordinary fresh-root path: the same root
// authority, kind, and relationship as a default fresh create, with nothing
// carried from another session. See the final report: the ADR 0358 ledger
// accessor is not present on this branch, so the proof asserts the fresh-root
// seam that ledger will be stamped at.
func TestTUIMultiSession_Scenario2_FreshEnrollmentProvenance(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	ctx := context.Background()
	base, _ := tmsRepo(t)
	built := tmsBuild(t, Config{Workspace: base, Shell: "/bin/sh", TrustProject: true})
	fresh, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := built.Service.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault, server.WithNewWorktree())
	if err != nil {
		t.Fatal(err)
	}
	if worktree.Kind != session.SessionKindMain || worktree.Relationship != (session.SessionRelationship{}) || len(worktree.Conversation.Messages) != 0 {
		t.Fatalf("worktree session is not a fresh root: kind=%q relationship=%+v messages=%d", worktree.Kind, worktree.Relationship, len(worktree.Conversation.Messages))
	}
	if !reflect.DeepEqual(worktree.Authority, fresh.Authority) {
		t.Fatalf("worktree session authority %+v differs from a fresh root %+v", worktree.Authority, fresh.Authority)
	}
	if fmt.Sprint(worktree.Owner) != fmt.Sprint(fresh.Owner) {
		t.Fatalf("owner %v differs from fresh %v", worktree.Owner, fresh.Owner)
	}
}

// --- Decision 3 ownership (used by deletion, task 2) ------------------------

func TestTUIMultiSession_ServerCreatedWorktreeOwnership(t *testing.T) {
	ctx := context.Background()
	base, sibling := tmsRepo(t)
	provider := tmsProvider(t, base)
	binding, err := provider.Bind(ctx, server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate, NewWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if ok := provider.serverCreatedWorktree(ctx, binding.Ref); !ok {
		t.Fatal("created worktree is not reported as server-created")
	}
	if ok := provider.serverCreatedWorktree(ctx, tmsSiblingRef(t, provider, sibling, localWorktreePlacementRevision)); ok {
		t.Fatal("a user worktree outside the managed root is reported as server-created")
	}
	if ok := provider.serverCreatedWorktree(ctx, configuredLocalPlacementRef(base)); ok {
		t.Fatal("the configured root is reported as server-created")
	}

	// A symlink under the managed root pointing at a user worktree is rejected,
	// and so is a symlink pointing at a real server-created sibling.
	for name, target := range map[string]string{"mecatl-11111111": sibling, "mecatl-22222222": binding.Ref.ID} {
		link := filepath.Join(provider.creator.managedRoot, name)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if ok := provider.serverCreatedWorktree(ctx, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: link, Revision: localWorktreePlacementRevision}); ok {
			t.Fatalf("symlink %s -> %s reported as server-created", name, target)
		}
	}

	// A managed-root child git does not list is not server-created.
	stray := filepath.Join(provider.creator.managedRoot, "mecatl-33333333")
	if err := os.MkdirAll(stray, 0o700); err != nil {
		t.Fatal(err)
	}
	if ok := provider.serverCreatedWorktree(ctx, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: stray, Revision: localWorktreePlacementRevision}); ok {
		t.Fatal("an unlisted directory under the managed root is reported as server-created")
	}

	// Removal through git ends ownership.
	wsRunGit(t, base, "worktree", "remove", binding.Ref.ID)
	if ok := provider.serverCreatedWorktree(ctx, binding.Ref); ok {
		t.Fatal("a removed worktree is still reported as server-created")
	}
}

func TestTUIMultiSession_ManagedWorktreeRoot(t *testing.T) {
	got := managedWorktreeRoot("/state", "/src/my repo")
	if dir, base := filepath.Split(got); dir != "/state/mecatl/worktrees/" || !strings.HasPrefix(base, "my repo-") || len(base) != len("my repo-")+8 {
		t.Fatalf("managed root = %q", got)
	}
	if managedWorktreeRoot("/state", "/src/a/repo") == managedWorktreeRoot("/state", "/src/b/repo") {
		t.Fatal("two repositories with one directory name share a managed root")
	}
}
