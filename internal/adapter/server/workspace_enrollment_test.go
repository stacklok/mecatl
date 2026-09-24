package server

import (
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memlease"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/wallclock"
	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	brokercontract "github.com/stacklok/mecatl/internal/mcpbroker"
)

var errFixtureEngineBuildFailed = errors.New("engine build failed")

type enrollmentAttachment struct {
	brokercontract.Attachment
	ref           brokercontract.WorkspaceEnrollmentRef
	result        brokercontract.WorkspaceEnrollmentResult
	catalogue     brokercontract.WorkspaceCatalogue
	cancelWait    bool
	cancelErr     error
	resetErr      error
	beginCalls    int
	observeCalls  int
	cancelCalls   int
	lastCancelRef brokercontract.WorkspaceEnrollmentRef
	mu            sync.Mutex
	beginEntered  chan struct{}
	releaseBegin  <-chan struct{}
}

func (a *enrollmentAttachment) ResetWorkspaceEnrollment(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.resetErr
}

func (a *enrollmentAttachment) BeginWorkspaceEnrollment(context.Context) (brokercontract.WorkspaceEnrollmentPresentation, error) {
	a.mu.Lock()
	a.beginCalls++
	entered, release := a.beginEntered, a.releaseBegin
	a.mu.Unlock()
	if entered != nil {
		close(entered)
	}
	if release != nil {
		<-release
	}
	return brokercontract.WorkspaceEnrollmentPresentation{Ref: a.ref, URL: "https://broker.example/authorize?state=opaque"}, nil
}

func (a *enrollmentAttachment) ObserveWorkspaceEnrollment(context.Context, brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.observeCalls++
	return a.result, nil
}

func (a *enrollmentAttachment) CancelWorkspaceEnrollment(ctx context.Context, ref brokercontract.WorkspaceEnrollmentRef) (brokercontract.WorkspaceEnrollmentResult, error) {
	a.mu.Lock()
	a.cancelCalls++
	a.lastCancelRef = ref
	cancelWait := a.cancelWait
	cancelErr := a.cancelErr
	a.mu.Unlock()
	if cancelWait {
		<-ctx.Done()
		return brokercontract.WorkspaceEnrollmentResult{}, ctx.Err()
	}
	if cancelErr != nil {
		return brokercontract.WorkspaceEnrollmentResult{}, cancelErr
	}
	return brokercontract.WorkspaceEnrollmentResult{Ref: a.ref, Status: brokercontract.WorkspaceEnrollmentCancelled}, nil
}

func (a *enrollmentAttachment) Tools() []tool.Tool {
	if a.catalogue != nil {
		return a.catalogue.Tools()
	}
	return a.Attachment.Tools()
}

type enrollmentBroker struct {
	brokercontract.Service
	attachment *enrollmentAttachment
	attachErr  error
}

func (b *enrollmentBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	if b.attachErr != nil {
		return nil, "", b.attachErr
	}
	attachment, outcome, err := b.Service.AttachSession(ctx, id)
	if err != nil {
		return nil, outcome, err
	}
	if b.attachment == nil {
		ref := brokercontract.WorkspaceEnrollmentRef{ID: "enrollment-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
		b.attachment = &enrollmentAttachment{Attachment: attachment, ref: ref, result: brokercontract.WorkspaceEnrollmentResult{Ref: ref, Status: brokercontract.WorkspaceEnrollmentPending}}
	}
	return b.attachment, outcome, nil
}

// AttachSessionExpectedBinding forwards the real broker's binding
// classification, as mcpbroker.Process does in production, so a binding from a
// replaced broker instance surfaces as structured instance loss.
func (b *enrollmentBroker) AttachSessionExpectedBinding(ctx context.Context, id session.SessionID, binding session.ExternalBinding) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	if b.attachErr != nil {
		return nil, "", b.attachErr
	}
	attacher, ok := b.Service.(brokercontract.ExpectedBindingAttacher)
	if !ok {
		return nil, "", brokercontract.ErrContinuityUnavailable
	}
	attachment, outcome, err := attacher.AttachSessionExpectedBinding(ctx, id, binding)
	if err != nil {
		return nil, outcome, err
	}
	if b.attachment == nil {
		ref := brokercontract.WorkspaceEnrollmentRef{ID: "enrollment-1", RequiredServices: 1, ExpiresAt: time.Now().Add(time.Hour)}
		b.attachment = &enrollmentAttachment{Attachment: attachment, ref: ref, result: brokercontract.WorkspaceEnrollmentResult{Ref: ref, Status: brokercontract.WorkspaceEnrollmentPending}}
	}
	return b.attachment, outcome, nil
}

type enrollmentTool struct{ name string }

func (t enrollmentTool) Spec() tool.ToolSpec {
	return tool.ToolSpec{Name: t.name, Schema: json.RawMessage(`{"type":"object"}`)}
}
func (enrollmentTool) ReadOnly() bool { return true }
func (enrollmentTool) Execute(context.Context, session.ToolCall, tool.Environment) (session.ToolResult, error) {
	return session.NewToolResult("call", "ok"), nil
}

func newWorkspaceEnrollmentHTTPService(t *testing.T, ownershipEnforced bool) (*Service, *enrollmentBroker, *session.Session) {
	t.Helper()
	return newWorkspaceEnrollmentServiceWithStore(t, ownershipEnforced, memstore.New())
}

func newWorkspaceEnrollmentServiceWithStore(t *testing.T, ownershipEnforced bool, store port.SessionStore) (*Service, *enrollmentBroker, *session.Session) {
	t.Helper()
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { _ = runtime.Close() })
	broker := &enrollmentBroker{Service: runtime}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "workspace-enrollment-http" }, MCPBroker: broker,
		OwnershipEnforced: ownershipEnforced,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	createdCtx := t.Context()
	if ownershipEnforced {
		createdCtx = session.WithPrincipal(createdCtx, &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser})
	}
	created, err := svc.CreateSession(createdCtx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	return svc, broker, created
}

func enrollmentCallCounts(a *enrollmentAttachment) (begin, observe, cancel int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.beginCalls, a.observeCalls, a.cancelCalls
}

// attachCountingBroker counts AttachSession calls so a test can prove ordinary
// run rehydration never attaches to the broker on its own (Scenario 2, AC2.1)
// versus an explicit refresh, which must.
type attachCountingBroker struct {
	brokercontract.Service
	calls atomic.Int32
}

func (b *attachCountingBroker) AttachSession(ctx context.Context, id session.SessionID) (brokercontract.Attachment, brokercontract.AttachOutcome, error) {
	b.calls.Add(1)
	return b.Service.AttachSession(ctx, id)
}

