package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/goccy/go-yaml"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/adapter/mcpcredential"
	"github.com/stacklok/mecatl/internal/adapter/mcplifecycle"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

const mcpLifecycleUsage = "mecated mcp {add NAME URL [--file PATH] [--credential-store auto|keyring|file] | list [--file PATH] | remove NAME [--force] [--file PATH]}"

var discoverMCPDirectIssuer = mcp.DiscoverDirectIssuer
var removeMCPOAuthDCR = mcp.RemoveOAuthDCR
var lookupMCPEnvironment = os.LookupEnv

type mcpLifecycleArgs struct {
	name, url, file, custody string
	force                    bool
}

type mcpSettingsSource struct {
	path     string
	snapshot mcpSettingsSnapshot
	config   permconfig.Config
}

func parseMCPLifecycleArgs(command string, args []string) (mcpLifecycleArgs, error) {
	result := mcpLifecycleArgs{custody: "auto"}
	for i := 0; i < len(args); i++ {
		consumed, handled, err := parseMCPOption(args, i, &result)
		if err != nil {
			return mcpLifecycleArgs{}, err
		}
		if handled {
			i += consumed
			continue
		}
		if !parseMCPPositional(command, args[i], &result) {
			return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
		}
	}
	if !validMCPLifecycleArgs(command, result) {
		return mcpLifecycleArgs{}, errors.New(mcpLifecycleUsage)
	}
	return result, nil
}

func parseMCPOption(args []string, index int, result *mcpLifecycleArgs) (int, bool, error) {
	arg := args[index]
	if arg == "--force" {
		if result.force {
			return 0, false, errors.New(mcpLifecycleUsage)
		}
		result.force = true
		return 0, true, nil
	}
	if arg == mcpFileFlag || arg == "--credential-store" {
		if index+1 == len(args) || args[index+1] == "" || strings.HasPrefix(args[index+1], "-") {
			return 0, false, errors.New(mcpLifecycleUsage)
		}
		if arg == mcpFileFlag {
			if result.file != "" {
				return 0, false, errors.New(mcpLifecycleUsage)
			}
			result.file = args[index+1]
		} else {
			result.custody = args[index+1]
		}
		return 1, true, nil
	}
	if value, ok := strings.CutPrefix(arg, "--file="); ok {
		if value == "" || result.file != "" {
			return 0, false, errors.New(mcpLifecycleUsage)
		}
		result.file = value
		return 0, true, nil
	}
	if value, ok := strings.CutPrefix(arg, "--credential-store="); ok {
		if value == "" {
			return 0, false, errors.New(mcpLifecycleUsage)
		}
		result.custody = value
		return 0, true, nil
	}
	return 0, false, nil
}

func parseMCPPositional(command, arg string, result *mcpLifecycleArgs) bool {
	if strings.HasPrefix(arg, "-") {
		return false
	}
	if command == mcpAddCommand && result.name == "" {
		result.name = arg
		return true
	}
	if command == mcpAddCommand && result.url == "" {
		result.url = arg
		return true
	}
	if command == "remove" && result.name == "" {
		result.name = arg
		return true
	}
	return false
}

func validMCPLifecycleArgs(command string, result mcpLifecycleArgs) bool {
	if result.force && command != "remove" {
		return false
	}
	if command == mcpAddCommand && (result.name == "" || result.url == "") {
		return false
	}
	if command == "remove" && result.name == "" {
		return false
	}
	if command == "list" && result.name != "" {
		return false
	}
	if command != mcpAddCommand && result.custody != "auto" {
		return false
	}
	return result.custody == "auto" || result.custody == "keyring" || result.custody == "file"
}

