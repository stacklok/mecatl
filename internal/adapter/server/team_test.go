package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type teamLivenessTracker struct {
	mu     sync.Mutex
	active map[session.SessionID]int
}

func (r *teamLivenessTracker) Register(_ context.Context, id session.SessionID, _ context.CancelFunc) (func(), error) {
	r.mu.Lock()
	if r.active == nil {
		r.active = make(map[session.SessionID]int)
	}
	r.active[id]++
	r.mu.Unlock()
	return sync.OnceFunc(func() {
		r.mu.Lock()
		if r.active[id] <= 1 {
			delete(r.active, id)
		} else {
			r.active[id]--
		}
		r.mu.Unlock()
	}), nil
}

func (r *teamLivenessTracker) IsLive(id session.SessionID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active[id] > 0
}

// teamService builds a team-enabled Service whose per-member Engine uses the
// supplied mockllm provider (shared by all members) and carries that member's
// coordination tools.
func teamService(t *testing.T, llm *mockllm.Provider) *server.Service {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:     llm,
			Catalog: cat,
			Policy:  allow,
			Model:   "mock",
		})}
	}
	// A no-op engine for plain sessions (unused by these team tests).
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("x")),
		Catalog: tool.NewCatalog(),
		Policy:  allow,
		Model:   "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// teamServiceWithStore is teamService but returns the backing SessionStore too, so a
// test can assert member sessions persist under their published-id-derived ids.
func teamServiceWithStore(t *testing.T, llm *mockllm.Provider) (*server.Service, port.SessionStore) {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := memstore.New()
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

// TestMemberSessionIDRoundTripsGRPCPath pins the id-scheme contract on the gRPC
// CreateTeam path: the member session the supervisor persists must load under
// MemberSessionID(publishedTeamID, member), where publishedTeamID is the EXACT
// CreateTeamResponse.team_id (which is itself "team-<NewID()>", so the saved id
// carries the deliberate double "team-" prefix). Feeding the published id verbatim —
// the SAME string InspectMember derives from — round-trips byte-for-byte.
func TestMemberSessionIDRoundTripsGRPCPath(t *testing.T) {
	svc, store := teamServiceWithStore(t, mockllm.New(
		mockllm.TextTurn("delegating"), mockllm.TextTurn("report"),
	))
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	createResp, err := h.CreateTeam(ctx, newCreateTeamWith("/ws",
		&mecatlv1.TeammateSpec{Name: "lead", Lead: true, InitialPrompt: "go"},
	))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	publishedTeamID := createResp.GetTeamId()
	if publishedTeamID == "" {
		t.Fatal("CreateTeam returned an empty team id")
	}

	if _, err := svc.RunTeam(ctx, publishedTeamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}

	// The lead session must load under MemberSessionID(publishedTeamID, "lead") — the
	// exact string a caller would feed InspectMember.
	id := agent.MemberSessionID(publishedTeamID, "lead")
	got, lerr := store.Load(ctx, id)
	if lerr != nil {
		t.Fatalf("lead session not persisted under MemberSessionID(%q,%q)=%q: %v", publishedTeamID, "lead", id, lerr)
	}
	if got.ID != id {
		t.Errorf("loaded session id = %q, want %q", got.ID, id)
	}
}

func TestDeclaredTeamMembersAdvertiseCanonicalSessionIDsBeforeRun(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("done")), Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	store := memstore.New()
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{
			LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
		}),
		Store: store, Now: func() time.Time { return time.Unix(0, 0) }, MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	h := server.NewHarnessServer(svc)
	ctx := context.Background()
	created, err := h.CreateTeam(ctx, newCreateTeamWith("/ws",
		&mecatlv1.TeammateSpec{Name: "lead", Lead: true, InitialPrompt: "go"},
	))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	teamID := created.GetTeamId()
	wantLead := string(agent.MemberSessionID(teamID, "lead"))
	if got := created.GetMembers()[0].GetSessionId(); got != wantLead {
		t.Fatalf("CreateTeam lead session_id = %q, want %q", got, wantLead)
	}

	spawned, err := h.SpawnTeammate(ctx, newSpawn(teamID, "worker", false, ""))
	if err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}
	wantWorker := string(agent.MemberSessionID(teamID, "worker"))
	if got := spawned.GetMember().GetSessionId(); got != wantWorker {
		t.Fatalf("SpawnTeammate worker session_id = %q, want %q", got, wantWorker)
	}

	listed, err := h.ListTeam(ctx, &mecatlv1.ListTeamRequest{TeamId: teamID})
	if err != nil {
		t.Fatalf("ListTeam: %v", err)
	}
	want := []string{wantLead, wantWorker}
	for i, member := range listed.GetMembers() {
		if got := member.GetSessionId(); got != want[i] {
			t.Errorf("ListTeam member %q session_id = %q, want %q", member.GetName(), got, want[i])
		}
		if _, err := store.Load(ctx, session.SessionID(want[i])); !errors.Is(err, port.ErrSessionNotFound) {
			t.Errorf("declared member %q was materialized before RunTeam: %v", want[i], err)
		}
	}

	if _, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	listed, err = h.ListTeam(ctx, &mecatlv1.ListTeamRequest{TeamId: teamID})
	if err != nil {
		t.Fatalf("ListTeam after RunTeam: %v", err)
	}
	for i, member := range listed.GetMembers() {
		if got := member.GetSessionId(); got != want[i] {
			t.Errorf("ListTeam after RunTeam member %q session_id = %q, want %q", member.GetName(), got, want[i])
		}
		if _, err := store.Load(ctx, session.SessionID(want[i])); err != nil {
			t.Errorf("persisted member %q: %v", want[i], err)
		}
	}
}

type creatorOnlyTeamStore struct {
	inner   *memstore.Store
	creates int
}

func (s *creatorOnlyTeamStore) Save(ctx context.Context, sess *session.Session) error {
	return s.inner.Save(ctx, sess)
}
func (s *creatorOnlyTeamStore) Load(ctx context.Context, id session.SessionID) (*session.Session, error) {
	return s.inner.Load(ctx, id)
}
func (s *creatorOnlyTeamStore) Create(ctx context.Context, sess *session.Session) error {
	s.creates++
	return s.inner.Create(ctx, sess)
}