// destructiveReplacementFixture is the common Scenario 1 precondition every
// AC1.2/AC1.3 fault-injection case starts from: an ESTABLISHED completed
// enrollment (tool bundle T0) plus one completed real prompt turn, so the
// refresh under test is a genuine mid-conversation destructive replacement,
// not a fresh pre-prompt enrollment.
type destructiveReplacementFixture struct {
	svc             *Service
	broker          *enrollmentBroker
	store           port.SessionStore
	created         *session.Session
	failEngineBuild atomic.Bool
}

func newDestructiveReplacementFixture(t *testing.T) *destructiveReplacementFixture {
	t.Helper()
	return newDestructiveReplacementFixtureWithStore(t, memstore.New())
}

func newDestructiveReplacementFixtureWithStore(t *testing.T, store port.SessionStore) *destructiveReplacementFixture {
	t.Helper()
	f := &destructiveReplacementFixture{store: store}
	runtime := testBrokerRuntime(t)
	t.Cleanup(func() { _ = runtime.Close() })
	f.broker = &enrollmentBroker{Service: runtime}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "destructive-replacement-session" }, MCPBroker: f.broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			if f.failEngineBuild.Load() {
				return SessionEngineResult{}, errFixtureEngineBuildFailed
			}
			return brokerEngineResultWithStore(store), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	f.svc = svc
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	f.created = created

	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	t0, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}})
	if err != nil {
		t.Fatal(err)
	}
	f.broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: t0}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}

	run, err := svc.StartRunContent(t.Context(), created.ID, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc.FinishRun(created.ID, run)

	return f
}

func (f *destructiveReplacementFixture) authorityTools(t *testing.T) []string {
	t.Helper()
	loaded, err := f.store.Load(t.Context(), f.created.ID)
	if err != nil {
		t.Fatal(err)
	}
	authority, _ := loaded.BoundAuthority()
	return authority.CapabilitySet.Tools
}

func (f *destructiveReplacementFixture) pendingID(t *testing.T) (session.WorkspaceEnrollmentID, bool) {
	t.Helper()
	loaded, err := f.store.Load(t.Context(), f.created.ID)
	if err != nil {
		t.Fatal(err)
	}
	pending, ok := loaded.PendingWorkspaceEnrollment()
	return pending.ID, ok
}

// TestIdleSessionBrokerRefresh_Scenario1_ExistingConversationControls pins
// AC1.1: an owner can begin, observe, retry, and cancel workspace enrollment
// after at least one completed prompt. The control reopens a completed
// session to idle while preserving history, and admits the existing gRPC/HTTP
// control (both funnel through the Service methods exercised here) with no
// conflicting control.
func TestIdleSessionBrokerRefresh_Scenario1_ExistingConversationControls(t *testing.T) {
	// newWorkspaceEnrollmentHTTPService's shared engine has no Store wired (a
	// silent no-op save that every OTHER test here tolerates because none of
	// them drive a real prompt) — this test needs a genuine completed turn to
	// actually persist, so it wires its own store-backed engine instead of
	// touching that shared helper.
	store := memstore.New()
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "existing-conversation-session" }, MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResultWithStore(store), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	run, err := svc.StartRunContent(t.Context(), created.ID, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svc.FinishRun(created.ID, run)

	before, err := svc.GetSession(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.State != session.StateCompleted || len(before.Conversation.Messages) == 0 {
		t.Fatalf("precondition: session state = %q, %d messages, want completed with history", before.State, len(before.Conversation.Messages))
	}
	historyLen := len(before.Conversation.Messages)

	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("ConnectWorkspaceServices (begin) after a completed prompt: %v", err)
	}
	if started.Status != brokercontract.WorkspaceEnrollmentPending || started.URL == "" {
		t.Fatalf("started = %#v", started)
	}

	reopened, err := svc.GetSession(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.State != session.StateIdle {
		t.Fatalf("state after begin = %q, want idle (reopened)", reopened.State)
	}
	if len(reopened.Conversation.Messages) != historyLen {
		t.Fatalf("conversation history after reopen = %d messages, want preserved %d", len(reopened.Conversation.Messages), historyLen)
	}

	observed, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || observed.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("observe (poll) = (%+v, %v)", observed, err)
	}

	retried, err := svc.RetryWorkspaceEnrollment(t.Context(), created.ID, started.Ref.ID)
	if err != nil {
		t.Fatalf("retry after a completed prompt: %v", err)
	}
	if retried.Status != brokercontract.WorkspaceEnrollmentPending || retried.Ref.ID == "" {
		t.Fatalf("retried = %#v", retried)
	}

	if _, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, retried.Ref.ID); err != nil {
		t.Fatalf("cancel after a completed prompt: %v", err)
	}

	final, err := svc.GetSession(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Conversation.Messages) != historyLen {
		t.Fatalf("conversation history after cancel = %d messages, want preserved %d", len(final.Conversation.Messages), historyLen)
	}
	if _, pending := final.PendingWorkspaceEnrollment(); pending {
		t.Fatal("cancel left a pending enrollment behind")
	}
}

