package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/agents"
	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
	"github.com/stacklok/mecatl/internal/adapter/hookexec"
	"github.com/stacklok/mecatl/internal/adapter/memory"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
)

// TestValidateDriverConfigExclusivity pins the mutual-exclusion rule: a local
// dir and a remote driver URL for the SAME store is a fatal config error;
// every other combination passes.
func TestValidateDriverConfigExclusivity(t *testing.T) {
	cases := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{name: "all empty", cfg: Config{}},
		{name: "local only", cfg: Config{StoreDir: "/tmp/s", MemoryDir: "/tmp/m"}},
		{name: "drivers only", cfg: Config{SessionStoreURL: "127.0.0.1:7443", MemoryStoreURL: "127.0.0.1:7443"}},
		{name: "mixed across seams", cfg: Config{StoreDir: "/tmp/s", MemoryStoreURL: "127.0.0.1:7443"}},
		{name: "redis only", cfg: Config{RedisURL: "redis:6379"}},
		{
			name:    "session store both",
			cfg:     Config{StoreDir: "/tmp/s", SessionStoreURL: "127.0.0.1:7443"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "redis and store-dir",
			cfg:     Config{RedisURL: "redis:6379", StoreDir: "/tmp/s"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "redis and session-store-url",
			cfg:     Config{RedisURL: "redis:6379", SessionStoreURL: "127.0.0.1:7443"},
			wantErr: "mutually exclusive",
		},
		{
			name:    "memory store both",
			cfg:     Config{MemoryDir: "/tmp/m", MemoryStoreURL: "127.0.0.1:7443"},
			wantErr: "mutually exclusive",
		},
		{name: "tls with files", cfg: Config{SessionStoreURL: "10.0.0.9:7443", DriverTLS: true, DriverTLSCA: "/tmp/ca.pem", DriverTLSCert: "/tmp/c.pem", DriverTLSKey: "/tmp/k.pem"}},
		{
			name:    "tls ca without driver-tls",
			cfg:     Config{SessionStoreURL: "127.0.0.1:7443", DriverTLSCA: "/tmp/ca.pem"},
			wantErr: "require --driver-tls",
		},
		{
			name:    "tls cert without driver-tls",
			cfg:     Config{SessionStoreURL: "127.0.0.1:7443", DriverTLSCert: "/tmp/c.pem"},
			wantErr: "require --driver-tls",
		},
		{
			name:    "tls key without driver-tls",
			cfg:     Config{SessionStoreURL: "127.0.0.1:7443", DriverTLSKey: "/tmp/k.pem"},
			wantErr: "require --driver-tls",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateDriverConfig(c.cfg)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDriverConfig = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("validateDriverConfig = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

// TestBuildStoreDriverURL pins the new buildStore branch: a SessionStoreURL
// yields the grpcdriver client. Offline-safe — grpc.NewClient is lazy, so no
// connection is attempted; the close func releases the (never-connected)
// conn.
func TestBuildStoreDriverURL(t *testing.T) {
	st, eventLog, closeFn, err := buildStore(Config{SessionStoreURL: "127.0.0.1:7443"})
	if err != nil {
		t.Fatalf("buildStore(driver URL): %v", err)
	}
	defer closeFn()
	if _, ok := st.(*grpcdriver.SessionStore); !ok {
		t.Fatalf("buildStore(driver URL) = %T, want *grpcdriver.SessionStore", st)
	}
	// EventLog over the driver is a 3c concern; the 3a driver branch leaves it
	// nil (the relay then records nothing).
	if eventLog != nil {
		t.Fatalf("buildStore(driver URL) eventLog = %T, want nil (3c)", eventLog)
	}
}

// TestBuildStoreDefaults pins that the existing branches are untouched: empty
// config still yields the in-memory store, StoreDir still yields the JSONL
// store.
func TestBuildStoreDefaults(t *testing.T) {
	t.Run("empty -> memstore", func(t *testing.T) {
		st, eventLog, closeFn, err := buildStore(Config{})
		if err != nil {
			t.Fatalf("buildStore(empty): %v", err)
		}
		defer closeFn()
		if _, ok := st.(*memstore.Store); !ok {
			t.Fatalf("buildStore(empty) = %T, want *memstore.Store", st)
		}
		// The memstore path supplies an in-memory EventLog sibling so the seam is
		// never nil offline.
		if _, ok := eventLog.(*memstore.EventLog); !ok {
			t.Fatalf("buildStore(empty) eventLog = %T, want *memstore.EventLog", eventLog)
		}
	})
	t.Run("store-dir -> jsonlstore", func(t *testing.T) {
		st, eventLog, closeFn, err := buildStore(Config{StoreDir: t.TempDir()})
		if err != nil {
			t.Fatalf("buildStore(StoreDir): %v", err)
		}
		defer closeFn()
		js, ok := st.(*jsonlstore.Store)
		if !ok {
			t.Fatalf("buildStore(StoreDir) = %T, want *jsonlstore.Store", st)
		}
		// The one jsonlstore Store doubles as the EventLog (same instance).
		if el, ok := eventLog.(*jsonlstore.Store); !ok || el != js {
			t.Fatalf("buildStore(StoreDir) eventLog should be the same *jsonlstore.Store instance, got %T", eventLog)
		}
	})
}

// TestBuildStoreRedisURL pins the ADR-0048 Redis branch of buildSessionStore:
// a RedisURL yields the redisstore adapter, which doubles as its own EventLog
// (the same Store instance, like jsonlstore). The test uses an in-process
// miniredis so it is fully offline. A Save/Load round-trip proves the adapter
// is wired through composition, not just constructed.
func TestBuildStoreRedisURL(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	st, eventLog, closeFn, err := buildStore(Config{RedisURL: mr.Addr()})
	if err != nil {
		t.Fatalf("buildStore(RedisURL): %v", err)
	}
	defer closeFn()
	rs, ok := st.(*redisstore.Store)
	if !ok {
		t.Fatalf("buildStore(RedisURL) = %T, want *redisstore.Store", st)
	}
	// The one redisstore Store doubles as the EventLog (same instance).
	if el, ok := eventLog.(*redisstore.Store); !ok || el != rs {
		t.Fatalf("buildStore(RedisURL) eventLog should be the same *redisstore.Store instance, got %T", eventLog)
	}
	// A Save/Load round-trip through the composition-wired adapter proves it is
	// the real store, not a nil stub.
	s := session.New("redis-build-test", session.ModeAccept, "/work", session.Limits{}, time.Now())
	if err := st.Save(context.Background(), s); err != nil {
		t.Fatalf("Save through composition-wired redisstore: %v", err)
	}
	got, err := st.Load(context.Background(), s.ID)
	if err != nil {
		t.Fatalf("Load through composition-wired redisstore: %v", err)
	}
	if got.ID != s.ID {
		t.Errorf("Load.ID = %q, want %q", got.ID, s.ID)
	}
}

// TestBuildStoreEventLogURL pins the cloud-native 3c override: an
// --event-log-url points the durable EventLog at a grpcdriver EventLogService
// client, INDEPENDENT of where the session store lives. Offline-safe (lazy
// grpc.NewClient). The two cases prove the override applies both when the
// store-derived default is a concrete log (in-memory) and when it is nil (a
// session-store driver).
func TestBuildStoreEventLogURL(t *testing.T) {
	t.Run("over the in-memory default", func(t *testing.T) {
		st, eventLog, closeFn, err := buildStore(Config{EventLogURL: "127.0.0.1:7444"})
		if err != nil {
			t.Fatalf("buildStore(EventLogURL): %v", err)
		}
		defer closeFn()
		if _, ok := st.(*memstore.Store); !ok {
			t.Fatalf("buildStore(EventLogURL) store = %T, want *memstore.Store (store unaffected)", st)
		}
		if _, ok := eventLog.(*grpcdriver.EventLog); !ok {
			t.Fatalf("buildStore(EventLogURL) eventLog = %T, want *grpcdriver.EventLog", eventLog)
		}
	})
	t.Run("over a session-store driver (nil default)", func(t *testing.T) {
		_, eventLog, closeFn, err := buildStore(Config{SessionStoreURL: "127.0.0.1:7443", EventLogURL: "127.0.0.1:7444"})
		if err != nil {
			t.Fatalf("buildStore(SessionStoreURL+EventLogURL): %v", err)
		}
		defer closeFn()
		if _, ok := eventLog.(*grpcdriver.EventLog); !ok {
			t.Fatalf("buildStore(SessionStoreURL+EventLogURL) eventLog = %T, want *grpcdriver.EventLog (override beats the nil driver default)", eventLog)
		}
	})
}

// TestDriverConnsShareEqualTargets pins the connection cache: two dials of
// the SAME target share one ClientConn (a deployment pointing both stores at
// one driver multiplexes one connection), distinct targets do not, and the
// once-guarded close tolerates being run from both consumers' chains.
func TestDriverConnsShareEqualTargets(t *testing.T) {
	conns := newDriverConns()
	cfg := Config{}
	c1, close1, err := conns.dial(cfg, "127.0.0.1:7443")
	if err != nil {
		t.Fatalf("dial #1: %v", err)
	}
	c2, close2, err := conns.dial(cfg, "127.0.0.1:7443")
	if err != nil {
		t.Fatalf("dial #2: %v", err)
	}
	if c1 != c2 {
		t.Error("equal targets returned distinct ClientConns, want one shared conn")
	}
	c3, close3, err := conns.dial(cfg, "127.0.0.1:7444")
	if err != nil {
		t.Fatalf("dial #3: %v", err)
	}
	if c3 == c1 {
		t.Error("distinct targets share one ClientConn, want separate conns")
	}
	close1()
	close2() // second close of the shared conn must be a no-op, not a panic/double-close
	close3()
}

// TestBuildCatalogMemoryDriverRegistersTools proves the REAL wiring
// (buildCatalog, the same function buildEngine calls) takes the
// MemoryStoreURL driver branch and registers the memory tool family — the
// driver-backed analogue of TestBuildCatalogRegistersMemorySearchWhenEnabled.
// Offline: grpc.NewClient is lazy, so registration needs no live driver.
func TestBuildCatalogMemoryDriverRegistersTools(t *testing.T) {
	ctx := context.Background()
	provider := mockllm.New(mockllm.TextTurn("x"))
	hooks := hookexec.New(nil)

	cfg := Config{MemoryStoreURL: "127.0.0.1:7443"}
	cat, assets, _, _, closeFn, err := buildCatalog(ctx, cfg, regForTest(provider, providerMock, cfg.Model), provider, hooks, agents.NewRegistry(nil), memstore.New(), nil)
	if err != nil {
		t.Fatalf("buildCatalog(memory driver): %v", err)
	}
	defer closeFn()

	for _, name := range []string{memory.SearchMemoryToolName, memory.RecallToolName, memory.RememberToolName} {
		if _, ok := cat.Lookup(name); !ok {
			t.Errorf("memory driver enabled (MemoryStoreURL set): catalog is missing %q", name)
		}
	}
	if _, ok := assets.memStore.(*grpcdriver.MemoryStore); !ok {
		t.Errorf("assets.memStore = %T, want *grpcdriver.MemoryStore (the driver branch must have been taken)", assets.memStore)
	}
}

// TestBuildRejectsExclusiveStoreConfig pins the Build()-level call site of
// validateDriverConfig: an exclusivity misconfig fails the WHOLE build, not
// just the helper in isolation.
func TestBuildRejectsExclusiveStoreConfig(t *testing.T) {
	_, err := Build(context.Background(), Config{StoreDir: t.TempDir(), SessionStoreURL: "127.0.0.1:7443"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Build(StoreDir+SessionStoreURL) error = %v, want the mutual-exclusion config error", err)
	}
}