func TestRunTeamRejectsCreatorStoreWithoutRollbackBeforeEnrollment(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := &creatorOnlyTeamStore{inner: memstore.New()}
	builds := 0
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"}),
		Store:  store,
		MemberEngine: func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
			builds++
			return agent.MemberBuild{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := svc.CreateTeamOnDefaultPlacement(t.Context(), "team", "goal", 0, []agent.MemberSpec{{Name: "lead", Lead: true}})
	if err != nil {
		t.Fatal(err)
	}
	store.creates = 0
	if _, err := svc.RunTeam(t.Context(), id, nil); !errors.Is(err, server.ErrFailedPrecondition) {
		t.Fatalf("RunTeam = %v, want ErrFailedPrecondition", err)
	}
	if builds != 0 || store.creates != 0 {
		t.Fatalf("startup side effects: builds=%d creates=%d", builds, store.creates)
	}
}

type teamRollbackStore struct {
	*memstore.Store
	mu               sync.Mutex
	deleteContextErr []error
	deleted          []session.SessionID
}

func (s *teamRollbackStore) Delete(ctx context.Context, id session.SessionID) error {
	s.mu.Lock()
	s.deleteContextErr = append(s.deleteContextErr, ctx.Err())
	s.deleted = append(s.deleted, id)
	s.mu.Unlock()
	return s.Store.Delete(ctx, id)
}

func (s *teamRollbackStore) deletion(id session.SessionID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, deleted := range s.deleted {
		if deleted == id {
			return true, s.deleteContextErr[i]
		}
	}
	return false, nil
}

type teamCountingForker struct {
	mu       sync.Mutex
	forks    int
	cleanups int
}

func (f *teamCountingForker) Fork(_ context.Context, parent tool.Environment, _ string) (tool.Environment, func() error, string, error) {
	f.mu.Lock()
	f.forks++
	f.mu.Unlock()
	return parent, sync.OnceValue(func() error {
		f.mu.Lock()
		f.cleanups++
		f.mu.Unlock()
		return nil
	}), "", nil
}

func (f *teamCountingForker) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forks, f.cleanups
}

func TestRunTeamConstructionFailureRetainsDeclarationsForRetry(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := memstore.New()
	var requestsMu sync.Mutex
	var requests []port.LLMRequest
	provider := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(func(req port.LLMRequest) {
		requestsMu.Lock()
		requests = append(requests, req)
		requestsMu.Unlock()
	})}, mockllm.TextTurn("done"), mockllm.TextTurn("done"), mockllm.TextTurn("report"))
	builds := 0
	closes := 0
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		builds++
		if builds == 2 {
			return agent.MemberBuild{}
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{
			Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: cat, Policy: allow, Model: "mock"}),
			Close:  func() error { closes++; return nil },
		}
	}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"}),
		Store:  store, Now: func() time.Time { return time.Unix(0, 0) }, MemberEngine: memberEngine,
	})
	if err != nil {
		t.Fatal(err)
	}
	members := []agent.MemberSpec{
		{Name: "lead", Lead: true, InitialPrompt: "work"},
		{Name: "worker", InitialPrompt: "help"},
	}
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(t.Context(), "retry", "goal", 0, members)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SendTeammateMessage(t.Context(), teamID, "", "lead", "preserved message"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunTeam(t.Context(), teamID, func(agent.TeamEvent) {}); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("first RunTeam = %v, want construction ErrInternal", err)
	}
	for _, spec := range members {
		if _, err := store.Load(t.Context(), agent.MemberSessionID(teamID, spec.Name)); !errors.Is(err, port.ErrSessionNotFound) {
			t.Fatalf("failed startup persisted member %q: %v", spec.Name, err)
		}
	}
	if closes != 1 {
		t.Fatalf("failed startup member close count = %d, want 1", closes)
	}
	if _, err := svc.RunTeam(t.Context(), teamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("retry RunTeam: %v", err)
	}
	foundMessage := false
	requestsMu.Lock()
	defer requestsMu.Unlock()
	for _, request := range requests {
		for _, message := range request.Messages {
			foundMessage = foundMessage || strings.Contains(message.Text, "preserved message")
		}
	}
	if !foundMessage {
		t.Fatalf("retry lost queued declaration message: %+v", requests)
	}
}

type teamLossLease struct {
	mu       sync.Mutex
	lostID   session.SessionID
	lost     chan struct{}
	lostOnce sync.Once
	releases map[session.SessionID]int
}

func (l *teamLossLease) Acquire(_ context.Context, id session.SessionID, owner string) (port.Lease, error) {
	return port.Lease{SessionID: id, Owner: owner, Token: 1, Expiry: time.Now().Add(time.Hour)}, nil
}

func (l *teamLossLease) Renew(_ context.Context, lease port.Lease) (port.Lease, error) {
	if lease.SessionID == l.lostID {
		return port.Lease{}, port.ErrLeaseHeld
	}
	return lease, nil
}

func (l *teamLossLease) Release(_ context.Context, lease port.Lease) error {
	l.mu.Lock()
	l.releases[lease.SessionID]++
	lost := lease.SessionID == l.lostID
	l.mu.Unlock()
	if lost {
		l.lostOnce.Do(func() { close(l.lost) })
	}
	return nil
}

func (l *teamLossLease) releaseCount(id session.SessionID) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.releases[id]
}

func TestRunTeamCancelledPartialStartupRollsBackWithDetachedContext(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := &teamRollbackStore{Store: memstore.New()}
	forker := &teamCountingForker{}
	ctx, cancel := context.WithCancel(t.Context())
	builds := 0
	closes := 0
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		builds++
		if builds == 2 {
			cancel()
			return agent.MemberBuild{}
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{
			Engine:          agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: cat, Policy: allow, Model: "mock"}),
			IsolateReadOnly: true,
			Close: func() error {
				closes++
				return nil
			},
		}
	}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"}),
		Store:  store, MemberEngine: memberEngine, ReadOnlyForker: forker,
	})
	if err != nil {
		t.Fatal(err)
	}
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "rollback", "goal", 0, []agent.MemberSpec{
		{Name: "lead", Lead: true}, {Name: "worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunTeam(ctx, teamID, nil); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("RunTeam = %v, want ErrInternal", err)
	}
	leadID := agent.MemberSessionID(teamID, "lead")
	if deleted, contextErr := store.deletion(leadID); !deleted || contextErr != nil {
		t.Fatalf("lead rollback deletion = %v with context error %v, want detached live context", deleted, contextErr)
	}
	if _, err := store.Load(context.Background(), leadID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("rolled-back lead persisted: %v", err)
	}
	if closes != 1 {
		t.Fatalf("member Close calls = %d, want 1", closes)
	}
	if forks, cleanups := forker.counts(); forks != 1 || cleanups != 1 {
		t.Fatalf("fork/cleanup calls = %d/%d, want 1/1", forks, cleanups)
	}
}