// TestIdleSessionBrokerRefresh_Scenario1_DestructiveCatalogueReplacement pins
// AC1.2/AC1.3: starting a refresh removes the active broker wrappers and
// invalidates the completed catalogue before the replacement begins, and a
// failure at any of the named boundaries (broker reset, engine build,
// attachment commit, snapshot persistence) — or a terminal denial — leaves no
// stale or partial broker wrapper executable, without restoring the old
// bundle, retaining a partial replacement, or auto-retrying. Each subtest
// injects the fault at a different boundary and then proves a subsequent
// explicit retry succeeds cleanly.
func TestIdleSessionBrokerRefresh_Scenario1_DestructiveCatalogueReplacement(t *testing.T) {
	t.Run("BrokerResetFailureLeavesCurrentEnrollmentUndisturbed", func(t *testing.T) {
		// The only failure branch inside the real Attachment.ResetWorkspaceEnrollment
		// (engine/adapter/mcpbroker) returns BEFORE any mutation, so a reset
		// failure never gets the chance to withdraw the old engine either — the
		// pre-refresh bundle stays fully intact rather than becoming "unavailable".
		f := newDestructiveReplacementFixture(t)
		before := f.authorityTools(t)

		f.broker.attachment.mu.Lock()
		f.broker.attachment.resetErr = errors.New("reset failed")
		f.broker.attachment.mu.Unlock()

		if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID); err == nil {
			t.Fatal("refresh succeeded despite a broker reset failure")
		}
		if _, ok := f.pendingID(t); ok {
			t.Fatal("a failed reset started a new pending enrollment")
		}
		if got := f.authorityTools(t); !slices.Equal(got, before) {
			t.Fatalf("authority tools after a failed reset = %v, want unchanged %v", got, before)
		}

		f.broker.attachment.mu.Lock()
		f.broker.attachment.resetErr = nil
		f.broker.attachment.mu.Unlock()
		retried, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil || retried.Status != brokercontract.WorkspaceEnrollmentPending {
			t.Fatalf("retry after the reset was fixed = (%+v, %v)", retried, err)
		}
	})

	t.Run("EngineBuildFailureLeavesNoPartialAuthority", func(t *testing.T) {
		f := newDestructiveReplacementFixture(t)
		before := f.authorityTools(t)

		started, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil || started.Status != brokercontract.WorkspaceEnrollmentPending {
			t.Fatalf("start replacement = (%+v, %v)", started, err)
		}
		// Persisted names are inert metadata (ADR 0335): they still name the
		// withdrawn T0 bundle until a completion overwrites them, even though the
		// live engine underneath has already been withdrawn by the reset above.
		if got := f.authorityTools(t); !slices.Equal(got, before) {
			t.Fatalf("authority tools before completion = %v, want still %v", got, before)
		}

		t1, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__slack__post"}})
		if err != nil {
			t.Fatal(err)
		}
		f.broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: t1}
		f.failEngineBuild.Store(true)
		if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID); err == nil {
			t.Fatal("completion succeeded despite an engine build failure")
		}
		if pending, ok := f.pendingID(t); !ok || pending != started.Ref.ID {
			t.Fatalf("pending after an engine build failure = (%v, %v), want the original replacement still pending", pending, ok)
		}
		if got := f.authorityTools(t); !slices.Equal(got, before) {
			t.Fatalf("authority tools after an engine build failure = %v, want unchanged %v (T1 must not be partially installed)", got, before)
		}

		f.failEngineBuild.Store(false)
		completed, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil || completed.Status != brokercontract.WorkspaceEnrollmentConnected {
			t.Fatalf("retry after the engine build was fixed = (%+v, %v)", completed, err)
		}
		if got := f.authorityTools(t); !slices.Equal(got, []string{"mcp__slack__post"}) {
			t.Fatalf("authority tools after a successful retry = %v, want the new bundle only", got)
		}
	})

	t.Run("AttachmentCommitInconsistencyLeavesNoPartialAuthority", func(t *testing.T) {
		f := newDestructiveReplacementFixture(t)
		before := f.authorityTools(t)

		started, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil {
			t.Fatal(err)
		}
		// The broker CLAIMS connected for started.Ref but its own committed
		// catalogue names a different transaction — an attachment-commit
		// inconsistency mecatl must refuse rather than trust.
		mismatchedRef := started.Ref
		mismatchedRef.ID = "attachment-committed-a-different-transaction"
		inconsistent, err := brokercontract.NewWorkspaceCatalogue(mismatchedRef, []tool.Tool{enrollmentTool{name: "mcp__slack__post"}})
		if err != nil {
			t.Fatal(err)
		}
		f.broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: inconsistent}
		if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID); err == nil {
			t.Fatal("observation with a mismatched committed catalogue succeeded")
		}
		if pending, ok := f.pendingID(t); !ok || pending != started.Ref.ID {
			t.Fatalf("pending after a commit inconsistency = (%v, %v), want unchanged", pending, ok)
		}
		if got := f.authorityTools(t); !slices.Equal(got, before) {
			t.Fatalf("authority tools after a commit inconsistency = %v, want unchanged %v", got, before)
		}

		t1, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__slack__post"}})
		if err != nil {
			t.Fatal(err)
		}
		f.broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: t1}
		if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID); err != nil {
			t.Fatalf("retry with a consistent catalogue: %v", err)
		}
	})

	t.Run("SnapshotPersistenceFailureLeavesNoPartialAuthority", func(t *testing.T) {
		inner := memstore.New()
		failing := &failNextAuthorizationSaveStore{SessionStore: inner}
		f := newDestructiveReplacementFixtureWithStore(t, failing)
		before := f.authorityTools(t)

		started, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil {
			t.Fatal(err)
		}
		t1, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__slack__post"}})
		if err != nil {
			t.Fatal(err)
		}
		f.broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: t1}
		failing.failures.Store(1)
		if _, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID); !errors.Is(err, ErrInternal) {
			t.Fatalf("completion despite a failed snapshot save = %v, want ErrInternal", err)
		}
		if pending, ok := f.pendingID(t); !ok || pending != started.Ref.ID {
			t.Fatalf("pending after a save failure = (%v, %v), want unchanged", pending, ok)
		}
		if got := f.authorityTools(t); !slices.Equal(got, before) {
			t.Fatalf("authority tools after a save failure = %v, want unchanged %v", got, before)
		}

		retried, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil || retried.Status != brokercontract.WorkspaceEnrollmentConnected {
			t.Fatalf("retry after the save was fixed = (%+v, %v)", retried, err)
		}
		if got := f.authorityTools(t); !slices.Equal(got, []string{"mcp__slack__post"}) {
			t.Fatalf("authority tools after a successful retry = %v, want the new bundle only", got)
		}
	})

	t.Run("TerminalDenialDuringReplacementLeavesToolsUnavailableWithoutAutoRetry", func(t *testing.T) {
		f := newDestructiveReplacementFixture(t)
		before := f.authorityTools(t)

		started, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil {
			t.Fatal(err)
		}
		f.broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentDenied}
		denied, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil || denied.Status != brokercontract.WorkspaceEnrollmentDenied {
			t.Fatalf("denial observation = (%+v, %v)", denied, err)
		}
		if _, ok := f.pendingID(t); ok {
			t.Fatal("a denied replacement left a pending enrollment behind")
		}
		if got := f.authorityTools(t); !slices.Equal(got, before) {
			t.Fatalf("authority tools after denial = %v, want unchanged %v (denial must not restore or partially grant anything)", got, before)
		}

		// No automatic retry: a second call succeeds only because the CALLER
		// explicitly asked again, not because the system retried on its own.
		again, err := f.svc.ConnectWorkspaceServices(t.Context(), f.created.ID)
		if err != nil || again.Status != brokercontract.WorkspaceEnrollmentPending {
			t.Fatalf("explicit re-attempt after denial = (%+v, %v)", again, err)
		}
	})
}

// TestInvariant_idle_session_broker_refresh_serialization pins AC1.4: a
// refresh is rejected while the session is not idle, and retry holds the
// session's run-entry/lease serialization continuously from cancellation
// through replacement begin so no prompt can enter between them.
func TestInvariant_idle_session_broker_refresh_serialization(t *testing.T) {
	t.Run("NonIdleSessionRejectsRefresh", func(t *testing.T) {
		// workspaceEnrollmentTarget gates on ONE shared `sess.State != StateIdle`
		// check that excludes every non-idle state alike (an active run, a
		// pending permission/lazy authorization, or another in-flight control),
		// so driving the session directly into StateRunning proves the shared
		// gate without needing a live dispatched run.
		svc, _, created := newWorkspaceEnrollmentHTTPService(t, false)

		loaded, err := svc.GetSession(t.Context(), created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := loaded.BeginTurn(); err != nil {
			t.Fatal(err)
		}
		if err := svc.saveSession(t.Context(), loaded); err != nil {
			t.Fatal(err)
		}

		if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); !errors.Is(err, ErrFailedPrecondition) {
			t.Fatalf("refresh while session is StateRunning = %v, want ErrFailedPrecondition", err)
		}
	})

	t.Run("RetryHoldsRunEntryContinuously", TestRetryWorkspaceEnrollmentHoldsRunEntryContinuously)
	t.Run("RetryAcquiresRunEntryMuExactlyOnce", TestRetryWorkspaceEnrollmentAcquiresRunEntryMuExactlyOnce)
}

