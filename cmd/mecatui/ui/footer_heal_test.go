package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

// healModel builds a connected model whose CreateSession echoed a 0-window
// ResolvedModel — the RACED-create case. The 0 is EXACTLY the server's DELIBERATE
// PROVISIONAL value (issue #66): the echo resolver returns 0 for a live-only model
// while the one-shot live model-list refresh is still in flight, honestly signalling
// "live window not in yet" so the client refetches on turn-end. It also seeds the
// conversation occupancy so the footer would render a denominator bar IF the window
// were known.
func healModel(t *testing.T, conv *fakeConv) Model {
	t.Helper()
	m := newTestModelFromDeps(Deps{
		Session:     conv,
		Conv:        conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         t.Context(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 40},
		// Raced create: the server echoed a DELIBERATE PROVISIONAL 0 window for the
		// live-only model (the echo resolver's pre-completion value, not a 128k floor).
		client.SessionReadyMsg{SessionID: "sess-test-0001", ResolvedModel: client.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5.5", ContextWindow: 0}},
	)
	m.contextTokens = 40000
	m.phase = phaseIdle
	m.sel = selection{} // inactive: show the status+meter footer, not the selection count
	m.refreshView()
	return m
}

// footerStr renders the (ANSI-stripped) footer the user sees.
func footerStr(m Model) string { return stripANSIstr(m.fitFooter("connected", 160)) }

// TestFooterHealRaceThenHeal is the decisive offline reproduction of the issue-#66
// footer-heal race: a raced create echoes a 0 window (the footer degrades to the
// bare "ctx 40K", no bar), the turn-end gate fires a GetSession refetch, and the
// server's healed live window (1.05M) raises the denominator so the footer renders
// the "40K/1.0M" bar — WITHOUT a model switch or restart.
func TestFooterHealRaceThenHeal(t *testing.T) {
	conv := &fakeConv{
		recv: &fakeRecver{}, send: &fakeSender{},
		// The healed resolved model the server returns post-swap.
		getSessionResults: []client.ResolvedModel{
			{ProviderID: "openrouter", ModelID: "openai/gpt-5.5", ContextWindow: 1_050_000},
		},
	}
	m := healModel(t, conv)

	// 1) Raced create → window unknown → the footer degrades (no denominator bar).
	if got := m.contextWindow(); got != 0 {
		t.Fatalf("contextWindow() = %d, want 0 (raced create echoed a 0 window)", got)
	}
	if foot := footerStr(m); !strings.Contains(foot, "ctx 40K") || strings.Contains(foot, "/") {
		t.Fatalf("footer = %q, want the degraded bare 'ctx 40K' with no '/' denominator", foot)
	}

	// 2) A turn boundary fires the heal refetch. Drive the REAL reducer with a
	// TurnEndMsg and assert it emits a command (the RefreshResolvedModelCmd batched
	// alongside afterEvent's cmd).
	mm, cmd := m.Update(client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000, OutputTokens: 100}})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("TurnEndMsg with an unknown window emitted no command — the heal refetch did not fire")
	}
	// Executing the batched command tree runs the refetch goroutine; assert the fake's
	// GetSession was invoked (call-count, not batch-sniffing).
	msg := cmd()
	drainBatch(t, msg)
	if n := conv.getSessionCalls(); n != 1 {
		t.Fatalf("GetSession called %d times after one turn-end with unknown window, want 1", n)
	}

	// 3) Feed the heal result back through the reducer: the window heals to 1.05M,
	// contextWindow() reports it, and the footer renders the bar.
	healed := client.ResolvedModelMsg{SessionID: "sess-test-0001", Resolved: client.ResolvedModel{ProviderID: "openrouter", ModelID: "openai/gpt-5.5", ContextWindow: 1_050_000}}
	m = applyAll(m, healed)
	if got := m.resolvedSessionModel.ContextWindow; got != 1_050_000 {
		t.Fatalf("resolvedSessionModel.ContextWindow = %d, want 1,050,000 after the heal", got)
	}
	if got := m.contextWindow(); got != 1_050_000 {
		t.Fatalf("contextWindow() = %d, want 1,050,000 after the heal", got)
	}
	if foot := footerStr(m); !strings.Contains(foot, "40K/1.1M") {
		t.Fatalf("footer = %q, want the healed '40K/1.1M' bar", foot)
	}
	if foot := footerStr(m); !strings.Contains(foot, ctxGlyphEmpty) && !strings.Contains(foot, ctxGlyphOk) {
		t.Fatalf("footer = %q, want a meter bar glyph after the heal", footerStr(m))
	}

	// 4) Identity untouched (only the denominator self-corrected).
	if m.resolvedSessionModel.ProviderID != "openrouter" || m.resolvedSessionModel.ModelID != "openai/gpt-5.5" {
		t.Fatalf("heal mutated the model identity: %+v", m.resolvedSessionModel)
	}
}

