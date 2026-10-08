package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/prompt"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/forker"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type parkedHarnessCommands struct {
	started chan struct{}
	resume  chan struct{}
	once    *sync.Once
}

func (c parkedHarnessCommands) List(ctx context.Context) ([]prompt.Command, error) {
	c.once.Do(func() { close(c.started) })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.resume:
		return []prompt.Command{{Name: "go"}}, nil
	}
}

func (parkedHarnessCommands) Expand(_ context.Context, _ string) (string, bool, error) {
	return "EXPANDED-OLD-GENERATION", true, nil
}

func TestHarnessRetiredServiceEngineDrainsParkedOperation(t *testing.T) {
	kinds := harnessEmptyKinds()
	kinds.Instructions = permconfig.HarnessContextKind{Sources: []string{"tenant"}, Mode: "combine"}
	kinds.Commands = permconfig.HarnessContextKind{Sources: []string{"tenant"}, Mode: "combine"}
	cfg := harnessPolicyConfig(t, permconfig.HarnessContextSection{EnabledSources: []string{"tenant"}, Kinds: kinds})
	started, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var cleaned atomic.Int32
	var requests []port.LLMRequest
	resolver := &harnessCommandResolver{}
	cfg.harnessResolver = resolver
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(request port.LLMRequest) { requests = append(requests, request) })}, mockllm.TextTurn("done"))
	cfg.HarnessInstructionSources = []HarnessSourceRegistration[prompt.InstructionAssembler]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (prompt.InstructionAssembler, func() error, error) {
		return hcAssembler("INSTRUCTIONS-OLD-GENERATION"), nil, nil
	}}}
	cfg.HarnessCommandSources = []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "tenant", Scope: HarnessSourceScopePrincipal, Provenance: HarnessProvenancePolicy{Fixed: "driver"}, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return parkedHarnessCommands{started: started, resume: resume, once: &once}, func() error { cleaned.Add(1); return nil }, nil
	}}}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	sess, err := built.Service.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := built.Service.StartRun(t.Context(), sess.ID, "/go")
	if err != nil {
		t.Fatal(err)
	}
	<-started
	resolver.Retire(sess.ID)
	if _, _, err := resolver.Borrow(t.Context(), sess.ID, nil, ""); err == nil {
		t.Fatal("ordinary borrow reopened retired generation")
	}
	close(resume)
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	built.Service.CloseSession(sess.ID)
	if len(requests) != 1 {
		t.Fatalf("model requests=%d", len(requests))
	}
	var actual strings.Builder
	actual.WriteString(requests[0].System.Render())
	for _, message := range requests[0].Messages {
		actual.WriteString(message.Text)
	}
	if !strings.Contains(actual.String(), "EXPANDED-OLD-GENERATION") {
		t.Fatalf("old leased engine lost the command expansion after its parked list: %q", actual.String())
	}
	if !strings.Contains(actual.String(), "INSTRUCTIONS-OLD-GENERATION") {
		t.Fatalf("retired engine could not borrow its instruction source generation: %q", actual.String())
	}
	if cleaned.Load() != 1 {
		t.Fatalf("retired generation cleanup calls = %d, want 1", cleaned.Load())
	}
}

func TestHarnessResolverBindingDoesNotHoldGlobalLock(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	var blockedOnce sync.Once
	var cleaned atomic.Int32
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(_ context.Context, scope HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		if scope.Principal != nil && scope.Principal.Subject == "blocked" {
			blockedOnce.Do(func() { close(started) })
			<-unblock
		}
		return &hcCommands{values: map[string]string{"x": scope.Principal.Subject}}, func() error { cleaned.Add(1); return nil }, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	blocked := &session.Principal{Issuer: "issuer", Subject: "blocked"}
	other := &session.Principal{Issuer: "issuer", Subject: "other"}
	blockedDone := make(chan error, 1)
	go func() {
		_, release, borrowErr := resolver.Borrow(context.Background(), "blocked", blocked, "")
		if release != nil {
			release()
		}
		blockedDone <- borrowErr
	}()
	<-started
	binding, release, err := resolver.Borrow(t.Context(), "other", other, "")
	if err != nil {
		t.Fatalf("unrelated borrow blocked by remote binder: %v", err)
	}
	if out, ok, expandErr := binding.Expand(t.Context(), "/x"); expandErr != nil || !ok || out != "other" {
		t.Fatalf("unrelated binding = %q,%v,%v", out, ok, expandErr)
	}
	release()

	waitCtx, cancel := context.WithCancel(context.Background())
	waitDone := make(chan error, 1)
	go func() {
		_, _, waitErr := resolver.Borrow(waitCtx, "blocked", blocked, "")
		waitDone <- waitErr
	}()
	cancel()
	if waitErr := <-waitDone; !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("same-id canceled waiter = %v", waitErr)
	}

	resolver.Close()
	close(unblock)
	if bindErr := <-blockedDone; bindErr == nil {
		t.Fatal("binding published after resolver shutdown")
	}
	if cleaned.Load() != 2 {
		t.Fatalf("cleanup calls = %d, want successful unrelated and late blocked results", cleaned.Load())
	}
}

