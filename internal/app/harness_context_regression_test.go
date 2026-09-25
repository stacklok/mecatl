package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestDefaultHarnessInstructionsFlowToChildren(t *testing.T) {
	for _, tc := range []struct {
		name      string
		explicit  bool
		profile   server.SessionProfile
		tool      string
		args      string
		untrusted bool
		denyWrite bool
	}{
		{name: "implicit-default", profile: server.ProfileDefault, denyWrite: true},
		{name: "implicit-no-fs", profile: server.ProfileNoFS, denyWrite: true},
		{name: "explicit-local-control", explicit: true, profile: server.ProfileDefault},
		{name: "direct-write", profile: server.ProfileDefault, args: `{"prompt":"inspect the task","mode":"read-write"}`},
		{name: "parallel", profile: server.ProfileDefault, tool: "Parallel", args: `{"tasks":["inspect the task"],"join":"all"}`},
		{name: "team", profile: server.ProfileDefault, tool: "Team", args: `{"goal":"inspect the task","members":[{"name":"reader","role":"inspect once"}]}`},
		{name: "untrusted", profile: server.ProfileDefault, untrusted: true, denyWrite: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Workspace: t.TempDir(), UseMock: true, Headless: true, TrustProject: true, AllowAllTools: true, Shell: "/bin/sh", UserModelDir: t.TempDir(), NoSoul: true}
			if tc.explicit {
				kinds := harnessEmptyKinds()
				kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"local"}, Mode: "combine"}
				cfg = harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"local"}, Kinds: kinds})
				cfg.AllowAllTools = true
				cfg.Shell = "/bin/sh"
			}
			cfg.TrustProject = !tc.untrusted
			cfg.EnableTeams = tc.tool == "Team"
			cfg.EnableParallel = tc.tool == "Parallel"
			const marker = "PARENT-ADMITTED-INSTRUCTIONS"
			if err := os.WriteFile(filepath.Join(cfg.Workspace, "AGENTS.md"), []byte(marker), 0o600); err != nil {
				t.Fatal(err)
			}
			// Execution (and its forks) deliberately disagree with the startup source.
			executionRoot := t.TempDir()
			if tc.profile != server.ProfileNoFS {
				if err := os.WriteFile(filepath.Join(executionRoot, "AGENTS.md"), []byte("UNSELECTED-EXECUTION-INSTRUCTIONS"), 0o600); err != nil {
					t.Fatal(err)
				}
				workspace, err := osfs.NewWorkspace(executionRoot)
				if err != nil {
					t.Fatal(err)
				}
				ref := configuredLocalPlacementRef(executionRoot)
				placement := server.PlacementBinding{Ref: ref, Environment: tool.MustEnvironment(ref, workspace, memledger.New(), nil)}
				cfg.PlacementProvider = &harnessExecutionProvider{bindings: map[session.EnvironmentRef]server.PlacementBinding{ref: placement}}
				cfg.PlacementScope = "default-child-test"
			}
			delegateTool, args := tc.tool, tc.args
			if delegateTool == "" {
				delegateTool = "Subagent"
			}
			if args == "" {
				args = `{"prompt":"inspect the task"}`
			}
			turns := []mockllm.Turn{mockllm.ToolCallTurn(session.ToolCall{ID: "delegate", Name: delegateTool, Args: json.RawMessage(args)})}
			if tc.denyWrite {
				turns = append(turns, mockllm.ToolCallTurn(session.ToolCall{ID: "forbidden-write", Name: "Write", Args: json.RawMessage(`{"path":"must-not-exist.txt","content":"forbidden"}`)}))
			}
			turns = append(turns, mockllm.TextTurn("child done"))
			if tc.tool == "Team" {
				turns = append(turns, mockllm.TextTurn("team report"))
			}
			turns = append(turns, mockllm.TextTurn("parent done"))
			var requests []port.LLMRequest
			var requestsMu sync.Mutex
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				requestsMu.Lock()
				defer requestsMu.Unlock()
				requests = append(requests, r)
			})}, turns...)
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			sess, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, tc.profile)
			if err != nil {
				t.Fatal(err)
			}
			harnessRun(t, built, t.Context(), sess.ID, "delegate")
			requestsMu.Lock()
			defer requestsMu.Unlock()
			wantRequests := 3
			if tc.tool == "Team" {
				wantRequests++ // Team's report is a separate summarization request.
			}
			if tc.denyWrite {
				wantRequests++
			}
			if len(requests) != wantRequests {
				for i, request := range requests {
					for _, message := range request.Messages {
						if message.ToolResult != nil {
							t.Logf("request %d tool result: %+v", i, message.ToolResult)
						}
					}
				}
				t.Fatalf("got %d requests, want %d parent/child requests", len(requests), wantRequests)
			}
			wantMarkers := 1
			if tc.untrusted {
				wantMarkers = 0
			}
			for i, request := range requests {
				if tc.tool == "Team" && i == 2 {
					continue // Report summarization is not a parent or member run.
				}
				text := harnessRequestText(request)
				if count := strings.Count(text, marker); count != wantMarkers || strings.Contains(text, "UNSELECTED-EXECUTION-INSTRUCTIONS") {
					t.Fatalf("request %d retargeted or duplicated instructions: marker count=%d, want %d", i, count, wantMarkers)
				}
			}
			wantWritable := tc.name == "direct-write" || tc.name == "parallel"
			if requestHasTool(requests[1], "Write") != wantWritable {
				t.Fatalf("child Write capability changed, want writable=%v", wantWritable)
			}
			for _, name := range []string{"Subagent", "Parallel"} {
				if requestHasTool(requests[1], name) {
					t.Fatalf("child gained delegation tool %s", name)
				}
			}
			if tc.denyWrite {
				for _, spec := range requests[1].Tools {
					if spec.Name == "Write" || spec.Name == "Edit" || spec.Name == "Subagent" || spec.Name == "Parallel" || (tc.profile == server.ProfileNoFS && spec.Name == "Read") {
						t.Fatalf("child gained %s", spec.Name)
					}
				}
				rejected := false
				for _, message := range requests[2].Messages {
					if message.ToolResult != nil && message.ToolResult.CallID == "forbidden-write" {
						rejected = message.ToolResult.IsError
					}
				}
				if !rejected {
					t.Fatal("unauthorized child Write was not rejected")
				}
				for _, root := range []string{executionRoot, cfg.Workspace} {
					if _, err := os.Stat(filepath.Join(root, "must-not-exist.txt")); !os.IsNotExist(err) {
						t.Fatalf("unauthorized child Write changed %s: %v", root, err)
					}
				}
			}
		})
	}
}

