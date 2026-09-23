package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
)

type discoveryBorrowedResource struct {
	active            atomic.Int32
	closed            atomic.Bool
	closedWhileActive atomic.Bool
}

func (r *discoveryBorrowedResource) Close() error {
	if r.active.Load() != 0 {
		r.closedWhileActive.Store(true)
	}
	r.closed.Store(true)
	return nil
}

func TestProviderModelDiscovery_Scenario4_CloseAndRestart(t *testing.T) {
	for _, failBuild := range []bool{false, true} {
		name := "normal Close"
		if failBuild {
			name = "Build failure"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				resource := &discoveryBorrowedResource{}
				entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
				var enteredOnce, cancelledOnce sync.Once
				defer closeIfOpen(release)
				client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					resource.active.Add(1)
					defer resource.active.Add(-1)
					enteredOnce.Do(func() { close(entered) })
					<-req.Context().Done()
					cancelledOnce.Do(func() { close(cancelled) })
					<-release
					return nil, req.Context().Err()
				})}
				cfg := isolateConfig(t, Config{Workspace: t.TempDir(), NoSoul: true, NoShell: true, NoUserModel: true,
					liveModelHTTPClient: client, ProviderCredentialLifecycle: resource,
					providerConstructor: func(_ Config, _, _, _ string) port.LLMProvider { return mockllm.New() },
					envDetector:         fakeEnv(map[string]string{"OPENROUTER_API_KEY": "offline"}),
				})
				if failBuild {
					cfg.envDetector = fakeEnv(nil)
					cfg.ToolhiveLLMBaseURL = "http://127.0.0.1:14000/v1"
					cfg.DefaultProvider = "not-configured"
				}
				type builtResult struct {
					built *Built
					err   error
				}
				builtDone := make(chan builtResult, 1)
				go func() { b, err := Build(context.Background(), cfg); builtDone <- builtResult{b, err} }()
				<-entered
				if failBuild {
					<-cancelled
					synctest.Wait()
					select {
					case got := <-builtDone:
						if got.built != nil {
							got.built.Close()
						}
						t.Fatalf("Build returned before fetch joined: %v", got.err)
					default:
					}
				} else {
					got := <-builtDone
					if got.err != nil {
						t.Fatal(got.err)
					}
					go func() { got.built.Close(); builtDone <- got }()
					<-cancelled
					synctest.Wait()
				}
				if resource.closed.Load() {
					t.Fatal("borrowed resource closed before held fetch returned")
				}
				close(release)
				got := <-builtDone
				if failBuild && got.err == nil {
					t.Fatal("invalid default provider did not fail Build")
				}
				if !resource.closed.Load() || resource.closedWhileActive.Load() || resource.active.Load() != 0 {
					t.Fatal("resource teardown was not cancel/join/close")
				}
			})
		})
	}
	t.Run("fresh owner", func(t *testing.T) {
		lister := &fakeLister{models: []modelEntry{{ID: "m", ContextLimit: 222222}}}
		first := discoveryFixture(t, map[string]providerEntry{"p": {lister: lister}})
		_, _ = first.request(context.Background(), "p", discoveryAdmission)
		first.Close()
		second := discoveryFixture(t, map[string]providerEntry{"p": {lister: lister}})
		state := second.snapshot().providers["p"]
		if len(state.observations) != 0 || state.attemptID != 0 || !state.nextEligible.IsZero() {
			t.Fatalf("restart retained volatile discovery: %+v", state)
		}
		_, err := second.request(context.Background(), "p", discoveryAdmission)
		if err != nil || lister.calls.Load() != 2 {
			t.Fatalf("fresh demand=%v, calls=%d", err, lister.calls.Load())
		}
	})
}

func TestProviderModelDiscovery_Scenario1_BootstrapAndNoLister(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, pid := range []string{providerToolhive, providerOpenAICodex} {
			var calls atomic.Int32
			var deadline time.Duration
			d := discoveryFixture(t, map[string]providerEntry{pid: {intentDriven: isToolhiveProvider(pid), lister: discoveryListerFunc(func(ctx context.Context) ([]modelEntry, error) {
				calls.Add(1)
				end, ok := ctx.Deadline()
				if !ok {
					t.Error("bootstrap missing deadline")
				}
				deadline = time.Until(end)
				return []modelEntry{{ID: "m", ContextLimit: 222222}}, nil
			})}})
			d.reg.defaultID = pid // fixture selection, before any attempt
			_, err := d.request(context.Background(), pid, discoveryBootstrap)
			if err != nil {
				t.Fatal(err)
			}
			synctest.Wait()
			d.start(true, 0)
			want := toolhiveProbeTimeout
			if pid == providerOpenAICodex {
				want = openAICodexBootstrapTimeout
			}
			if calls.Load() != 1 || deadline != want {
				t.Fatalf("bootstrap calls=%d budget=%v, want one/%v", calls.Load(), deadline, want)
			}
			if d.reg.ResolvedDefaultModel() != "m" || !d.CurrentModelSnapshot().ProviderStatus[0].DefaultModelAutoSelected {
				t.Fatal("bootstrap response did not contain accepted default selection")
			}
			d.Close()
		}
		for _, pid := range []string{providerToolhive, providerOpenAICodex} {
			for _, failure := range []error{nil, errors.New("offline"), discoveryUnauthorized{}} {
				d := discoveryFixture(t, map[string]providerEntry{pid: {intentDriven: isToolhiveProvider(pid), lister: &fakeLister{err: failure}}})
				d.reg.defaultID = pid
				var err error
				if pid == providerOpenAICodex {
					err = bootstrapOpenAICodexDefault(context.Background(), d.reg, Config{})
				} else {
					err = probeToolhive(d.reg, Config{})
				}
				if pid == providerOpenAICodex && err == nil {
					t.Fatal("Codex default bootstrap accepted missing entitlements")
				}
				if pid == providerToolhive && failure == nil && !errors.Is(err, errToolhiveNoModels) {
					t.Fatalf("ToolHive empty default bootstrap=%v", err)
				}
				if pid == providerToolhive && failure != nil && err != nil {
					t.Fatalf("unreachable ToolHive must remain bootable: %v", err)
				}
				d.Close()
			}
		}
		d := discoveryFixture(t, map[string]providerEntry{providerMock: {}})
		before := d.snapshot()
		d.start(false, 0)
		d.refresh(context.Background(), discoveryPicker)
		synctest.Wait()
		if len(d.attempts) != 0 || d.snapshot() != before {
			t.Fatal("no-lister configuration created discovery work")
		}
		d.Close()
		if _, err := d.request(context.Background(), providerMock, discoveryPicker); !errors.Is(err, context.Canceled) {
			t.Fatal("closed no-lister owner accepted request")
		}
	})
}
