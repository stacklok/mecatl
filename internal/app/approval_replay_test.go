package app

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// sseEvent is the minimal projection of a relayed proto Event JSON line the
// approval-replay gate needs: the event type and (for a permission.ask) the
// ask id. It decodes only what the assertions read, so the test needs no proto
// import.
type sseEvent struct {
	Type string `json:"type"`
	Ask  struct {
		AskID string `json:"ask_id"`
	} `json:"ask"`
}

// promptOverHTTP POSTs a prompt to the session's SSE prompt endpoint and drains
// the event stream, invoking onEvent for each decoded event (so a caller can
// approve a permission.ask inline via a concurrent /approve). It returns whether
// any permission.ask was observed on the stream. The relay loop behind /prompt is
// what Appends every event to the durable EventLog (the loop itself never does),
// so driving through it is what exercises the Phase 3a logging the 3b replay
// consumes.
func promptOverHTTP(t *testing.T, srvURL, id, text string, onEvent func(ev sseEvent)) (sawAsk bool) {
	t.Helper()
	resp, err := http.Post(srvURL+"/v1/sessions/"+id+"/prompt", "application/json",
		strings.NewReader(`{"text":`+strconv.Quote(text)+`}`))
	if err != nil {
		t.Fatalf("POST prompt: %v", err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(strings.TrimRight(sc.Text(), "\r\n"), "data: ")
		if !ok {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		if ev.Type == "permission.ask" {
			sawAsk = true
		}
		if onEvent != nil {
			onEvent(ev)
		}
	}
	return sawAsk
}

// TestApprovalReplayAfterRestartE2E is the cloud-native Phase 3b permstore-replay
// sub-gate through the FULL composition (app.Build + server.Service over the HTTP
// SSE relay), offline over a real on-disk jsonlstore. It proves an allow-always
// verdict survives a process restart via the durable EventLog: the consumer that
// kills the Phase 2 re-ask wart.
//
//  1. built1 over a shared StoreDir. The model issues a single Write tool call
//     (gated Ask under ModeDefault: a mutating tool, no allow rule; learnable, its
//     pattern is the exact path). Drive it over the HTTP /prompt relay (the relay
//     Appends every event to the durable EventLog) and approve the ask ALLOW_ALWAYS
//     via a concurrent /approve: the verdict is logged AND learned into built1's
//     in-memory permstore.
//  2. built1.Close() = process death: the in-memory permstore dies; only the durable
//     session snapshot + event log survive.
//  3. built2 over the SAME store. The model issues the SAME Write again. On the
//     run-entry load, ReplayApprovals reads the logged allow-always verdict,
//     correlates the askID back to the original ToolCall in the loaded conversation,
//     and re-Learns the rule into built2's fresh permstore, so the second call is
//     NOT re-asked.
//
// MUTATION-KILL: nil out Config.ReplayApprovals (skip the replay) and the second
// run re-asks (sawAsk2 == true) and parks forever; the "must not re-ask" assertion
// fails.
func TestApprovalReplayAfterRestartE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir()

	// Write under ModeDefault asks (a mutating tool, no allow rule) and is learnable
	// (its pattern is the exact path), so a learned rule for this path means a later
	// Write to the SAME path is not re-asked. The two runs Write the SAME path so the
	// replayed rule matches the second call exactly.
	const path = "note.txt"
	writeCall := func(id string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Write", json.RawMessage(`{"path":"`+path+`","content":"hello"}`))
	}

	baseCfg := func() Config {
		return Config{
			Workspace:           workspace,
			NoSoul:              true,
			StoreDir:            storeDir,
			MemoryDir:           memoryDir,
			envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	// built1: the model issues the Write call, then (after approval) ends the turn.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			mockllm.ToolCallTurn(writeCall("b1")),
			mockllm.TextTurn("done"),
		)
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	srv1 := httptest.NewServer(server.NewHTTPHandler(built1.Service))

	// Drive the run over /prompt and approve the ask ALLOW_ALWAYS via /approve. The
	// approve runs from the SSE drain callback; the same-process live run resolves it.
	var approved bool
	saw1 := promptOverHTTP(t, srv1.URL, string(sess.ID), "build it", func(ev sseEvent) {
		if ev.Type == "permission.ask" && !approved {
			approved = true
			body, _ := json.Marshal(map[string]any{"ask_id": ev.Ask.AskID, "verdict": session.VerdictStringAllowAlways})
			ar, aerr := http.Post(srv1.URL+"/v1/sessions/"+string(sess.ID)+"/approve",
				"application/json", strings.NewReader(string(body)))
			if aerr != nil {
				t.Errorf("POST approve: %v", aerr)
				return
			}
			ar.Body.Close()
		}
	})
	srv1.Close()
	if !saw1 || !approved {
		built1.Close()
		t.Fatalf("built1 never asked-and-approved the Write (sawAsk=%v approved=%v)", saw1, approved)
	}
	built1.Close() // process death: in-memory permstore gone; durable log + snapshot remain.

	// built2: a fresh Build over the SAME store. The model re-issues the SAME Write.
	cfg2 := baseCfg()
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			mockllm.ToolCallTurn(writeCall("b2")),
			mockllm.TextTurn("done again"),
		)
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()
	srv2 := httptest.NewServer(server.NewHTTPHandler(built2.Service))
	defer srv2.Close()

	// Drive the SECOND run. With the replay it must NOT re-ask (the SSE stream ends
	// with a result and no permission.ask was seen). Without the replay it parks on a
	// permission.ask that nobody answers and the stream would never terminate; so if a
	// re-ask is seen we POST /cancel to end the stream cleanly (the assertion below
	// then fails fast instead of hanging on the mutation).
	sawAsk2 := promptOverHTTP(t, srv2.URL, string(sess.ID), "build it again", func(ev sseEvent) {
		if ev.Type == "permission.ask" {
			body, _ := json.Marshal(map[string]any{})
			ar, aerr := http.Post(srv2.URL+"/v1/sessions/"+string(sess.ID)+"/cancel",
				"application/json", strings.NewReader(string(body)))
			if aerr == nil {
				ar.Body.Close()
			}
		}
	})
	if sawAsk2 {
		t.Fatalf("the second run RE-ASKED for Write %q after restart: the allow-always verdict was not replayed from the durable log (Phase 2 wart not fixed)", path)
	}

	final, err := built2.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after replay: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("second run final state = %q, want completed", final.State)
	}
}