// TestIdleSessionBrokerRefresh_Scenario2_RestartAndExplicitRecovery pins
// AC2.1-AC2.3: after simulated broker-process loss (a fresh Service + fresh
// broker Runtime over the SAME durable store — a Runtime's binding prefix is
// random per process, so a restarted process can never match the persisted
// binding again), the session stays usable for a non-broker prompt with no
// live broker tools and without mecatl ever attaching on its own; the owner's
// explicit refresh is the only path that creates a fresh attachment and
// installs the exact new admitted names.
func TestIdleSessionBrokerRefresh_Scenario2_RestartAndExplicitRecovery(t *testing.T) {
	store := memstore.New()
	sessionID := session.SessionID("restart-recovery-session")

	runtimeA := testBrokerRuntime(t)
	defer runtimeA.Close()
	brokerA := &enrollmentBroker{Service: runtimeA}
	svcA, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return sessionID }, MCPBroker: brokerA,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		// rehydrateSession requires a non-nil SessionEngine even for a broker
		// session (it never actually calls it here: MCPBroker configured routes
		// through SessionEngineWithTools instead), so this is a precondition-only
		// wiring, not a second engine path under real exercise.
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return brokerEngineResultWithStore(store), nil
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResultWithStore(store), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svcA.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	started, err := svcA.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	t0, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}})
	if err != nil {
		t.Fatal(err)
	}
	brokerA.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: t0}
	if _, err := svcA.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatal(err)
	}

	run, err := svcA.StartRunContent(t.Context(), created.ID, "hello", nil)
	if err != nil {
		t.Fatal(err)
	}
	for range run.Events() {
	}
	svcA.FinishRun(created.ID, run)
	svcA.Close()

	beforeRestart, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	bindingBeforeRestart := beforeRestart.ExternalBinding
	if bindingBeforeRestart == "" {
		t.Fatal("precondition: no broker binding recorded before restart")
	}
	authorityBeforeRestart, _ := beforeRestart.BoundAuthority()

	// Simulated broker-process loss.
	runtimeB := testBrokerRuntime(t)
	defer runtimeB.Close()
	brokerB := &enrollmentBroker{Service: runtimeB}
	countingBrokerB := &attachCountingBroker{Service: brokerB}
	svcB, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return sessionID }, MCPBroker: countingBrokerB,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngine: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode) (SessionEngineResult, error) {
			return brokerEngineResultWithStore(store), nil
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResultWithStore(store), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svcB.Close()

	// AC2.1: ordinary run rehydration never attaches/rebinds/discovers.
	run2, err := svcB.StartRunContent(t.Context(), created.ID, "post-restart prompt", nil)
	if err != nil {
		t.Fatalf("StartRunContent on a restored session with a lost broker binding: %v", err)
	}
	for range run2.Events() {
	}
	svcB.FinishRun(created.ID, run2)
	if countingBrokerB.calls.Load() != 0 {
		t.Fatalf("ordinary run rehydration called AttachSession %d times, want 0", countingBrokerB.calls.Load())
	}

	afterOrdinaryRun, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterOrdinaryRun.ExternalBinding != bindingBeforeRestart {
		t.Fatal("ordinary rehydration changed the persisted broker binding")
	}
	authorityAfterOrdinaryRun, _ := afterOrdinaryRun.BoundAuthority()
	if !slices.Equal(authorityAfterOrdinaryRun.CapabilitySet.Tools, authorityBeforeRestart.CapabilitySet.Tools) {
		t.Fatalf("persisted tool names changed by ordinary rehydration = %v, want unchanged %v (names are inert metadata, but must not be rewritten either)", authorityAfterOrdinaryRun.CapabilitySet.Tools, authorityBeforeRestart.CapabilitySet.Tools)
	}

	// AC2.2: the owner's explicit refresh creates a fresh attachment/binding
	// and installs the exact new admitted names only after complete discovery.
	recovered, err := svcB.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("explicit refresh after broker-process loss: %v", err)
	}
	if recovered.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("refresh after restart = %#v, want a fresh pending bundle", recovered)
	}
	if countingBrokerB.calls.Load() == 0 {
		t.Fatal("explicit refresh never attached to the live broker incarnation")
	}
	t1, err := brokercontract.NewWorkspaceCatalogue(recovered.Ref, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}, enrollmentTool{name: "mcp__github__review"}})
	if err != nil {
		t.Fatal(err)
	}
	brokerB.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: recovered.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: t1}
	connected, err := svcB.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || connected.Status != brokercontract.WorkspaceEnrollmentConnected {
		t.Fatalf("complete recovery = (%+v, %v)", connected, err)
	}

	final, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.ExternalBinding == bindingBeforeRestart || final.ExternalBinding == "" {
		t.Fatalf("binding after recovery = %q, want rebound to the new live incarnation (was %q)", final.ExternalBinding, bindingBeforeRestart)
	}
	finalAuthority, _ := final.BoundAuthority()
	if !slices.Equal(finalAuthority.CapabilitySet.Tools, []string{"mcp__calendar__list", "mcp__github__review"}) {
		t.Fatalf("authority tools after recovery = %v, want the exact new bundle", finalAuthority.CapabilitySet.Tools)
	}
}

