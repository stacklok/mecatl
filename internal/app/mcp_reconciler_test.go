package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	mcpsource "github.com/stacklok/mecatl/internal/adapter/mcp/source"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

type reconciliationSource struct {
	name string
	mu   sync.Mutex
	cfgs []mcp.ServerConfig
	err  error
}

func (s *reconciliationSource) Name() string { return s.name }
func (s *reconciliationSource) Servers(context.Context) ([]mcp.ServerConfig, []mcpsource.SkipError, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]mcp.ServerConfig(nil), s.cfgs...), nil, s.err
}
func (s *reconciliationSource) set(cfgs []mcp.ServerConfig, err error) {
	s.mu.Lock()
	s.cfgs, s.err = cfgs, err
	s.mu.Unlock()
}

func candidateFromConfigs(configs []mcp.ServerConfig, generation uint64) *mcpReconcileCandidate {
	return &mcpReconcileCandidate{configs: cloneMCPConfigs(configs), generation: generation}
}

// elapsedMCPReconcileCooldown supplies already-fired timers for tests of reconciliation
// results. Cooldown enforcement tests keep their manually controlled timers.
func elapsedMCPReconcileCooldown(t *testing.T) func(time.Duration) <-chan time.Time {
	t.Helper()
	return func(d time.Duration) <-chan time.Time {
		if d != minMCPReconcileCooldown {
			t.Errorf("reconcile cooldown = %v, want %v", d, minMCPReconcileCooldown)
		}
		tick := make(chan time.Time, 1)
		tick <- time.Time{}
		return tick
	}
}

func TestMCPSourceReconcilerCloseOwnership(t *testing.T) {
	t.Run("accepted-publication-transfers-ownership", func(t *testing.T) {
		runtimes := newMCPRuntimeSet(nil)
		r := newMCPSourceReconciler(mcpReconcilerOptions{
			sources: []mcpsource.Source{&reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "live", URL: "http://live/mcp"}}}},
			build: func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
				return candidateFromConfigs(configs, 1), nil
			},
			publish: runtimes.publish,
		})
		result, err := r.Reconcile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		if result.candidate.isClosed() {
			t.Fatal("reconciler closed runtime-set-owned current candidate")
		}
		runtimes.close()
		if !result.candidate.isClosed() {
			t.Fatal("runtime set did not close accepted candidate")
		}
	})

	t.Run("default-publication-retains-reconciler-ownership", func(t *testing.T) {
		r := newMCPSourceReconciler(mcpReconcilerOptions{
			sources: []mcpsource.Source{&reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "live", URL: "http://live/mcp"}}}},
			build: func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
				return candidateFromConfigs(configs, 1), nil
			},
		})
		result, err := r.Reconcile(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		r.Close()
		if !result.candidate.isClosed() {
			t.Fatal("reconciler leaked its current candidate")
		}
	})
}

func TestMCPSourceReconciliation_Scenario1_OrderedSourcesLKGAndEmpty(t *testing.T) {
	static := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "same", URL: "http://static/mcp"}}}
	toolhive := &reconciliationSource{name: "toolhive(default)", cfgs: []mcp.ServerConfig{{Name: "same", URL: "http://toolhive/mcp"}, {Name: "dynamic", URL: "http://dynamic/mcp"}}}
	var generation atomic.Uint64
	r := newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{static, toolhive},
		after:   elapsedMCPReconcileCooldown(t),
		build: func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
			return candidateFromConfigs(configs, generation.Add(1)), nil
		},
	})
	t.Cleanup(r.Close)

	first, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("initial reconcile: %v", err)
	}
	if got := configURL(first.candidate.configs, "same"); got != "http://static/mcp" {
		t.Fatalf("configured/static source lost precedence: %q", got)
	}
	if len(first.inventory) != 2 || len(first.inventory[1].Servers) != 2 {
		t.Fatalf("pre-shadow source rows not retained: %+v", first.inventory)
	}

	toolhive.set(nil, errors.New("runtime unavailable: https://secret.example/mcp?token=do-not-log"))
	failed, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("failed-source reconcile should retain LKG: %v", err)
	}
	if !failed.stale || configURL(failed.candidate.configs, "dynamic") == "" {
		t.Fatalf("source failure did not retain LKG/stale: %+v", failed)
	}
	for _, d := range failed.diagnostics {
		if len(d) > maxMCPDiagnosticBytes || containsSecret(d) {
			t.Fatalf("diagnostic is unbounded or secret-bearing: %q", d)
		}
	}

	toolhive.set([]mcp.ServerConfig{}, nil)
	empty, err := r.Reconcile(context.Background())
	if err != nil {
		t.Fatalf("authoritative-empty reconcile: %v", err)
	}
	if empty.stale || configURL(empty.candidate.configs, "dynamic") != "" {
		t.Fatalf("successful empty did not withdraw ToolHive LKG: %+v", empty)
	}
}