func TestHarnessInstructionBindingIdentitySeparatesSources(t *testing.T) {
	one, two := memfs.NewWorkspace("/one"), memfs.NewWorkspace("/two")
	for ws, text := range map[*memfs.Workspace]string{one: "first", two: "second"} {
		if err := ws.Write(t.Context(), "AGENTS.md", []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	entry := &commandBindingEntry{id: "one-session"}
	selected := policyInstructionAssembler{mode: harnessModeCombine, sources: []prompt.InstructionAssembler{
		fixedInstructionAssembler{id: "one", inner: prompt.RootAssembler{Source: one, SourceID: "shared", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true},
		fixedInstructionAssembler{id: "two", inner: prompt.RootAssembler{Source: two, SourceID: "shared", SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: "project"}, projectAdmitted: true},
	}}
	bound, _ := bindInstructionRoots(selected, entry)
	messages, rows, err := bound.Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(messages) != 2 || len(rows) != 2 || rows[0].SourceID == rows[1].SourceID {
		t.Fatalf("two selected bindings must not share cached source scope: messages=%v rows=%v err=%v", messages, rows, err)
	}
	invalid, _ := bindInstructionRoots(prompt.RootAssembler{Source: one, SourcePrefix: "."}, entry)
	if _, _, err := invalid.Assemble(t.Context(), []string{"."}, &session.InstructionSnapshot{}, 65536); err == nil {
		t.Fatal("binding minted a source ID for an unidentified source")
	}
}

func TestForkCorrespondenceRejectsCustomReadersAndChangedBindings(t *testing.T) {
	one, two := memfs.NewWorkspace("/same"), memfs.NewWorkspace("/same")
	root := func(source tool.WorkspaceReader, id string, trusted bool) prompt.InstructionAssembler {
		return fixedInstructionAssembler{id: "repository", inner: prompt.RootAssembler{Source: source, SourceID: id, SourcePrefix: "."}, provenance: HarnessProvenancePolicy{Fixed: harnessProjectTier}, projectAdmitted: true, repositoryBinding: trusted}
	}
	if correspondingInstructionRoots(root(one, "repository", false), root(two, "repository", false), nil, false) {
		t.Fatal("custom sources with identical display roots inherited each other's snapshots")
	}
	if correspondingInstructionRoots(root(one, "repository", true), root(two, "other", true), nil, false) {
		t.Fatal("a changed root identity inherited a repository snapshot")
	}
	if correspondingInstructionRoots(root(one, "repository", true), root(two, "repository", false), nil, false) {
		t.Fatal("a custom rebound reader inherited the first-party snapshot")
	}
	if !correspondingInstructionRoots(root(one, "repository", true), root(two, "repository", true), nil, false) {
		t.Fatal("admitted same-placement repository bindings must correspond without pointer equality")
	}
}

func TestHarnessGenerationRevisionDropsCachedInstructions(t *testing.T) {
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return &hcCommands{}, nil, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	source := memfs.NewWorkspace("/source")
	if err := source.Write(t.Context(), "AGENTS.md", []byte("old guidance")); err != nil {
		t.Fatal(err)
	}
	state := &session.InstructionSnapshot{}
	sessionID := session.SessionID("session")
	assemble := func() string {
		t.Helper()
		binding, release, borrowErr := resolver.Borrow(t.Context(), sessionID, nil, "")
		if borrowErr != nil {
			t.Fatal(borrowErr)
		}
		defer release()
		g := generationInstructions{harnessGeneration: binding.(*resolvedCommandBinding).generation, source: prompt.RootAssembler{Source: source, SourceID: "selected", SourcePrefix: "."}}
		messages, _, assembleErr := g.Assemble(t.Context(), []string{"."}, state, 65536)
		if assembleErr != nil || len(messages) != 1 {
			t.Fatalf("messages=%v err=%v", messages, assembleErr)
		}
		return messages[0].Text
	}
	first := assemble()
	// A child copied from this snapshot must not keep a source that the
	// selected generation no longer admits, even when the binding revision is unchanged.
	binding, release, err := resolver.Borrow(t.Context(), sessionID, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	other := memfs.NewWorkspace("/other")
	if err := other.Write(t.Context(), "AGENTS.md", []byte("other guidance")); err != nil {
		t.Fatal(err)
	}
	childState := *state
	childState.Directories = append([]string(nil), state.Directories...)
	childState.Scopes = append([]session.InstructionScope(nil), state.Scopes...)
	selected := generationInstructions{harnessGeneration: binding.(*resolvedCommandBinding).generation, source: prompt.RootAssembler{Source: other, SourceID: "replacement", SourcePrefix: "."}}
	messages, _, err := selected.Assemble(t.Context(), []string{"."}, &childState, 65536)
	release()
	if err != nil || len(messages) != 1 || !strings.Contains(messages[0].Text, "other guidance") || strings.Contains(fmt.Sprint(messages), "old guidance") || len(childState.Scopes) != 1 {
		t.Fatalf("removed source remained in copied child snapshot: messages=%v scopes=%v err=%v", messages, childState.Scopes, err)
	}
	if err := source.Write(t.Context(), "AGENTS.md", []byte("new guidance")); err != nil {
		t.Fatal(err)
	}
	if second := assemble(); second != first {
		t.Fatalf("same binding reread guidance: %q vs %q", first, second)
	}
	resolver.Retire(sessionID)
	sessionID = "next-session"
	if after := assemble(); !strings.Contains(after, "new guidance") || strings.Contains(after, "old guidance") {
		t.Fatalf("changed binding reused stale scope: %q", after)
	}
}

type invalidatingHarnessAssembler struct {
	closed *atomic.Bool
}

func (invalidatingHarnessAssembler) TargetScoped() bool { return false }

func (a invalidatingHarnessAssembler) Assemble(context.Context, []string, *session.InstructionSnapshot, int) ([]session.Message, []prompt.InstructionManifest, error) {
	if a.closed.Load() {
		return nil, nil, errors.New("source invalidated")
	}
	return []session.Message{session.NewSystemMessage("held source")}, []prompt.InstructionManifest{{Kind: prompt.InstructionKindTurn0, HasGuidance: true}}, nil
}

func TestChildGenerationRetainsInvalidatingSourceUntilRunEnds(t *testing.T) {
	var closed atomic.Bool
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return &hcCommands{values: map[string]string{"x": "ready"}}, func() error {
			closed.Store(true)
			return nil
		}, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	binding, releaseOwner, err := resolver.Borrow(t.Context(), "parent", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	generation := binding.(*resolvedCommandBinding).generation
	child := childGenerationInstructions{generationInstructions: generationInstructions{harnessGeneration: generation, source: invalidatingHarnessAssembler{closed: &closed}}}
	ctx, cancel := context.WithCancel(t.Context())
	if _, _, err := child.Assemble(ctx, []string{"."}, &session.InstructionSnapshot{}, 65536); err != nil {
		t.Fatal(err)
	}
	resolver.Retire("parent")
	releaseOwner()
	if closed.Load() {
		t.Fatal("parent retirement invalidated a source retained by the child run")
	}
	if _, _, err := child.Assemble(ctx, []string{"."}, &session.InstructionSnapshot{}, 65536); err != nil {
		t.Fatalf("held child read after parent retirement: %v", err)
	}
	cancel()
	for !closed.Load() {
		runtime.Gosched()
	}
	if _, _, err := (invalidatingHarnessAssembler{closed: &closed}).Assemble(t.Context(), nil, nil, 65536); err == nil {
		t.Fatal("fixture remained readable after the child released its generation")
	}
}

type blockingInvalidatingAssembler struct {
	started, unblock chan struct{}
	closed           *atomic.Int32
}

func (blockingInvalidatingAssembler) TargetScoped() bool { return false }

func (a blockingInvalidatingAssembler) Assemble(ctx context.Context, directories []string, state *session.InstructionSnapshot, maxContentBytes int) ([]session.Message, []prompt.InstructionManifest, error) {
	close(a.started)
	<-a.unblock
	if a.closed.Load() != 0 {
		return nil, nil, errors.New("source invalidated during assembly")
	}
	return hcAssembler("held source").Assemble(ctx, directories, state, maxContentBytes)
}

func TestChildUnmappedSourceDoesNotProjectScopedGuidance(t *testing.T) {
	source := memfs.NewWorkspace("/repo/website")
	for name, text := range map[string]string{"AGENTS.md": "START", "nested/AGENTS.md": "CORRECT", "website/nested/AGENTS.md": "POISON"} {
		if err := source.Write(t.Context(), name, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	messages, rows, err := (childUnmappedInstructions{source: prompt.RootAssembler{Source: source, SourceID: "source", SourcePrefix: "."}}).Assemble(t.Context(), []string{"website/nested"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(messages) != 2 || len(rows) != len(messages) || !strings.Contains(messages[0].Text, "START") || !strings.Contains(messages[1].Text, "mapping unavailable") || strings.Contains(fmt.Sprint(messages), "CORRECT") || strings.Contains(fmt.Sprint(messages), "POISON") {
		t.Fatalf("unmapped child guidance=%v rows=%v err=%v", messages, rows, err)
	}
}

func TestChildGenerationRetainsParentExecutionMapping(t *testing.T) {
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return &hcCommands{}, func() error { return nil }, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	binding, release, err := resolver.Borrow(t.Context(), "parent", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	source := memfs.NewWorkspace("/repo")
	for name, text := range map[string]string{"AGENTS.md": "ancestor", "website/AGENTS.md": "website", "website/nested/AGENTS.md": "correct", "nested/AGENTS.md": "poison"} {
		if err := source.Write(t.Context(), name, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	g := generationInstructions{harnessGeneration: binding.(*resolvedCommandBinding).generation, source: prompt.RootAssembler{Source: source, SourceID: "source", SourcePrefix: "website"}, executionRoot: "/repo/website"}
	parent, _, err := g.Assemble(t.Context(), []string{"nested"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(parent) != 3 || !strings.Contains(parent[2].Text, "correct") {
		t.Fatalf("parent scoped guidance=%v err=%v", parent, err)
	}
	shared, sharedRows, err := (childGenerationInstructions{generationInstructions: g}).Assemble(t.Context(), []string{"nested"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(shared) != 3 || len(sharedRows) != len(shared) || !strings.Contains(shared[2].Text, "correct") || strings.Contains(fmt.Sprint(shared), "poison") {
		t.Fatalf("shared-root child lost supported nested scope: messages=%v rows=%v err=%v", shared, sharedRows, err)
	}
	for _, row := range sharedRows {
		if row.Provenance != prompt.InstructionProvenanceProject {
			t.Fatalf("shared-root child manifest: %v", sharedRows)
		}
	}
	child, rows, err := (childUnmappedInstructions{source: g.source}).Assemble(t.Context(), []string{"nested"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(child) != 3 || len(rows) != len(child) {
		t.Fatalf("child=%v rows=%v err=%v", child, rows, err)
	}
	if !strings.Contains(child[0].Text, "ancestor") || !strings.Contains(child[1].Text, "website") || !strings.Contains(child[2].Text, "mapping unavailable") || strings.Contains(fmt.Sprint(child), "correct") || strings.Contains(fmt.Sprint(child), "poison") || len(parent) != 3 {
		t.Fatalf("child scope must remain unprojected without mapping: parent=%v child=%v", parent, child)
	}
}

func TestChildGitForkPreservesParentSubfolderMapping(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	home := t.TempDir()
	gitConfig := filepath.Join(t.TempDir(), "config")
	templateDir := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_STATE_HOME": filepath.Join(home, "state"), "XDG_CACHE_HOME": filepath.Join(home, "cache"),
		"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": gitConfig, "GIT_CONFIG_COUNT": "0", "GIT_TEMPLATE_DIR": templateDir,
	} {
		t.Setenv(key, value)
	}
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "website", "website", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{
		"AGENTS.md": "source-ancestor", "website/AGENTS.md": "starting-folder",
		"website/nested/AGENTS.md": "correct-nested", "website/website/nested/AGENTS.md": "poison-duplicated-path",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, name), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	initBaseGitRepo(t, repo)
	parentRoot := filepath.Join(repo, "website")
	parentWS, err := osfs.NewWorkspace(parentRoot)
	if err != nil {
		t.Fatal(err)
	}
	forked, cleanup, _, err := forker.New(newForkWorkspace()).Fork(t.Context(), testEnvironment(parentWS, nil), "hierarchy")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	if _, err := os.Stat(filepath.Join(forked.Workspace().Root(), "AGENTS.md")); err != nil {
		t.Fatalf("fork must preserve the parent's execution subtree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(forked.Workspace().Root(), "nested", "AGENTS.md")); err != nil {
		t.Fatalf("fork must expose the parent's nested subtree: %v", err)
	}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return &hcCommands{}, func() error { return nil }, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	binding, release, err := resolver.Borrow(t.Context(), "parent", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	source, err := osfs.NewWorkspace(repo)
	if err != nil {
		t.Fatal(err)
	}
	g := generationInstructions{harnessGeneration: binding.(*resolvedCommandBinding).generation, source: prompt.RootAssembler{Source: source, SourceID: "source", SourcePrefix: "website"}, executionRoot: parentRoot}
	deps := childEngineDepsForProvider(Config{UseMock: true, Model: "mock", harnessInstructions: g}, "task", mockllm.New(), testProviderModel("mock"), fixedDefaultWindow, tool.NewCatalog(), prompt.Config{}, nil)
	messages, rows, err := prompt.AssembleWithManifest(t.Context(), deps.Instructions, []string{"nested"}, &session.InstructionSnapshot{}, 65536)
	if err != nil || len(rows) != len(messages) {
		t.Fatalf("messages=%v rows=%v err=%v", messages, rows, err)
	}
	text := fmt.Sprint(messages)
	if !strings.Contains(text, "source-ancestor") || !strings.Contains(text, "starting-folder") || !strings.Contains(text, "correct-nested") || strings.Contains(text, "mapping unavailable") || strings.Contains(text, "poison-duplicated-path") {
		t.Fatalf("fork mapped to the wrong instruction subtree: %s", text)
	}
}

func TestChildHierarchyProviderFactoryScopesPreserveForkSnapshotsAndIsolation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	for _, fork := range []bool{false, true} {
		t.Run(fmt.Sprintf("fork=%v", fork), func(t *testing.T) {
			home := t.TempDir()
			for key, value := range map[string]string{
				"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, "config"),
				"XDG_DATA_HOME": filepath.Join(home, "data"), "XDG_STATE_HOME": filepath.Join(home, "state"), "XDG_CACHE_HOME": filepath.Join(home, "cache"),
				"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": filepath.Join(t.TempDir(), "config"), "GIT_CONFIG_COUNT": "0", "GIT_TEMPLATE_DIR": t.TempDir(),
			} {
				t.Setenv(key, value)
			}
			repo := t.TempDir()
			website := filepath.Join(repo, "website")
			for name, text := range map[string]string{
				"AGENTS.md": "POISON-ANCESTOR", "website/AGENTS.md": "ROOT-OLD",
				"website/nested/AGENTS.md": "NESTED-OLD", "website/nested/file.txt": "content",
				"website/website/nested/AGENTS.md": "POISON-DUPLICATE",
			} {
				path := filepath.Join(repo, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			initBaseGitRepo(t, repo)
			source, err := osfs.NewWorkspace(website)
			if err != nil {
				t.Fatal(err)
			}
			cfg := hcConfiguredFiles(t, source)
			cfg.Workspace, cfg.Shell, cfg.AllowAllTools, cfg.NoSoul, cfg.GuardrailsDisabled = website, "/bin/sh", true, true, true
			var requests []port.LLMRequest
			args := `{"prompt":"read nested file","model":"mock"}`
			if fork {
				args = `{"prompt":"read nested file","fork":true}`
			}
			secondArgs := `{"prompt":"read nested file","fork":true}`
			if fork {
				secondArgs = `{"prompt":"read nested file","model":"mock"}`
			}
			cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
				requests = append(requests, r)
				if len(requests) == 2 {
					if err := os.WriteFile(filepath.Join(website, "AGENTS.md"), []byte("ROOT-NEW"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(website, "nested", "AGENTS.md"), []byte("NESTED-NEW"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			})},
				mockllm.ToolCallTurn(session.ToolCall{ID: "parent-read", Name: "Read", Args: []byte(`{"path":"nested/file.txt"}`)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "delegate", Name: "Subagent", Args: []byte(args)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "child-read", Name: "Read", Args: []byte(`{"path":"nested/file.txt"}`)}),
				mockllm.TextTurn("child done"),
				mockllm.ToolCallTurn(session.ToolCall{ID: "delegate-sibling", Name: "Subagent", Args: []byte(secondArgs)}),
				mockllm.ToolCallTurn(session.ToolCall{ID: "sibling-read", Name: "Read", Args: []byte(`{"path":"nested/file.txt"}`)}),
				mockllm.TextTurn("sibling done"), mockllm.TextTurn("parent done"))
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer built.Close()
			events := harnessRun(t, built, t.Context(), harnessCreate(t, built, t.Context()), "read then delegate")
			if len(requests) != 8 {
				for _, ev := range events {
					if ev.ToolResult != nil {
						t.Logf("result: %+v", ev.ToolResult)
					}
				}
				t.Fatalf("provider requests=%d, want 8", len(requests))
			}
			child := harnessRequestText(requests[3])
			want, absent := "ROOT-NEW", "ROOT-OLD"
			wantNested, absentNested := "NESTED-NEW", "NESTED-OLD"
			if fork {
				want, absent, wantNested, absentNested = "ROOT-OLD", "ROOT-NEW", "NESTED-OLD", "NESTED-NEW"
			}
			if !strings.Contains(child, want) || !strings.Contains(child, wantNested) || strings.Contains(child, absent) || strings.Contains(child, absentNested) || strings.Contains(child, "POISON-ANCESTOR") || strings.Contains(child, "POISON-DUPLICATE") {
				t.Fatalf("child provider request after Read: %s", child)
			}
			sibling := harnessRequestText(requests[6])
			if !strings.Contains(sibling, absent) || !strings.Contains(sibling, absentNested) || strings.Contains(sibling, want) || strings.Contains(sibling, wantNested) || strings.Contains(sibling, "POISON-ANCESTOR") || strings.Contains(sibling, "POISON-DUPLICATE") {
				t.Fatalf("sibling inherited the other child's snapshot: %s", sibling)
			}
			parent := harnessRequestText(requests[7])
			if !strings.Contains(parent, "ROOT-OLD") || !strings.Contains(parent, "NESTED-OLD") || strings.Contains(parent, "ROOT-NEW") || strings.Contains(parent, "NESTED-NEW") {
				t.Fatalf("child discovery changed the parent's snapshot: %s", parent)
			}
		})
	}
}

func TestChildNoFSForkRetainsRootWithoutDiscoveringNested(t *testing.T) {
	source := memfs.NewWorkspace("/selected")
	for name, text := range map[string]string{"AGENTS.md": "ROOT-OLD", "nested/AGENTS.md": "NESTED-POISON"} {
		if err := source.Write(t.Context(), name, []byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	cfg := hcConfiguredFiles(t, source)
	cfg.AllowAllTools, cfg.NoSoul, cfg.GuardrailsDisabled = true, true, true
	var requests []port.LLMRequest
	cfg.MockProvider = mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(r port.LLMRequest) {
		requests = append(requests, r)
		if len(requests) == 1 {
			if err := source.Write(t.Context(), "AGENTS.md", []byte("ROOT-NEW")); err != nil {
				t.Fatal(err)
			}
		}
	})},
		mockllm.ToolCallTurn(session.ToolCall{ID: "delegate", Name: "Subagent", Args: []byte(`{"prompt":"inspect","fork":true}`)}),
		mockllm.ToolCallTurn(session.ToolCall{ID: "forbidden", Name: "Read", Args: []byte(`{"path":"nested/file.txt"}`)}),
		mockllm.TextTurn("child done"), mockllm.TextTurn("parent done"))
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	parent, err := built.Service.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileNoFS)
	if err != nil {
		t.Fatal(err)
	}
	harnessRun(t, built, t.Context(), parent.ID, "delegate")
	if len(requests) != 4 {
		t.Fatalf("requests=%d", len(requests))
	}
	child := harnessRequestText(requests[2])
	if !strings.Contains(child, "ROOT-OLD") || strings.Contains(child, "ROOT-NEW") || strings.Contains(child, "NESTED-POISON") {
		t.Fatalf("no-FS fork scope: %s", child)
	}
	for _, spec := range requests[2].Tools {
		if spec.Name == "Read" {
			t.Fatal("no-FS child gained file tools")
		}
	}
}

func TestChildGenerationCancellationKeepsActiveAssembly(t *testing.T) {
	var closed atomic.Int32
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		return &hcCommands{}, func() error { closed.Add(1); return nil }, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	binding, release, err := r.Borrow(t.Context(), "parent", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	g := binding.(*resolvedCommandBinding).generation
	source := blockingInvalidatingAssembler{started: make(chan struct{}), unblock: make(chan struct{}), closed: &closed}
	child := childGenerationInstructions{generationInstructions{harnessGeneration: g, source: source, executionRoot: "/repo/website"}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := child.Assemble(ctx, []string{"."}, &session.InstructionSnapshot{}, 65536)
		done <- err
	}()
	<-source.started
	r.Retire("parent")
	release()
	r.mu.Lock()
	beforeCancel := g.entry.refs
	r.mu.Unlock()
	cancel()
	// Wait until the run-lifetime callback has released; the operation must
	// still hold its own reference while the backend ignores cancellation.
	var operationRefs int
	for {
		r.mu.Lock()
		operationRefs = g.entry.refs
		r.mu.Unlock()
		if operationRefs < beforeCancel {
			break
		}
		runtime.Gosched()
	}
	wasClosed := closed.Load()
	close(source.unblock)
	assemblyErr := <-done
	if operationRefs == 0 || wasClosed != 0 || assemblyErr != nil {
		t.Fatalf("active assembly invalidated: refs=%d closes=%d err=%v", operationRefs, wasClosed, assemblyErr)
	}
	for closed.Load() == 0 {
		runtime.Gosched()
	}
	if closed.Load() != 1 {
		t.Fatalf("cleanup calls=%d", closed.Load())
	}
}

func TestHarnessResolverCancellationBeforePublicationReleasesAttempt(t *testing.T) {
	var binds, cleanups atomic.Int32
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(ctx context.Context, _ HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		binds.Add(1)
		if cancel, ok := ctx.Value(cancelHarnessBindKey{}).(context.CancelFunc); ok {
			cancel()
		}
		return &hcCommands{values: map[string]string{"x": "ready"}}, func() error {
			cleanups.Add(1)
			return nil
		}, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()

	ctx, cancel := context.WithCancel(t.Context())
	ctx = context.WithValue(ctx, cancelHarnessBindKey{}, cancel)
	if _, _, err := resolver.Borrow(ctx, "cancelled", nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled borrow = %v", err)
	}
	if cleanups.Load() != 1 {
		t.Fatalf("cancelled attempt cleanups = %d, want 1", cleanups.Load())
	}
	binding, release, err := resolver.Borrow(t.Context(), "cancelled", nil, "")
	if err != nil {
		t.Fatalf("retry after cancellation: %v", err)
	}
	release()
	if binding == nil || binds.Load() != 2 {
		t.Fatalf("retry binding=%v binds=%d", binding, binds.Load())
	}
}

type cancelHarnessBindKey struct{}

func TestHarnessGenerationOldReleaseCannotEvictReplacement(t *testing.T) {
	var binds, closed atomic.Int32
	oldCleanupStarted, finishOldCleanup := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(finishOldCleanup) })
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		generation := binds.Add(1)
		return &hcCommands{values: map[string]string{"x": fmt.Sprint(generation)}}, func() error {
			if generation == 1 {
				close(oldCleanupStarted)
				<-finishOldCleanup
			}
			closed.Add(1)
			return nil
		}, nil
	}}
	resolver, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer resolver.Close()
	old, releaseOld, err := resolver.Borrow(t.Context(), "s", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration := old.(*resolvedCommandBinding).generation
	resolver.Retire("s")
	leased := generationCommands{harnessGeneration: oldGeneration, source: old}
	if out, ok, expandErr := leased.Expand(t.Context(), "/x"); expandErr != nil || !ok || out != "1" {
		t.Fatalf("already-borrowed generation stopped draining after retirement: %q,%v,%v", out, ok, expandErr)
	}
	if _, _, err := resolver.Borrow(t.Context(), "s", nil, ""); err == nil {
		t.Fatal("ordinary borrow resurrected retirement")
	}
	if err := resolver.Activate(t.Context(), "s", nil, ""); err != nil {
		t.Fatal(err)
	}
	current, releaseCurrent, err := resolver.Borrow(t.Context(), "s", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseCurrent()
	if current == old {
		t.Fatal("retired generation reopened")
	}
	done := make(chan struct{})
	go func() { releaseOld(); close(done) }()
	select {
	case <-oldCleanupStarted:
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	out, ok, err := current.Expand(t.Context(), "/x")
	if err != nil || !ok || out != "2" {
		t.Fatalf("replacement during old cleanup=%q,%v,%v", out, ok, err)
	}
	unblock.Do(func() { close(finishOldCleanup) })
	<-done
	if _, _, err := leased.Expand(t.Context(), "/x"); err == nil {
		t.Fatal("drained retired generation remained usable after owner release")
	}
	if oldEntry := oldGeneration.entry; oldEntry.binding != nil || oldEntry.cleanup != nil {
		t.Fatal("drained generation retained binding or cleanup payload")
	}
	releaseOld()
	again, releaseAgain, err := resolver.Borrow(t.Context(), "s", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if again != current || binds.Load() != 2 {
		t.Fatal("old cleanup evicted replacement")
	}
	releaseAgain()
	resolver.Close()
	if err := resolver.Activate(t.Context(), "s", nil, ""); err == nil {
		t.Fatal("activation after Build shutdown")
	}
	if closed.Load() != 1 {
		t.Fatalf("in-use replacement closed early: %d", closed.Load())
	}
	releaseCurrent()
	if closed.Load() != 2 {
		t.Fatalf("cleanup calls=%d", closed.Load())
	}
}

func TestHarnessGenerationActivationRechecksScopeAndAuthorization(t *testing.T) {
	owner := &session.Principal{Issuer: "issuer", Subject: "alice"}
	var revoked atomic.Bool
	var binds atomic.Int32
	denied := errors.New("source access revoked")
	reg := HarnessSourceRegistration[server.CommandSourceBinding]{ID: "a", Scope: HarnessSourceScopePrincipal, Bind: func(context.Context, HarnessSourceScope) (server.CommandSourceBinding, func() error, error) {
		binds.Add(1)
		if revoked.Load() {
			return nil, nil, denied
		}
		return &hcCommands{values: map[string]string{"x": "allowed"}}, nil, nil
	}}
	r, err := newHarnessCommandResolver(t.Context(), harnessKindPolicy{sources: []HarnessSourceID{"a"}, mode: harnessModeCombine}, []HarnessSourceRegistration[server.CommandSourceBinding]{reg})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	_, release, err := r.Borrow(t.Context(), "s", owner, "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	r.Retire("s")
	if err := r.Activate(t.Context(), "s", &session.Principal{Issuer: "issuer", Subject: "bob"}, ""); err == nil {
		t.Fatal("changed owner activated")
	}
	if err := r.Activate(t.Context(), "s", owner, "no-fs"); err == nil {
		t.Fatal("changed profile activated")
	}
	if binds.Load() != 1 {
		t.Fatal("mismatched scope reached binder")
	}
	revoked.Store(true)
	if err := r.Activate(t.Context(), "s", owner, ""); !errors.Is(err, denied) {
		t.Fatalf("activation=%v", err)
	}
	if _, _, err := r.Borrow(t.Context(), "s", owner, ""); err == nil {
		t.Fatal("failed activation removed retirement")
	}
	revoked.Store(false)
	if err := r.Activate(t.Context(), "s", owner, ""); err != nil {
		t.Fatal(err)
	}
	if binds.Load() != 3 {
		t.Fatal("activation did not retry fresh binding")
	}
}

func TestHarnessGenerationServiceReloadOwnerAuthorization(t *testing.T) {
	source := memfs.NewWorkspace("/logical")
	if _, err := source.CreateFile(t.Context(), ".mecatl/commands/x.md", []byte("selected")); err != nil {
		t.Fatal(err)
	}
	cfg := hcConfiguredFiles(t, source)
	cfg.OwnershipEnforced = true
	built, err := buildIsolated(t, t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer built.Close()
	alice := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "alice", GrantType: session.GrantTypeUser})
	bob := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "issuer", Subject: "bob", GrantType: session.GrantTypeUser})
	s, err := built.Service.CreateSession(alice, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := built.Service.ListCommandsForSession(alice, s.ID); err != nil {
		t.Fatal(err)
	}
	built.Service.CloseSession(s.ID)
	if _, err := built.Service.LoadSession(bob, s.ID); !errors.Is(err, server.ErrNotFound) {
		t.Fatalf("foreign reload=%v", err)
	}
	if _, err := built.Service.ListCommandsForSession(alice, s.ID); err == nil {
		t.Fatal("unauthorized reload reactivated source")
	}
	if _, err := built.Service.LoadSession(alice, s.ID); err != nil {
		t.Fatal(err)
	}
	commands, err := built.Service.ListCommandsForSession(alice, s.ID)
	if err != nil || len(commands) != 1 {
		t.Fatalf("authorized reload listing=%v,%v", commands, err)
	}
}