// answerAsks drives a run over /prompt and answers EVERY permission.ask with the
// given verdict (so the run terminates cleanly even when it re-asks). It returns
// whether any ask was seen. It is the re-ask-expecting twin of the inline approve
// in the headline test.
func answerAsks(t *testing.T, srvURL, id, text, verdict string) (sawAsk bool) {
	t.Helper()
	return promptOverHTTP(t, srvURL, id, text, func(ev sseEvent) {
		if ev.Type != "permission.ask" {
			return
		}
		body, _ := json.Marshal(map[string]any{"ask_id": ev.Ask.AskID, "verdict": verdict})
		ar, aerr := http.Post(srvURL+"/v1/sessions/"+id+"/approve",
			"application/json", strings.NewReader(string(body)))
		if aerr != nil {
			t.Errorf("POST approve: %v", aerr)
			return
		}
		ar.Body.Close()
	})
}

// TestApprovalReplayClearedOnCloseSession is the FIX-1 regression: CloseSession must
// clear the once-per-id replay marker, so a session that was closed (its learned
// rules Forgotten) and then RELOADED in the SAME process replays its allow-always
// verdicts AGAIN — otherwise the marker short-circuits the replay and the rules stay
// evicted, re-opening the exact re-ask wart 3b kills.
//
//	build (one process, one store): run #1 allow-always's a Write (logged + learned,
//	marker set) → CloseSession (Forget evicts the rules) → run #2 over the SAME id
//	must NOT re-ask, because loadAndReopen replays the logged verdict back into the
//	fresh permstore (only possible if the marker was cleared on close).
//
// MUTATION-KILL: drop the `delete(s.replayedApprovals, id)` in CloseSession and run
// #2 re-asks (the marker is still set → replay skipped → rules gone).
func TestApprovalReplayClearedOnCloseSession(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	workspace := t.TempDir()

	const path = "note.txt"
	writeCall := func(id string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Write", json.RawMessage(`{"path":"`+path+`","content":"hi"}`))
	}
	cfg := Config{
		Workspace:           workspace,
		NoSoul:              true,
		StoreDir:            storeDir,
		MemoryDir:           t.TempDir(),
		envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
		liveModelHTTPClient: offlineHTTPClient(),
	}
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			mockllm.ToolCallTurn(writeCall("c1")), // run #1: ask -> allow-always
			mockllm.TextTurn("done"),
			mockllm.ToolCallTurn(writeCall("c2")), // run #2: must NOT re-ask
			mockllm.TextTurn("done again"),
		)
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer built.Close()
	srv := httptest.NewServer(server.NewHTTPHandler(built.Service))
	defer srv.Close()

	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	// Run #1: allow-always the Write (learned + logged).
	if saw := answerAsks(t, srv.URL, string(sess.ID), "write it", session.VerdictStringAllowAlways); !saw {
		t.Fatal("run #1 never asked for the Write")
	}
	// Close the session: OnCloseSession Forgets the learned rules; the marker must be
	// cleared too so the next load can replay them.
	built.Service.CloseSession(sess.ID)

	// Run #2 over the SAME id, SAME process. With the marker cleared, loadAndReopen
	// replays the logged allow-always verdict into the (now-empty) permstore, so the
	// Write is NOT re-asked. If a re-ask is seen we still answer it (allow-once) so the
	// stream terminates; the assertion then fails fast rather than hanging.
	sawAsk2 := answerAsks(t, srv.URL, string(sess.ID), "write it again", session.VerdictStringAllowOnce)
	if sawAsk2 {
		t.Fatal("run #2 RE-ASKED after CloseSession: the replay marker was not cleared, so the Forgotten rules were never replayed (FIX 1 regressed)")
	}
}