func TestRunTeamLeaseLossPreservesPersistedMember(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := &teamRollbackStore{Store: memstore.New()}
	lease := &teamLossLease{lost: make(chan struct{}), releases: make(map[session.SessionID]int)}
	builds := 0
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		builds++
		if builds == 2 {
			<-lease.lost
			return agent.MemberBuild{}
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: cat, Policy: allow, Model: "mock"})}
	}
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: agent.NewEngine(agent.Deps{LLM: mockllm.New(), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"}),
		Store:  store, MemberEngine: memberEngine, SessionLease: lease, LeaseOwner: "test",
		LeaseTTL: time.Hour, LeaseRenewInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(svc.Close)
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(t.Context(), "lease-loss", "goal", 0, []agent.MemberSpec{
		{Name: "lead", Lead: true}, {Name: "worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	leadID := agent.MemberSessionID(teamID, "lead")
	workerID := agent.MemberSessionID(teamID, "worker")
	lease.lostID = leadID
	if _, err := svc.RunTeam(t.Context(), teamID, nil); !errors.Is(err, server.ErrInternal) {
		t.Fatalf("RunTeam = %v, want ErrInternal", err)
	}
	if deleted, _ := store.deletion(leadID); deleted {
		t.Fatal("lease-lost member was deleted without mutation authority")
	}
	if _, err := store.Load(t.Context(), leadID); err != nil {
		t.Fatalf("lease-lost member snapshot was not preserved: %v", err)
	}
	if got := lease.releaseCount(leadID); got != 1 {
		t.Fatalf("lost lead lease releases = %d, want 1", got)
	}
	if got := lease.releaseCount(workerID); got != 1 {
		t.Fatalf("worker lease releases after failed cleanup = %d, want 1", got)
	}
	if err := svc.CleanupTeam(t.Context(), teamID); err != nil {
		t.Fatalf("CleanupTeam: %v", err)
	}
	if got := lease.releaseCount(leadID); got != 1 {
		t.Fatalf("cleanup double-released lost lead lease: %d", got)
	}
}

// usageTurn builds a single mock member turn that carries token usage via a
// UsageChunk — the shared spend knob the team-budget tests tune to cross (or not
// cross) a budget bound.
func usageTurn(text string, in int) mockllm.Turn {
	return mockllm.ChunksTurn(
		mockllm.TextChunk(text),
		mockllm.UsageChunk(session.Usage{InputTokens: in}),
		mockllm.DoneChunk(session.StopEndTurn),
	)
}

// teamServicePerMember is teamService with PER-MEMBER providers (keyed by member
// name) and an optional server-side team token budget — needed when a test's
// member scripts must not race over one shared turn queue (round-0 members run
// concurrently) or must differ per member.
func teamServicePerMember(t *testing.T, providers map[string]*mockllm.Provider, budget int) *server.Service {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		llm := providers[spec.Name]
		if llm == nil {
			t.Fatalf("no provider scripted for member %q", spec.Name)
		}
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:             func() time.Time { return time.Unix(0, 0) },
		MemberEngine:    memberEngine,
		TeamTokenBudget: budget,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// budgetTripProviders scripts a lead + a self-pinging worker whose round-0 spend
// (lead 100 + worker 600 = 700) crosses a 500 budget. The worker's SendMessage to
// ITSELF leaves a pending mailbox message at the round-1 boundary (the lead's
// synthesis would drain the LEAD's inbox, so the ping must target a non-lead), so
// the trip fires NON-quiescent — the shape that maps the team stop to "budget"
// (teamStop's !Quiescent && BudgetExhausted rule), which the wire outcome-frame
// tests assert. The synthesis turn still runs; the round-1 turns must never be
// consumed.
func budgetTripProviders() map[string]*mockllm.Provider {
	ping := session.NewToolCall("wp", "SendMessage", json.RawMessage(`{"to":"worker","body":"keep going"}`))
	return map[string]*mockllm.Provider{
		"lead": mockllm.New(
			usageTurn("delegating", 100),         // round 0
			usageTurn("CONSOLIDATED report", 50), // synthesis
			usageTurn("EXTRA lead turn (should never run)", 100),
		),
		"worker": mockllm.New(
			mockllm.ChunksTurn( // round 0: ping self so round 1 WOULD be scheduled
				mockllm.ToolCallChunk(ping),
				mockllm.UsageChunk(session.Usage{InputTokens: 300}),
				mockllm.DoneChunk(session.StopEndTurn),
			),
			usageTurn("worker round-0 done", 300),
			usageTurn("ROUND-1 worker turn (should never run)", 1000),
		),
	}
}

// teamServiceWithBudget builds a team-enabled Service whose CreateTeam supervisor
// carries a team-wide token budget (Config.TeamTokenBudget), with a per-member engine
// driven by the supplied provider. It is the seam under TestRunTeamBudgetExhaustedOutcome.
func teamServiceWithBudget(t *testing.T, llm *mockllm.Provider, budget int) *server.Service {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:             func() time.Time { return time.Unix(0, 0) },
		MemberEngine:    memberEngine,
		TeamTokenBudget: budget,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestRunTeamBudgetExhaustedOutcome pins the gRPC CreateTeam path threading
// Config.TeamTokenBudget into the supervisor (agent.WithTeamTokenBudget): a lead-only
// team whose round-0 spend crosses the budget returns a TeamOutcome with BudgetExhausted
// set. This asserts on the Service.RunTeam return value directly; the wire projection of
// the same outcome (the terminal TeamEvent.outcome frame, issue #36) is pinned by
// TestGRPCRunTeamEmitsOutcomeFrame / TestGRPCRunTeamSurfacesBudgetExhausted (grpc_test.go)
// and TestHTTPRunTeamEmitsOutcomeFrame (http_test.go).
func TestRunTeamBudgetExhaustedOutcome(t *testing.T) {
	// Each member turn carries usage via a UsageChunk; the lead spends 600 in round 0.
	llm := mockllm.New(
		usageTurn("round-0 work", 600),       // round 0 (crosses the 500 budget)
		usageTurn("CONSOLIDATED report", 50), // synthesis
		usageTurn("EXTRA (should never run)", 600),
	)
	const budget = 500
	svc := teamServiceWithBudget(t, llm, budget)
	ctx := context.Background()

	id, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "do one round of work", 0, []agent.MemberSpec{
		{Name: "lead", Lead: true, InitialPrompt: "do the work then stop"},
	})
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	out, err := svc.RunTeam(ctx, id, func(agent.TeamEvent) {})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	if !out.BudgetExhausted {
		t.Errorf("RunTeam outcome.BudgetExhausted = false, want true (budget %d, round-0 spent 600)", budget)
	}
	if out.Usage.TotalTokens() < budget {
		t.Errorf("RunTeam outcome.Usage.TotalTokens() = %d, want >= budget %d", out.Usage.TotalTokens(), budget)
	}
}

// TestCreateTeamTightensTeamTokenBudget pins the per-request budget knob landing in
// issue #36: CreateTeam's maxTeamTokens folds into Config.TeamTokenBudget via
// agent.TightenTeamTokenBudget at create time, TIGHTEN-ONLY. The effective budget is
// observed behaviourally through a budget-exhausted run whose usage is tuned to cross
// one bound but not the other:
//
//   - request BELOW server: spend crosses the request but not the server budget —
//     BudgetExhausted=true proves the request applied (a broken fold leaving the
//     server's 1000 in place would NOT trip on a 600 spend);
//   - request ABOVE server: spend crosses the server budget but not the request —
//     BudgetExhausted=true proves the server capped it (a loosening fold to 2000
//     would NOT trip on an 1100 spend).
func TestCreateTeamTightensTeamTokenBudget(t *testing.T) {
	run := func(t *testing.T, serverBudget, request, round0Spend int) agent.TeamOutcome {
		t.Helper()
		llm := mockllm.New(
			usageTurn("round-0 work", round0Spend),
			usageTurn("CONSOLIDATED report", 1), // synthesis
			usageTurn("EXTRA (should never run)", round0Spend),
		)
		svc := teamServiceWithBudget(t, llm, serverBudget)
		ctx := context.Background()
		id, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "do one round of work", request, []agent.MemberSpec{
			{Name: "lead", Lead: true, InitialPrompt: "do the work then stop"},
		})
		if err != nil {
			t.Fatalf("CreateTeam: %v", err)
		}
		out, err := svc.RunTeam(ctx, id, func(agent.TeamEvent) {})
		if err != nil {
			t.Fatalf("RunTeam: %v", err)
		}
		return out
	}

	t.Run("request below server budget applies", func(t *testing.T) {
		// Effective budget must be the request (500): a 600 round-0 spend crosses it
		// but stays well below the server's 1000.
		out := run(t, 1000, 500, 600)
		if !out.BudgetExhausted {
			t.Errorf("BudgetExhausted = false, want true (request 500 must tighten the server's 1000; spend 600)")
		}
	})

	t.Run("request above server budget is capped", func(t *testing.T) {
		// Effective budget must stay the server's 1000: an 1100 round-0 spend crosses
		// it but stays below the requested 2000 — a loosened budget would not trip.
		out := run(t, 1000, 2000, 1100)
		if !out.BudgetExhausted {
			t.Errorf("BudgetExhausted = false, want true (request 2000 must NOT loosen the server's 1000; spend 1100)")
		}
	})
}

// teamServiceWithGoalTrust builds a team-enabled Service whose CreateTeam goal-trust
// posture is set by the goalUntrusted flag (Config.TeamGoalUntrusted), returning the
// Service and its backing store so a test can load a member session and inspect the
// rendered round-0 prompt. It mirrors teamServiceWithStore but threads the flag.
func teamServiceWithGoalTrust(t *testing.T, llm *mockllm.Provider, goalUntrusted bool) (*server.Service, port.SessionStore) {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	store := memstore.New()
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM: llm, Catalog: cat, Policy: allow, Model: "mock",
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  store,

		Now:               func() time.Time { return time.Unix(0, 0) },
		MemberEngine:      memberEngine,
		TeamGoalUntrusted: goalUntrusted,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc, store
}

// leadPromptContaining loads the persisted lead session and returns the FIRST user
// message that mentions the "Team goal:" header — the rendered round-0 prompt whose
// goal-trust rendering the opt-in controls. It fails the test if no such prompt exists.
func leadPromptContaining(t *testing.T, store port.SessionStore, publishedTeamID string) string {
	t.Helper()
	id := agent.MemberSessionID(publishedTeamID, "lead")
	sess, err := store.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("lead session not persisted under %q: %v", id, err)
	}
	for _, m := range sess.Conversation.Messages {
		if m.Role == session.RoleUser && strings.Contains(m.Text, "Team goal:") {
			return m.Text
		}
	}
	t.Fatalf("no lead user prompt carrying a 'Team goal:' header in session %q", id)
	return ""
}

// TestCreateTeamGoalTrustOptIn pins the multi-tenant safety valve END-TO-END: with
// Config.TeamGoalUntrusted UNSET (default) the gRPC CreateTeam goal renders TRUSTED
// (plain, after the "Team goal:" header, NOT inside an <<<UNTRUSTED fence) in the
// member's round-0 prompt; with the flag SET the same goal is re-fenced as UNTRUSTED
// data. This proves CreateTeam threads agent.WithUntrustedGoal(true) only when the
// relay opt-in is configured.
//
// Fail-on-regression: if CreateTeam stopped threading the flag (e.g. dropped the
// `if s.cfg.TeamGoalUntrusted { ... WithUntrustedGoal(true) }` branch), the
// fenced-when-set assertion below would fail (the goal would render trusted regardless).
func TestCreateTeamGoalTrustOptIn(t *testing.T) {
	const goal = "audit the login flow"

	// fence is the literal marker writeUntrustedBlock emits immediately after the
	// "Team goal:\n" header when the goal is fenced as UNTRUSTED.
	const fencedGoalHeader = "Team goal:\n<<<UNTRUSTED"
	const trustedGoalHeader = "Team goal:\n" + goal

	run := func(t *testing.T, goalUntrusted bool) string {
		// Two TextTurns: one for the lead's round-0 turn, one for the lead's synthesis turn.
		svc, store := teamServiceWithGoalTrust(t, mockllm.New(
			mockllm.TextTurn("delegating"), mockllm.TextTurn("report"),
		), goalUntrusted)
		h := server.NewHarnessServer(svc)
		ctx := context.Background()

		createResp, err := h.CreateTeam(ctx, &mecatlv1.CreateTeamRequest{
			SessionId: "source", Name: "test", Goal: goal,
			Members: []*mecatlv1.TeammateSpec{{Name: "lead", Lead: true, InitialPrompt: "go"}},
		})
		if err != nil {
			t.Fatalf("CreateTeam: %v", err)
		}
		teamID := createResp.GetTeamId()
		if _, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {}); err != nil {
			t.Fatalf("RunTeam: %v", err)
		}
		return leadPromptContaining(t, store, teamID)
	}

	t.Run("default trusted", func(t *testing.T) {
		prompt := run(t, false)
		if !strings.Contains(prompt, trustedGoalHeader) {
			t.Fatalf("default CreateTeam must render the goal TRUSTED (plain after the header):\n%s", prompt)
		}
		if strings.Contains(prompt, fencedGoalHeader) {
			t.Fatalf("default CreateTeam must NOT fence the goal:\n%s", prompt)
		}
	})

	t.Run("opt-in fenced", func(t *testing.T) {
		prompt := run(t, true)
		if !strings.Contains(prompt, fencedGoalHeader) {
			t.Fatalf("Config.TeamGoalUntrusted=true must re-fence the CreateTeam goal as UNTRUSTED:\n%s", prompt)
		}
	})
}

func newCreateTeam(string) *mecatlv1.CreateTeamRequest {
	return &mecatlv1.CreateTeamRequest{SessionId: "source", Name: "test"}
}

func newSpawn(teamID, name string, lead bool, initialPrompt string) *mecatlv1.SpawnTeammateRequest {
	return &mecatlv1.SpawnTeammateRequest{
		TeamId: teamID, Name: name, Lead: lead, InitialPrompt: initialPrompt,
	}
}

// newCreateTeamWith builds a CreateTeamRequest carrying an initial roster — the
// atomic create+populate path.
func newCreateTeamWith(_ string, members ...*mecatlv1.TeammateSpec) *mecatlv1.CreateTeamRequest {
	return &mecatlv1.CreateTeamRequest{SessionId: "source", Name: "test", Members: members}
}

// wantRunningSentinel asserts a Service-level method returned the ErrTeamRunning
// sentinel (the Service returns raw sentinels; the gRPC layer maps them).
func wantRunningSentinel(t *testing.T, err error, what string) {
	t.Helper()
	if !errors.Is(err, server.ErrTeamRunning) {
		t.Errorf("%s: err = %v, want ErrTeamRunning", what, err)
	}
}

// wantFailedPrecondition asserts a gRPC-layer (HarnessServer) call returned a
// FailedPrecondition status — verifying ErrTeamRunning is mapped by toStatus.
func wantFailedPrecondition(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error, got nil", what)
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("%s: code = %v, want FailedPrecondition (err=%v)", what, status.Code(err), err)
	}
}

// TestTeamRunStateMachineRejectsConcurrent asserts Fix B at the gRPC boundary: once
// a team is running, a second RunTeam and a SpawnTeammate are both rejected with
// FailedPrecondition. The first RunTeam is held busy by a member whose mock blocks
// on ctx (blockingChunks); the test observes the busy stream, probes the
// rejections, then cancels to let the run finish.
func TestTeamRunStateMachineRejectsConcurrent(t *testing.T) {
	// The member's single turn streams text then blocks until ctx is cancelled, so
	// RunTeam stays in the running phase for the duration of the probes.
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := teamService(t, llm)
	h := server.NewHarnessServer(svc)

	ctx := context.Background()
	createResp, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	teamID := createResp.GetTeamId()

	if _, err := h.SpawnTeammate(ctx, newSpawn(teamID, "lead", true, "go")); err != nil {
		t.Fatalf("SpawnTeammate(lead): %v", err)
	}

	// Drive RunTeam on a goroutine via the Service (no stream plumbing needed). The
	// first member event tells us the run is live and in the running phase.
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	firstEvent := make(chan struct{}, 1)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = svc.RunTeam(runCtx, teamID, func(agent.TeamEvent) {
			select {
			case firstEvent <- struct{}{}:
			default:
			}
		})
	}()

	select {
	case <-firstEvent:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the team run to start emitting events")
	}

	// Concurrent second RunTeam is rejected (Service-level sentinel).
	_, err = svc.RunTeam(context.Background(), teamID, func(agent.TeamEvent) {})
	wantRunningSentinel(t, err, "concurrent RunTeam")

	// SpawnTeammate after the run started is rejected — through the gRPC layer, so
	// it must surface as a FailedPrecondition status (toStatus mapping).
	_, err = h.SpawnTeammate(ctx, newSpawn(teamID, "late", false, ""))
	wantFailedPrecondition(t, err, "SpawnTeammate after run")

	cancelRun()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the cancelled run to return")
	}

	// After the run is done, a second RunTeam is still rejected (created→running is
	// one-shot; the team is now done).
	_, err = svc.RunTeam(context.Background(), teamID, func(agent.TeamEvent) {})
	wantRunningSentinel(t, err, "RunTeam after done")
}

