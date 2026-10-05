package server_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

type availabilityPartsTool struct{ scriptTool }

func (s *availabilityPartsTool) Execute(_ context.Context, in session.ToolCall, _ tool.Environment) (session.ToolResult, error) {
	s.runCount.Add(1)
	return session.NewToolResultWithParts(in.ID, "visible result", []session.Content{
		session.NewTextBlock("visible result"),
		session.NewStructuredContentBlock(`{"ok":true}`),
		{BlockKind: session.BlockImage, Kind: session.MediaImage, MIMEType: "image/png", Data: []byte{1, 2, 3}},
	}), nil
}

type blockedReadPreparationPolicy struct {
	port.PermissionPolicy
	entered  chan struct{}
	observed chan struct{}
	release  chan struct{}
}

func (p *blockedReadPreparationPolicy) Evaluate(ctx context.Context, id session.SessionID, mode session.PermissionMode, call session.ToolCall, ws tool.WorkspaceReader) port.PermissionResult {
	close(p.entered)
	select {
	case <-ctx.Done():
		close(p.observed)
	case <-p.release:
	}
	return p.PermissionPolicy.Evaluate(ctx, id, mode, call, ws)
}

// TestADR_0370_Scenario3_LiveOnlyProjection pins the live/durable split at
// the transport boundary, rather than only checking the recorder in isolation.
func TestADR_0370_Scenario3_LiveOnlyProjection(t *testing.T) {
	for _, transport := range []string{"grpc", "http", "subscription"} {
		t.Run(transport, func(t *testing.T) {
			log := memstore.NewEventLog()
			store := memstore.New()
			cat := tool.NewCatalog()
			cat.MustRegister(&availabilityPartsTool{scriptTool: scriptTool{name: "Read", readOnly: true}})
			llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "Read", `{}`)), mockllm.TextTurn("done"))
			recorder := &heldResultRecorder{}
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: cat, Policy: permpolicy.NewPolicy(allowRules(), nil), Model: "test-model", ToolCallRecorder: recorder})
			svc, err := newPlacementTestService(server.Config{Engine: engine, Store: store, EventLog: log, DefaultCapabilities: llm.Capabilities()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			client, cleanup := dialGRPC(t, svc)
			defer cleanup()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var live []*mecatlv1.Event
			switch transport {
			case "grpc":
				stream, err := client.Converse(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"}}}); err != nil {
					t.Fatal(err)
				}
				_ = stream.CloseSend()
				for {
					response, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
					live = append(live, response.GetEvent())
				}
			case "http":
				httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
				defer httpServer.Close()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", strings.NewReader(`{"text":"go"}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("prompt status = %d", resp.StatusCode)
				}
				live = parseSSE(t, bufio.NewReader(resp.Body))
			case "subscription":
				stream, err := client.StreamSessionLive(ctx, &mecatlv1.StreamSessionLiveRequest{SessionId: string(sess.ID)})
				if err != nil {
					t.Fatal(err)
				}
				received := make(chan *mecatlv1.Event, 64)
				go func() {
					for {
						ev, err := stream.Recv()
						if err != nil {
							return
						}
						select {
						case received <- ev:
						case <-ctx.Done():
							return
						}
					}
				}()
				// A probe handshake ensures the asynchronous gRPC subscription is installed.
				probe := session.Event{Type: session.EvNoProgress, Text: "subscription-ready"}
				ready := false
				for !ready {
					svc.PublishSessionEvent(sess.ID, probe)
					select {
					case ev := <-received:
						ready = ev.GetType() == string(probe.Type) && ev.GetText() == probe.Text
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
				run, err := svc.StartRunContent(ctx, sess.ID, "go", nil)
				if err != nil {
					t.Fatal(err)
				}
				runRecorder := server.NewRunEventRecorder(ctx, svc, sess.ID)
				for ev := range run.Events() {
					runRecorder.Observe(ev)
					svc.PublishSessionEvent(sess.ID, ev)
				}
				runRecorder.Close()
				svc.Persist(ctx, sess.ID)
				svc.FinishRun(sess.ID, run)
				for {
					select {
					case ev := <-received:
						live = append(live, ev)
						if ev.GetType() == string(session.EvResult) {
							goto subscriptionDone
						}
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
			subscriptionDone:
			}
			available, canonical := 0, 0
			var availableResult, canonicalResult *mecatlv1.ToolResult
			for _, ev := range live {
				if ev.GetType() != string(session.EvToolResultAvailable) && ev.GetType() != string(session.EvToolResult) {
					continue
				}
				r := ev.GetToolResult()
				if r.GetCallId() != "c1" || r.GetContent() != "visible result" || r.GetIsError() || r.GetStructuredContent() != `{"ok":true}` || len(r.GetBlocks()) != 3 || r.GetBlocks()[2].GetKind() != mecatlv1.ContentBlock_KIND_IMAGE || string(r.GetBlocks()[2].GetData()) != string([]byte{1, 2, 3}) {
					t.Fatalf("%s projection: %+v", ev.GetType(), r)
				}
				if ev.GetType() == string(session.EvToolResultAvailable) {
					available++
					availableResult = r
				} else {
					canonical++
					canonicalResult = r
				}
			}
			if available != 1 || canonical != 1 || !proto.Equal(availableResult, canonicalResult) {
				t.Fatalf("%s availability=%d canonical=%d equal=%t", transport, available, canonical, proto.Equal(availableResult, canonicalResult))
			}
			logged := readEventLog(t, log, sess.ID)
			durable := 0
			for _, ev := range logged {
				if ev.Type == session.EvToolResultAvailable {
					t.Fatalf("%s availability persisted: %+v", transport, ev)
				}
				if ev.Type == session.EvToolResult {
					durable++
				}
			}
			if durable != 1 {
				t.Fatalf("%s durable canonical results = %d", transport, durable)
			}
			replay, err := client.StreamSessionEvents(ctx, &mecatlv1.StreamSessionEventsRequest{SessionId: string(sess.ID)})
			if err != nil {
				t.Fatal(err)
			}
			replayed := 0
			for {
				ev, err := replay.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if ev.GetType() == string(session.EvToolResultAvailable) {
					t.Fatal("availability reached gRPC replay")
				}
				if ev.GetType() == string(session.EvToolResult) {
					replayed++
				}
			}
			if replayed != 1 {
				t.Fatalf("gRPC replay canonical results = %d", replayed)
			}
			watch, err := client.WatchSessionEvents(ctx, &mecatlv1.WatchSessionEventsRequest{SessionId: string(sess.ID)})
			if err != nil {
				t.Fatal(err)
			}
			watched := 0
			for {
				frame, err := watch.Recv()
				if err != nil {
					t.Fatal(err)
				}
				if frame.GetEvent().GetType() == string(session.EvToolResultAvailable) {
					t.Fatal("availability reached gRPC watch")
				}
				if frame.GetEvent().GetType() == string(session.EvToolResult) {
					watched++
				}
				if frame.GetEvent() == nil && frame.GetPhase() == server.WatchPhaseLive {
					break
				}
			}
			if watched != 1 {
				t.Fatalf("gRPC watch canonical results = %d", watched)
			}
			httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
			defer httpServer.Close()
			replayResp, err := http.Get(httpServer.URL + "/v1/sessions/" + string(sess.ID) + "/events")
			if err != nil {
				t.Fatal(err)
			}
			if replayResp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP replay status = %d", replayResp.StatusCode)
			}
			httpReplay := parseSSE(t, bufio.NewReader(replayResp.Body))
			_ = replayResp.Body.Close()
			httpResults := 0
			for _, ev := range httpReplay {
				if ev.GetType() == string(session.EvToolResultAvailable) {
					t.Fatal("availability reached HTTP replay")
				}
				if ev.GetType() == string(session.EvToolResult) {
					httpResults++
				}
			}
			if httpResults != 1 {
				t.Fatalf("HTTP replay canonical results = %d", httpResults)
			}
			watchReq, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/v1/sessions/"+string(sess.ID)+"/watch", nil)
			if err != nil {
				t.Fatal(err)
			}
			watchResp, err := http.DefaultClient.Do(watchReq)
			if err != nil {
				t.Fatal(err)
			}
			defer watchResp.Body.Close()
			if watchResp.StatusCode != http.StatusOK {
				t.Fatalf("HTTP watch status = %d", watchResp.StatusCode)
			}
			reader := bufio.NewReader(watchResp.Body)
			httpWatched := 0
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
				if !ok {
					continue
				}
				var frame mecatlv1.WatchSessionEventsResponse
				if err := json.Unmarshal([]byte(data), &frame); err != nil {
					t.Fatal(err)
				}
				if frame.GetEvent().GetType() == string(session.EvToolResultAvailable) {
					t.Fatal("availability reached HTTP watch")
				}
				if frame.GetEvent().GetType() == string(session.EvToolResult) {
					httpWatched++
				}
				if frame.GetEvent() == nil && frame.GetPhase() == server.WatchPhaseLive {
					break
				}
			}
			if httpWatched != 1 {
				t.Fatalf("HTTP watch canonical results = %d", httpWatched)
			}
			loaded, err := store.Load(ctx, sess.ID)
			if err != nil {
				t.Fatal(err)
			}
			folded, err := eventsource.Fold(eventsource.SessionMeta{ID: sess.ID, Mode: session.ModeDefault, EnvironmentRef: loaded.EnvironmentRef, CreatedAt: loaded.CreatedAt}, log.Read(ctx, sess.ID))
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Counters.ToolCalls != 1 || folded.Counters.ToolCalls != 1 {
				t.Fatalf("tool call counters: live=%d folded=%d", loaded.Counters.ToolCalls, folded.Counters.ToolCalls)
			}
			if recorded := recorder.snapshot(); len(recorded) != 1 || recorded[0].Content != "visible result" || len(recorded[0].Parts) != 3 {
				t.Fatalf("tool call recorder = %+v, want one complete canonical result", recorded)
			}
			for _, history := range [][]session.Message{loaded.Conversation.Messages, folded.Conversation.Messages} {
				results := 0
				for _, msg := range history {
					if msg.ToolResult != nil {
						results++
						if msg.ToolResult.Content != "visible result" || len(msg.ToolResult.Parts) != 3 {
							t.Fatalf("history result = %+v", msg.ToolResult)
						}
					}
				}
				if results != 1 {
					t.Fatalf("history has %d tool results, want one", results)
				}
			}
		})
	}
	for _, transport := range []string{"grpc-disconnect", "http-disconnect", "grpc-preparation-disconnect", "http-preparation-disconnect"} {
		t.Run(transport, func(t *testing.T) {
			log := memstore.NewEventLog()
			gate := make(chan struct{})
			policy := port.PermissionPolicy(permpolicy.NewPolicy(allowRules(), nil))
			var blocked *blockedReadPreparationPolicy
			if strings.Contains(transport, "preparation") {
				blocked = &blockedReadPreparationPolicy{PermissionPolicy: policy, entered: make(chan struct{}), observed: make(chan struct{}), release: make(chan struct{})}
				policy = blocked
			}
			llm := mockllm.New(mockllm.ToolCallTurn(call("c1", "Read", `{}`)), mockllm.TextTurn("done"))
			engine := agent.NewEngine(agent.Deps{LLM: llm, Catalog: catalogWith(&gateTool{name: "Read", release: gate}), Policy: policy, Model: "test-model"})
			svc, err := newPlacementTestService(server.Config{Engine: engine, Store: memstore.New(), EventLog: log, DefaultCapabilities: llm.Capabilities()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			sess, err := svc.CreateSession(t.Context(), session.ModeDefault, session.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch transport {
			case "grpc-disconnect", "grpc-preparation-disconnect":
				client, cleanup := dialGRPC(t, svc)
				defer cleanup()
				stream, err := client.Converse(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(sess.ID), Text: "go"}}}); err != nil {
					t.Fatal(err)
				}
				for {
					frame, err := stream.Recv()
					if err != nil {
						t.Fatal(err)
					}
					if frame.GetEvent().GetType() == string(session.EvToolCall) {
						break
					}
				}
			case "http-disconnect", "http-preparation-disconnect":
				httpServer := httptest.NewServer(server.NewHTTPHandler(svc))
				defer httpServer.Close()
				req, err := http.NewRequestWithContext(ctx, http.MethodPost, httpServer.URL+"/v1/sessions/"+string(sess.ID)+"/prompt", strings.NewReader(`{"text":"go"}`))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("prompt status = %d", resp.StatusCode)
				}
				reader := bufio.NewReader(resp.Body)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					data, ok := strings.CutPrefix(strings.TrimSpace(line), "data: ")
					if !ok {
						continue
					}
					var frame mecatlv1.Event
					if err := json.Unmarshal([]byte(data), &frame); err != nil {
						t.Fatal(err)
					}
					if frame.GetType() == string(session.EvToolCall) {
						break
					}
				}
				defer resp.Body.Close()
			}
			if blocked != nil {
				select {
				case <-blocked.entered:
				case <-time.After(5 * time.Second):
					close(blocked.release)
					t.Fatal("read preparation did not reach permission evaluation")
				}
			}
			cancel()
			if blocked != nil {
				select {
				case <-blocked.observed:
				case <-time.After(5 * time.Second):
					close(blocked.release)
					t.Fatal("disconnect did not cancel read preparation")
				}
			}
			close(gate)
			deadline := time.NewTimer(5 * time.Second)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				logged := readEventLog(t, log, sess.ID)
				available, canonical, terminal := 0, 0, false
				for _, ev := range logged {
					switch ev.Type {
					case session.EvToolResultAvailable:
						available++
					case session.EvToolResult:
						canonical++
					case session.EvResult:
						terminal = true
					}
				}
				if available != 0 {
					t.Fatalf("%s persisted %d availability events after disconnect", transport, available)
				}
				if terminal {
					if canonical != 1 {
						t.Fatalf("%s persisted %d canonical results after disconnect", transport, canonical)
					}
					break
				}
				select {
				case <-tick.C:
				case <-deadline.C:
					t.Fatalf("%s never persisted terminal after disconnect (canonical=%d)", transport, canonical)
				}
			}
		})
	}
}
