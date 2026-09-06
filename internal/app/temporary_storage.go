package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/managedtemp"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

type temporaryStorageMode string

const (
	temporaryStorageManaged temporaryStorageMode = "managed"
	temporaryStorageSystem  temporaryStorageMode = "system"
)

// temporaryStorageConfig is the resolved, operator-owned temporary storage
// policy. Allocation is deliberately outside this configuration seam.
type temporaryStorageConfig struct {
	Mode                temporaryStorageMode
	ManagedRoot         string
	SystemTempDir       string
	CommandReapAfter    time.Duration
	ReapInterval        time.Duration
	ReapTimeout         time.Duration
	ShutdownReapTimeout time.Duration
}

type managedTemporaryStorage struct {
	namespace  *managedtemp.Namespace
	mu         sync.Mutex
	workspaces []*managedtemp.Workspace
}

func openManagedTemporaryStorage(cfg temporaryStorageConfig) (*managedTemporaryStorage, error) {
	if cfg.Mode != temporaryStorageManaged {
		return nil, nil
	}
	namespace, err := managedtemp.Open(cfg.ManagedRoot)
	if err != nil {
		return nil, err
	}
	return &managedTemporaryStorage{namespace: namespace}, nil
}

func (s *managedTemporaryStorage) workspace(root string) (*managedtemp.Workspace, error) {
	if s == nil {
		return nil, nil
	}
	identity, err := managedWorkspaceIdentity(root)
	if err != nil {
		return nil, err
	}
	workspace, err := s.namespace.OpenWorkspace("osfs", identity, identity)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.workspaces = append(s.workspaces, workspace)
	s.mu.Unlock()
	return workspace, nil
}

func (s *managedTemporaryStorage) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	for _, workspace := range s.workspaces {
		_ = workspace.Close()
	}
	s.workspaces = nil
	s.mu.Unlock()
	_ = s.namespace.Close()
}

func foldOperatorTemporaryStorage(cfg Config) (Config, error) {
	res, ok := cfg.permResolver.(*permconfig.Resolver)
	if !ok || res == nil {
		resolved, err := resolveTemporaryStorage(cfg, os.Getenv, runtime.GOOS)
		cfg.temporaryStorage = resolved
		return cfg, err
	}
	section, err := res.OperatorTemporaryStorage()
	if err != nil {
		return cfg, fmt.Errorf("temporary storage configuration: %w", err)
	}
	if section != nil {
		cfg.temporaryStorage = temporaryStorageConfig{
			Mode: temporaryStorageMode(section.Mode), ManagedRoot: section.ManagedRoot,
			SystemTempDir: section.SystemTempDir, CommandReapAfter: section.CommandReapAfter,
			ReapInterval: section.ReapInterval, ReapTimeout: section.ReapTimeout,
			ShutdownReapTimeout: section.ShutdownReapTimeout,
		}
	}
	resolved, err := resolveTemporaryStorage(cfg, os.Getenv, runtime.GOOS)
	cfg.temporaryStorage = resolved
	return cfg, err
}

func resolveTemporaryStorage(cfg Config, getenv func(string) string, platform string) (temporaryStorageConfig, error) {
	result := cfg.temporaryStorage
	if result.Mode == "" {
		result.Mode = temporaryStorageManaged
	}
	if result.CommandReapAfter == 0 {
		result.CommandReapAfter = time.Hour
	}
	if result.ReapInterval == 0 {
		result.ReapInterval = time.Hour
	}
	if result.ReapTimeout == 0 {
		result.ReapTimeout = 5 * time.Minute
	}
	if result.ShutdownReapTimeout == 0 {
		result.ShutdownReapTimeout = time.Minute
	}
	if result.Mode != temporaryStorageManaged && result.Mode != temporaryStorageSystem {
		return temporaryStorageConfig{}, fmt.Errorf("temporary storage mode must be managed or system")
	}
	if result.Mode == temporaryStorageManaged && platform != "linux" {
		return temporaryStorageConfig{}, fmt.Errorf("temporary storage mode managed is supported only on linux")
	}
	inherited := inheritedSystemTempDir(getenv)
	var err error
	if result.SystemTempDir, err = resolveTempPath(result.SystemTempDir, inherited, "system_temp_dir"); err != nil {
		return temporaryStorageConfig{}, err
	}
	if result.ManagedRoot == "" {
		result.ManagedRoot = "mecatl"
	}
	if result.ManagedRoot, err = resolveTempPath(result.ManagedRoot, result.SystemTempDir, "managed_root"); err != nil {
		return temporaryStorageConfig{}, err
	}
	if result.Mode == temporaryStorageManaged && filepath.IsAbs(cfg.temporaryStorage.ManagedRoot) {
		if err := validateControlledRoot(result.ManagedRoot); err != nil {
			return temporaryStorageConfig{}, err
		}
	}
	return result, nil
}

func inheritedSystemTempDir(getenv func(string) string) string {
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if value := getenv(key); value != "" {
			return value
		}
	}
	return os.TempDir()
}

func resolveTempPath(raw, base, name string) (string, error) {
	if raw == "" {
		return filepath.Clean(base), nil
	}
	if filepath.IsAbs(raw) {
		return filepath.Clean(raw), nil
	}
	clean := filepath.Clean(raw)
	if clean == ".." || len(clean) > 3 && clean[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("temporary storage %s escapes inherited system temporary directory", name)
	}
	return filepath.Join(base, clean), nil
}