// TestTeamRunStateMachineDeterministic exercises the state machine directly,
// without a blocking member: a normal run completes, after which a second RunTeam
// and a SpawnTeammate are both rejected (the team is done, not created).
func TestTeamRunStateMachineDeterministic(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := teamService(t, llm)
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	createResp, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	teamID := createResp.GetTeamId()
	if _, err := h.SpawnTeammate(ctx, newSpawn(teamID, "solo", true, "go")); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	if _, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {}); err != nil {
		t.Fatalf("RunTeam: %v", err)
	}

	_, err = svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
	wantRunningSentinel(t, err, "second RunTeam after completion")

	_, err = h.SpawnTeammate(ctx, newSpawn(teamID, "late", false, ""))
	wantFailedPrecondition(t, err, "SpawnTeammate after completion")
}

// TestSpawnTeammateErrorClassification asserts finding J: SpawnTeammate no longer
// collapses every AddMember failure to InvalidArgument. A Mutating member spawned
// into a Service with no EnvironmentForker is a server misconfiguration the client
// cannot fix by changing its args, so it must surface as FailedPrecondition (not
// InvalidArgument). A duplicate-name spawn stays InvalidArgument (a real bad
// request), confirming the classifier discriminates rather than blanket-remapping.
func TestSpawnTeammateErrorClassification(t *testing.T) {
	// teamService wires no Forker, so a Mutating member trips ErrNoForker.
	svc := teamService(t, mockllm.New(mockllm.TextTurn("done")))
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	createResp, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	teamID := createResp.GetTeamId()

	// Mutating member, no forker configured → FailedPrecondition, NOT InvalidArgument.
	mutating := &mecatlv1.SpawnTeammateRequest{TeamId: teamID, Name: "writer", Mutating: true}
	_, err = h.SpawnTeammate(ctx, mutating)
	wantFailedPrecondition(t, err, "SpawnTeammate(Mutating, no forker)")

	// A genuine bad request — duplicate name — still maps to InvalidArgument.
	if _, err := h.SpawnTeammate(ctx, newSpawn(teamID, "lead", true, "go")); err != nil {
		t.Fatalf("SpawnTeammate(lead): %v", err)
	}
	_, err = h.SpawnTeammate(ctx, newSpawn(teamID, "lead", false, ""))
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("SpawnTeammate(duplicate): code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}
}

// TestUnknownTeamNotFound asserts a lookup on an unknown team id returns the
// team-specific ErrTeamNotFound sentinel (Service level) and maps to NotFound at
// the gRPC boundary — not the session-flavoured ErrNotFound.
func TestUnknownTeamNotFound(t *testing.T) {
	svc := teamService(t, mockllm.New(mockllm.TextTurn("done")))
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	if _, _, _, err := svc.ListTeam(ctx, "team-nope"); !errors.Is(err, server.ErrTeamNotFound) {
		t.Errorf("ListTeam(unknown): err = %v, want ErrTeamNotFound", err)
	}
	if err := svc.CleanupTeam(ctx, "team-nope"); !errors.Is(err, server.ErrTeamNotFound) {
		t.Errorf("CleanupTeam(unknown): err = %v, want ErrTeamNotFound", err)
	}
	_, err := h.ListTeam(ctx, &mecatlv1.ListTeamRequest{TeamId: "team-nope"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("ListTeam(unknown) gRPC: code = %v, want NotFound (err=%v)", status.Code(err), err)
	}
}

// teamServiceMaxTeams builds a team-enabled Service with an explicit MaxTeams cap.
func teamServiceMaxTeams(t *testing.T, llm *mockllm.Provider, maxTeams int) *server.Service {
	t.Helper()
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Model: "mock"})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock",
	})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: memberEngine,
		MaxTeams:     maxTeams,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestCreateTeamMaxTeams asserts Fix D's registry cap: CreateTeam past MaxTeams is
