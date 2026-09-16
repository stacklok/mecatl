package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"
	"golang.org/x/sys/unix"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpcredential"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
)

const mcpLifecycleUsage = "mecated mcp {add NAME URL [--file PATH] [--credential-store auto|keyring|file] | list [--file PATH] | remove NAME [--file PATH]}"

type mcpLifecycleArgs struct {
	name, url, file, custody string
}

func parseMCPLifecycleArgs(command string, args []string) (mcpLifecycleArgs, error) {
	var result mcpLifecycleArgs
	result.custody = "auto"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--file" || arg == "--credential-store" {
			if i+1 == len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
			}
			i++
			if arg == "--file" {
				if result.file != "" {
					return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
				}
				result.file = args[i]
			} else {
				result.custody = args[i]
			}
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--file="); ok && value != "" {
			if result.file != "" {
				return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
			}
			result.file = value
			continue
		}
		if value, ok := strings.CutPrefix(arg, "--credential-store="); ok && value != "" {
			result.custody = value
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
		}
		if command == "add" && result.name == "" {
			result.name = arg
			continue
		}
		if command == "add" && result.url == "" {
			result.url = arg
			continue
		}
		if command == "remove" && result.name == "" {
			result.name = arg
			continue
		}
		return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
	}
	if command == "add" && (result.name == "" || result.url == "") || command == "remove" && result.name == "" || command == "list" && result.name != "" {
		return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
	}
	if command != "add" && result.custody != "auto" || result.custody != "auto" && result.custody != "keyring" && result.custody != "file" {
		return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
	}
	return result, nil
}

func defaultMCPSettingsFile() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("operator settings path is unavailable")
	}
	return filepath.Join(base, permconfig.UserSettingsRelPath), nil
}

func mcpSettingsFile(explicit string) (string, error) {
	if explicit != "" {
		path, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return canonicalMCPSettingsPath(path)
	}
	path, err := defaultMCPSettingsFile()
	if err != nil {
		return "", err
	}
	return canonicalMCPSettingsPath(path)
}

func canonicalMCPSettingsPath(path string) (string, error) {
	path = filepath.Clean(path)
	var suffix []string
	for probe := path; ; probe = filepath.Dir(probe) {
		info, err := os.Lstat(probe)
		if err == nil {
			if !info.IsDir() {
				if probe != path || info.Mode()&os.ModeSymlink != 0 {
					return "", errors.New("MCP settings path is unsafe")
				}
				suffix = append(suffix, filepath.Base(probe))
				continue
			}
			base, err := filepath.EvalSymlinks(probe)
			if err != nil {
				return "", err
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				base = filepath.Join(base, suffix[i])
			}
			return base, nil
		}
		if !errors.Is(err, os.ErrNotExist) || probe == string(filepath.Separator) {
			return "", err
		}
		suffix = append(suffix, filepath.Base(probe))
	}
}

const maxMCPSettingsBytes = 4 << 20

type mcpSettingsVersion struct {
	exists         bool
	device, inode  uint64
	size, modNanos int64
}
type mcpSettingsSnapshot struct {
	data    []byte
	version mcpSettingsVersion
}

func readMCPSettings(path string) (mcpSettingsSnapshot, error) {
	path, canonicalErr := canonicalMCPSettingsPath(path)
	if canonicalErr != nil {
		return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q cannot be read", path)
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return mcpSettingsSnapshot{data: []byte("{}\n")}, nil
	}
	if err != nil {
		return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q cannot be read", path)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		_ = unix.Close(fd)
		return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q cannot be read", path)
	}
	file := os.NewFile(uintptr(fd), path)
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMCPSettingsBytes+1))
	if err != nil || len(data) > maxMCPSettingsBytes {
		return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q cannot be read", path)
	}
	if err := permconfig.ValidateYAML(data); err != nil {
		return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q is invalid: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q cannot be read", path)
	}
	return mcpSettingsSnapshot{data: data, version: mcpSettingsVersion{exists: true, device: uint64(st.Dev), inode: uint64(st.Ino), size: info.Size(), modNanos: info.ModTime().UnixNano()}}, nil
}

func writeMCPSettings(path string, before mcpSettingsSnapshot, after []byte) error {
	path, canonicalErr := canonicalMCPSettingsPath(path)
	if canonicalErr != nil {
		return errors.New("MCP settings changed while this command was running; retry")
	}
	current, err := readMCPSettings(path)
	if err != nil || !sameMCPSettingsVersion(current, before) || string(current.data) != string(before.data) {
		return errors.New("MCP settings changed while this command was running; retry")
	}
	dirPath, base := filepath.Dir(path), filepath.Base(path)
	if err := secureMCPSettingsDir(dirPath); err != nil {
		return errors.New("MCP settings directory cannot be created")
	}
	dir, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("MCP settings directory cannot be created")
	}
	defer unix.Close(dir)
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("MCP settings cannot be staged")
	}
	tempName := "." + base + "." + hex.EncodeToString(random[:]) + ".tmp"
	fd, err := unix.Openat(dir, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errors.New("MCP settings cannot be staged")
	}
	defer unix.Unlinkat(dir, tempName, 0)
	if err = writeAllMCPSettings(fd, after); err == nil {
		err = unix.Fsync(fd)
	}
	if closeErr := unix.Close(fd); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("MCP settings cannot be staged")
	}
	latest, err := readMCPSettings(path)
	if err != nil || !sameMCPSettingsVersion(latest, before) || string(latest.data) != string(before.data) {
		return errors.New("MCP settings changed while this command was running; retry")
	}
	var st unix.Stat_t
	if before.version.exists {
		err = unix.Fstatat(dir, base, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || !sameMCPSettingsStat(st, before.version) {
			return errors.New("MCP settings changed while this command was running; retry")
		}
	} else if err := unix.Fstatat(dir, base, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return errors.New("MCP settings changed while this command was running; retry")
	}
	if err := unix.Renameat(dir, tempName, dir, base); err != nil {
		return errors.New("MCP settings cannot be published")
	}
	if err := unix.Fsync(dir); err != nil {
		return errors.New("MCP settings cannot be published")
	}
	return nil
}

