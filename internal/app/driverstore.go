package app

import (
	"fmt"
	"path/filepath"
	"sync"

	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/internal/adapter/grpcdriver"
)

// Remote store drivers (Phase B): the composition seam that swaps the local
// session/memory stores for gRPC driver clients (internal/adapter/grpcdriver)
// when the operator points a *StoreURL at a driver process. All-empty URLs
// keep today's behaviour byte-identical (validateDriverConfig + the untouched
// default branches in buildStore/buildCatalog guarantee it).

// validateDriverConfig rejects a config that sets BOTH a local store
// directory and a remote driver URL for the same store — the two are
// mutually exclusive (one store per seam; a silent precedence would hide an
// operator mistake) — and a driver-TLS file knob without --driver-tls itself
// (the files are consulted only under TLS, so they would be SILENTLY ignored:
// a misconfig, not a default). It is fatal at the top of Build, the
// validateSkillDraftConfig precedent.
func validateDriverConfig(cfg Config) error {
	if cfg.StoreDir != "" && cfg.SessionStoreURL != "" {
		return fmt.Errorf("--store-dir %q and --session-store-url %q are mutually exclusive: the session store is either the local JSONL dir or the remote driver, never both", cfg.StoreDir, cfg.SessionStoreURL)
	}
	// Redis Secret-file fields are meaningful only when the Redis store is selected.
	// Reject them rather than silently falling back to an unrelated store.
	if cfg.RedisURL == "" && (cfg.RedisUsernameFile != "" || cfg.RedisPasswordFile != "" || cfg.RedisTLSCAFile != "" || cfg.RedisTLS) {
		return fmt.Errorf("--redis-username-file/--redis-password-file/--redis-tls-ca/--redis-tls require --redis-url: Redis connection material must not be silently ignored")
	}
	// Redis (ADR 0048, mecak8s) is a third store option, mutually exclusive with
	// BOTH the local dir and the gRPC driver (one store per seam — a silent
	// precedence would hide an operator mistake).
	if cfg.RedisURL != "" && cfg.StoreDir != "" {
		return fmt.Errorf("--redis-url %q and --store-dir %q are mutually exclusive: the session store is either the Redis managed service or the local JSONL dir, never both", cfg.RedisURL, cfg.StoreDir)
	}
	if cfg.RedisURL != "" && cfg.SessionStoreURL != "" {
		return fmt.Errorf("--redis-url %q and --session-store-url %q are mutually exclusive: the session store is either the Redis managed service or the remote gRPC driver, never both", cfg.RedisURL, cfg.SessionStoreURL)
	}
	if cfg.MemoryDir != "" && cfg.MemoryStoreURL != "" {
		return fmt.Errorf("--memory-dir %q and --memory-store-url %q are mutually exclusive: the memory store is either the local flock dir or the remote driver, never both", cfg.MemoryDir, cfg.MemoryStoreURL)
	}
	if err := validateMemoryDirIsolation(cfg); err != nil {
		return err
	}
	if err := validateDriverSourceConfig(cfg); err != nil {
		return err
	}
	return validateDriverTLSConfig(cfg)
}

func validateDriverSourceConfig(cfg Config) error {
	if cfg.SkillSourceURL != "" && (len(cfg.SkillsDirs) > 0 || cfg.SkillsConventional) {
		return fmt.Errorf("--skill-source-url %q and --skills-dir/--skills-conventional are mutually exclusive: skills come either from the local directories or from the remote driver, never both", cfg.SkillSourceURL)
	}
	if cfg.SoulSourceURL != "" && cfg.SoulPath != "" {
		return fmt.Errorf("--soul-source-url %q and --soul-file %q are mutually exclusive: the user-slot soul is either the local file or the remote driver, never both (--no-soul still disables either)", cfg.SoulSourceURL, cfg.SoulPath)
	}
	// Explicit agent dirs clash with the driver; default conventional discovery
	// is superseded because it is enabled and inert by default.
	if cfg.AgentSourceURL != "" && len(cfg.AgentsDirs) > 0 {
		return fmt.Errorf("--agent-source-url %q and --agents-dir are mutually exclusive: agent definitions come either from the explicit local directories or from the remote driver, never both (the default conventional discovery is superseded, not an error)", cfg.AgentSourceURL)
	}
	return nil
}