func TestMCPSourceReconciliation_Scenario1_ProductionTriggerMatrix(t *testing.T) {
	t.Run("polling-and-manual-coalescing", func(t *testing.T) {
		static := &reconciliationSource{name: "static"}
		var builds atomic.Int64
		started := make(chan struct{}, 1)
		release := make(chan struct{})
		r := newMCPSourceReconciler(mcpReconcilerOptions{
			sources: []mcpsource.Source{static},
			after:   elapsedMCPReconcileCooldown(t),
			build: func(ctx context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
				builds.Add(1)
				select {
				case started <- struct{}{}:
				default:
				}
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return candidateFromConfigs(configs, uint64(builds.Load())), nil
			},
		})
		if r.polling() {
			t.Fatal("polling enabled without ToolHive")
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { _, err := r.Reconcile(ctx); done <- err }()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter error = %v", err)
		}
		other := make(chan error, 1)
		go func() { _, err := r.Reconcile(context.Background()); other <- err }()
		close(release)
		if err := <-other; err != nil {
			t.Fatalf("coalesced waiter: %v", err)
		}
		if got := builds.Load(); got > 2 {
			t.Fatalf("manual work not serialized/coalesced: %d builds", got)
		}
		r.Close()
	})

	t.Run("toolhive-poll-and-stable-no-reconnect", func(t *testing.T) {
		th := &reconciliationSource{name: "toolhive(default)", cfgs: []mcp.ServerConfig{{
			Name: "a", URL: "http://a/mcp", OAuth: &mcp.OAuthOptions{Presenter: mcp.OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) { return nil, nil })},
		}}}
		ticks := make(chan time.Time, 4)
		cooldown := elapsedMCPReconcileCooldown(t)
		var builds atomic.Int64
		r := newMCPSourceReconciler(mcpReconcilerOptions{
			sources: []mcpsource.Source{th}, toolHive: true,
			after: func(d time.Duration) <-chan time.Time {
				if d == minMCPReconcileCooldown {
					return cooldown(d)
				}
				if d < minMCPPollInterval || d > maxMCPPollInterval {
					t.Errorf("poll interval outside jitter bounds: %v", d)
				}
				return ticks
			},
			build: func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
				return candidateFromConfigs(configs, uint64(builds.Add(1))), nil
			},
		})
		t.Cleanup(r.Close)
		if !r.polling() {
			t.Fatal("ToolHive polling is disabled")
		}
		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		ticks <- time.Now()
		eventuallyReconcile(t, func() bool { return r.cycles() >= 2 })
		if got := builds.Load(); got != 1 {
			t.Fatalf("unchanged poll reconnected candidate: builds=%d", got)
		}
	})

	t.Run("dirty-all-lists-and-stale-notification", func(t *testing.T) {
		var builds atomic.Int64
		var notify func()
		r := newMCPSourceReconciler(mcpReconcilerOptions{
			sources: []mcpsource.Source{&reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "live", URL: "http://live/mcp"}}}},
			after:   elapsedMCPReconcileCooldown(t),
			build: func(_ context.Context, configs []mcp.ServerConfig, changed func()) (*mcpReconcileCandidate, error) {
				notify = changed
				g := uint64(builds.Add(1))
				return &mcpReconcileCandidate{configs: cloneMCPConfigs(configs), generation: g, tools: []mcpToolMeta{{Name: "tool"}}, resources: []mcp.Resource{{URI: "r", Description: string(rune('0' + g))}}, prompts: []mcp.Prompt{{Name: "p", Description: string(rune('0' + g))}}}, nil
			},
		})
		t.Cleanup(r.Close)
		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		oldNotify := notify
		oldNotify()
		eventuallyReconcile(t, func() bool { return builds.Load() == 2 })
		oldNotify()
		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := builds.Load(); got != 2 {
			t.Fatalf("stale runtime notification triggered build: %d", got)
		}
	})

	t.Run("notification-while-preparing-queues-one-successor", func(t *testing.T) {
		var builds atomic.Int64
		r := newMCPSourceReconciler(mcpReconcilerOptions{
			sources: []mcpsource.Source{&reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "a", URL: "http://a/mcp"}}}},
			after:   elapsedMCPReconcileCooldown(t),
			build: func(_ context.Context, configs []mcp.ServerConfig, changed func()) (*mcpReconcileCandidate, error) {
				n := builds.Add(1)
				if n == 1 {
					changed()
				}
				return candidateFromConfigs(configs, uint64(n)), nil
			},
		})
		t.Cleanup(r.Close)
		if _, err := r.Reconcile(context.Background()); err != nil {
			t.Fatal(err)
		}
		eventuallyReconcile(t, func() bool { return r.cycles() >= 2 })
		if got := builds.Load(); got != 2 {
			t.Fatalf("pre-publication notification builds = %d, want one successor", got)
		}
	})

	t.Run("real-notification-refreshes-complete-snapshot", func(t *testing.T) {
		remote := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "live", Version: "v1"}, nil)
		mcpsdk.AddTool(remote, &mcpsdk.Tool{Name: "initial"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{}, nil, nil
		})
		httpServer := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return remote }, nil))
		src := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "live", URL: httpServer.URL}}}
		r := newMCPSourceReconciler(mcpReconcilerOptions{sources: []mcpsource.Source{src}, build: buildMCPReconcileCandidate(Config{}), after: elapsedMCPReconcileCooldown(t)})
		t.Cleanup(httpServer.Close)
		t.Cleanup(r.Close)
		first, err := r.Reconcile(context.Background())
		if err != nil {
			t.Fatalf("initial reconcile: %v", err)
		}
		firstGeneration := first.candidate.generation

		remote.AddResource(&mcpsdk.Resource{URI: "test://late", Name: "late-resource"}, func(_ context.Context, req *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
			return &mcpsdk.ReadResourceResult{Contents: []*mcpsdk.ResourceContents{{URI: req.Params.URI, Text: "late"}}}, nil
		})
		remote.AddPrompt(&mcpsdk.Prompt{Name: "late-prompt"}, func(context.Context, *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
			return &mcpsdk.GetPromptResult{}, nil
		})
		mcpsdk.AddTool(remote, &mcpsdk.Tool{Name: "late-tool"}, func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{}, nil, nil
		})

		eventuallyReconcile(t, func() bool {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.current != nil && r.current.generation != firstGeneration && len(r.current.tools) == 2 && len(r.current.resources) == 1 && len(r.current.prompts) == 1
		})
	})
}