// rejected with ResourceExhausted, and cleaning up a (created) team frees a slot so
// a subsequent CreateTeam succeeds again.
func TestCreateTeamMaxTeams(t *testing.T) {
	svc := teamServiceMaxTeams(t, mockllm.New(mockllm.TextTurn("x")), 2)
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	r1, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam #1: %v", err)
	}
	if _, err := h.CreateTeam(ctx, newCreateTeam("/ws")); err != nil {
		t.Fatalf("CreateTeam #2: %v", err)
	}

	// The registry is full; the third create is rejected with ResourceExhausted.
	_, err = h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err == nil {
		t.Fatal("CreateTeam past MaxTeams: expected an error, got nil")
	}
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("CreateTeam past MaxTeams: code = %v, want ResourceExhausted (err=%v)", status.Code(err), err)
	}
	// And the Service returns the mapped sentinel.
	if _, _, serr := svc.CreateTeamOnDefaultPlacement(ctx, "x", "", 0, nil); !errors.Is(serr, server.ErrTooManyTeams) {
		t.Fatalf("Service.CreateTeam past cap: err = %v, want ErrTooManyTeams", serr)
	}

	// Cleaning up a created team frees a slot; the next CreateTeam succeeds.
	if _, err := h.CleanupTeam(ctx, &mecatlv1.CleanupTeamRequest{TeamId: r1.GetTeamId()}); err != nil {
		t.Fatalf("CleanupTeam: %v", err)
	}
	if _, err := h.CreateTeam(ctx, newCreateTeam("/ws")); err != nil {
		t.Fatalf("CreateTeam after freeing a slot: %v", err)
	}
}

