// Command mecatl-guest-agent serves the unified Workspace and exec protocol inside the guest.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/environment/microvm"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/guestagent"
	"github.com/stacklok/mecatl/environment/microvm/guestexec"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

type repositoryBootConfig struct {
	Owner, RepositoryKey, VMID, Endpoint string
	Generation                           uint32
}

type bootConfig struct {
	microvm.GuestPrebootConfig
	CapabilityKey string                `json:"capability_key"`
	Repository    *repositoryBootConfig `json:"repository,omitempty"`
}

var errCapabilityMaterialWorkloadOwned = errors.New("guest capability material is owned by the workload mapping")

func main() {
	configPath := flag.String("config", microvm.GuestPrebootConfigPath, "immutable guest preboot configuration")
	flag.Parse()
	if err := run(*configPath); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(configPath string) error { //nolint:gocyclo // explicit legacy/repository guest boot state machine
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	if err := hardenPrivilegedConfig(configPath, rootlessSafeChown, os.Chmod); err != nil {
		if !errors.Is(err, errCapabilityMaterialWorkloadOwned) {
			return fmt.Errorf("harden guest capability material: %w", err)
		}
		if err := consumePrivilegedConfig(configPath); err != nil {
			return fmt.Errorf("consume guest capability material: %w", err)
		}
	}
	if cfg.Repository != nil {
		if err := prepareRepositoryGuestMount(); err != nil {
			return fmt.Errorf("mount repository guest namespace: %w", err)
		}
	} else if err := prepareGuestMounts(); err != nil {
		return fmt.Errorf("mount guest execution environment: %w", err)
	}
	if err := prepareGuestNetwork(); err != nil {
		return fmt.Errorf("configure guest network: %w", err)
	}
	if cfg.DisableIPv6 {
		if err := microvm.NewSysctlGuestNetwork(writeSysctl).DisableIPv6(context.Background()); err != nil {
			return err
		}
	}
	key, err := base64.RawStdEncoding.DecodeString(cfg.CapabilityKey)
	if err != nil {
		return fmt.Errorf("decode guest capability key: %w", err)
	}
	var serve func(context.Context, io.ReadWriteCloser) error
	if cfg.Repository != nil {
		repositoryServer, serverErr := guestagent.NewRepositoryServer(guestagent.RepositoryServerConfig{
			Owner: cfg.Repository.Owner, RepositoryKey: cfg.Repository.RepositoryKey, VMID: cfg.Repository.VMID, Endpoint: cfg.Repository.Endpoint,
			Generation: cfg.Repository.Generation, AuthorityKey: key, ExecLimits: guestexec.Limits{MaxFrameBytes: cfg.MaxMessageBytes}, Shell: "/bin/sh",
			WorkloadIdentity: guestexec.DefaultWorkloadIdentity(), RuntimeContract: guestexec.DefaultRuntimeContract(),
		})
		err = serverErr
		if repositoryServer != nil {
			serve = repositoryServer.ServeAuthenticated
		}
	} else {
		var server *guestagent.Server
		server, err = guestagent.NewServer(guestagent.ServerConfig{
			Binding: cfg.Binding, CapabilityKey: key, WorkspaceRoot: worktree.GuestWorkspace,
			ExecLimits: guestexec.Limits{MaxFrameBytes: cfg.MaxMessageBytes}, Shell: "/bin/sh",
			WorkloadIdentity: guestexec.DefaultWorkloadIdentity(), RuntimeContract: guestexec.DefaultRuntimeContract(),
		})
		if server != nil {
			serve = server.Serve
		}
	}
	for i := range key {
		key[i] = 0
	}
	if err != nil {
		return err
	}
	if err := lockWorkloadPrivileges(); err != nil {
		return fmt.Errorf("lock guest workload privilege escalation: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	dial := func(ctx context.Context) (io.ReadWriteCloser, error) {
		return guestagent.DialHostVsock(ctx, control.GuestControlPort)
	}
	if cfg.Repository != nil {
		return serveRepositoryConnections(ctx, dial, serve)
	}
	stream, err := dial(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	return serve(ctx, stream)
}

// Keep one connection beyond the persistent logical-data capacity so health,
// register, and unregister control exchanges cannot be starved by attachments.
const maxRepositoryConnections = 17

func serveRepositoryConnections(ctx context.Context, dial func(context.Context) (io.ReadWriteCloser, error), serve func(context.Context, io.ReadWriteCloser) error) error {
	slots := make(chan struct{}, maxRepositoryConnections)
	for {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		stream, err := dial(ctx)
		if err != nil {
			<-slots
			if ctx.Err() != nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}
		go func() {
			stopClose := context.AfterFunc(ctx, func() { _ = stream.Close() })
			_ = serve(ctx, stream)
			stopClose()
			_ = stream.Close()
			<-slots
		}()
	}
}

func rootlessSafeChown(path string, uid, gid int) error {
	return rootlessSafeChownWith(path, uid, gid, os.Chown, os.Lstat)
}

func rootlessSafeChownWith(path string, uid, gid int, chown func(string, int, int) error, lstat func(string) (os.FileInfo, error)) error {
	err := chown(path, uid, gid)
	if err == nil || (!errors.Is(err, syscall.EPERM) && !errors.Is(err, syscall.EINVAL)) {
		return err
	}
	info, statErr := lstat(path)
	if statErr != nil {
		return statErr
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return err
	}
	if stat.Uid == guestexec.DefaultWorkloadIdentity().UID || stat.Gid == guestexec.DefaultWorkloadIdentity().GID {
		return errCapabilityMaterialWorkloadOwned
	}
	// A single-ID user namespace reports EINVAL for unmapped guest root; older
	// rootless libkrun paths report EPERM. In both cases preserve the mapped host
	// owner only when it is not the workload identity. Modes 0700/0600 then keep
	// the capability outside workload UID/GID 65532.
	return nil
}

func consumePrivilegedConfig(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("guest capability config is not a regular file")
	}
	parent := filepath.Dir(path)
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		return errors.New("guest capability config parent contains unexpected entries")
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return os.Remove(parent)
}

func hardenPrivilegedConfig(path string, chown func(string, int, int) error, chmod func(string, os.FileMode) error) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("guest capability config is not a regular file")
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("guest capability config parent is not a directory")
	}
	if err := chown(parent, 0, 0); err != nil {
		return err
	}
	if err := chmod(parent, 0o700); err != nil {
		return err
	}
	if err := chown(path, 0, 0); err != nil {
		return err
	}
	return chmod(path, 0o600)
}