// TestApprovalReplayIgnoresNonAllowAlways is the MUST-ADD-3 security gate: a
// one-time (allow-once) or DENIED verdict must NEVER be replayed into the permstore
// across a restart. Only allow-always learns a durable rule; replaying a one-time
// grant or a denial as a learned allow would be a SILENT re-grant of a permission the
// user never gave.
//
//	built1 over a shared store: one Write is allow-ONCE'd, a sibling Write to a
//	DIFFERENT path is DENIED. Neither learns a durable rule. built1 dies.
//	built2 over the SAME store re-issues BOTH Writes; BOTH must RE-ASK (the replay
//	repopulated nothing).
//
// MUTATION-KILL: drop the `!ev.Approval.AllowAlways` filter in replayApprovals and
// the allow-once Write is replayed as a learned allow → built2 does NOT re-ask it →
// this fails.
func TestApprovalReplayIgnoresNonAllowAlways(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir()

	const oncePath = "once.txt"
	const denyPath = "deny.txt"
	write := func(id, path string) session.ToolCall {
		return session.NewToolCall(session.ToolCallID(id), "Write", json.RawMessage(`{"path":"`+path+`","content":"x"}`))
	}
	baseCfg := func() Config {
		return Config{
			Workspace:           workspace,
			NoSoul:              true,
			StoreDir:            storeDir,
			MemoryDir:           memoryDir,
			envDetector:         fakeEnv(map[string]string{"OPENAI_API_KEY": "sk-openai"}),
			liveModelHTTPClient: offlineHTTPClient(),
		}
	}

	// built1: Write once.txt (allow-once), then Write deny.txt (deny), then end.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			mockllm.ToolCallTurn(write("o1", oncePath)),
			mockllm.ToolCallTurn(write("d1", denyPath)),
			mockllm.TextTurn("done"),
		)
	}
	built1, err := Build(ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	srv1 := httptest.NewServer(server.NewHTTPHandler(built1.Service))

	// Answer the FIRST ask allow-once, the SECOND deny (the two Writes hit distinct
	// paths so they are distinct asks).
	asks := 0
	saw1 := promptOverHTTP(t, srv1.URL, string(sess.ID), "do both", func(ev sseEvent) {
		if ev.Type != "permission.ask" {
			return
		}
		verdict := session.VerdictStringAllowOnce
		if asks == 1 {
			verdict = session.VerdictStringDeny
		}
		asks++
		body, _ := json.Marshal(map[string]any{"ask_id": ev.Ask.AskID, "verdict": verdict})
		ar, aerr := http.Post(srv1.URL+"/v1/sessions/"+string(sess.ID)+"/approve",
			"application/json", strings.NewReader(string(body)))
		if aerr == nil {
			ar.Body.Close()
		} else {
			t.Errorf("POST approve: %v", aerr)
		}
	})
	srv1.Close()
	if !saw1 || asks < 2 {
		built1.Close()
		t.Fatalf("built1 did not raise both asks (sawAsk=%v asks=%d)", saw1, asks)
	}
	built1.Close()

	// built2: re-issue BOTH Writes. NEITHER may be auto-allowed by a replayed rule.
	cfg2 := baseCfg()
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(
			mockllm.ToolCallTurn(write("o2", oncePath)),
			mockllm.ToolCallTurn(write("d2", denyPath)),
			mockllm.TextTurn("done again"),
		)
	}
	built2, err := Build(ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()
	srv2 := httptest.NewServer(server.NewHTTPHandler(built2.Service))
	defer srv2.Close()

	// Count the re-asks; answer each (allow-once) so the run terminates. BOTH Writes
	// must re-ask: nothing was learned, so nothing was replayed.
	reasks := 0
	promptOverHTTP(t, srv2.URL, string(sess.ID), "do both again", func(ev sseEvent) {
		if ev.Type != "permission.ask" {
			return
		}
		reasks++
		body, _ := json.Marshal(map[string]any{"ask_id": ev.Ask.AskID, "verdict": session.VerdictStringAllowOnce})
		ar, aerr := http.Post(srv2.URL+"/v1/sessions/"+string(sess.ID)+"/approve",
			"application/json", strings.NewReader(string(body)))
		if aerr == nil {
			ar.Body.Close()
		}
	})
	if reasks != 2 {
		t.Fatalf("built2 re-asked %d time(s), want 2 (neither a one-time grant nor a denial may be replayed as a learned allow)", reasks)
	}
}