func defaultMCPSettingsFile() (string, error) {
	base := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	if base == "" {
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
	defer func() { _ = file.Close() }()
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
	return mcpSettingsSnapshot{data: data, version: mcpSettingsVersion{exists: true, device: mcpSettingsDevice(st), inode: st.Ino, size: info.Size(), modNanos: info.ModTime().UnixNano()}}, nil //nolint:gosec // Stat_t device and inode values are non-negative OS identities.
}

func mcpSettingsUnchanged(path string, before mcpSettingsSnapshot) bool {
	current, err := readMCPSettings(path)
	return err == nil && sameMCPSettingsVersion(current, before) && string(current.data) == string(before.data)
}

func writeMCPSettings(path string, before mcpSettingsSnapshot, after []byte) error {
	path, canonicalErr := canonicalMCPSettingsPath(path)
	if canonicalErr != nil {
		return errors.New("MCP settings changed while this command was running; retry")
	}
	if !mcpSettingsUnchanged(path, before) {
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
	defer func() { _ = unix.Close(dir) }()
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return errors.New("MCP settings cannot be staged")
	}
	tempName := "." + base + "." + hex.EncodeToString(random[:]) + ".tmp"
	fd, err := unix.Openat(dir, tempName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return errors.New("MCP settings cannot be staged")
	}
	defer func() { _ = unix.Unlinkat(dir, tempName, 0) }()
	if err = writeAllMCPSettings(fd, after); err == nil {
		err = unix.Fsync(fd)
	}
	if closeErr := unix.Close(fd); err == nil {
		err = closeErr
	}
	if err != nil {
		return errors.New("MCP settings cannot be staged")
	}
	if !mcpSettingsUnchanged(path, before) {
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
	return v.exists && mcpSettingsDevice(st) == v.device && st.Ino == v.inode && st.Size == v.size
}

// lockMCPSettings serializes operator-owned lifecycle writes for one target.
func lockMCPSettings(path string) (func(), error) {
	return lockMCPSettingsContext(context.Background(), path)
}

func lockMCPSettingsContext(ctx context.Context, path string) (func(), error) {
	if err := secureMCPSettingsDir(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("MCP settings lock %q cannot be opened", path+".lock")
	}
	dirPath, base := filepath.Dir(path), filepath.Base(path)
	dir, err := unix.Open(dirPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("MCP settings lock %q cannot be opened: %w", path+".lock", err)
	}
	defer func() { _ = unix.Close(dir) }()
	fd, err := unix.Openat(dir, base+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC, 0600)
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Openat(dir, base+".lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, fmt.Errorf("MCP settings lock %q cannot be opened: %w", path+".lock", err)
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || st.Mode&0o077 != 0 { //nolint:gosec // Getuid is the OS-provided non-negative uid used for ownership comparison.
		_ = unix.Close(fd)
		return nil, fmt.Errorf("MCP settings lock %q is unsafe", path+".lock")
	}
	for {
		if err := ctx.Err(); err != nil {
			_ = unix.Close(fd)
			return nil, err
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			_ = unix.Close(fd)
			return nil, fmt.Errorf("MCP settings lock %q cannot be acquired", path+".lock")
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = unix.Close(fd)
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return func() { _ = unix.Close(fd) }, nil
}

func mcpSettingsSources(target string, extra []string) ([]mcpSettingsSource, error) {
	paths := append([]string{target}, extra...)
	if conventional, err := defaultMCPSettingsFile(); err == nil {
		paths = append(paths, conventional)
	}
	seen := make(map[string]struct{}, len(paths))
	var sources []mcpSettingsSource
	for _, raw := range paths {
		path, err := mcpSettingsFile(raw)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		snapshot, err := readMCPSettings(path)
		if err != nil {
			return nil, err
		}
		if !snapshot.version.exists && path != target {
			continue
		}
		var cfg permconfig.Config
		if err := yaml.Unmarshal(snapshot.data, &cfg); err != nil {
			return nil, fmt.Errorf("MCP settings %q is invalid: %w", path, err)
		}
		sources = append(sources, mcpSettingsSource{path: path, snapshot: snapshot, config: cfg})
	}
	return sources, nil
}

func mcpMutationTarget(target string) (mcpSettingsSnapshot, error) {
	sources, err := mcpSettingsSources(target, nil)
	if err != nil {
		return mcpSettingsSnapshot{}, err
	}
	for i, source := range sources {
		if i > 0 && source.config.MCP != nil {
			return mcpSettingsSnapshot{}, fmt.Errorf("MCP settings %q supplies mcp: and would shadow writable target %q", source.path, target)
		}
	}
	return sources[0].snapshot, nil
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

func runMCPAdd(args []string, stdout, stderr io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runMCPAddContext(ctx, args, stdout, stderr)
}

func runMCPAddContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := parseMCPLifecycleArgs(mcpAddCommand, args)
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
	_, _ = fmt.Fprintf(stdout, "MCP settings writable target: %s\n", path)

	// Validate and snapshot the selected target before doing any network work. A
	// typo, unsafe path, or shadowed layered source must not cause discovery (or
	// any other externally visible onboarding activity).
	unlock, err := lockMCPSettingsContext(ctx, path)
	if err != nil {
		return err
	}
	before, err := mcpMutationTarget(path)
	unlock()
	if err != nil {
		return err
	}
	var settings permconfig.Config
	if err := yaml.Unmarshal(before.data, &settings); err != nil {
		return fmt.Errorf("MCP settings %q is invalid: %w", path, err)
	}
	if settings.MCP != nil && settings.MCP.Mode == "broker" {
		// Direct onboarding must reject the mutually exclusive broker authority
		// before discovery, custody selection, or any settings mutation.
		return errors.New("MCP direct onboarding is unavailable when mcp.mode is broker")
	}

	_, _ = fmt.Fprintln(stdout, "MCP onboarding: discovering protected resource")
	discovery, err := discoverMCPDirectIssuer(ctx, parsed.url)
	if err != nil {
		return err
	}

	// Re-read after discovery: the first snapshot is only the preflight. Never
	// publish over a settings change made while discovery was in flight.
	unlock, err = lockMCPSettingsContext(ctx, path)
	if err != nil {
		return err
	}
	defer func() {
		if unlock != nil {
			unlock()
		}
	}()
	before, err = mcpMutationTarget(path)
	if err != nil {
		return err
	}
	issuer := discovery.Issuer
	if err := publishMCPAdd(ctx, path, before, parsed, issuer, stdout); err != nil {
		return err
	}
	unlock()
	unlock = nil
	return runMCPLoginContext(ctx, []string{parsed.name, mcpFileFlag, path}, stdout, stderr)
}

func readMCPConfirmation(ctx context.Context, input io.Reader) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	result := make(chan struct {
		text string
		err  error
	}, 1)
	go func() {
		text, err := bufio.NewReader(input).ReadString('\n')
		result <- struct {
			text string
			err  error
		}{text, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case outcome := <-result:
		if outcome.err != nil && !errors.Is(outcome.err, io.EOF) {
			return "", outcome.err
		}
		return outcome.text, nil
	}
}

func publishMCPAdd(ctx context.Context, path string, before mcpSettingsSnapshot, parsed mcpLifecycleArgs, issuer string, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	configDir := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	stateDir := xdgconfig.UserStateDir(xdgconfig.OSEnv)
	if configDir == "" || stateDir == "" {
		return errors.New("MCP credential path is unavailable")
	}
	credentialRoot := filepath.Join(stateDir, "mecatl", "mcp-credentials")
	keyPath := filepath.Join(configDir, "mecatl", "mcp-credential-key")
	result, err := mcplifecycle.Add(ctx, mcplifecycle.AddRequest{
		Name: parsed.name, URL: parsed.url, Issuer: issuer, Settings: before.data,
		CredentialRoot: credentialRoot, FileKeyPath: keyPath, CredentialStore: parsed.custody,
		Attended: term.IsTerminal(int(os.Stdin.Fd())),
		ConfirmFile: func(confirmCtx context.Context) (bool, error) {
			_, _ = fmt.Fprint(stdout, "MCP keyring unavailable; store the key in a protected file instead? [y/N] ")
			answer, readErr := readMCPConfirmation(confirmCtx, os.Stdin)
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return false, readErr
			}
			return strings.EqualFold(strings.TrimSpace(answer), "y"), nil
		},
		Progress: func(stage string) { _, _ = fmt.Fprintf(stdout, "MCP onboarding: %s\n", stage) },
	})
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeMCPSettings(path, before, result.Settings); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "MCP onboarding: settings saved")
	return nil
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
	sources, err := mcpSettingsSources(path, nil)
	if err != nil {
		return err
	}
	for _, source := range sources {
		_, _ = fmt.Fprintf(stdout, "MCP settings source: %s\n", source.path)
	}
	for _, source := range sources {
		if source.config.MCP == nil {
			continue
		}
		_, _ = fmt.Fprintf(stdout, "MCP settings winner: %s\n", source.path)
		for _, server := range mcplifecycle.List(source.config) {
			_, _ = fmt.Fprintf(stdout, "%s\t%s\t%s\t%s\t%s\n", server.Name, server.URL, server.Kind, server.CredentialStatus, source.path)
		}
		break
	}
	_, _ = fmt.Fprintln(stdout, "Changes affect newly started daemons; existing-daemon activation is unknown.")
	return nil
}

const mcpCredentialStatusUnknown = "unknown"

// mcpCredentialStatus deliberately performs only bounded, non-presenting
// inspection. In particular, it never opens a keyring, reads an environment
// credential, unlocks an encrypted store, or starts OAuth.
func mcpCredentialStatus(server permconfig.MCPServerProfile) string {
	if server.Auth.OAuth == nil {
		if server.Auth.Mode == "none" {
			return "ready"
		}
		return mcpCredentialStatusUnknown
	}
	credentials := server.Auth.OAuth.Credentials
	switch credentials.Mode {
	case "environment":
		return mcpCredentialStatusUnknown // The value is intentionally not read by list.
	case "local":
		if credentials.Local == nil || credentials.Local.Key == nil {
			return mcpCredentialStatusUnknown
		}
		switch mcpcredential.InspectMarker(credentials.Local.Root) {
		case mcpcredential.MarkerMissing:
			return "login required"
		case mcpcredential.MarkerUnavailable:
			return "unavailable"
		case mcpcredential.MarkerLocked:
			return "locked"
		case mcpcredential.MarkerRecovery:
			return "recovery required"
		case mcpcredential.MarkerPresent:
			return mcpCredentialStatusUnknown // Presence of custody metadata is not proof of a grant.
		default:
			return mcpCredentialStatusUnknown
		}
	default:
		return mcpCredentialStatusUnknown
	}
}

func legacyMCPAbandonEligible(data []byte, name string) error {
	var settings permconfig.Config
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return errors.New("MCP profile is not eligible for forced removal")
	}
	if settings.MCP == nil || settings.MCP.Mode == "broker" {
		return errors.New("MCP profile is not eligible for forced removal")
	}
	var selected *permconfig.MCPServerProfile
	for i := range settings.MCP.Servers {
		if strings.EqualFold(settings.MCP.Servers[i].Name, name) {
			if selected != nil {
				return errors.New("MCP profile is not eligible for forced removal")
			}
			selected = &settings.MCP.Servers[i]
		}
	}
	if selected == nil || selected.Auth.Mode != "oauth" || selected.Auth.OAuth == nil {
		return errors.New("MCP profile is not eligible for forced removal")
	}
	oauth := selected.Auth.OAuth
	if oauth.Client.Mode != "dcr" || oauth.Client.DCR == nil || oauth.Client.DCR.DiscoveryURL != "" || oauth.Credentials.Mode != "local" || oauth.Credentials.Local == nil || oauth.Credentials.Local.Key != nil || oauth.Credentials.Local.KeyEnv == "" {
		return errors.New("MCP profile is not eligible for forced removal")
	}
	if value, present := lookupMCPEnvironment(oauth.Credentials.Local.KeyEnv); present && value != "" {
		return errors.New("MCP profile is not eligible for forced removal")
	}
	return nil
}

func runMCPRemove(args []string, stdout io.Writer) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runMCPRemoveContext(ctx, args, stdout)
}

// runMCPForceRemove abandons an eligible legacy direct-DCR profile locally. The
// caller holds the settings lock for the whole call, so before.data cannot change
// underneath it; the eligibility recheck immediately before publication exists to
// catch a custody environment variable that became available in the meantime, not
// a settings-file race (writeMCPSettings independently re-verifies that against
// before before publishing).
func runMCPForceRemove(ctx context.Context, path string, before mcpSettingsSnapshot, name string, stdout io.Writer) error {
	if err := legacyMCPAbandonEligible(before.data, name); err != nil {
		return err
	}
	after, err := mcplifecycle.Remove(before.data, name)
	if err != nil {
		return err
	}
	if err := legacyMCPAbandonEligible(before.data, name); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeMCPSettings(path, before, after); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "MCP profile abandoned locally; encrypted records were retained, no upstream client was revoked, and later re-enrollment may create a new registration.")
	return nil
}

