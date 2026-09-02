package server

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"iter"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

type mutationInventoryLease struct {
	mu       sync.Mutex
	held     map[session.SessionID]bool
	acquires []session.SessionID
}

func (l *mutationInventoryLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = make(map[session.SessionID]bool)
	}
	if l.held[id] {
		return port.Lease{}, port.ErrLeaseHeld
	}
	l.held[id] = true
	l.acquires = append(l.acquires, id)
	return port.Lease{SessionID: id, Owner: owner, Token: uint64(len(l.acquires)), Expiry: time.Now().Add(time.Hour)}, nil
}

func (*mutationInventoryLease) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	return lease, nil
}

func (l *mutationInventoryLease) Release(_ context.Context, lease port.Lease) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.held, lease.SessionID)
	return nil
}

func (l *mutationInventoryLease) owns(id session.SessionID) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.held[id]
}

type mutationInventoryStore struct {
	*memstore.Store
	lease *mutationInventoryLease
	mu    sync.Mutex
	ops   []string
}

func (s *mutationInventoryStore) record(id session.SessionID, operation string) error {
	if !s.lease.owns(id) {
		return errors.New("durable mutation started without the session lease")
	}
	s.mu.Lock()
	s.ops = append(s.ops, operation)
	s.mu.Unlock()
	return nil
}

func (s *mutationInventoryStore) Save(ctx context.Context, sess *session.Session) error {
	if err := s.record(sess.ID, "snapshot.save"); err != nil {
		return err
	}
	return s.Store.Save(ctx, sess)
}

func (s *mutationInventoryStore) Append(_ context.Context, id session.SessionID, _ session.Event) error {
	return s.record(id, "event.append")
}

func (*mutationInventoryStore) Read(context.Context, session.SessionID) iter.Seq2[session.Event, error] {
	return func(func(session.Event, error) bool) {}
}

func TestSessionAffinityAndHandoff_Scenario5_MutationLeaseInventory(t *testing.T) {
	base := memstore.New()
	ref := session.EnvironmentRef{Kind: session.EnvKindMem, ID: "/ws", Revision: "in-tree-v1"}
	sess := session.New("mutation-inventory", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
	if err := base.Save(context.Background(), sess); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	lease := &mutationInventoryLease{}
	store := &mutationInventoryStore{Store: base, lease: lease}
	eng := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("ok")), Catalog: tool.NewCatalog(),
		Policy: permpolicy.NewPolicy(nil, nil), Model: "test",
	})
	svc, err := NewService(Config{
		Engine: eng, Store: store, EventLog: store,
		PlacementProvider: &repairPlacementProvider{binding: PlacementBinding{
			Ref: ref, Environment: tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), nil),
		}},
		PlacementScope: "test", SharedEngineRoot: "/ws",
		SessionLease: lease, LeaseOwner: "inventory", LeaseTTL: time.Hour, LeaseRenewInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	t.Cleanup(svc.Close)

	if _, err := svc.SetMode(context.Background(), sess.ID, session.ModePlan); err != nil {
		t.Fatalf("SetMode: %v", err)
	}

	store.mu.Lock()
	got := append([]string(nil), store.ops...)
	store.mu.Unlock()
	if strings.Join(got, ",") != "snapshot.save" {
		t.Fatalf("durable operations = %v, want leased snapshot.save", got)
	}
	for _, name := range []string{
		"persistNewSession", "CompactSession", "RenameSession", "DeleteSession",
		"DeleteSessionForRetentionCandidate", "DeleteSessionForRetention", "SetMode",
		"repairTerminalState", "prepareFailedStepRetry", "startRunContent", "Persist",
		"appendEvent", "engine/agent/dispatch.go:ToolCall", "adapter:family-derivatives",
		"SettleIfStale", "migrateOneFamily", "createPlacedSuccessor", "persistPlacedCreatedSession",
	} {
		entry, ok := sessionMutationInventory[name]
		if !ok || !entry.mutatesDurableFamily() {
			t.Errorf("session mutation %q classification = %#v, present=%t", name, entry, ok)
		}
	}
}