func TestDeferredRuntimeDrainAutomaticallyReconcilesAndPublishes(t *testing.T) {
	source := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "svc", URL: "http://svc-0.invalid/mcp"}}}
	cooldowns := make(chan chan time.Time, 16)
	runtimes := newMCPRuntimeSet(nil)
	var builds atomic.Int64
	r := newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{source},
		after: func(d time.Duration) <-chan time.Time {
			if d != time.Second {
				t.Errorf("reconcile cooldown = %v, want 1s", d)
			}
			ch := make(chan time.Time, 1)
			cooldowns <- ch
			return ch
		},
		build: func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
			return candidateFromConfigs(configs, uint64(builds.Add(1))), nil
		},
		publish: runtimes.publish,
	})
	runtimes.setRetry(r.invalidate)
	pins := make([]func(), 0, maxMCPRetainedRuntimes+1)
	t.Cleanup(func() {
		for _, release := range pins {
			release()
		}
		r.Close()
		runtimes.close()
	})

	reconcile := func(first bool) mcpReconcileResult {
		t.Helper()
		done := make(chan struct {
			result mcpReconcileResult
			err    error
		}, 1)
		go func() {
			result, err := r.Reconcile(t.Context())
			done <- struct {
				result mcpReconcileResult
				err    error
			}{result, err}
		}()
		if !first {
			(<-cooldowns) <- time.Unix(1, 0)
		}
		got := <-done
		if got.err != nil {
			t.Fatalf("Reconcile: %v", got.err)
		}
		return got.result
	}

	current := reconcile(true).candidate
	for i := 0; i < maxMCPRetainedRuntimes; i++ {
		_, release, err := runtimes.pin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		pins = append(pins, release)
		source.set([]mcp.ServerConfig{{Name: "svc", URL: fmt.Sprintf("http://svc-%d.invalid/mcp", i+1)}}, nil)
		current = reconcile(false).candidate
	}
	_, releaseCurrent, err := runtimes.pin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pins = append(pins, releaseCurrent)
	source.set([]mcp.ServerConfig{{Name: "svc", URL: "http://deferred.invalid/mcp"}}, nil)
	deferred := reconcile(false)
	if !deferred.stale || runtimes.currentRevision() != current.generation {
		t.Fatalf("deferred publication = stale %v revision %d, want stale at %d", deferred.stale, runtimes.currentRevision(), current.generation)
	}

	pins[0]()
	(<-cooldowns) <- time.Unix(2, 0)
	eventuallyReconcile(t, func() bool { return runtimes.currentRevision() > current.generation })
	if got := runtimes.currentRevision(); got != uint64(builds.Load()) {
		t.Fatalf("automatic successor revision = %d, builds = %d", got, builds.Load())
	}
	for _, release := range pins[1:] {
		release()
	}
}