type blockedCommandSource struct {
	calls   atomic.Int32
	entered chan struct{}
	resume  chan struct{}
	closed  atomic.Bool
}

func (s *blockedCommandSource) List(context.Context) ([]prompt.Command, error) {
	if s.calls.Add(1) == 2 {
		close(s.entered)
		<-s.resume
	}
	if s.closed.Load() {
		return nil, errors.New("source closed during List")
	}
	return []prompt.Command{{Name: "x"}}, nil
}
func (*blockedCommandSource) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}

func TestHarnessCommandShutdownWaitsForUnpublishedCreation(t *testing.T) {
	source := &blockedCommandSource{entered: make(chan struct{}), resume: make(chan struct{})}
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "process", Scope: HarnessSourceScopeProcess, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return source, func() error { source.closed.Store(true); return nil }, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"process"}, mode: harnessModeReplace}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	done := make(chan error, 1)
	exited := make(chan struct{})
	var unblock sync.Once
	t.Cleanup(func() {
		unblock.Do(func() { close(source.resume) })
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("creation worker did not exit")
		}
	})
	go func() {
		defer close(exited)
		_, release, err := r.Borrow(t.Context(), "new-session", nil, "")
		if release != nil {
			release()
		}
		done <- err
	}()
	select {
	case <-source.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("List did not start")
	}
	r.Close()
	premature := source.closed.Load()
	unblock.Do(func() { close(source.resume) })
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("borrow published despite shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("borrow failed to drain")
	}
	if premature {
		t.Fatal("process source closed while unpublished first binding was still reading it")
	}
	if !source.closed.Load() {
		t.Fatal("source not closed after read drained")
	}
}

// Check the publication lock directly rather than relying on a sleep to guess
// when a binder has reached its mutex wait.
type publicationCheckedContext struct {
	context.Context
	mu       *sync.Mutex
	unlocked bool
}

func (c *publicationCheckedContext) Err() error {
	if c.mu.TryLock() {
		c.unlocked = true
		c.mu.Unlock()
	}
	return c.Context.Err()
}