// TestInvariant_idle_session_broker_refresh_preserves_toolhive_custody pins
// AC2.4: mecatl's own outer attachment and enrollment correlation are never
// resumed or inferred after broker-process loss — a pre-restart pending
// reference no longer resolves, and the owner's fresh explicit refresh mints
// an entirely new correlation and durably rebinds rather than reusing
// anything from before the crash. (Upstream OAuth material redaction itself
// is proven exhaustively by TestADR_0298_ToolHiveEnrollmentControlsRedactUpstreamStateE2E
// and TestADR_0298_OpaqueBrokerCredentialIsNotDecodedOrCopied; this test is
// the restart-specific half of AC2.4.)
func TestInvariant_idle_session_broker_refresh_preserves_toolhive_custody(t *testing.T) {
	store := memstore.New()
	sessionID := session.SessionID("custody-session")

	runtimeA := testBrokerRuntime(t)
	defer runtimeA.Close()
	brokerA := &enrollmentBroker{Service: runtimeA}
	svcA, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return sessionID }, MCPBroker: brokerA,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := svcA.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	pendingA, err := svcA.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || pendingA.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("pre-restart begin = (%+v, %v)", pendingA, err)
	}
	svcA.Close()

	beforeRestart, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	bindingBeforeRestart := beforeRestart.ExternalBinding

	runtimeB := testBrokerRuntime(t)
	defer runtimeB.Close()
	brokerB := &enrollmentBroker{Service: runtimeB}
	svcB, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return sessionID }, MCPBroker: brokerB,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svcB.Close()

	// The pre-restart pending reference is never resumed: it belonged to a
	// broker incarnation that no longer exists.
	if _, err := svcB.CancelWorkspaceEnrollment(t.Context(), created.ID, pendingA.Ref.ID); err == nil {
		t.Fatal("a pre-restart pending enrollment reference resolved after process loss")
	}

	pendingB, err := svcB.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil || pendingB.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("post-restart explicit refresh = (%+v, %v)", pendingB, err)
	}

	final, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.ExternalBinding == bindingBeforeRestart || final.ExternalBinding == "" {
		t.Fatalf("binding after refresh = %q, want rebound away from the dead pre-restart incarnation %q", final.ExternalBinding, bindingBeforeRestart)
	}
	pending, ok := final.PendingWorkspaceEnrollment()
	if !ok || pending.ID != pendingB.Ref.ID {
		t.Fatalf("durable pending after refresh = (%+v, %v), want the fresh post-restart correlation only", pending, ok)
	}
}