func TestADR_0355_ReconciliationBoundsConsentAndShutdown(t *testing.T) {
	if maxMCPReconcileSources <= 0 || maxMCPReconcileServers <= 0 || maxMCPActiveListEntries <= 0 || maxMCPCandidatePages <= 0 || maxMCPCandidateBytes <= 0 || maxMCPRetainedRuntimes <= 0 || maxMCPReconcileCycleDuration <= 0 {
		t.Fatal("every reconciliation dimension must have an independent finite bound")
	}
	tooMany := make([]mcp.ServerConfig, maxMCPReconcileServers+1)
	for i := range tooMany {
		tooMany[i] = mcp.ServerConfig{Name: "server"}
	}
	r := newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{&reconciliationSource{name: "static", cfgs: tooMany}},
		build: func(context.Context, []mcp.ServerConfig, func()) (*mcpReconcileCandidate, error) {
			t.Fatal("candidate built beyond server bound")
			return nil, nil
		},
	})
	result, err := r.Reconcile(context.Background())
	if err == nil || !result.stale {
		t.Fatalf("bound failure = (%+v, %v), want stale error", result, err)
	}
	r.Close()

	for _, tc := range []struct {
		name      string
		candidate *mcpReconcileCandidate
	}{
		{"active-list", &mcpReconcileCandidate{tools: make([]mcpToolMeta, maxMCPActiveListEntries+1)}},
		{"page-count", &mcpReconcileCandidate{pages: maxMCPCandidatePages + 1}},
		{"byte-count", &mcpReconcileCandidate{bytes: maxMCPCandidateBytes + 1}},
	} {
		t.Run(tc.name+"-bound", func(t *testing.T) {
			if err := validateMCPCandidate(tc.candidate); err == nil {
				t.Fatal("over-bound candidate accepted")
			}
		})
	}
	if err := validateMCPCandidate(&mcpReconcileCandidate{tools: make([]mcpToolMeta, maxMCPActiveListEntries), pages: maxMCPCandidatePages, bytes: maxMCPCandidateBytes}); err != nil {
		t.Fatalf("candidate boundary rejected: %v", err)
	}
	presenter := mcp.OAuthPresenterFunc(nil)
	cloned := cloneMCPConfigs([]mcp.ServerConfig{{OAuth: &mcp.OAuthOptions{Presenter: presenter}}})
	if cloned[0].OAuth == nil || cloned[0].OAuth.Presenter != nil {
		t.Fatal("serving reconciliation retained an OAuth consent presenter")
	}

	blocked := make(chan struct{})
	joined := make(chan struct{})
	r = newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{&reconciliationSource{name: "toolhive(default)"}}, toolHive: true,
		build: func(ctx context.Context, _ []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
			close(blocked)
			<-ctx.Done()
			close(joined)
			return nil, ctx.Err()
		},
	})
	go func() { _, _ = r.Reconcile(context.Background()) }()
	<-blocked
	r.Close()
	select {
	case <-joined:
	default:
		t.Fatal("Close returned before in-flight reconciliation finished")
	}
}