func TestLazyCommandBindingCancellationBeforePublication(t *testing.T) {
	for _, scope := range []string{"principal", "process"} {
		for _, bindErr := range []error{nil, errors.New("bind failed")} {
			t.Run(scope+"/"+fmt.Sprint(bindErr), func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var binds, closed int
				bind := func(context.Context) (server.CommandSourceBinding, func() error, error) {
					binds++
					cleanup := func() error { closed++; return nil }
					if binds == 1 {
						cancel()
						return &hcCommands{}, cleanup, bindErr
					}
					return &hcCommands{values: map[string]string{"x": "fresh"}}, cleanup, nil
				}
				var get func(context.Context) (server.CommandSourceBinding, error)
				var closeHolder func()
				checkedCtx := &publicationCheckedContext{Context: ctx}
				if scope == "principal" {
					holder := &lazyCommandBinding{bind: bind}
					checkedCtx.mu = &holder.mu
					get, closeHolder = holder.get, func() { _ = holder.Close() }
				} else {
					holder := &lazyProcessCommandBinding{reg: HarnessSourceRegistration[server.CommandSourceBinding]{ID: "deferred", Bind: func(ctx context.Context, _ HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
						return bind(ctx)
					}}}
					checkedCtx.mu = &holder.mu
					get, closeHolder = holder.Bind, holder.Close
				}
				t.Cleanup(closeHolder)
				wantErr := bindErr
				if wantErr == nil {
					wantErr = context.Canceled
				}
				if binding, err := get(checkedCtx); binding != nil || !errors.Is(err, wantErr) || closed != 1 {
					t.Fatalf("failed attempt binding=%v err=%v cleanup=%d, want nil, %v, 1 before Close", binding, err, closed, wantErr)
				}
				if checkedCtx.unlocked {
					t.Fatal("cancellation sampled outside the publication critical section")
				}
				binding, err := get(t.Context())
				if err != nil || binding == nil || binds != 2 || closed != 1 {
					t.Fatalf("same-holder retry binding=%v err=%v binds=%d cleanup=%d", binding, err, binds, closed)
				}
				if out, ok, err := binding.Expand(t.Context(), "/x"); err != nil || !ok || out != "fresh" {
					t.Fatalf("retry expansion=%q,%v,%v", out, ok, err)
				}
				closeHolder()
				closeHolder()
				if closed != 2 {
					t.Fatalf("cleanup=%d, want each attempt closed exactly once", closed)
				}
			})
		}
	}
}

type concurrentCreationCommands struct {
	entered  chan struct{}
	resume   chan struct{}
	listErr  error
	closed   atomic.Bool
	cleanups atomic.Int32
}

func (s *concurrentCreationCommands) List(context.Context) ([]prompt.Command, error) {
	s.entered <- struct{}{}
	<-s.resume
	if s.closed.Load() {
		return nil, errors.New("source closed during List")
	}
	return nil, s.listErr
}

func (*concurrentCreationCommands) Expand(_ context.Context, input string) (string, bool, error) {
	return input, false, nil
}