func runMCPRemoveContext(ctx context.Context, args []string, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	parsed, err := parseMCPLifecycleArgs("remove", args)
	if err != nil {
		return err
	}
	path, err := mcpSettingsFile(parsed.file)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "MCP settings writable target: %s\n", path)
	unlock, err := lockMCPSettingsContext(ctx, path)
	if err != nil {
		return err
	}
	defer unlock()
	before, err := mcpMutationTarget(path)
	if err != nil {
		return err
	}
	if parsed.force {
		return runMCPForceRemove(ctx, path, before, parsed.name, stdout)
	}
	profiles, profileErr := loadMCPLoginProfilesSelected([]string{path}, parsed.name)
	if profileErr != nil {
		return profileErr
	}
	defer func() { _ = profiles.Close() }()
	if server, ok := profiles.OAuthServer(parsed.name); ok {
		if server.OAuth == nil || server.OAuth.Client.DCR == nil {
			return errors.New("MCP profile removal is supported only for direct OAuth DCR profiles")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result, removeErr := removeMCPOAuthDCR(ctx, server.URL, *server.OAuth)
		if removeErr != nil {
			return mcpLoginRemedy(removeErr)
		}
		if !result.LifecycleFound {
			// A missing lifecycle record is the only safe settings-only case.
			_, _ = fmt.Fprintln(stdout, "MCP OAuth lifecycle record not found; removing settings only")
		}
	}
	after, err := mcplifecycle.Remove(before.data, parsed.name)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writeMCPSettings(path, before, after); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "MCP profile removed locally; no upstream client was revoked.")
	return nil
}