// TestFooterHealStopsRefetchingOnceKnown proves the refetch is BOUNDED: once the
// window is known (non-zero) the turn-end gate skips the RPC entirely — it fires at
// most until the first heal lands, never on every turn forever.
func TestFooterHealStopsRefetchingOnceKnown(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := healModel(t, conv)
	m.resolvedSessionModel.ContextWindow = 200000 // window already KNOWN

	_, cmd := m.Update(client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000}})
	if cmd != nil {
		// afterEvent itself can return a non-nil cmd; what matters is that GetSession
		// is NOT among whatever it returned. Execute and confirm no refetch fired.
		drainBatch(t, cmd())
	}
	if n := conv.getSessionCalls(); n != 0 {
		t.Fatalf("GetSession called %d times with a KNOWN window, want 0 (refetch must be skipped)", n)
	}
}

// TestFooterHealCatalogued128KNeverRefetches proves the client keys the heal gate
// ONLY off ==0, never <=128000: a session whose echo is a GENUINE 128000 (a real
// catalogued window, non-zero from the first echo — the server's echoWindowResolver
// returns it from t=0 because the model is known-at-real-value, never provisional)
// must NOT fire the turn-end refetch. A naive gate keyed off "<=128k" would refetch a
// real 128k model forever; the actual ==0 gate skips it.
func TestFooterHealCatalogued128KNeverRefetches(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := newTestModelFromDeps(Deps{
		Session: conv, Conv: conv,
		Theme:       theme.New("aztec", theme.AztecPalette()),
		Ctx:         t.Context(),
		NoAltScreen: true,
	})
	m = applyAll(m,
		tea.WindowSizeMsg{Width: 160, Height: 40},
		// A genuinely-catalogued model echoes its real 128000 window from t=0.
		client.SessionReadyMsg{SessionID: "sess-cat-128k", ResolvedModel: client.ResolvedModel{ProviderID: "openai", ModelID: "gpt-cat", ContextWindow: 128_000}},
	)
	m.contextTokens = 40000
	m.phase = phaseIdle
	m.sel = selection{}
	m.refreshView()

	if got := m.resolvedSessionModel.ContextWindow; got != 128_000 {
		t.Fatalf("precondition: resolvedSessionModel.ContextWindow = %d, want a genuine 128000 first echo", got)
	}
	_, cmd := m.Update(client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000}})
	if cmd != nil {
		drainBatch(t, cmd()) // afterEvent may return a cmd; what matters is no GetSession fires
	}
	if n := conv.getSessionCalls(); n != 0 {
		t.Fatalf("GetSession called %d times for a genuine 128k window, want 0 (the gate keys ONLY off ==0, never <=128k)", n)
	}
}

// TestFooterHealDropsStaleSession proves the heal is session-correlated: a
// ResolvedModelMsg whose SessionID no longer matches the current session (a refetch
// in flight when a /models switch rebound the id) is dropped — its stale window must
// not land on the new session.
func TestFooterHealDropsStaleSession(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := healModel(t, conv) // sessionID == "sess-test-0001", window 0
	m.caps = client.Capabilities{Image: true, Teams: true}

	m = applyAll(m, client.ResolvedModelMsg{SessionID: "sess-OTHER", Resolved: client.ResolvedModel{ContextWindow: 1_050_000}, Capabilities: client.Capabilities{SessionMediaPresent: true}})
	if got := m.resolvedSessionModel.ContextWindow; got != 0 {
		t.Fatalf("stale-session heal landed: window = %d, want 0 (dropped)", got)
	}
	if !m.caps.Image || !m.caps.Teams {
		t.Fatalf("stale-session heal changed capabilities: %+v", m.caps)
	}
}

// TestFooterHealRaiseOnly proves the heal only ever RAISES the denominator: a
// smaller (or zero) healed window must not shrink an already-known window.
func TestFooterHealRaiseOnly(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
	m := healModel(t, conv)
	m.resolvedSessionModel.ContextWindow = 1_050_000 // already known-large

	// A smaller window arrives (a transient floor read) — must NOT lower.
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "sess-test-0001", Resolved: client.ResolvedModel{ContextWindow: 128000}})
	if got := m.resolvedSessionModel.ContextWindow; got != 1_050_000 {
		t.Fatalf("smaller heal lowered the window to %d, want it kept at 1,050,000 (raise-only)", got)
	}
	// A zero window (unknown) must NOT erase it either.
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "sess-test-0001", Resolved: client.ResolvedModel{ContextWindow: 0}})
	if got := m.resolvedSessionModel.ContextWindow; got != 1_050_000 {
		t.Fatalf("zero heal erased the window to %d, want it kept at 1,050,000 (raise-only)", got)
	}
}