func loadConfig(path string) (bootConfig, error) {
	file, err := os.Open(path) // #nosec G304 -- root-owned immutable path selected by the image entrypoint/operator.
	if err != nil {
		return bootConfig{}, fmt.Errorf("open guest preboot config: %w", err)
	}
	defer func() { _ = file.Close() }()
	var cfg bootConfig
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cfg); err != nil {
		return bootConfig{}, fmt.Errorf("decode guest preboot config: %w", err)
	}
	if cfg.CapabilityKey == "" || cfg.MaxMessageBytes == 0 || (cfg.Repository == nil && cfg.Binding.Ref == "") ||
		(cfg.Repository != nil && (cfg.Repository.Owner == "" || cfg.Repository.RepositoryKey == "" || cfg.Repository.VMID == "" || cfg.Repository.Endpoint == "" || cfg.Repository.Generation == 0)) {
		return bootConfig{}, errors.New("guest preboot config is incomplete")
	}
	return cfg, nil
}

func writeSysctl(key, value string) error {
	path := filepath.Join("/proc/sys", filepath.FromSlash(keyToPath(key)))
	return os.WriteFile(path, []byte(value), 0o600)
}

func keyToPath(key string) string {
	result := []byte(key)
	for i := range result {
		if result[i] == '.' {
			result[i] = '/'
		}
	}
	return string(result)
}