func TestADR_0298_ToolHiveEnrollmentControlsRedactUpstreamStateE2E(t *testing.T) {
	type enrollmentResponse struct {
		EnrollmentID     string `json:"enrollment_id"`
		Status           string `json:"status"`
		RequiredServices uint32 `json:"required_services"`
		PresentationURL  string `json:"presentation_url"`
	}
	request := func(t *testing.T, h http.Handler, ctx context.Context, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)).WithContext(ctx)
		resp := httptest.NewRecorder()
		h.ServeHTTP(resp, req)
		return resp
	}
	decode := func(t *testing.T, resp *httptest.ResponseRecorder) enrollmentResponse {
		t.Helper()
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(resp.Body.Bytes(), &fields); err != nil {
			t.Fatalf("decode enrollment fields: %v", err)
		}
		for name := range fields {
			switch name {
			case "enrollment_id", "status", "required_services", "presentation_url":
			default:
				t.Fatalf("unsafe enrollment response field %q", name)
			}
		}
		var result enrollmentResponse
		if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode enrollment response: %v", err)
		}
		return result
	}

	t.Run("connect observes and projects only safe fields", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		path := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/connect"
		startedResp := request(t, h, t.Context(), path, "")
		if startedResp.Code != http.StatusOK {
			t.Fatalf("connect status = %d: %s", startedResp.Code, startedResp.Body.String())
		}
		started := decode(t, startedResp)
		if started.EnrollmentID != "enrollment-1" || started.Status != "pending" || started.RequiredServices != 1 || started.PresentationURL == "" {
			t.Fatalf("connect response = %#v", started)
		}
		if attachment := broker.attachment; attachment.beginCalls != 1 {
			t.Fatalf("begin calls = %d, want 1", attachment.beginCalls)
		}
		observedResp := request(t, h, t.Context(), path, "")
		if observedResp.Code != http.StatusOK {
			t.Fatalf("observe status = %d: %s", observedResp.Code, observedResp.Body.String())
		}
		observed := decode(t, observedResp)
		if observed.EnrollmentID != started.EnrollmentID || observed.Status != "pending" || observed.PresentationURL != "" {
			t.Fatalf("observed response = %#v", observed)
		}
		if attachment := broker.attachment; attachment.observeCalls != 1 {
			t.Fatalf("observe calls = %d, want 1", attachment.observeCalls)
		}
	})

	t.Run("retry uses the exact pending correlation", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, t.Context(), "/v1/sessions/"+string(created.ID)+"/workspace-enrollment/connect", ""))
		resp := request(t, h, t.Context(), base+started.EnrollmentID+"/retry", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("retry status = %d: %s", resp.Code, resp.Body.String())
		}
		if got := decode(t, resp); got.EnrollmentID != started.EnrollmentID || got.Status != "pending" {
			t.Fatalf("retry response = %#v", got)
		}
		attachment := broker.attachment
		if attachment.cancelCalls != 1 || !sameWorkspaceEnrollmentRef(attachment.lastCancelRef, attachment.ref) || attachment.beginCalls != 2 {
			t.Fatalf("retry calls = cancel:%d ref:%#v begin:%d", attachment.cancelCalls, attachment.lastCancelRef, attachment.beginCalls)
		}
	})

	t.Run("cancel rejects stale and malformed correlation without effects", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		_ = request(t, h, t.Context(), "/v1/sessions/"+string(created.ID)+"/workspace-enrollment/connect", "")
		for _, tc := range []struct {
			name, id string
			want     int
		}{{"stale", "other", http.StatusPreconditionFailed}, {"malformed", "bad%20id", http.StatusBadRequest}} {
			t.Run(tc.name, func(t *testing.T) {
				resp := request(t, h, t.Context(), base+tc.id+"/cancel", "")
				if resp.Code != tc.want {
					t.Fatalf("cancel status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
				}
			})
		}
		if got := broker.attachment.cancelCalls; got != 0 {
			t.Fatalf("cancel calls = %d, want 0", got)
		}
	})

	t.Run("foreign owner has no broker side effects", func(t *testing.T) {
		foreign := &session.Principal{Issuer: "https://idp.example", Subject: "foreign", GrantType: session.GrantTypeUser}
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, true)
		h := NewHTTPHandler(svc)
		resp := request(t, h, session.WithPrincipal(t.Context(), foreign), "/v1/sessions/"+string(created.ID)+"/workspace-enrollment/connect", "")
		if resp.Code != http.StatusNotFound {
			t.Fatalf("foreign connect status = %d, want 404", resp.Code)
		}
		attachment := broker.attachment
		if attachment.beginCalls != 0 || attachment.observeCalls != 0 || attachment.cancelCalls != 0 {
			t.Fatalf("foreign control side effects = begin:%d observe:%d cancel:%d", attachment.beginCalls, attachment.observeCalls, attachment.cancelCalls)
		}
	})

	t.Run("rejects request bodies", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		for _, path := range []string{
			"/v1/sessions/" + string(created.ID) + "/workspace-enrollment/connect",
			"/v1/sessions/" + string(created.ID) + "/workspace-enrollment/enrollment-1/retry",
			"/v1/sessions/" + string(created.ID) + "/workspace-enrollment/enrollment-1/cancel",
		} {
			resp := request(t, h, t.Context(), path, `{}`)
			if resp.Code != http.StatusBadRequest {
				t.Fatalf("body rejection status = %d, want 400: %s", resp.Code, resp.Body.String())
			}
		}
		attachment := broker.attachment
		if attachment.beginCalls != 0 || attachment.observeCalls != 0 || attachment.cancelCalls != 0 {
			t.Fatalf("body rejection side effects = begin:%d observe:%d cancel:%d", attachment.beginCalls, attachment.observeCalls, attachment.cancelCalls)
		}
	})

	t.Run("retry rejects malformed and stale IDs without disturbing the pending enrollment", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, t.Context(), base+"connect", ""))
		beforeBegin, beforeObserve, beforeCancel := enrollmentCallCounts(broker.attachment)
		for _, tc := range []struct {
			name, id string
			want     int
		}{{"malformed", "bad%20id", http.StatusBadRequest}, {"stale", "other", http.StatusPreconditionFailed}} {
			t.Run(tc.name, func(t *testing.T) {
				resp := request(t, h, t.Context(), base+tc.id+"/retry", "")
				if resp.Code != tc.want {
					t.Fatalf("retry status = %d, want %d: %s", resp.Code, tc.want, resp.Body.String())
				}
			})
		}
		if begin, observe, cancel := enrollmentCallCounts(broker.attachment); begin != beforeBegin || observe != beforeObserve || cancel != beforeCancel {
			t.Fatalf("retry rejection side effects = begin:%d observe:%d cancel:%d, want %d:%d:%d", begin, observe, cancel, beforeBegin, beforeObserve, beforeCancel)
		}
		loaded, err := svc.cfg.Store.Load(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("load pending enrollment: %v", err)
		}
		if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != session.WorkspaceEnrollmentID(started.EnrollmentID) {
			t.Fatalf("pending enrollment = %#v, %v", pending, ok)
		}
	})

	t.Run("foreign retry and cancel are non-disclosing and side-effect free", func(t *testing.T) {
		owner := &session.Principal{Issuer: "https://idp.example", Subject: "owner", GrantType: session.GrantTypeUser}
		foreign := &session.Principal{Issuer: "https://idp.example", Subject: "foreign", GrantType: session.GrantTypeUser}
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, true)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, session.WithPrincipal(t.Context(), owner), base+"connect", ""))
		beforeBegin, beforeObserve, beforeCancel := enrollmentCallCounts(broker.attachment)
		for _, action := range []string{"retry", "cancel"} {
			t.Run(action, func(t *testing.T) {
				resp := request(t, h, session.WithPrincipal(t.Context(), foreign), base+started.EnrollmentID+"/"+action, "")
				if resp.Code != http.StatusNotFound {
					t.Fatalf("foreign %s status = %d, want 404", action, resp.Code)
				}
			})
		}
		if begin, observe, cancel := enrollmentCallCounts(broker.attachment); begin != beforeBegin || observe != beforeObserve || cancel != beforeCancel {
			t.Fatalf("foreign control side effects = begin:%d observe:%d cancel:%d, want %d:%d:%d", begin, observe, cancel, beforeBegin, beforeObserve, beforeCancel)
		}
	})

	t.Run("contending connects begin once then observe the same enrollment", func(t *testing.T) {
		svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		attachment := broker.attachment
		entered := make(chan struct{})
		release := make(chan struct{})
		attachment.mu.Lock()
		attachment.beginEntered = entered
		attachment.releaseBegin = release
		attachment.mu.Unlock()
		path := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/connect"
		responses := make(chan *httptest.ResponseRecorder, 2)
		for range 2 {
			go func() { responses <- request(t, h, t.Context(), path, "") }()
		}
		<-entered
		close(release)
		first, second := <-responses, <-responses
		if first.Code != http.StatusOK || second.Code != http.StatusOK {
			t.Fatalf("connect statuses = %d, %d", first.Code, second.Code)
		}
		one, two := decode(t, first), decode(t, second)
		if one.EnrollmentID != two.EnrollmentID || one.Status != "pending" || two.Status != "pending" {
			t.Fatalf("contended connect responses = %#v, %#v", one, two)
		}
		if (one.PresentationURL == "") == (two.PresentationURL == "") {
			t.Fatalf("contended connect presentation URLs = %q, %q, want one initial URL and one empty observation URL", one.PresentationURL, two.PresentationURL)
		}
		if begin, observe, cancel := enrollmentCallCounts(attachment); begin != 1 || observe != 1 || cancel != 0 {
			t.Fatalf("contended connect calls = begin:%d observe:%d cancel:%d", begin, observe, cancel)
		}
	})

	t.Run("terminal cancel projects an empty presentation URL", func(t *testing.T) {
		svc, _, created := newWorkspaceEnrollmentHTTPService(t, false)
		h := NewHTTPHandler(svc)
		base := "/v1/sessions/" + string(created.ID) + "/workspace-enrollment/"
		started := decode(t, request(t, h, t.Context(), base+"connect", ""))
		cancelledResp := request(t, h, t.Context(), base+started.EnrollmentID+"/cancel", "")
		if cancelledResp.Code != http.StatusOK {
			t.Fatalf("cancel status = %d: %s", cancelledResp.Code, cancelledResp.Body.String())
		}
		cancelled := decode(t, cancelledResp)
		if cancelled.Status != "cancelled" || cancelled.PresentationURL != "" {
			t.Fatalf("cancel response = %#v", cancelled)
		}
	})
}

func TestWorkspaceEnrollmentGRPCProjectsOnlyAggregateDenial(t *testing.T) {
	svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)
	harness := NewHarnessServer(svc)
	started, err := harness.ConnectWorkspaceServices(t.Context(), &mecatlv1.WorkspaceEnrollmentConnectRequest{SessionId: string(created.ID)})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: broker.attachment.ref, Status: brokercontract.WorkspaceEnrollmentDenied}
	denied, err := harness.ConnectWorkspaceServices(t.Context(), &mecatlv1.WorkspaceEnrollmentConnectRequest{SessionId: string(created.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if denied.GetEnrollmentId() != started.GetEnrollmentId() || denied.GetStatus() != string(brokercontract.WorkspaceEnrollmentDenied) || denied.GetRequiredServices() != 1 || denied.GetPresentationUrl() != "" {
		t.Fatalf("gRPC denial projection = %+v", denied)
	}
	fields := denied.ProtoReflect().Descriptor().Fields()
	for i := range fields.Len() {
		switch name := string(fields.Get(i).Name()); name {
		case "enrollment_id", "status", "required_services", "expires_at", "presentation_url":
		default:
			t.Fatalf("unsafe workspace-enrollment gRPC field %q", name)
		}
	}
}

func TestTerminalWorkspaceEnrollmentSaveFailureRecoversOnNextObservation(t *testing.T) {
	for _, status := range []brokercontract.WorkspaceEnrollmentStatus{
		brokercontract.WorkspaceEnrollmentDenied,
		brokercontract.WorkspaceEnrollmentExpired,
		brokercontract.WorkspaceEnrollmentFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			inner := memstore.New()
			store := &failNextAuthorizationSaveStore{SessionStore: inner}
			svc, broker, created := newWorkspaceEnrollmentServiceWithStore(t, false, store)
			started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: status}
			store.failures.Store(1)
			if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); !errors.Is(err, ErrInternal) {
				t.Fatalf("first terminal observation error = %v, want ErrInternal", err)
			}
			loaded, err := inner.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
				t.Fatalf("failed save changed durable pending enrollment = (%+v, %v)", pending, ok)
			}

			recovered, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
			if err != nil || recovered.Status != status || !sameWorkspaceEnrollmentRef(recovered.Ref, started.Ref) {
				t.Fatalf("next observation = (%+v, %v), want %q", recovered, err, status)
			}
			loaded, err = inner.Load(t.Context(), created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
				t.Fatal("recovered terminal observation remained pending")
			}
		})
	}
}