// TestFooterHealErrorBenign proves a failed refetch is benign: the ResolvedModelMsg
// error arm keeps the current denominator (the next turn boundary retries), and the
// turn-end gate still fired the RPC.
func TestFooterHealErrorBenign(t *testing.T) {
	conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}, getSessionErr: errFakeGet}
	m := healModel(t, conv)
	m.caps = client.Capabilities{Image: true, Teams: true}

	mm, cmd := m.Update(client.TurnEndMsg{Turn: 1, Usage: client.Usage{InputTokens: 40000}})
	m = mm.(Model)
	if cmd == nil {
		t.Fatal("turn-end with unknown window emitted no command")
	}
	drainBatch(t, cmd()) // runs the failing refetch
	if n := conv.getSessionCalls(); n != 1 {
		t.Fatalf("GetSession called %d times, want 1", n)
	}
	// Feed the error result back: window stays unknown (0), no panic, footer degraded.
	m = applyAll(m, client.ResolvedModelMsg{SessionID: "sess-test-0001", Capabilities: client.Capabilities{SessionMediaPresent: true}, Err: errFakeGet})
	if got := m.resolvedSessionModel.ContextWindow; got != 0 {
		t.Fatalf("error heal changed the window to %d, want 0 (benign)", got)
	}
	if !m.caps.Image || !m.caps.Teams {
		t.Fatalf("error heal changed capabilities: %+v", m.caps)
	}
}

func TestSessionCapabilitiesSnapshotUpdatesMediaWithoutLosingGlobals(t *testing.T) {
	seed := client.Capabilities{
		Image: true, Audio: true, MCP: true, Teams: true, Memory: true,
	}
	preserved := seed
	preserved.Image = false
	preserved.Audio = false
	preserved.SessionMediaPresent = true

	tests := []struct {
		name string
		caps client.Capabilities
		want client.Capabilities
	}{
		{
			name: "explicit false",
			caps: client.Capabilities{SessionMediaPresent: true},
			want: preserved,
		},
		{
			name: "image",
			caps: client.Capabilities{SessionMediaPresent: true, Image: true},
			want: func() client.Capabilities { c := preserved; c.Image = true; return c }(),
		},
		{
			name: "audio",
			caps: client.Capabilities{SessionMediaPresent: true, Audio: true},
			want: func() client.Capabilities { c := preserved; c.Audio = true; return c }(),
		},
		{
			name: "both",
			caps: client.Capabilities{SessionMediaPresent: true, Image: true, Audio: true},
			want: func() client.Capabilities { c := preserved; c.Image = true; c.Audio = true; return c }(),
		},
		{
			name: "full snapshot replaces globals",
			caps: client.Capabilities{SessionMediaPresent: true, Image: true, SlashCommands: true},
			want: client.Capabilities{SessionMediaPresent: true, Image: true, SlashCommands: true},
		},
		{
			name: "legacy absent snapshot",
			caps: client.Capabilities{},
			want: seed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conv := &fakeConv{recv: &fakeRecver{}, send: &fakeSender{}}
			m := healModel(t, conv)
			m.caps = seed
			m = applyAll(m, client.ResolvedModelMsg{
				SessionID:    "sess-test-0001",
				Capabilities: tt.caps,
			})
			if m.caps != tt.want {
				t.Fatalf("capabilities = %+v, want %+v", m.caps, tt.want)
			}
		})
	}
}

// drainBatch executes a (possibly batched) command-result tree, feeding any nested
// tea.BatchMsg / tea.Cmd produced. It is how a test "runs" the commands a reducer
// returned so a fake collaborator (GetSession) is actually invoked.
func drainBatch(t *testing.T, msg tea.Msg) {
	t.Helper()
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			if c == nil {
				continue
			}
			drainBatch(t, c())
		}
	case tea.Cmd:
		drainBatch(t, v())
	}
}

// errFakeGet is the sentinel a fake GetSession returns to drive the benign-error path.
var errFakeGet = fakeGetErr("get session failed")

type fakeGetErr string

func (e fakeGetErr) Error() string { return string(e) }