func TestMCPSourceReconciliation_StatusPublishesOnlyActiveCandidateInventory(t *testing.T) {
	for _, tc := range []struct {
		name      string
		build     func([]mcp.ServerConfig) (*mcpReconcileCandidate, error)
		publish   func(*mcpReconcileCandidate, *mcpReconcileCandidate) bool
		sourceErr error
	}{
		{
			name:      "source failure",
			sourceErr: errors.New("source unavailable"),
		},
		{
			name: "build failure",
			build: func([]mcp.ServerConfig) (*mcpReconcileCandidate, error) {
				return nil, errors.New("build failed")
			},
		},
		{
			name: "validation failure",
			build: func(configs []mcp.ServerConfig) (*mcpReconcileCandidate, error) {
				return &mcpReconcileCandidate{configs: cloneMCPConfigs(configs), tools: make([]mcpToolMeta, maxMCPActiveListEntries+1)}, nil
			},
		},
		{
			name: "publication deferred",
			build: func(configs []mcp.ServerConfig) (*mcpReconcileCandidate, error) {
				return candidateFromConfigs(configs, 0), nil
			},
			publish: func(*mcpReconcileCandidate, *mcpReconcileCandidate) bool { return false },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "a", URL: "http://a/mcp"}}}
			build := func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
				return candidateFromConfigs(configs, 0), nil
			}
			r := newMCPSourceReconciler(mcpReconcilerOptions{sources: []mcpsource.Source{source}, build: build, after: elapsedMCPReconcileCooldown(t)})
			t.Cleanup(r.Close)
			if _, err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("initial reconcile: %v", err)
			}
			initial := r.statusSnapshot()
			if initial.Revision == 0 || sourceStatusServerNames(initial) != "a" {
				t.Fatalf("initial status = %+v", initial)
			}

			source.set([]mcp.ServerConfig{{Name: "b", URL: "http://b/mcp"}}, tc.sourceErr)
			if tc.build != nil {
				r.build = func(_ context.Context, configs []mcp.ServerConfig, _ func()) (*mcpReconcileCandidate, error) {
					return tc.build(configs)
				}
			}
			r.publish = tc.publish
			_, _ = r.Reconcile(context.Background())
			failed := r.statusSnapshot()
			if failed.Revision != initial.Revision || sourceStatusServerNames(failed) != "a" || !failed.Stale {
				t.Fatalf("failed status = %+v, want active candidate A marked stale", failed)
			}
			if tc.sourceErr != nil && !strings.Contains(strings.Join(failed.Sources[0].Diagnostics, " "), "source consultation failed") {
				t.Fatalf("source failure diagnostic missing from status: %+v", failed)
			}

			r.build = build
			r.publish = nil
			source.set([]mcp.ServerConfig{{Name: "b", URL: "http://b/mcp"}}, nil)
			if _, err := r.Reconcile(context.Background()); err != nil {
				t.Fatalf("later reconcile: %v", err)
			}
			success := r.statusSnapshot()
			if success.Revision == initial.Revision || sourceStatusServerNames(success) != "b" || success.Stale {
				t.Fatalf("successful status = %+v, want published candidate B", success)
			}
		})
	}
}