// TestCleanupTeamRejectsRunning asserts Fix D: CleanupTeam on a running team is
// rejected with FailedPrecondition (deleting it would orphan the live supervisor),
// while a created or done team can be cleaned up and frees its slot.
func TestCleanupTeamRejectsRunning(t *testing.T) {
	// A member whose single turn blocks until ctx is cancelled keeps the team in the
	// running phase for the duration of the probe.
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	svc := teamService(t, llm)
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	createResp, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	teamID := createResp.GetTeamId()

	// A created (not-yet-running) team can be cleaned up.
	otherResp, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam #2: %v", err)
	}
	if _, err := h.CleanupTeam(ctx, &mecatlv1.CleanupTeamRequest{TeamId: otherResp.GetTeamId()}); err != nil {
		t.Fatalf("CleanupTeam(created): %v", err)
	}

	if _, err := h.SpawnTeammate(ctx, newSpawn(teamID, "lead", true, "go")); err != nil {
		t.Fatalf("SpawnTeammate: %v", err)
	}

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	firstEvent := make(chan struct{}, 1)
	runDone := make(chan struct{})
	go func() {
		defer close(runDone)
		_, _ = svc.RunTeam(runCtx, teamID, func(agent.TeamEvent) {
			select {
			case firstEvent <- struct{}{}:
			default:
			}
		})
	}()

	select {
	case <-firstEvent:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the team run to start")
	}

	// CleanupTeam on the running team is rejected (FailedPrecondition).
	_, err = h.CleanupTeam(ctx, &mecatlv1.CleanupTeamRequest{TeamId: teamID})
	wantFailedPrecondition(t, err, "CleanupTeam(running)")
	// And the Service returns the ErrTeamRunning sentinel.
	if serr := svc.CleanupTeam(ctx, teamID); !errors.Is(serr, server.ErrTeamRunning) {
		t.Fatalf("Service.CleanupTeam(running): err = %v, want ErrTeamRunning", serr)
	}

	cancelRun()
	select {
	case <-runDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the cancelled run to return")
	}

	// Once done, the team can be cleaned up.
	if _, err := h.CleanupTeam(ctx, &mecatlv1.CleanupTeamRequest{TeamId: teamID}); err != nil {
		t.Fatalf("CleanupTeam(done): %v", err)
	}
	// And it is gone: a second cleanup is NotFound.
	_, err = h.CleanupTeam(ctx, &mecatlv1.CleanupTeamRequest{TeamId: teamID})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("CleanupTeam(already gone): code = %v, want NotFound (err=%v)", status.Code(err), err)
	}
}

