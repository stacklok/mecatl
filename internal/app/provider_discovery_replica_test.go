package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const replicaDiscoveryModel = "replica-live-only-model"

func replicaDiscoveryConfig(t *testing.T, root string, transport http.RoundTripper, capture *coldGatewayCapture) Config {
	t.Helper()
	cfg := nativeRegistryConfig("native", &nativeBearerFixture{token: "offline-fixture"}, transport)
	cfg.MockProvider = nil
	definition := cfg.ProviderDefinitions["native"]
	definition.DefaultModel = replicaDiscoveryModel
	cfg.ProviderDefinitions["native"] = definition
	cfg.DefaultProvider, cfg.DefaultModel = "native", replicaDiscoveryModel
	cfg.Workspace, cfg.StoreDir = root+"/workspace", root+"/store"
	cfg.MemoryDir, cfg.UserModelDir = root+"/memory", root+"/usermodel"
	cfg.NoSoul, cfg.NoShell, cfg.NoUserModel = true, true, true
	cfg.LearningMode = learning.Off
	cfg.permConfigEnv = isolatedPermConfigEnv(t)
	cfg.envDetector = fakeEnv(nil)
	cfg.liveModelRefreshSync = true
	cfg.providerConstructor = func(_ Config, _, _, _ string) port.LLMProvider {
		return newReplicaMockProvider(capture)
	}
	return cfg
}

func newReplicaMockProvider(capture *coldGatewayCapture) port.LLMProvider {
	turns := make([]mockllm.Turn, 8)
	for i := range turns {
		turns[i] = mockllm.TextTurn("offline replica answer")
	}
	return mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(capture.observe)}, turns...)
}

func replicaListingResponse(r *http.Request, window int) (*http.Response, error) {
	body := fmt.Sprintf(`{"data":[{"id":%q,"context_window":%d}]}`, replicaDiscoveryModel, window)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func createReplicaSession(t *testing.T, built *Built) session.SessionID {
	t.Helper()
	sess, err := built.Service.CreateSessionWithProvider(context.Background(), session.ModeDefault, session.Limits{MaxTurns: 3}, server.ProviderSelector{ProviderID: "native", ModelID: replicaDiscoveryModel})
	if err != nil {
		t.Fatal(err)
	}
	return sess.ID
}

func assertReplicaRunBlocked(t *testing.T, built *Built, id session.SessionID, capture *coldGatewayCapture, events *discoveryEventCapture, result <-chan coldGatewayRunResult) {
	t.Helper()
	select {
	case got := <-result:
		t.Fatalf("run passed admission before local discovery completed: %v", got.err)
	default:
	}
	saved, err := built.Service.GetSession(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	compactions, archives, _, _ := coldGatewayEventFacts(events.snapshot())
	if len(saved.Conversation.Messages) != 0 || capture.count() != 0 || compactions != 0 || archives != 0 {
		t.Fatalf("blocked admission side effects: messages=%d inference=%d compactions=%d archives=%d", len(saved.Conversation.Messages), capture.count(), compactions, archives)
	}
}

func TestProviderModelDiscovery_Scenario6_ReplicaLocalFirstDemand(t *testing.T) {
	const windowA, windowB = 240_001, 360_001
	var callsA, callsB atomic.Int32
	captureA, captureB := newColdGatewayCapture(), newColdGatewayCapture()
	buildA, err := buildIsolated(t, context.Background(), replicaDiscoveryConfig(t, t.TempDir(), nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		callsA.Add(1)
		return replicaListingResponse(r, windowA)
	}), captureA))
	if err != nil {
		t.Fatal(err)
	}
	defer buildA.Close()
	idA := createReplicaSession(t, buildA)
	eventsA := runColdGatewayPrompt(t, buildA, idA, "A learns its own metadata")
	_, _, gotWindowA, _ := coldGatewayEventFacts(eventsA)
	if callsA.Load() != 1 || captureA.count() != 1 || gotWindowA != windowA {
		t.Fatalf("A discovery/run: listings=%d inference=%d window=%d", callsA.Load(), captureA.count(), gotWindowA)
	}

	enteredB, releaseB := make(chan struct{}), make(chan struct{})
	var enterB sync.Once
	eventsB := &discoveryEventCapture{}
	cfgB := replicaDiscoveryConfig(t, t.TempDir(), nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		callsB.Add(1)
		enterB.Do(func() { close(enteredB) })
		select {
		case <-releaseB:
			return replicaListingResponse(r, windowB)
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}), captureB)
	cfgB.Sink = eventsB
	buildB, err := buildIsolated(t, context.Background(), cfgB)
	if err != nil {
		t.Fatal(err)
	}
	defer buildB.Close()
	defer closeIfOpen(releaseB)
	idB := createReplicaSession(t, buildB)
	if got := buildB.Service.ResolvedModel(idB).ContextWindow; got != 0 || callsB.Load() != 0 {
		t.Fatalf("A evidence leaked into cold B: echo=%d listings=%d", got, callsB.Load())
	}
	resultB := startColdGatewayPrompt(context.Background(), buildB, idB, "B first prompt without picker")
	waitColdGatewayEntered(t, enteredB)
	assertReplicaRunBlocked(t, buildB, idB, captureB, eventsB, resultB)
	close(releaseB)
	result := receiveColdGatewayRun(t, resultB)
	if result.err != nil {
		t.Fatal(result.err)
	}
	_, _, gotWindowB, gotModelB := coldGatewayEventFacts(result.events)
	if callsB.Load() != 1 || captureB.count() != 1 || gotWindowB != windowB || gotModelB != replicaDiscoveryModel {
		t.Fatalf("B discovery/run: listings=%d inference=%d window=%d model=%q", callsB.Load(), captureB.count(), gotWindowB, gotModelB)
	}
}