func discoveredSessionMutationBoundaries(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	mutatingCalls := map[string]bool{
		"Save": true, "Create": true, "Delete": true, "Append": true, "AppendEvent": true,
		"ToolCall": true, "MigrateSessionFamily": true, "DeleteSessionIfUnchanged": true,
		"SetMode": true, "RenameTitle": true, "CompactSession": true, "Reopen": true,
		"Interrupt": true, "Recover": true, "Abandon": true, "PrepareFailedStepRetry": true,
		"persistNewSession": true, "persistCreatedSession": true, "repairTerminalState": true,
		"prepareFailedStepRetry": true, "appendEvent": true, "DeleteSessionForRetentionCandidate": true,
	}
	seen := make(map[string]bool)
	fset := token.NewFileSet()
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") || path == "session_mutation_inventory.go" {
			continue
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || !serviceMethod(fn) {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if ok && mutatingCalls[sel.Sel.Name] {
					// AttemptRepository.Abandon mutates the separate learning-attempt
					// family, not a durable session family.
					if sel.Sel.Name == "Abandon" {
						if receiver, receiverOK := sel.X.(*ast.SelectorExpr); receiverOK && receiver.Sel.Name == "Attempts" {
							return true
						}
					}
					seen[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	return out
}

func serviceMethod(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return false
	}
	typ := fn.Recv.List[0].Type
	if ptr, ok := typ.(*ast.StarExpr); ok {
		typ = ptr.X
	}
	ident, ok := typ.(*ast.Ident)
	return ok && ident.Name == "Service"
}

func scanUnsanctionedWrites(files map[string]*ast.File, allowed map[string]bool) []string {
	var violations []string
	for path, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || allowed[fn.Name.Name] {
				continue
			}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Save", "Create", "Append", "AppendEvent", "ToolCall":
					violations = append(violations, path+":"+fn.Name.Name+"."+sel.Sel.Name)
				case "Delete":
					// sync.Map deletion is process-local, not a durable family write.
					if x, ok := sel.X.(*ast.SelectorExpr); !ok || x.Sel.Name != "recoverNotices" {
						violations = append(violations, path+":"+fn.Name.Name+".Delete")
					}
				}
				return true
			})
		}
	}
	sort.Strings(violations)
	return violations
}

func TestADR_0294_DurableSessionWritesUseSanctionedWrappers(t *testing.T) {
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"persistNewSession": true, "saveSession": true, "deleteSessionFamily": true, "appendEvent": true,
		// Schedule records are a distinct caller-owned family, not a session family;
		// naming the sanctioned seams keeps schedule_manager.go inside this scan.
		"CreateSchedule": true, "UpdateSchedule": true, "DeleteSchedule": true,
	}
	files := make(map[string]*ast.File)
	fset := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") || path == "mutation_capability.go" {
			continue
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		files[path] = file
	}
	if violations := scanUnsanctionedWrites(files, allowed); len(violations) != 0 {
		t.Fatalf("durable session writes bypass sanctioned wrappers: %v", violations)
	}

	fixture, err := parser.ParseFile(token.NewFileSet(), "fixture.go", `package server
func freeHelper(store interface{ Save() }) { store.Save() }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	violations := scanUnsanctionedWrites(map[string]*ast.File{"fixture.go": fixture}, allowed)
	if len(violations) != 1 || !strings.Contains(violations[0], "freeHelper.Save") {
		t.Fatalf("real architecture scanner violations = %v, want freeHelper.Save", violations)
	}
}

func TestADR_0294_AllSessionMutatorsClassified(t *testing.T) {
	if errs := validateSessionMutationNames(sessionMutationInventory, discoveredSessionMutationBoundaries(t)); len(errs) != 0 {
		for _, err := range errs {
			t.Error(err)
		}
	}

	fixture := map[string]SessionMutationEntry{
		"GetSession": {Class: SessionMutationReadOnly, Rationale: "loads an authoritative snapshot without changing the durable family"},
		"SetModels":  {Class: SessionMutationComposition, Rationale: "changes only process-wide composition state and touches no session family"},
	}
	err := validateSessionMutationNames(fixture, []string{"NewSessionMutator"})
	if len(err) != 1 || !strings.Contains(err[0].Error(), "NewSessionMutator") {
		t.Fatalf("unclassified mutator errors = %v, want one error naming NewSessionMutator", err)
	}
	if fixture["GetSession"].Class == fixture["SetModels"].Class {
		t.Fatal("read-only operations and composition setters must have distinct classifications")
	}
}