func TestDirectRunTeamProtectsMembersWithIndependentLiveness(t *testing.T) {
	tracker := &teamLivenessTracker{}
	llm := mockllm.New(mockllm.ChunksTurn(blockingChunks()...))
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: allow, Model: "mock"})}
	}
	engine := agent.NewEngine(agent.Deps{LLM: mockllm.New(mockllm.TextTurn("x")), Catalog: tool.NewCatalog(), Policy: allow, Model: "mock"})
	svc, err := newPlacementTeamTestService(server.Config{
		Engine: engine, Store: memstore.New(),
		Now: func() time.Time { return time.Unix(0, 0) }, MemberEngine: memberEngine, SessionLiveness: tracker,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	ctx := context.Background()
	teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "test", "goal", 0, []agent.MemberSpec{{Name: "lead", Lead: true, InitialPrompt: "go"}})
	if err != nil {
		t.Fatalf("CreateTeamWithRoster: %v", err)
	}
	memberID := agent.MemberSessionID(teamID, "lead")

	runCtx, cancel := context.WithCancel(ctx)
	started := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = svc.RunTeam(runCtx, teamID, func(agent.TeamEvent) {
			select {
			case started <- struct{}{}:
			default:
			}
		})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for direct team member to run")
	}
	if !tracker.IsLive(memberID) {
		t.Fatalf("direct team member %q was not protected while running", memberID)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for direct team cleanup")
	}
	if tracker.IsLive(memberID) {
		t.Fatalf("direct team member %q leaked liveness after cleanup", memberID)
	}
}

// TestCreateTeamWithRoster asserts the atomic create+populate path: CreateTeam with
// an initial roster (a lead + a read-only worker) returns the team id AND the
// enrolled members, ListTeam shows both, and RunTeam works without any separate
// SpawnTeammate call.
func TestCreateTeamWithRoster(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	svc := teamService(t, llm)
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	createResp, err := h.CreateTeam(ctx, newCreateTeamWith("/ws",
		&mecatlv1.TeammateSpec{Name: "lead", Lead: true, InitialPrompt: "go"},
		&mecatlv1.TeammateSpec{Name: "worker"}, // read-only (Mutating defaults false)
	))
	if err != nil {
		t.Fatalf("CreateTeam(roster): %v", err)
	}
	teamID := createResp.GetTeamId()
	if teamID == "" {
		t.Fatal("CreateTeam(roster): empty team id")
	}

	// The response echoes the enrolled roster in enrolment order — no follow-up
	// ListTeam needed.
	got := createResp.GetMembers()
	if len(got) != 2 || got[0].GetName() != "lead" || got[1].GetName() != "worker" {
		t.Fatalf("CreateTeam(roster) members = %v, want [lead worker]", got)
	}

	// ListTeam shows both members.
	listResp, err := h.ListTeam(ctx, &mecatlv1.ListTeamRequest{TeamId: teamID})
	if err != nil {
		t.Fatalf("ListTeam: %v", err)
	}
	if len(listResp.GetMembers()) != 2 {
		t.Fatalf("ListTeam members = %d, want 2", len(listResp.GetMembers()))
	}

	// RunTeam works with no separate SpawnTeammate call. The lead runs its initial
	// prompt and reports back; the bare read-only worker (no task, no message) is never
	// scheduled, which is fine — the run completes without error and the lead produced
	// its terminal text.
	outcome, err := svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
	if err != nil {
		t.Fatalf("RunTeam: %v", err)
	}
	if len(outcome.Members) != 2 {
		t.Fatalf("RunTeam outcome members = %d, want 2 (outcome=%+v)", len(outcome.Members), outcome)
	}
	if outcome.Members[0].Name != "lead" || outcome.Members[0].LastText != "done" {
		t.Errorf("RunTeam lead outcome = %+v, want LastText=%q", outcome.Members[0], "done")
	}
}

// TestCreateTeamWithRosterAtomicFailure asserts the atomicity guarantee: a roster
// containing a member that cannot enrol (here a duplicate name within the roster)
// fails the WHOLE CreateTeam, and the would-be team is never registered — a
// subsequent ListTeam on no team exists, and the MaxTeams slot was NOT consumed (the
// caller can still create up to the cap).
func TestCreateTeamWithRosterAtomicFailure(t *testing.T) {
	// MaxTeams=1 so we can prove the failed create did not consume the only slot.
	svc := teamServiceMaxTeams(t, mockllm.New(mockllm.TextTurn("done")), 1)
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	// A roster with a duplicate name: the second "dup" trips ErrMemberAlreadyAdded,
	// which classifies to InvalidArgument. The whole team must be abandoned.
	_, err := h.CreateTeam(ctx, newCreateTeamWith("/ws",
		&mecatlv1.TeammateSpec{Name: "dup", Lead: true, InitialPrompt: "go"},
		&mecatlv1.TeammateSpec{Name: "dup"},
	))
	if err == nil {
		t.Fatal("CreateTeam(duplicate in roster): expected an error, got nil")
	}
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("CreateTeam(duplicate in roster): code = %v, want InvalidArgument (err=%v)", status.Code(err), err)
	}

	// No team leaked: the registry is empty, so a fresh create against the MaxTeams=1
	// cap succeeds (the failed create did not consume the slot).
	createResp, err := h.CreateTeam(ctx, newCreateTeam("/ws"))
	if err != nil {
		t.Fatalf("CreateTeam after a failed atomic create: %v (the failed create must not consume a slot)", err)
	}
	if createResp.GetTeamId() == "" {
		t.Fatal("CreateTeam after a failed atomic create: empty team id")
	}
}

