package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// Convert a stopped, test-owned current store into the pre-identity format,
// retaining the already-issued review/ack key and the committed event history.
func legacyPendingReviewStore(t *testing.T, dir, oldReview, newReview string) []byte {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "sid-v1", "*.session.json"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("snapshots: %v %v", paths, err)
	}
	convert := func(data []byte) []byte {
		t.Helper()
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
		var walk func(any) any
		walk = func(v any) any {
			switch v := v.(type) {
			case map[string]any:
				if _, hook := v["Guardrail"]; hook {
					if _, phase := v["Phase"]; phase {
						delete(v, "Revision")
						delete(v, "SessionID")
					}
				}
				if _, ask := v["AskID"]; ask {
					delete(v, "session_id")
					delete(v, "SessionID")
				}
				for k, child := range v {
					switch strings.ToLower(k) {
					case "invocationid", "invocation_id", "hookid", "hook_id", "hook_revision":
						delete(v, k)
					default:
						v[k] = walk(child)
					}
				}
			case []any:
				for i := range v {
					v[i] = walk(v[i])
				}
			case string:
				if v == newReview {
					return oldReview
				}
			}
			return v
		}
		out, err := json.Marshal(walk(value))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	snapshot, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0], convert(snapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	logs, err := filepath.Glob(filepath.Join(dir, "sid-v1", "*.events.jsonl"))
	if err != nil || len(logs) != 1 {
		t.Fatalf("logs: %v %v", logs, err)
	}
	log, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(log), []byte("\n"))
	for i, line := range lines {
		lines[i] = convert(line)
	}
	if err := os.WriteFile(logs[0], append(bytes.Join(lines, []byte("\n")), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestLegacyContextualReviewContinuationAfterRestart(t *testing.T) {
	for _, verdict := range []session.ApprovalVerdict{session.VerdictAllowOnce, session.VerdictDeny} {
		t.Run(session.VerdictString(verdict), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cfg := guardrailE2ECfg(t, true, PostureAuto, "printf x >> guard_marker")
			cfg.OwnershipEnforced = true
			owner := session.WithPrincipal(ctx, &session.Principal{Issuer: "test", Subject: "alice", GrantType: session.GrantTypeUser})
			other := session.WithPrincipal(ctx, &session.Principal{Issuer: "test", Subject: "bob", GrantType: session.GrantTypeUser})
			cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
				return mockllm.New(guardrailE2EScript("printf x >> guard_marker")...)
			}
			first, err := buildIsolated(t, owner, cfg)
			if err != nil {
				t.Fatal(err)
			}
			sess, err := first.Service.CreateSession(owner, session.ModeDefault, session.Limits{MaxTurns: 3})
			if err != nil {
				t.Fatal(err)
			}
			run, err := first.Service.StartInteractiveRunContent(owner, sess.ID, "write", nil)
			if err != nil {
				t.Fatal(err)
			}
			var ask session.PendingAsk
			var oldEvents []session.Event
			var runID string
			for ev := range run.Events() {
				if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Guardrail != nil {
					oldEvents = append(oldEvents, ev)
				}
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil {
					oldEvents = append(oldEvents, ev)
					ask = *ev.Ask
					runID = ev.RunID
					first.Service.Persist(owner, sess.ID)
					break
				}
			}
			if ask.Guardrail == nil || ask.InvocationID == "" {
				t.Fatalf("initial review ask: %+v", ask)
			}
			first.Close()
			storeBefore, err := jsonlstore.New(cfg.StoreDir)
			if err != nil {
				t.Fatal(err)
			}
			for _, ev := range oldEvents {
				if err := storeBefore.Append(owner, sess.ID, ev); err != nil {
					t.Fatal(err)
				}
			}
			legacyID := string(sess.ID) + ":c1:action"
			original := legacyPendingReviewStore(t, cfg.StoreDir, legacyID, ask.Guardrail.ReviewID)
			before, err := jsonlstore.New(cfg.StoreDir)
			if err != nil {
				t.Fatal(err)
			}
			oldHook := false
			for ev, readErr := range before.Read(owner, sess.ID) {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.Guardrail != nil {
					oldHook = true
					if ev.Hook.HookID != "" || ev.Hook.SessionID != "" || ev.Hook.Revision != 0 || ev.Hook.Guardrail.ReviewID != legacyID {
						t.Fatalf("historical review was not legacy: %+v", ev.Hook)
					}
				}
			}
			legacyAsk := false
			for ev, readErr := range before.Read(owner, sess.ID) {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if ev.Type == session.EvPermissionAsk && ev.Ask != nil && ev.Ask.AskID == ask.AskID {
					legacyAsk = true
					if ev.Ask.SessionID != "" || ev.Ask.InvocationID != "" {
						t.Fatalf("historical ask was not originless legacy: %+v", ev.Ask)
					}
				}
			}
			if !legacyAsk {
				t.Fatal("missing committed legacy ask")
			}
			if !oldHook {
				t.Fatal("missing committed legacy review")
			}
			cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
				return mockllm.New(mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn(`{"assessment":"acceptable","concerns":[],"evidence":[],"missing_evidence":[]}`), mockllm.TextTurn("done"))
			}
			second, err := buildIsolated(t, owner, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer second.Close()
			restored, err := second.Service.GetSession(owner, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			pending, ok := restored.PendingAsk()
			if !ok || pending.Guardrail == nil || pending.Guardrail.ReviewID != legacyID || pending.SessionID != "" || !strings.HasPrefix(string(pending.InvocationID), "legacy_tc_") || pending.InvocationID != restored.Conversation.Messages[len(restored.Conversation.Messages)-1].ToolCalls[0].InvocationID {
				t.Fatalf("restored scope/call: %+v", pending)
			}
			watch, err := second.Service.WatchSessionEvents(owner, sess.ID, "", runID)
			if err != nil {
				t.Fatal(err)
			}
			seenAsk := false
			for envelope, watchErr := range watch {
				if watchErr != nil {
					t.Fatal(watchErr)
				}
				if envelope.Event != nil && envelope.Event.Type == session.EvPermissionAsk && envelope.Event.Ask != nil && envelope.Event.Ask.AskID == ask.AskID {
					seenAsk = true
					if envelope.Event.Ask.SessionID != sess.ID || envelope.Event.Ask.InvocationID != pending.InvocationID || envelope.Event.Ask.Guardrail.ReviewID != legacyID {
						t.Fatalf("legacy replay ask provenance: %+v", envelope.Event.Ask)
					}
					break
				}
			}
			if !seenAsk {
				t.Fatal("missing replayed legacy ask")
			}
			snapshots, _ := filepath.Glob(filepath.Join(cfg.StoreDir, "sid-v1", "*.session.json"))
			unchanged, err := os.ReadFile(snapshots[0])
			if err != nil || !bytes.Equal(unchanged, original) {
				t.Fatalf("read wrote migration: %v", err)
			}
			if _, err := second.Service.GetGuardrailReviewDetail(owner, sess.ID, legacyID); !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("restart retained live-only detail: %v", err)
			}
			if _, err := second.Service.GetGuardrailReviewDetail(other, sess.ID, legacyID); !errors.Is(err, server.ErrNotFound) {
				t.Fatalf("foreign detail: %v", err)
			}
			approval := agent.ApprovalResolution{AskID: ask.AskID, ReviewID: legacyID, Kind: session.GuardrailApprovalAction, Verdict: verdict}
			for _, bad := range []struct {
				ctx      context.Context
				run      string
				approval agent.ApprovalResolution
			}{
				{other, runID, approval}, {owner, "stale-run", approval}, {owner, runID, agent.ApprovalResolution{AskID: ask.AskID, ReviewID: "review_" + string(pending.InvocationID) + "_action", Kind: session.GuardrailApprovalAction, Verdict: verdict}},
			} {
				if _, err := second.Service.ResolveScopedRunAsk(bad.ctx, sess.ID, bad.run, bad.approval); err == nil {
					t.Fatal("invalid owner/token/review resolved old ask")
				}
			}
			ack, err := second.Service.ResolveScopedRunAsk(owner, sess.ID, runID, approval)
			if err != nil || ack.AskID != ask.AskID {
				t.Fatalf("legacy ack: %+v %v", ack, err)
			}
			if verdict == session.VerdictAllowOnce {
				// A restarted review may ask again when its checker cannot establish
				// fresh context; that second human decision must keep the old key.
				for {
					current, err := second.Service.GetSession(owner, sess.ID)
					if err != nil {
						t.Fatal(err)
					}
					fresh, ok := current.PendingAsk()
					if ok && fresh.AskID != ask.AskID {
						if fresh.Guardrail == nil || fresh.Guardrail.ReviewID != legacyID || fresh.InvocationID != pending.InvocationID {
							t.Fatalf("re-review replaced outstanding identity: %+v", fresh)
						}
						detail, err := second.Service.GetGuardrailReviewDetail(owner, sess.ID, legacyID)
						if err != nil || detail.ReviewID != legacyID || detail.ToolCallID != pending.InvocationID || detail.OriginSessionID != sess.ID {
							t.Fatalf("owner live re-review detail: %+v %v", detail, err)
						}
						if _, err := second.Service.GetGuardrailReviewDetail(other, sess.ID, legacyID); !errors.Is(err, server.ErrNotFound) {
							t.Fatalf("foreign live detail: %v", err)
						}
						if _, err := second.Service.GetGuardrailReviewDetail(owner, sess.ID, "review_"+string(pending.InvocationID)+"_action"); !errors.Is(err, server.ErrNotFound) {
							t.Fatalf("derived key aliased legacy detail: %v", err)
						}
						// An exact retry can acknowledge the already accepted verdict,
						// but it cannot resolve the distinct pending re-review ask.
						if ack, err := second.Service.ResolveScopedRunAsk(owner, sess.ID, runID, approval); err == nil && ack.AskID != ask.AskID {
							t.Fatalf("stale token acknowledged another ask: %+v", ack)
						}
						still, err := second.Service.GetSession(owner, sess.ID)
						if err != nil {
							t.Fatal(err)
						}
						stillAsk, ok := still.PendingAsk()
						if !ok || stillAsk.AskID != fresh.AskID {
							t.Fatalf("stale token advanced new ask: %+v", stillAsk)
						}
						if _, err := second.Service.ResolveScopedRunAsk(owner, sess.ID, runID, agent.ApprovalResolution{AskID: fresh.AskID, ReviewID: "review_" + string(pending.InvocationID) + "_action", Kind: fresh.Guardrail.Kind, Verdict: session.VerdictAllowOnce}); err == nil {
							t.Fatal("derived review token resolved legacy re-review")
						}
						if _, err := second.Service.ResolveScopedRunAsk(owner, sess.ID, runID, agent.ApprovalResolution{AskID: fresh.AskID, ReviewID: legacyID, Kind: fresh.Guardrail.Kind, Verdict: session.VerdictAllowOnce}); err != nil {
							t.Fatalf("re-reviewed human approval: %v", err)
						}
						break
					}
					if ctx.Err() != nil {
						t.Fatalf("no fresh review after resumed approval: %v", ctx.Err())
					}
					time.Sleep(20 * time.Millisecond)
				}
			}
			store, err := jsonlstore.New(cfg.StoreDir)
			if err != nil {
				t.Fatal(err)
			}
			var reviews []session.HookPayload
			var receipt session.ApprovalPayload
			results := 0
			for {
				reviews = nil
				results = 0
				receipt = session.ApprovalPayload{}
				complete := false
				for ev, readErr := range store.Read(ctx, sess.ID) {
					if readErr != nil {
						t.Fatal(readErr)
					}
					if ev.Type == session.EvHook && ev.Hook != nil && ev.Hook.HookID == legacyID {
						reviews = append(reviews, *ev.Hook)
					}
					if ev.Type == session.EvApproval && ev.Approval != nil && ev.Approval.AskID == ask.AskID {
						receipt = *ev.Approval
					}
					if ev.Type == session.EvToolResult && ev.ToolResult != nil && ev.ToolResult.CallID == "c1" {
						results++
						if ev.ToolResult.InvocationID != pending.InvocationID || ev.ToolResult.IsError != (verdict == session.VerdictDeny) {
							t.Fatalf("result provenance/denial: %+v", ev.ToolResult)
						}
					}
					if ev.Type == session.EvResult && ev.RunID == runID {
						complete = true
					}
				}
				if complete && results > 0 {
					break
				}
				if ctx.Err() != nil {
					t.Fatalf("resumed review timed out: %v; results=%d receipt=%+v reviews=%+v", ctx.Err(), results, receipt, reviews)
				}
				time.Sleep(20 * time.Millisecond)
			}
			if results != 1 || receipt.ReviewID != legacyID || receipt.InvocationID != pending.InvocationID || receipt.SessionID != sess.ID || len(reviews) == 0 {
				t.Fatalf("continuation results=%d receipt=%+v reviews=%+v", results, receipt, reviews)
			}
			want := "execute"
			if verdict == session.VerdictDeny {
				want = "deny"
			}
			last := reviews[len(reviews)-1]
			if last.Guardrail == nil || last.Guardrail.ReviewID != legacyID || last.Guardrail.Disposition != want || last.InvocationID != pending.InvocationID || last.SessionID != sess.ID || last.Revision == 0 {
				t.Fatalf("legacy review disposition: %+v", last)
			}
			data, statErr := os.ReadFile(filepath.Join(cfg.Workspace, guardrailMarker))
			if verdict == session.VerdictDeny {
				if !os.IsNotExist(statErr) {
					t.Fatalf("denied action disclosed/executed: %q %v", data, statErr)
				}
			} else if statErr != nil || string(data) != "x" {
				t.Fatalf("action not executed exactly once: %q %v", data, statErr)
			}
			if _, err := second.Service.ResolveScopedRunAsk(owner, sess.ID, runID, approval); err == nil {
				t.Fatal("settled legacy ask accepted a stale token")
			}
			second.Close()
			// The completed provider pairing ID may recur, but the next accepted
			// invocation must derive a fresh review key instead of reusing the
			// outstanding legacy acknowledgement key.
			cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
				return mockllm.New(guardrailE2EScript("printf y >> guard_marker")...)
			}
			third, err := buildIsolated(t, owner, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer third.Close()
			next, err := third.Service.StartInteractiveRunContent(owner, sess.ID, "another action", nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for ev := range next.Events() {
				if ev.Type != session.EvPermissionAsk || ev.Ask == nil || ev.Ask.Guardrail == nil {
					continue
				}
				found = true
				if ev.Ask.Guardrail.ReviewID == legacyID || ev.Ask.Guardrail.ReviewID != "review_"+string(ev.Ask.InvocationID)+"_action" || ev.Ask.InvocationID == pending.InvocationID {
					t.Fatalf("new invocation reused legacy identity: %+v", ev.Ask)
				}
				next.Cancel()
			}
			if !found {
				t.Fatal("next accepted call did not reach contextual review")
			}
			third.Service.FinishRun(sess.ID, next)
		})
	}
}