func TestProviderModelDiscovery_Scenario6_WarmAndColdOutage(t *testing.T) {
	const windowA = 420_001
	var callsA, callsB, offsetA atomic.Int32
	captureA, captureB := newColdGatewayCapture(), newColdGatewayCapture()
	cfgA := replicaDiscoveryConfig(t, t.TempDir(), nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if callsA.Add(1) == 1 {
			return replicaListingResponse(r, windowA)
		}
		return nil, errors.New("controlled replica A listing outage")
	}), captureA)
	cfgA.modelDiscoveryNow = func() time.Time { return time.Now().Add(time.Duration(offsetA.Load()) * time.Second) }
	buildA, err := buildIsolated(t, context.Background(), cfgA)
	if err != nil {
		t.Fatal(err)
	}
	defer buildA.Close()
	idA := createReplicaSession(t, buildA)
	runColdGatewayPrompt(t, buildA, idA, "warm A")
	offsetA.Store(11)
	snapshotA := buildA.Service.ListModelSnapshot(context.Background())
	statuses := snapshotA.ProviderStatus
	if callsA.Load() != 2 || len(statuses) != 1 || statuses[0].GetState() != statusUnreachable || buildA.Service.ResolvedModel(idA).ContextWindow != windowA {
		t.Fatalf("A last-good/latest failure: calls=%d statuses=%+v resolved=%+v", callsA.Load(), statuses, buildA.Service.ResolvedModel(idA))
	}

	eventsB := &discoveryEventCapture{}
	cfgB := replicaDiscoveryConfig(t, t.TempDir(), nativeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		callsB.Add(1)
		return nil, errors.New("controlled replica B listing outage")
	}), captureB)
	cfgB.Sink = eventsB
	buildB, err := buildIsolated(t, context.Background(), cfgB)
	if err != nil {
		t.Fatal(err)
	}
	defer buildB.Close()
	idB := createReplicaSession(t, buildB)
	runB, err := buildB.Service.StartRun(context.Background(), idB, "cold B must reject")
	if runB != nil {
		for range runB.Events() {
		}
		buildB.Service.FinishRun(idB, runB)
	}
	assertReplicaRunBlocked(t, buildB, idB, captureB, eventsB, make(chan coldGatewayRunResult))
	if !errors.Is(err, server.ErrContextWindowUnavailable) || runB != nil {
		t.Fatalf("cold B admission: run=%t err=%v", runB != nil, err)
	}
	if callsB.Load() != 1 || buildB.Service.ResolvedModel(idB).ContextWindow != 0 {
		t.Fatalf("cold B borrowed evidence: calls=%d resolved=%+v", callsB.Load(), buildB.Service.ResolvedModel(idB))
	}

	runColdGatewayPrompt(t, buildA, idA, "A remains admissible from last-good metadata")
	if callsA.Load() != 2 || captureA.count() != 2 || buildA.Service.ResolvedModel(idA).ContextWindow != windowA {
		t.Fatalf("warm A after outage: listings=%d inference=%d resolved=%+v", callsA.Load(), captureA.count(), buildA.Service.ResolvedModel(idA))
	}
}