// TestCreateTeamWithRosterMutatingNoForkerAtomicFailure asserts the same atomicity
// for a server-misconfiguration failure class: a Mutating member with no Forker
// configured (teamServiceMaxTeams wires none) is a FailedPrecondition, and again the
// team is abandoned — no id is returned and the MaxTeams slot is untouched.
func TestCreateTeamWithRosterMutatingNoForkerAtomicFailure(t *testing.T) {
	svc := teamServiceMaxTeams(t, mockllm.New(mockllm.TextTurn("done")), 1)
	h := server.NewHarnessServer(svc)
	ctx := context.Background()

	_, err := h.CreateTeam(ctx, newCreateTeamWith("/ws",
		&mecatlv1.TeammateSpec{Name: "lead", Lead: true, InitialPrompt: "go"},
		&mecatlv1.TeammateSpec{Name: "writer", Mutating: true}, // no forker → ErrNoForker
	))
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("CreateTeam(Mutating, no forker): code = %v, want FailedPrecondition (err=%v)", status.Code(err), err)
	}

	// The slot was not consumed: a fresh create succeeds under MaxTeams=1.
	if _, err := h.CreateTeam(ctx, newCreateTeam("/ws")); err != nil {
		t.Fatalf("CreateTeam after a failed atomic create: %v", err)
	}
}

// TestSpawnTeammateRaceWithRunTeam is the FIX 2 (-race) test: for each of many
// freshly-created teams it fires SpawnTeammate and RunTeam CONCURRENTLY. Before the
// fix, SpawnTeammate checked the phase under Service.mu, released it, then called
// AddMember — letting a RunTeam win the teamCreated→teamRunning transition in the gap
// and start sup.Run (which iterates the supervisor's unsynchronised member/order
// maps) while AddMember was writing them: a concurrent map write/iterate panic that
// `go test -race` flags.
//
// The per-team `run` mutex serialises the two, so the test asserts:
//   - no race / panic (the point under -race);
//   - a CONSISTENT outcome per team: the spawn either WINS the race (returns nil, and
//     the member is on the final roster) or LOSES it (returns ErrTeamRunning) — never
//     a torn intermediate. RunTeam always succeeds (a created team is runnable).
func TestSpawnTeammateRaceWithRunTeam(t *testing.T) {
	const teams = 50
	svc := teamService(t, mockllm.New(mockllm.TextTurn("done")))
	ctx := context.Background()

	for i := 0; i < teams; i++ {
		teamID, _, err := svc.CreateTeamOnDefaultPlacement(ctx, "race", "", 0, nil)
		if err != nil {
			t.Fatalf("CreateTeam #%d: %v", i, err)
		}
		// A lead is enrolled up front so RunTeam has work to plan and actually starts
		// iterating the member maps (the read side of the race).
		if _, err := svc.SpawnTeammate(ctx, teamID, agent.MemberSpec{Name: "lead", Lead: true, InitialPrompt: "go"}); err != nil {
			t.Fatalf("SpawnTeammate(lead) #%d: %v", i, err)
		}

		var (
			spawnErr error
			runErr   error
			wg       sync.WaitGroup
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, spawnErr = svc.SpawnTeammate(ctx, teamID, agent.MemberSpec{Name: "late", InitialPrompt: "x"})
		}()
		go func() {
			defer wg.Done()
			_, runErr = svc.RunTeam(ctx, teamID, func(agent.TeamEvent) {})
		}()
		wg.Wait()

		if runErr != nil {
			t.Fatalf("RunTeam #%d: unexpected error %v", i, runErr)
		}
		// The spawn either won (nil) or was cleanly rejected as the team had started.
		if spawnErr != nil && !errors.Is(spawnErr, server.ErrTeamRunning) {
			t.Fatalf("SpawnTeammate #%d: err = %v, want nil or ErrTeamRunning", i, spawnErr)
		}

		// Consistency: if the spawn reported success, the member must be on the roster;
		// if it reported ErrTeamRunning, it must NOT be — no torn half-enrolment.
		members, _, _, err := svc.ListTeam(ctx, teamID)
		if err != nil {
			t.Fatalf("ListTeam #%d: %v", i, err)
		}
		hasLate := false
		for _, m := range members {
			if m.Name == "late" {
				hasLate = true
			}
		}
		switch {
		case spawnErr == nil && !hasLate:
			t.Fatalf("team #%d: spawn succeeded but member 'late' is not on the roster", i)
		case spawnErr != nil && hasLate:
			t.Fatalf("team #%d: spawn was rejected (%v) but member 'late' is on the roster", i, spawnErr)
		}
	}
}

// TestCreateTeamNilWorkspaceFactoryReturnsErrorNotPanic pins the issue-#462
// review fix: CreateTeam must NEVER MustEnvironment on a client-derived
// workspace. When the WorkspaceFactory returns nil (a misconfigured factory, a
// bad root), CreateTeam returns an ErrInvalidArgument-wrapped error rather than
// panicking inside MustEnvironment.
func TestCreateTeamNilWorkspaceFactoryReturnsErrorNotPanic(t *testing.T) {
	allow := permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil)
	memberEngine := func(tm *team.Team, spec agent.MemberSpec, _ string) agent.MemberBuild {
		cat := tool.NewCatalog()
		for _, tl := range agent.MemberTools(tm, spec.Name, nil) {
			cat.MustRegister(tl)
		}
		return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{
			LLM:     mockllm.New(mockllm.TextTurn("x")),
			Catalog: cat,
			Policy:  allow,
			Model:   "mock",
		})}
	}
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("x")),
		Catalog: tool.NewCatalog(),
		Policy:  allow,
		Model:   "mock",
	})
	_, err := newPlacementTeamTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),
		PlacementProvider: testPlacementProvider{
			root: "/ws", workspaces: func(string) tool.Workspace { return nil },
		},
		PlacementScope: "test",
		// provider-private environment construction failure
		Now:          func() time.Time { return time.Unix(0, 0) },
		MemberEngine: memberEngine,
	})
	if !errors.Is(err, server.ErrPlacementUnavailable) {
		t.Fatalf("new service err = %v, want ErrPlacementUnavailable", err)
	}
}