func TestWorkspaceEnrollmentCompensationIsBounded(t *testing.T) {
	oldTimeout := engineCloseTimeout
	engineCloseTimeout = 20 * time.Millisecond
	defer func() { engineCloseTimeout = oldTimeout }()

	attachment := &enrollmentAttachment{cancelWait: true}
	started := time.Now()
	cancelWorkspaceEnrollmentDetached(context.Background(), attachment, brokercontract.WorkspaceEnrollmentRef{})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("compensation took %s", elapsed)
	}
}

func TestWorkspaceEnrollmentAcquiresLeaseBeforePersisting(t *testing.T) {
	// ConnectWorkspaceServices/cancelWorkspaceEnrollment must acquire the
	// session mutation lease themselves: a fresh session that never had a run
	// driven through it (the ordinary case — /tools-connect is a pre-prompt
	// gate) never gets Grant()-ed any other way, and under a configured
	// SessionLease the guarded store then rejects every save with
	// ErrSessionLeasedElsewhere. This regressed silently because no other
	// workspace-enrollment test configures a SessionLease at all.
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	lease := memlease.New(wallclock.Clock{}, time.Minute)
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:        func() session.SessionID { return "leased-enrollment-session" },
		MCPBroker:    broker,
		SessionLease: lease,
		LeaseOwner:   "test-owner",
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil {
		t.Fatalf("ConnectWorkspaceServices on a never-run session under a configured lease: %v", err)
	}
	pending, ok := created.PendingWorkspaceEnrollment()
	if loaded, loadErr := store.Load(t.Context(), created.ID); loadErr == nil {
		pending, ok = loaded.PendingWorkspaceEnrollment()
	}
	if !ok {
		t.Fatal("no pending enrollment persisted")
	}
	if _, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, pending.ID); err != nil {
		t.Fatalf("CancelWorkspaceEnrollment under a configured lease: %v", err)
	}
}

// TestRetryWorkspaceEnrollmentHoldsRunEntryContinuously proves the lock is
// held for retry's ENTIRE duration, including its begin phase: it pauses the
// fake attachment's BeginWorkspaceEnrollment mid-retry (after its cancel has
// already completed) and confirms a concurrent ConnectWorkspaceServices call
// for the SAME session blocks until retry finishes. This alone cannot
// distinguish "one continuous acquisition" from "two acquisitions with an
// unwinnably narrow gap between them" — see
// TestRetryWorkspaceEnrollmentAcquiresRunEntryMuExactlyOnce for that
// structural guarantee, which is what actually pins the AC1.4 requirement
// ("Retry keeps the session's run-entry/lease serialization continuously ...
// so a prompt cannot enter between them").
func TestRetryWorkspaceEnrollmentHoldsRunEntryContinuously(t *testing.T) {
	svc, broker, created := newWorkspaceEnrollmentHTTPService(t, false)

	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	broker.attachment.mu.Lock()
	broker.attachment.beginEntered = entered
	broker.attachment.releaseBegin = release
	broker.attachment.mu.Unlock()

	retryDone := make(chan error, 1)
	go func() {
		_, err := svc.RetryWorkspaceEnrollment(t.Context(), created.ID, started.Ref.ID)
		retryDone <- err
	}()

	select {
	case <-entered:
		// Retry's cancel has completed and it is now paused inside begin,
		// still (if the fix holds) with runEntryMu held.
	case <-time.After(2 * time.Second):
		t.Fatal("RetryWorkspaceEnrollment never reached BeginWorkspaceEnrollment")
	}

	concurrentDone := make(chan error, 1)
	go func() {
		_, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
		concurrentDone <- err
	}()

	select {
	case err := <-concurrentDone:
		t.Fatalf("concurrent ConnectWorkspaceServices returned (%v) while retry's begin was still in flight: runEntryMu was released between cancel and begin", err)
	case <-time.After(100 * time.Millisecond):
		// Still blocked, as required.
	}

	close(release)

	if err := <-retryDone; err != nil {
		t.Fatalf("RetryWorkspaceEnrollment = %v", err)
	}
	select {
	case err := <-concurrentDone:
		if err != nil {
			t.Fatalf("concurrent ConnectWorkspaceServices after retry completed = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent ConnectWorkspaceServices never unblocked after retry completed")
	}
}

// TestRetryWorkspaceEnrollmentAcquiresRunEntryMuExactlyOnce is a regression
// test for RetryWorkspaceEnrollment composing two independently-locking
// public methods (cancel, then ConnectWorkspaceServices), which released and
// reacquired runEntryMu between them — a real but unwinnably narrow window
// for a concurrent prompt start to slip through, undetectable by racing real
// goroutines against it. The structural invariant that actually rules the
// gap out is simpler to state and pin than the race: RetryWorkspaceEnrollment
// must call s.runEntryMu.lock exactly once, delegating cancel and connect to
// the *Locked helpers that assume the lock is already held.
func TestRetryWorkspaceEnrollmentAcquiresRunEntryMuExactlyOnce(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "workspace_enrollment.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name.Name == "RetryWorkspaceEnrollment" {
			fn = f
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatal("RetryWorkspaceEnrollment not found in workspace_enrollment.go")
	}
	lockCalls := 0
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if ok && sel.Sel.Name == "lock" {
			if recv, ok := sel.X.(*ast.SelectorExpr); ok && recv.Sel.Name == "runEntryMu" {
				lockCalls++
			}
		}
		return true
	})
	if lockCalls != 1 {
		t.Fatalf("RetryWorkspaceEnrollment calls s.runEntryMu.lock %d times, want exactly 1 (a prompt could otherwise slip in through the gap between separate acquisitions)", lockCalls)
	}
}