func TestProviderModelDiscovery_Scenario6_IndependentOwnerLifetimes(t *testing.T) {
	for _, phase := range []string{"during B cooldown", "during B active retry"} {
		t.Run(phase, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const windowB = 510_001
				enteredA, cancelledA, exitedA, releaseA := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
				enteredB, failB := make(chan struct{}), make(chan struct{})
				retriedB, releaseB := make(chan struct{}), make(chan struct{})
				var callsA, callsB atomic.Int32
				var clockB atomic.Int64
				captureA, captureB := newColdGatewayCapture(), newColdGatewayCapture()
				cfgA := replicaDiscoveryConfig(t, t.TempDir(), nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					callsA.Add(1)
					close(enteredA)
					<-r.Context().Done()
					close(cancelledA)
					<-releaseA
					close(exitedA)
					return nil, r.Context().Err()
				}), captureA)
				// A's time cannot make B eligible: only B's own clock is advanced below.
				cfgA.modelDiscoveryNow = func() time.Time { return time.Unix(1_000, 0) }
				buildA, err := buildIsolated(t, context.Background(), cfgA)
				if err != nil {
					t.Fatal(err)
				}
				defer buildA.Close()
				defer closeIfOpen(releaseA)
				eventsB := &discoveryEventCapture{}
				cfgB := replicaDiscoveryConfig(t, t.TempDir(), nativeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
					switch callsB.Add(1) {
					case 1:
						close(enteredB)
						select {
						case <-failB:
							return nil, errors.New("controlled first B listing failure")
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					case 2:
						close(retriedB)
						select {
						case <-releaseB:
							return replicaListingResponse(r, windowB)
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
					default:
						return nil, errors.New("unexpected extra B discovery attempt")
					}
				}), captureB)
				cfgB.Sink = eventsB
				cfgB.modelDiscoveryNow = func() time.Time { return time.Unix(100, clockB.Load()) }
				buildB, err := buildIsolated(t, context.Background(), cfgB)
				if err != nil {
					t.Fatal(err)
				}
				defer buildB.Close()
				defer closeIfOpen(failB)
				defer closeIfOpen(releaseB)
				idA, idB := createReplicaSession(t, buildA), createReplicaSession(t, buildB)
				ctxA, cancelA := context.WithCancel(context.Background())
				defer cancelA()
				resultA := startColdGatewayPrompt(ctxA, buildA, idA, "cancel A waiter")
				resultB := startColdGatewayPrompt(context.Background(), buildB, idB, "B first attempt fails")
				waitColdGatewayEntered(t, enteredA)
				waitColdGatewayEntered(t, enteredB)
				close(failB)
				if got := receiveColdGatewayRun(t, resultB); !errors.Is(got.err, server.ErrContextWindowUnavailable) {
					t.Fatalf("B first admission = %v", got.err)
				}

				assertCoolingDown := func() {
					t.Helper()
					demand := startColdGatewayPrompt(context.Background(), buildB, idB, "B must remain cooling down")
					synctest.Wait()
					if callsB.Load() != 1 {
						t.Fatalf("B fetched before its own cooldown: listings=%d", callsB.Load())
					}
					select {
					case got := <-demand:
						if !errors.Is(got.err, server.ErrContextWindowUnavailable) {
							t.Fatalf("B cooldown admission = %v", got.err)
						}
					default:
						t.Fatal("B cooldown demand did not reject immediately")
					}
					assertReplicaRunBlocked(t, buildB, idB, captureB, eventsB, nil)
					if got := buildB.Service.ResolvedModel(idB).ContextWindow; got != 0 {
						t.Fatalf("B has evidence before its own successful listing: %d", got)
					}
				}
				assertCoolingDown()
				cancelA()
				if got := receiveColdGatewayRun(t, resultA); !errors.Is(got.err, context.Canceled) {
					t.Fatalf("A cancelled waiter = %v", got.err)
				}
				synctest.Wait()
				select {
				case <-cancelledA:
					t.Fatal("cancelling A waiter cancelled A's owned fetch")
				default:
				}
				select {
				case <-exitedA:
					t.Fatal("A lister exited before Build.Close")
				default:
				}
				closeA := func() {
					t.Helper()
					closedA := make(chan struct{})
					go func() { buildA.Close(); close(closedA) }()
					synctest.Wait()
					select {
					case <-cancelledA:
					default:
						t.Fatal("Build.Close did not cancel A's owned fetch")
					}
					select {
					case <-closedA:
						t.Fatal("Build.Close returned without joining A's held lister")
					default:
					}
					close(releaseA)
					waitColdGatewayEntered(t, closedA)
					select {
					case <-exitedA:
					default:
						t.Fatal("closing A did not join A-owned lister")
					}
				}
				if phase == "during B cooldown" {
					clockB.Store(int64(discoveryCooldown / 2))
					assertCoolingDown()
					closeA()
					assertCoolingDown()
				}
				clockB.Store(int64(discoveryCooldown - time.Nanosecond))
				assertCoolingDown()

				clockB.Store(int64(discoveryCooldown))
				resultB = startColdGatewayPrompt(context.Background(), buildB, idB, "B eligible retry")
				synctest.Wait()
				select {
				case <-retriedB:
				default:
					t.Fatalf("B did not retry at its original cooldown deadline: listings=%d", callsB.Load())
				}
				assertReplicaRunBlocked(t, buildB, idB, captureB, eventsB, resultB)
				if phase == "during B active retry" {
					closeA()
				}
				synctest.Wait()
				assertReplicaRunBlocked(t, buildB, idB, captureB, eventsB, resultB)
				if callsA.Load() != 1 || callsB.Load() != 2 || buildB.Service.ResolvedModel(idB).ContextWindow != 0 {
					t.Fatalf("owner isolation before B release: callsA=%d callsB=%d resolvedB=%+v", callsA.Load(), callsB.Load(), buildB.Service.ResolvedModel(idB))
				}
				close(releaseB)
				result := receiveColdGatewayRun(t, resultB)
				if result.err != nil {
					t.Fatal(result.err)
				}
				_, _, gotWindow, gotModel := coldGatewayEventFacts(result.events)
				if callsB.Load() != 2 || captureB.count() != 1 || gotWindow != windowB || gotModel != replicaDiscoveryModel || buildB.Service.ResolvedModel(idB).ContextWindow != windowB {
					t.Fatalf("B after A close: listings=%d inference=%d window=%d model=%q", callsB.Load(), captureB.count(), gotWindow, gotModel)
				}
				saved, err := buildB.Service.GetSession(context.Background(), idB)
				if err != nil {
					t.Fatal(err)
				}
				if len(saved.Conversation.Messages) != 2 || saved.Conversation.Messages[0].Role != session.RoleUser || saved.Conversation.Messages[0].Text != "B eligible retry" {
					t.Fatalf("B recorded rejected prompts or duplicated the admitted prompt: %+v", saved.Conversation.Messages)
				}
			})
		})
	}
}