func validateDriverTLSConfig(cfg Config) error {
	if !cfg.DriverTLS && (cfg.DriverTLSCA != "" || cfg.DriverTLSCert != "" || cfg.DriverTLSKey != "") {
		return fmt.Errorf("--driver-tls-ca/--driver-tls-cert/--driver-tls-key require --driver-tls: without it they would be silently ignored and the driver connection would ride plaintext")
	}
	return nil
}

func validateMemoryDirIsolation(cfg Config) error {
	if cfg.MemoryDir == "" || cfg.NoUserModel {
		return nil
	}
	userPath := resolveUserModelDir(cfg.UserModelDir)
	if userPath == "" {
		return nil
	}
	memoryPath := cfg.MemoryDir
	memoryDir, err := canonicalConfiguredDir(memoryPath)
	if err != nil {
		return fmt.Errorf("resolve --memory-dir %q: %w", memoryPath, err)
	}
	userDir, err := canonicalConfiguredDir(userPath)
	if err != nil {
		return fmt.Errorf("resolve --user-model-dir %q: %w", userPath, err)
	}
	if memoryDir == userDir {
		return fmt.Errorf("--memory-dir %q and --user-model-dir %q resolve to the same directory; project and operator memory must be isolated", memoryPath, userPath)
	}
	return nil
}

func canonicalConfiguredDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return filepath.Clean(resolved), nil
	}
	// A not-yet-created leaf cannot be symlink-aliased; resolve its existing parent.
	parent, parentErr := filepath.EvalSymlinks(filepath.Dir(abs))
	if parentErr != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(abs)), nil
}

// driverConns is the per-target driver connection cache: equal URLs share ONE
// lazy grpc.ClientConn (a deployment pointing the session AND memory stores
// at the same driver process multiplexes one connection), and each acquired
// close is once-guarded so the shared conn closes exactly once however many
// consumers fold the close into their teardown chains.
type driverConns struct {
	mu    sync.Mutex
	conns map[string]*driverConn
}

type driverConn struct {
	conn *grpc.ClientConn
	once sync.Once
}

func newDriverConns() *driverConns {
	return &driverConns{conns: make(map[string]*driverConn)}
}

// dial returns the cached connection for target (dialling lazily via
// grpcdriver.Dial on first use, with the auth/TLS posture from cfg) plus an
// idempotent close func. The dial itself never touches the network
// (grpc.NewClient is lazy); the first RPC surfaces a connect error.
func (d *driverConns) dial(cfg Config, target string) (*grpc.ClientConn, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if dc, ok := d.conns[target]; ok {
		return dc.conn, dc.close, nil
	}
	conn, err := grpcdriver.Dial(target, driverDialOptions(cfg)...)
	if err != nil {
		return nil, nil, err
	}
	dc := &driverConn{conn: conn}
	d.conns[target] = dc
	return dc.conn, dc.close, nil
}

// close closes the underlying conn exactly once; later calls are no-ops.
func (dc *driverConn) close() {
	dc.once.Do(func() { _ = dc.conn.Close() })
}

// driverDialOptions maps the Config's driver auth/TLS fields onto grpcdriver
// dial options. The CA/cert/key files are consulted only with DriverTLS set
// (mirroring mecated's --tls + --tls-ca client posture).
func driverDialOptions(cfg Config) []grpcdriver.Option {
	var opts []grpcdriver.Option
	if cfg.DriverAuthToken != "" {
		opts = append(opts, grpcdriver.WithBearerToken(cfg.DriverAuthToken))
	}
	if cfg.DriverTLS {
		opts = append(opts, grpcdriver.WithTLS(grpcdriver.TLSOptions{
			CAFile:         cfg.DriverTLSCA,
			ClientCertFile: cfg.DriverTLSCert,
			ClientKeyFile:  cfg.DriverTLSKey,
		}))
	}
	return opts
}

// drivers returns the build-scoped connection cache, or a fresh one when a
// helper is called directly by a unit test on a bare Config (the cfg.diag()
// nil-safety idiom). Build sets the field once, so every production dial in
// one Build shares the same cache.
func (c Config) drivers() *driverConns {
	if c.driverConns == nil {
		return newDriverConns()
	}
	return c.driverConns
}