func writeAllMCPSettings(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func sameMCPSettingsVersion(a, b mcpSettingsSnapshot) bool { return a.version == b.version }
func sameMCPSettingsStat(st unix.Stat_t, v mcpSettingsVersion) bool {
	return v.exists && uint64(st.Dev) == v.device && uint64(st.Ino) == v.inode && int64(st.Size) == v.size
}

// secureMCPSettingsDir prevents a selected settings path from inheriting a symlinked parent.
func secureMCPSettingsDir(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("settings directory must be absolute")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(path), string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		if err := unix.Mkdirat(fd, part, 0700); err != nil && !errors.Is(err, unix.EEXIST) {
			return err
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return err
		}
		_ = unix.Close(fd)
		fd = next
	}
	return nil
}

func runMCPAdd(args []string, stdout io.Writer) error {
	parsed, err := parseMCPLifecycleArgs("add", args)
	if err != nil {
		return err
	}
	if parsed.custody != "auto" && parsed.custody != mcpcredential.BackendKeyring && parsed.custody != mcpcredential.BackendFile {
		return errors.New("MCP credential-store must be auto, keyring, or file")
	}
	path, err := mcpSettingsFile(parsed.file)
	if err != nil {
		return err
	}
	before, err := readMCPSettings(path)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "MCP onboarding: discovering protected resource")
	discovery, err := mcp.DiscoverDirectIssuer(context.Background(), parsed.url)
	if err != nil {
		return err
	}
	issuer := discovery.Issuer
	_, _ = fmt.Fprintln(stdout, "MCP onboarding: updating settings")
	configDir, err := os.UserConfigDir()
	if err != nil {
		return errors.New("MCP credential path is unavailable")
	}
	credentialRoot := filepath.Join(configDir, "mecatl", "mcp-credentials")
	keyPath := filepath.Join(configDir, "mecatl", "mcp-credential-key")
	selected, err := mcpcredential.Resolve(context.Background(), credentialRoot, mcpcredential.Options{Requested: parsed.custody, FilePath: keyPath})
	if err != nil {
		return err
	}
	defer clear(selected.Key)
	var after []byte
	if selected.Backend == mcpcredential.BackendFile {
		after, err = permconfig.AddDirectMCPServerWithKey(before.data, parsed.name, parsed.url, issuer, credentialRoot, selected.Backend, selected.Locator)
	} else {
		after, err = permconfig.AddDirectMCPServerWithKey(before.data, parsed.name, parsed.url, issuer, credentialRoot, selected.Backend, "")
	}
	if err != nil {
		return err
	}
	if err := writeMCPSettings(path, before, after); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "MCP onboarding: settings saved; authorizing and verifying")
	return runMCPLogin([]string{parsed.name, "--permission-config", path}, stdout)
}

func runMCPList(args []string, stdout io.Writer) error {
	parsed, err := parseMCPLifecycleArgs("list", args)
	if err != nil {
		return err
	}
	path, err := mcpSettingsFile(parsed.file)
	if err != nil {
		return err
	}
	data, err := readMCPSettings(path)
	if err != nil {
		return err
	}
	var cfg permconfig.Config
	if err := yaml.Unmarshal(data.data, &cfg); err != nil {
		return fmt.Errorf("MCP settings %q is invalid: %w", path, err)
	}
	if cfg.MCP != nil {
		for _, server := range cfg.MCP.Servers {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\n", server.Name, server.URL, server.Auth.Mode, path)
		}
	}
	_, _ = fmt.Fprintln(stdout, "Changes affect newly started daemons; existing-daemon activation is unknown.")
	return nil
}

func runMCPRemove(args []string, stdout io.Writer) error {
	parsed, err := parseMCPLifecycleArgs("remove", args)
	if err != nil {
		return err
	}
	path, err := mcpSettingsFile(parsed.file)
	if err != nil {
		return err
	}
	before, err := readMCPSettings(path)
	if err != nil {
		return err
	}
	profiles, profileErr := loadMCPLoginProfiles([]string{path})
	if profileErr != nil {
		return profileErr
	}
	defer func() { _ = profiles.Close() }()
	if server, ok := profiles.OAuthServer(parsed.name); ok {
		result, removeErr := mcp.RemoveOAuthDCR(context.Background(), server.URL, *server.OAuth)
		if removeErr != nil {
			return mcpLoginRemedy(removeErr)
		}
		if !result.LifecycleFound {
			// A missing lifecycle record is the only safe settings-only case.
			_, _ = fmt.Fprintln(stdout, "MCP OAuth lifecycle record not found; removing settings only")
		}
	}
	after, err := permconfig.RemoveDirectMCPServer(before.data, parsed.name)
	if err != nil {
		return err
	}
	if err := writeMCPSettings(path, before, after); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "MCP profile removed locally; no upstream client was revoked.")
	return nil
}
