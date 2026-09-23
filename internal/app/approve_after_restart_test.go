package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// TestApproveAfterRestartE2E is the cloud-native Phase 2 falsifiable gate through
// the FULL composition (app.Build → server.Service), offline. It mirrors
// TestSelectorSessionSurvivesRestartE2E's two-Build shape:
//
//  1. built1 over a shared StoreDir; the model returns a Write tool call gated as
//     Ask (Write under ModeDefault asks). Start a run, range to EvPermissionAsk,
//     capture the AskID, and model the relay's Persist-on-ask (Service.Persist) so
//     a durable StateAwaiting snapshot is saved. DO NOT approve.
//  2. built1.Close() = process death: the parked run + askRegistry die; the durable
//     awaiting snapshot is the last write to the store.
//  3. built2 over the SAME store; POST the exact run/ask to the public
//     /controls/resolve-ask endpoint, which acknowledges before driving the
//     restored run independently.
//  4. Read the finite durable HTTP event replay: assert EXACTLY ONE non-error
//     tool.result for the pending call id, a clean StopEndTurn terminal, and a
//     completed session.
//
// Mutation-verified (each leg has a documented failing mutation):
//   - revert resumeFromAwaiting to noActiveRun → step 3 returns a non-2xx
//     resolve-ask response.
//   - skip the sibling close-out (the multi-tool engine unit test) → dangling
//     tool_use → the resumed run fails instead of completing.
//   - re-dispatch / double-record the pending call → two EvToolResult for the call
//     id (and the Write filesystem effect twice) → the exactly-once assertion fails.
func TestApproveAfterRestartE2E(t *testing.T) {
	ctx := context.Background()
	storeDir := t.TempDir()
	memoryDir := t.TempDir()
	workspace := t.TempDir()
	target := filepath.Join(workspace, "note.txt")

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

	// built1: the model issues a single Write tool call. The default FS catalog has a
	// real Write tool that ASKS under ModeDefault, so the run parks awaiting.
	cfg1 := baseCfg()
	cfg1.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.ToolCallTurn(
			session.NewToolCall("w1", "Write", json.RawMessage(`{"path":"note.txt","content":"survived the restart"}`)),
		))
	}
	built1, err := buildIsolated(t, ctx, cfg1)
	if err != nil {
		t.Fatalf("Build #1: %v", err)
	}
	sess, err := built1.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		built1.Close()
		t.Fatalf("CreateSession: %v", err)
	}
	run1, err := built1.Service.StartRun(ctx, sess.ID, "write the note")
	if err != nil {
		built1.Close()
		t.Fatalf("StartRun (pre-restart): %v", err)
	}
	var askID, runID string
	for ev := range run1.Events() {
		if ev.Type == session.EvPermissionAsk && ev.Ask != nil && askID == "" {
			askID = ev.Ask.AskID
			runID = ev.RunID
			// Model the relay's Persist-on-ask: a durable StateAwaiting snapshot.
			built1.Service.Persist(ctx, sess.ID)
			break // stop ranging; DO NOT approve. The parked run dies with built1.
		}
	}
	if askID == "" || runID == "" {
		built1.Close()
		t.Fatalf("the pre-restart run never raised an exact-run permission ask for Write (run_id=%q ask_id=%q)", runID, askID)
	}
	// The target must NOT exist yet — the Write is parked, not executed.
	if _, statErr := os.Stat(target); statErr == nil {
		built1.Close()
		t.Fatal("the Write executed before approval — it must be parked at the ask")
	}
	built1.Close() // process death: parked run + askRegistry die; awaiting snapshot persists.

	// built2: a brand-new Build over the SAME store. The model has the continuation
	// turn (the Write tool call already happened pre-restart).
	cfg2 := baseCfg()
	cfg2.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return mockllm.New(mockllm.TextTurn("done after approval"))
	}
	built2, err := buildIsolated(t, ctx, cfg2)
	if err != nil {
		t.Fatalf("Build #2: %v", err)
	}
	defer built2.Close()

	srv2 := httptest.NewServer(server.NewHTTPHandler(built2.Service))
	defer srv2.Close()
	controlBody, err := json.Marshal(map[string]string{
		"expected_run_id": runID,
		"ask_id":          askID,
		"verdict":         session.VerdictStringAllowOnce,
	})
	if err != nil {
		t.Fatal(err)
	}
	controlCtx, cancelControl := context.WithTimeout(ctx, 10*time.Second)
	defer cancelControl()
	controlReq, err := http.NewRequestWithContext(controlCtx, http.MethodPost,
		srv2.URL+"/v1/sessions/"+string(sess.ID)+"/controls/resolve-ask", bytes.NewReader(controlBody))
	if err != nil {
		t.Fatal(err)
	}
	controlReq.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(controlReq)
	if err != nil {
		t.Fatalf("POST resolve-ask after restart: %v", err)
	}
	ackBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST resolve-ask status = %d: %s", resp.StatusCode, ackBody)
	}
	var ack struct {
		RunID string `json:"run_id"`
		AskID string `json:"ask_id"`
	}
	if err := json.Unmarshal(ackBody, &ack); err != nil {
		t.Fatalf("decode resolve-ask acknowledgement: %v", err)
	}
	if ack.RunID != runID || ack.AskID != askID {
		t.Fatalf("resolve-ask acknowledgement = %+v, want run_id=%q ask_id=%q", ack, runID, askID)
	}

	var stop session.StopReason
	var nonErrorResults, totalResults int
	for {
		events, replayErr := readRestartEvents(controlCtx, srv2.URL+"/v1/sessions/"+string(sess.ID)+"/events")
		if replayErr != nil {
			t.Fatalf("GET durable events: %v", replayErr)
		}
		stop = ""
		nonErrorResults, totalResults = 0, 0
		for _, ev := range events {
			if ev.RunID != runID {
				continue
			}
			if ev.Type == "tool.result" && ev.ToolResult != nil && ev.ToolResult.CallID == "w1" {
				totalResults++
				if !ev.ToolResult.IsError {
					nonErrorResults++
				}
			}
			if ev.Type == "result" && ev.Result != nil {
				stop = session.StopReason(ev.Result.Stop)
			}
		}
		if stop != "" {
			break
		}
		if controlCtx.Err() != nil {
			t.Fatalf("detached resumed run %q did not reach a durable terminal event: %v", runID, controlCtx.Err())
		}
		time.Sleep(20 * time.Millisecond)
	}

	if nonErrorResults != 1 || totalResults != 1 {
		t.Fatalf("EvToolResult for w1: non-error=%d total=%d, want exactly 1 non-error / 1 total (exactly-once)", nonErrorResults, totalResults)
	}
	if stop != session.StopEndTurn {
		t.Fatalf("resumed run stop = %q, want %q", stop, session.StopEndTurn)
	}
	// The filesystem side effect happened EXACTLY ONCE.
	data, statErr := os.ReadFile(target)
	if statErr != nil {
		t.Fatalf("the approved Write did not create the target file: %v", statErr)
	}
	if string(data) != "survived the restart" {
		t.Fatalf("target content = %q, want %q", string(data), "survived the restart")
	}
	final, err := built2.Service.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession after resume: %v", err)
	}
	if final.State != session.StateCompleted {
		t.Fatalf("resumed session final state = %q, want completed", final.State)
	}
}

type restartReplayEvent struct {
	Type       string `json:"type"`
	RunID      string `json:"run_id"`
	ToolResult *struct {
		CallID  string `json:"call_id"`
		IsError bool   `json:"is_error"`
	} `json:"tool_result"`
	Result *struct {
		Stop string `json:"stop"`
	} `json:"result"`
}

func readRestartEvents(ctx context.Context, url string) ([]restartReplayEvent, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var events []restartReplayEvent
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		data, ok := strings.CutPrefix(strings.TrimRight(scanner.Text(), "\r"), "data: ")
		if !ok {
			continue
		}
		var ev restartReplayEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return nil, err
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}