func TestMCPSourceReconciliation_StatusIsEmptyWithoutAnActiveCandidate(t *testing.T) {
	source := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "unpublished", URL: "http://unpublished/mcp"}}}
	r := newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{source},
		build: func(context.Context, []mcp.ServerConfig, func()) (*mcpReconcileCandidate, error) {
			return nil, errors.New("build failed")
		},
	})
	t.Cleanup(r.Close)
	if _, err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("initial failed reconcile unexpectedly succeeded")
	}
	if status := r.statusSnapshot(); status.Revision != 0 || len(status.Sources) != 0 || !status.Stale {
		t.Fatalf("initial failed status = %+v, want empty stale status", status)
	}
}

func sourceStatusServerNames(status serveradapter.MCPSourceStatus) string {
	var names []string
	for _, source := range status.Sources {
		for _, server := range source.Servers {
			names = append(names, server.Name)
		}
	}
	return strings.Join(names, ",")
}

func TestMCPSourceReconciliation_PartialStartupAndRetry(t *testing.T) {
	healthyURL, deletes := newMCPTestServerCounting(t)
	recovered := newMCPTestServerWithResource(t)
	backend, err := url.Parse(recovered)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	var available atomic.Bool
	var failureCode atomic.Int32
	failureCode.Store(http.StatusGatewayTimeout)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !available.Load() {
			http.Error(w, "not an MCP response", int(failureCode.Load()))
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(broken.Close)
	badURL := broken.URL + "/mcp?access_token=do-not-log"
	source := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "healthy", URL: healthyURL}, {Name: "broken", URL: badURL}}}
	runtimes := newMCPRuntimeSet(nil)
	reconciler := newMCPSourceReconciler(mcpReconcilerOptions{
		sources: []mcpsource.Source{source},
		build:   buildMCPReconcileCandidate(Config{}),
		publish: runtimes.publish,
	})
	runtimes.setRetry(reconciler.invalidate)
	defer runtimes.close()
	defer reconciler.Close()

	first, err := reconciler.Reconcile(context.Background())
	if err != nil || !first.changed || len(first.candidate.manager.Servers()) != 1 {
		t.Fatalf("partial startup = (%+v, %v)", first, err)
	}
	previous := first.candidate
	previousRevision := runtimes.currentRevision()
	status := reconciler.statusSnapshot()
	if len(status.Sources) != 1 || len(status.Sources[0].Diagnostics) != 1 || !strings.Contains(status.Sources[0].Diagnostics[0], `"broken"`) || strings.Contains(fmt.Sprint(status), "do-not-log") {
		t.Fatalf("missing or unredacted server diagnostic: %+v", status)
	}
	result, err := runtimes.CallTool(context.Background(), "healthy", "echo", []byte(`{"text":"startup"}`))
	if err != nil || len(result.Content) != 1 || !strings.Contains(result.Content[0].Text, "startup") {
		t.Fatalf("healthy startup tool unavailable: %+v %v", result, err)
	}
	failureCode.Store(http.StatusBadGateway)
	again, err := reconciler.Reconcile(context.Background())
	if err != nil || again.changed || again.candidate != previous || previous.isClosed() || runtimes.currentRevision() != previousRevision {
		t.Fatalf("failed peer retry disrupted healthy runtime: %+v %v", again, err)
	}
	status = reconciler.statusSnapshot()
	if status.Revision != previousRevision || len(status.Sources[0].Diagnostics) != 1 || status.Sources[0].Diagnostics[0] == first.inventory[0].Diagnostics[0] || !strings.Contains(status.Sources[0].Diagnostics[0], "Bad Gateway") {
		t.Fatalf("retry did not update diagnostic without replacing runtime: %+v", status)
	}
	if atomic.LoadInt32(deletes) == 0 {
		t.Fatal("equal retry did not close temporary healthy connection")
	}

	available.Store(true)
	third, err := reconciler.Reconcile(context.Background())
	if err != nil || !third.changed || len(third.candidate.manager.Servers()) != 2 || len(third.candidate.resources) != 1 {
		t.Fatalf("recovery failed: %+v %v", third, err)
	}
	if len(reconciler.statusSnapshot().Sources[0].Diagnostics) != 0 {
		t.Fatal("recovered server still shows failure")
	}
	for _, name := range []string{"healthy", "broken"} {
		if _, err := runtimes.CallTool(context.Background(), name, "echo", []byte(`{"text":"ready"}`)); err != nil {
			t.Fatalf("%s unavailable after recovery: %v", name, err)
		}
	}
}