func TestWorkspaceEnrollmentStateLossClearsPendingGate(t *testing.T) {
	runtime := testBrokerRuntime(t)
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "lost-enrollment-session" },
		MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}

	svc.mu.Lock()
	delete(svc.brokerAttachments, created.ID)
	svc.mu.Unlock()
	broker.attachErr = brokercontract.ErrStateUnavailable
	failed, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != brokercontract.WorkspaceEnrollmentFailed || !sameWorkspaceEnrollmentRef(failed.Ref, started.Ref) {
		t.Fatalf("failed projection = %#v, want failed for %#v", failed, started.Ref)
	}
	loaded, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
		t.Fatal("unavailable broker left workspace enrollment pending")
	}

	broker.attachErr = nil
	if restarted, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil || restarted.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("restart after state loss = %#v, %v", restarted, err)
	}
}

// TestWorkspaceEnrollmentCancelHealsAlreadyGoneBrokerTransaction pins the
// cancel-path half of I-7: a Cancel arriving after the broker's own
// transaction is already gone (e.g. a prior terminal Observe already tore it
// down) must still clear the aggregate's pending record — "cancel this stuck
// enrollment" is exactly the gesture a user reaches for on a wedged session,
// and it must not itself hard-fail with the record left pending forever.
func TestWorkspaceEnrollmentCancelHealsAlreadyGoneBrokerTransaction(t *testing.T) {
	runtime := testBrokerRuntime(t)
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	svc, err := NewService(Config{
		Engine:            brokerEngineResult().Engine,
		Store:             store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID:     func() session.SessionID { return "cancel-heals-session" },
		MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(context.Context, ProviderSelector, []mcp.ServerConfig, SessionProfile, string, session.PermissionMode, []tool.Tool) (SessionEngineResult, error) {
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}

	broker.attachment.cancelErr = brokercontract.ErrAuthorizationNotFound
	result, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, started.Ref.ID)
	if err != nil {
		t.Fatalf("CancelWorkspaceEnrollment = %v, want a healed Cancelled result", err)
	}
	if result.Status != brokercontract.WorkspaceEnrollmentCancelled {
		t.Fatalf("healed cancel status = %q, want cancelled", result.Status)
	}
	loaded, err := store.Load(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := loaded.PendingWorkspaceEnrollment(); pending {
		t.Fatal("cancel healing left workspace enrollment pending")
	}

	// A fresh connect must now start a brand-new enrollment, proving the
	// session is not permanently wedged.
	broker.attachment.cancelErr = nil
	if restarted, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err != nil || restarted.Status != brokercontract.WorkspaceEnrollmentPending {
		t.Fatalf("reconnect after healed cancel = %#v, %v", restarted, err)
	}
}

func TestWorkspaceEnrollmentPublishesFrozenCatalogueBeforePrompt(t *testing.T) {
	runtime := testBrokerRuntime(t)
	defer runtime.Close()
	broker := &enrollmentBroker{Service: runtime}
	store := memstore.New()
	var catalogues [][]string
	svc, err := NewService(Config{
		Engine: brokerEngineResult().Engine, Store: store,
		PlacementProvider: brokerPlacementProvider{}, PlacementScope: "test",
		NewID: func() session.SessionID { return "enrollment-session" }, MCPBroker: broker,
		RootAuthority: func(session.SessionKind) session.Authority {
			return session.Authority{CapabilitySet: governance.CapabilitySet{Tools: []string{"mcp__calendar__list"}}, Provenance: "test"}
		},
		SessionEngineWithTools: func(_ context.Context, _ ProviderSelector, _ []mcp.ServerConfig, _ SessionProfile, _ string, _ session.PermissionMode, tools []tool.Tool) (SessionEngineResult, error) {
			names := make([]string, len(tools))
			for i, candidate := range tools {
				names[i] = candidate.Spec().Name
			}
			catalogues = append(catalogues, names)
			return brokerEngineResult(), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	created, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}

	started, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != brokercontract.WorkspaceEnrollmentPending || started.URL == "" {
		t.Fatalf("started = %#v", started)
	}
	loaded, _ := store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending = %#v, %v", pending, ok)
	}

	mismatchedRef := started.Ref
	mismatchedRef.ID = "enrollment-other"
	broker.attachment.ref = mismatchedRef
	if _, err := svc.CancelWorkspaceEnrollment(t.Context(), created.ID, started.Ref.ID); err == nil {
		t.Fatal("mismatched enrollment cancellation succeeded")
	}
	broker.attachment.ref = started.Ref
	loaded, _ = store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending after mismatched cancellation = %#v, %v", pending, ok)
	}

	mismatchedCatalogue, err := brokercontract.NewWorkspaceCatalogue(mismatchedRef, []tool.Tool{enrollmentTool{name: "mcp__calendar__list"}})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{
		Ref: mismatchedRef, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: mismatchedCatalogue,
	}
	if _, err := svc.ConnectWorkspaceServices(t.Context(), created.ID); err == nil {
		t.Fatal("mismatched enrollment observation succeeded")
	}
	loaded, _ = store.Load(t.Context(), created.ID)
	if pending, ok := loaded.PendingWorkspaceEnrollment(); !ok || pending.ID != started.Ref.ID {
		t.Fatalf("pending after mismatched observation = %#v, %v", pending, ok)
	}

	complete, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"}, enrollmentTool{name: "mcp__github__review"},
	})
	if err != nil {
		t.Fatal(err)
	}
	drifted, err := brokercontract.NewWorkspaceCatalogue(started.Ref, []tool.Tool{
		enrollmentTool{name: "mcp__calendar__list"},
	})
	if err != nil {
		t.Fatal(err)
	}
	broker.attachment.catalogue = drifted
	broker.attachment.result = brokercontract.WorkspaceEnrollmentResult{Ref: started.Ref, Status: brokercontract.WorkspaceEnrollmentConnected, Catalogue: complete}
	connected, err := svc.ConnectWorkspaceServices(t.Context(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if connected.Status != brokercontract.WorkspaceEnrollmentConnected || len(catalogues) != 2 {
		t.Fatalf("connected = %#v, catalogues = %v", connected, catalogues)
	}
	if got := catalogues[1]; len(got) != 2 || got[1] != "mcp__github__review" {
		t.Fatalf("rebuilt catalogue = %v", got)
	}
	loaded, _ = store.Load(t.Context(), created.ID)
	if _, ok := loaded.PendingWorkspaceEnrollment(); ok {
		t.Fatal("completed enrollment remained pending")
	}
	authority, ok := loaded.BoundAuthority()
	if !ok || len(authority.CapabilitySet.Tools) != 2 || authority.CapabilitySet.Tools[1] != "mcp__github__review" {
		t.Fatalf("authority = %#v, %v", authority, ok)
	}
}