func TestHarnessCommandShutdownWaitsForAllFailedCreations(t *testing.T) {
	sentinel := errors.New("list failed")
	source := &concurrentCreationCommands{entered: make(chan struct{}, 2), resume: make(chan struct{}), listErr: sentinel}
	regs := []HarnessSourceRegistration[server.CommandSourceBinding]{
		{ID: "principal", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return &hcCommands{}, nil, nil
		}},
		{ID: "process", Scope: HarnessSourceScopeProcess, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return source, func() error {
				source.closed.Store(true)
				source.cleanups.Add(1)
				return nil
			}, nil
		}},
	}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"principal", "process"}, mode: harnessModeReplace}, regs)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	remaining := 2
	var unblock sync.Once
	t.Cleanup(func() {
		unblock.Do(func() { close(source.resume) })
		resolver.Close()
		for remaining > 0 {
			select {
			case <-done:
				remaining--
			case <-time.After(5 * time.Second):
				t.Error("creation worker did not exit")
				return
			}
		}
	})
	awaitResult := func() {
		t.Helper()
		select {
		case borrowErr := <-done:
			remaining--
			if !errors.Is(borrowErr, sentinel) {
				t.Fatalf("borrow error=%v, want list failure", borrowErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("failed creation did not drain")
		}
	}
	for _, id := range []session.SessionID{"one", "two"} {
		go func() {
			_, release, borrowErr := resolver.Borrow(t.Context(), id, nil, "")
			if release != nil {
				release()
			}
			done <- borrowErr
		}()
	}
	for range 2 {
		select {
		case <-source.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent creation did not reach List")
		}
	}
	resolver.Close()
	select {
	case source.resume <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("first List did not resume")
	}
	awaitResult()
	if source.closed.Load() {
		t.Fatal("process source closed after only one creation drained")
	}
	unblock.Do(func() { close(source.resume) })
	awaitResult()
	if !source.closed.Load() || source.cleanups.Load() != 1 {
		t.Fatalf("process cleanup closed=%v calls=%d, want closed exactly once", source.closed.Load(), source.cleanups.Load())
	}
}

func TestHarnessCommandShutdownWaitsForAttemptCleanup(t *testing.T) {
	source := &concurrentCreationCommands{entered: make(chan struct{}, 2), resume: make(chan struct{})}
	cleanupEntered := make(chan struct{}, 2)
	finishCleanup := make(chan struct{})
	var attemptCloses atomic.Int32
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"principal", "process"}, mode: harnessModeReplace}, []HarnessSourceRegistration[server.CommandSourceBinding]{
		{ID: "principal", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return &hcCommands{}, func() error {
				cleanupEntered <- struct{}{}
				<-finishCleanup
				if source.closed.Load() {
					t.Error("process source closed before dependent attempt cleanup finished")
				}
				attemptCloses.Add(1)
				return nil
			}, nil
		}},
		{ID: "process", Scope: HarnessSourceScopeProcess, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
			return source, func() error { source.closed.Store(true); source.cleanups.Add(1); return nil }, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	remaining := 2
	var unblockLists, unblockCleanups sync.Once
	t.Cleanup(func() {
		unblockLists.Do(func() { close(source.resume) })
		unblockCleanups.Do(func() { close(finishCleanup) })
		resolver.Close()
		for remaining > 0 {
			select {
			case <-done:
				remaining--
			case <-time.After(5 * time.Second):
				t.Error("creation worker did not exit")
				return
			}
		}
	})
	for _, id := range []session.SessionID{"one", "two"} {
		go func() {
			_, release, err := resolver.Borrow(t.Context(), id, nil, "")
			if release != nil {
				release()
			}
			done <- err
		}()
	}
	for range 2 {
		select {
		case <-source.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("creation did not reach List")
		}
	}
	resolver.Close()
	unblockLists.Do(func() { close(source.resume) })
	for range 2 {
		select {
		case <-cleanupEntered:
		case <-time.After(5 * time.Second):
			t.Fatal("unpublished attempt did not reach cleanup")
		}
	}
	select {
	case finishCleanup <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("first cleanup did not resume")
	}
	select {
	case err := <-done:
		remaining--
		if err == nil {
			t.Fatal("creation published despite shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first creation did not drain")
	}
	if source.closed.Load() || attemptCloses.Load() != 1 {
		t.Fatal("process cleanup did not wait for the remaining attempt cleanup")
	}
	unblockCleanups.Do(func() { close(finishCleanup) })
	select {
	case err := <-done:
		remaining--
		if err == nil {
			t.Fatal("creation published despite shutdown")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("last creation did not drain")
	}
	resolver.Close()
	if source.cleanups.Load() != 1 || attemptCloses.Load() != 2 {
		t.Fatalf("process closes=%d attempt closes=%d", source.cleanups.Load(), attemptCloses.Load())
	}
}

func TestHarnessReplaceDeferredFallbackCancellationRetries(t *testing.T) {
	for _, scope := range []HarnessSourceScopeKind{HarnessSourceScopeProcess, HarnessSourceScopePrincipal} {
		t.Run(fmt.Sprint(scope), func(t *testing.T) {
			high := &hcCommands{values: map[string]string{"high": "high"}}
			var binds, cleanups int
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deferred := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "low", Scope: scope, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
				binds++
				body := "fresh"
				if binds == 1 {
					cancel()
					body = "cancelled"
				}
				return &hcCommands{values: map[string]string{"low": body}}, func() error { cleanups++; return nil }, nil
			}}
			resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"high", "low"}, mode: harnessModeReplace}, []HarnessSourceRegistration[server.CommandSourceBinding]{
				{ID: "high", Scope: HarnessSourceScopeProcess, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
					return high, nil, nil
				}},
				deferred,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(resolver.Close)
			binding, release, err := resolver.Borrow(t.Context(), "published", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(release)
			if commands, err := binding.List(t.Context()); err != nil || len(commands) != 1 || commands[0].Name != "high" || binds != 0 {
				t.Fatalf("initial winner commands=%v err=%v deferred binds=%d", commands, err, binds)
			}
			high.values = map[string]string{}
			if _, err := binding.List(ctx); !errors.Is(err, context.Canceled) || cleanups != 1 {
				t.Fatalf("cancelled deferred fallback err=%v immediate cleanups=%d", err, cleanups)
			}
			commands, err := binding.List(t.Context())
			if err != nil || len(commands) != 1 || commands[0].Name != "low" {
				t.Fatalf("fresh fallback commands=%v err=%v", commands, err)
			}
			if out, ok, err := binding.Expand(t.Context(), "/low"); err != nil || !ok || out != "fresh" || binds != 2 {
				t.Fatalf("fresh fallback expansion=%q,%v,%v binds=%d", out, ok, err, binds)
			}
			release()
			resolver.Close()
			resolver.Close()
			if cleanups != 2 {
				t.Fatalf("cleanups=%d, want cancelled and successful bindings closed once", cleanups)
			}
		})
	}
}