func TestMCPSourceReconciliation_AllFailedRetriesAndRetainsRuntime(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not an MCP response", http.StatusBadGateway)
	}))
	t.Cleanup(broken.Close)
	source := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "broken", URL: broken.URL}}}
	r := newMCPSourceReconciler(mcpReconcilerOptions{sources: []mcpsource.Source{source}, build: buildMCPReconcileCandidate(Config{})})
	defer r.Close()
	first, err := r.Reconcile(context.Background())
	if err != nil || first.candidate == nil || len(first.candidate.manager.Servers()) != 0 || len(r.statusSnapshot().Sources[0].Diagnostics) != 1 {
		t.Fatalf("all failed startup: %+v %v", first, err)
	}
	healthy := newMCPTestServer(t)
	source.set([]mcp.ServerConfig{{Name: "healthy", URL: healthy}}, nil)
	second, err := r.Reconcile(context.Background())
	if err != nil || len(second.candidate.manager.Servers()) != 1 {
		t.Fatalf("recovery: %+v %v", second, err)
	}
	source.set([]mcp.ServerConfig{{Name: "broken", URL: broken.URL}}, nil)
	third, err := r.Reconcile(context.Background())
	if err != nil || !third.stale || third.candidate != second.candidate || second.candidate.isClosed() {
		t.Fatalf("all failed refresh lost last good runtime: %+v %v", third, err)
	}
	if len(r.statusSnapshot().Sources[0].Diagnostics) != 1 {
		t.Fatalf("failure not reported against observed source: %+v", r.statusSnapshot())
	}
}

func TestMCPSourceReconciliation_BadToolCatalogIsolated(t *testing.T) {
	healthy := newMCPTestServer(t)
	remote := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "v1"}, nil)
	mcpsdk.AddTool(remote, &mcpsdk.Tool{Name: "invalid\nname"},
		func(context.Context, *mcpsdk.CallToolRequest, mcpEchoArgs) (*mcpsdk.CallToolResult, any, error) {
			return &mcpsdk.CallToolResult{}, nil, nil
		})
	bad := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return remote }, nil))
	defer bad.Close()
	src := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "healthy", URL: healthy}, {Name: "bad", URL: bad.URL}}}
	r := newMCPSourceReconciler(mcpReconcilerOptions{sources: []mcpsource.Source{src}, build: buildMCPReconcileCandidate(Config{})})
	defer r.Close()
	result, err := r.Reconcile(context.Background())
	if err != nil || len(result.candidate.tools) != 1 || len(result.candidate.manager.Servers()) != 1 {
		t.Fatalf("malformed tool catalog blocked healthy peer: %+v %v", result, err)
	}
	if diagnostics := r.statusSnapshot().Sources[0].Diagnostics; len(diagnostics) != 1 || !strings.Contains(diagnostics[0], `"bad"`) {
		t.Fatalf("malformed catalog diagnostic = %v", diagnostics)
	}
}

func TestMCPReconcileCandidateAggregatePagesFailClosed(t *testing.T) {
	paged := func() string {
		t.Helper()
		remote := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "paged", Version: "v1"}, nil)
		handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return remote }, nil)
		var pages atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "cannot read request", http.StatusBadRequest)
				return
			}
			var request struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if err := json.Unmarshal(body, &request); err == nil && request.Method == "tools/list" {
				result := map[string]any{"tools": []any{}}
				if pages.Add(1) < 129 {
					result["nextCursor"] = fmt.Sprint(pages.Load())
				}
				response, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
				if err != nil {
					t.Errorf("marshal response: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(response)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			handler.ServeHTTP(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	healthy, deletes := newMCPTestServerCounting(t)
	candidate, err := buildMCPReconcileCandidate(Config{})(context.Background(), []mcp.ServerConfig{
		{Name: "healthy", URL: healthy}, {Name: "page-a", URL: paged()}, {Name: "page-b", URL: paged()},
	}, func() {})
	if candidate != nil || err == nil || !strings.Contains(err.Error(), "budget exceeded") {
		if candidate != nil {
			defer candidate.close()
			t.Logf("peer failures: %v", candidate.failures)
		}
		t.Fatalf("aggregate page overrun = (%v, %v), want rejected candidate", candidate, err)
	}
	if atomic.LoadInt32(deletes) == 0 {
		t.Fatal("rejected candidate did not close healthy peer")
	}
}

func TestMCPSourceReconciliation_ResourcePromptListFailureIsolated(t *testing.T) {
	for _, method := range []string{"resources/list", "prompts/list"} {
		t.Run(method, func(t *testing.T) {
			remote := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fixture", Version: "v1"}, nil)
			remote.AddResource(&mcpsdk.Resource{URI: "test://doc", Name: "doc"}, func(context.Context, *mcpsdk.ReadResourceRequest) (*mcpsdk.ReadResourceResult, error) {
				return &mcpsdk.ReadResourceResult{}, nil
			})
			remote.AddPrompt(&mcpsdk.Prompt{Name: "example"}, func(context.Context, *mcpsdk.GetPromptRequest) (*mcpsdk.GetPromptResult, error) {
				return &mcpsdk.GetPromptResult{}, nil
			})
			handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return remote }, nil)
			bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "cannot read request", http.StatusBadRequest)
					return
				}
				var request struct {
					Method string `json:"method"`
				}
				if err := json.Unmarshal(body, &request); err == nil && request.Method == method {
					http.Error(w, "list unavailable", http.StatusBadGateway)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				handler.ServeHTTP(w, r)
			}))
			defer bad.Close()
			src := &reconciliationSource{name: "static", cfgs: []mcp.ServerConfig{{Name: "healthy", URL: newMCPTestServer(t)}, {Name: "bad", URL: bad.URL}}}
			r := newMCPSourceReconciler(mcpReconcilerOptions{sources: []mcpsource.Source{src}, build: buildMCPReconcileCandidate(Config{})})
			defer r.Close()
			result, err := r.Reconcile(context.Background())
			if err != nil || len(result.candidate.manager.Servers()) != 1 {
				t.Fatalf("partial candidate = (%+v, %v)", result, err)
			}
			if diagnostics := r.statusSnapshot().Sources[0].Diagnostics; len(diagnostics) != 1 || !strings.Contains(diagnostics[0], "list "+strings.Split(method, "/")[0]) {
				t.Fatalf("failure diagnostic = %v", diagnostics)
			}
		})
	}
}

func TestMCPReconcileCandidateCancellation(t *testing.T) {
	healthy, deletes := newMCPTestServerCounting(t)
	backend, err := url.Parse(healthy)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	listing := make(chan struct{})
	var listingOnce sync.Once
	observed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "cannot read request", http.StatusBadRequest)
			return
		}
		var request struct {
			Method string `json:"method"`
		}
		_ = json.Unmarshal(body, &request)
		// Initialization precedes tools/list; an SSE response need not reach EOF.
		if request.Method == "tools/list" {
			listingOnce.Do(func() { close(listing) })
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		proxy.ServeHTTP(w, r)
	}))
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	blocked := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-listing:
		case <-release:
			return
		case <-r.Context().Done():
			return
		}
		once.Do(func() { close(entered) })
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		close(release)
		blocked.Close()
		observed.CloseClientConnections()
		observed.Close()
	}()
	go func() {
		select {
		case <-entered:
			cancel()
		case <-ctx.Done():
		}
	}()
	candidate, err := buildMCPReconcileCandidate(Config{})(ctx, []mcp.ServerConfig{
		{Name: "healthy", URL: observed.URL}, {Name: "blocked", URL: blocked.URL},
	}, func() {})
	if candidate != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled candidate = (%v, %v)", candidate, err)
	}
	if atomic.LoadInt32(deletes) == 0 {
		t.Fatal("canceled candidate did not close initialized peer")
	}
}

func configURL(configs []mcp.ServerConfig, name string) string {
	for _, c := range configs {
		if c.Name == name {
			return c.URL
		}
	}
	return ""
}
func containsSecret(s string) bool {
	return len(s) >= len("do-not-log") && stringContains(s, "do-not-log")
}
func stringContains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
func eventuallyReconcile(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for reconciliation")
}
