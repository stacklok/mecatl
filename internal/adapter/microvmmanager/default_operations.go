package microvmmanager

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/envscrub"
	microvmclient "github.com/stacklok/mecatl/internal/adapter/microvm"
)

const maxReleaseBundleBytes = 2 << 30

const managedProcessSchema = "mecatl-microvmd-process/v1"

func scrubbedCommand(cmd *exec.Cmd) *exec.Cmd {
	cmd.Env = envscrub.Scrub(os.Environ())
	return cmd
}

type managedProcessRecord struct {
	Schema          string   `json:"schema"`
	PID             int      `json:"pid"`
	ProcessIdentity string   `json:"process_identity"`
	BinaryIdentity  string   `json:"binary_identity"`
	Args            []string `json:"args"`
	Socket          string   `json:"socket"`
}

// DefaultOperations implements the real local OS, release, installer, and
// daemon boundary.
type DefaultOperations struct {
	HTTPClient   *http.Client
	GOOS, GOARCH string
	usernsProbe  func(context.Context) error
}

func (o *DefaultOperations) platform() (string, string) {
	goos, goarch := o.GOOS, o.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

// Preflight checks the current user's platform, required host tools, hypervisor
// access, and ability to create the unprivileged user namespace used at launch.
func (o *DefaultOperations) Preflight(ctx context.Context, _ Paths) error {
	goos, goarch := o.platform()
	if !supportedPlatform(goos, goarch) {
		return fmt.Errorf("unsupported microVM platform %s/%s", goos, goarch)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is required for microVM worktrees")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		return errors.New("python3 is required by the verified microVM release installer; install Python 3 and retry")
	}
	if goos == "linux" {
		probe := o.usernsProbe
		if probe == nil {
			probe = checkLinuxUserNamespaces
		}
		if err := probe(ctx); err != nil {
			return err
		}
		file, err := os.OpenFile("/dev/kvm", os.O_RDWR|syscall.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open /dev/kvm read-write as the current user: %w (fix host access explicitly; the manager will not run sudo or change groups/ACLs)", err)
		}
		return file.Close()
	}
	version, err := scrubbedCommand(exec.CommandContext(ctx, "sw_vers", "-productVersion")).Output()
	if err != nil || darwinMajor(strings.TrimSpace(string(version))) < 15 {
		return errors.New("microVMs require Apple Silicon macOS 15 or newer")
	}
	output, err := scrubbedCommand(exec.CommandContext(ctx, "sysctl", "-n", "kern.hv_support")).Output()
	if err != nil || strings.TrimSpace(string(output)) != "1" {
		return errors.New("hypervisor.framework is unavailable to the current user")
	}
	return nil
}

func supportedPlatform(goos, goarch string) bool {
	return goos == "linux" && goarch == "amd64"
}

func darwinMajor(version string) int {
	major, _ := strconv.Atoi(strings.SplitN(version, ".", 2)[0])
	return major
}

// Download obtains, digest-verifies, and safely extracts one release bundle.
func (o *DefaultOperations) Download(ctx context.Context, release Release, destination string) (string, error) { //nolint:gocyclo // explicit fail-closed download transaction
	if release.bundlePath == "" {
		u, err := url.Parse(release.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return "", errors.New("release URL must be an absolute HTTPS URL without userinfo or fragment")
		}
	}
	if err := secureMkdirAll(destination); err != nil {
		return "", err
	}
	archivePath := filepath.Join(destination, "release.tar.gz")
	if err := refuseSymlink(archivePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	var source io.ReadCloser
	if release.bundlePath != "" {
		if release.bundleFile != nil {
			info, err := release.bundleFile.Stat()
			if err != nil {
				return "", err
			}
			source = io.NopCloser(io.NewSectionReader(release.bundleFile, 0, info.Size()))
		} else {
			file, err := os.Open(release.bundlePath) // #nosec G304 -- compatibility path for package-internal tests; development descriptors provide bundleFile.
			if err != nil {
				return "", err
			}
			source = file
		}
	} else {
		baseClient := o.HTTPClient
		if baseClient == nil {
			baseClient = &http.Client{Timeout: 10 * time.Minute}
		}
		client := *baseClient
		previousRedirect := client.CheckRedirect
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if req.URL.Scheme != "https" || req.URL.Host == "" || req.URL.User != nil || req.URL.Fragment != "" {
				return errors.New("release redirect must remain an absolute HTTPS URL")
			}
			if previousRedirect != nil {
				return previousRedirect(req, via)
			}
			if len(via) >= 10 {
				return errors.New("too many release redirects")
			}
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.URL, nil)
		if err != nil {
			return "", err
		}
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return "", fmt.Errorf("release download returned HTTP %d", resp.StatusCode)
		}
		source = resp.Body
	}
	defer func() { _ = source.Close() }()
	tmp, err := os.OpenFile(archivePath+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hash), io.LimitReader(source, maxReleaseBundleBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		_ = os.Remove(tmp.Name())
		return "", copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp.Name())
		return "", closeErr
	}
	if written > maxReleaseBundleBytes {
		_ = os.Remove(tmp.Name())
		return "", errors.New("release bundle exceeds size limit")
	}
	if hex.EncodeToString(hash.Sum(nil)) != release.SHA256 {
		_ = os.Remove(tmp.Name())
		return "", errors.New("release bundle SHA-256 mismatch")
	}
	if err := os.Rename(tmp.Name(), archivePath); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	unpacked := filepath.Join(destination, "unpacked")
	if err := os.RemoveAll(unpacked); err != nil {
		return "", err
	}
	if err := secureMkdirAll(unpacked); err != nil {
		return "", err
	}
	if err := extractReleaseBundle(archivePath, unpacked); err != nil {
		_ = os.RemoveAll(unpacked)
		return "", err
	}
	goos, goarch := o.platform()
	manifest := filepath.Join(unpacked, "microvm-release-"+goos+"-"+goarch+".json")
	if !regularFile(manifest) {
		_ = os.RemoveAll(unpacked)
		return "", errors.New("release bundle omitted the platform manifest")
	}
	return manifest, nil
}

func extractReleaseBundle(archivePath, destination string) error { //nolint:gocyclo // archive safety checks remain explicit
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(zr)
	count := 0
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		count++
		if count > 10000 {
			return errors.New("release bundle has too many entries")
		}
		name := filepath.Clean(filepath.FromSlash(header.Name))
		if name == "." || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe release bundle path %q", header.Name)
		}
		target := filepath.Join(destination, name)
		if !strings.HasPrefix(target, destination+string(filepath.Separator)) {
			return fmt.Errorf("unsafe release bundle path %q", header.Name)
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := secureMkdirAll(target); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Mode < 0 || header.Mode > 0o777 {
				return fmt.Errorf("invalid release bundle mode for %s", header.Name)
			}
			if err := secureMkdirAll(filepath.Dir(target)); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(header.Mode)&0o700) // #nosec G115 -- bounded to Unix permission bits above.
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(out, tr, header.Size)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("release bundle symlink or hard link is forbidden: %s", header.Name)
		default:
			return fmt.Errorf("unsupported release bundle entry type for %s", header.Name)
		}
	}
}

// Verify rechecks that the extracted manifest still belongs to the verified bundle.
func (*DefaultOperations) Verify(_ context.Context, release Release, manifest string) error {
	archive := filepath.Join(filepath.Dir(filepath.Dir(manifest)), "release.tar.gz")
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxReleaseBundleBytes+1)); err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != release.SHA256 {
		return errors.New("downloaded release changed after bootstrap verification")
	}
	return nil
}

// Install executes the installer from the verified bundle.
func (o *DefaultOperations) Install(ctx context.Context, manifest, installRoot string) (InstalledArtifacts, error) {
	installerData, err := readBundleInstaller(manifest)
	if err != nil {
		return InstalledArtifacts{}, err
	}
	installer := filepath.Join(filepath.Dir(manifest), ".bundle-installer")
	if err := atomicWriteMode(installer, installerData, 0o700); err != nil {
		return InstalledArtifacts{}, fmt.Errorf("materialize verified installer: %w", err)
	}
	cmd := scrubbedCommand(exec.CommandContext(ctx, installer, manifest, installRoot)) // #nosec G204 -- executable and each argument are distinct verified paths.
	if output, err := cmd.CombinedOutput(); err != nil {
		return InstalledArtifacts{}, fmt.Errorf("release installer: %w: %s", err, strings.TrimSpace(string(output)))
	}
	data, err := os.ReadFile(filepath.Join(installRoot, "microvmd-artifacts.json"))
	if err != nil {
		return InstalledArtifacts{}, err
	}
	var installed InstalledArtifacts
	if err := json.Unmarshal(data, &installed); err != nil {
		return InstalledArtifacts{}, err
	}
	if installed.Schema != "mecatl-microvmd-artifacts/v1" {
		return InstalledArtifacts{}, errors.New("release installer returned unsupported artifact schema")
	}
	goos, goarch := o.platform()
	source := filepath.Join(filepath.Dir(manifest), "mecatl-microvmd-"+goos+"-"+goarch)
	target := filepath.Join(filepath.Dir(installRoot), "bin", "mecatl-microvmd")
	if err := copyVerifiedExecutable(source, target); err != nil {
		return InstalledArtifacts{}, fmt.Errorf("install verified microvmd binary: %w", err)
	}
	return installed, nil
}

func readBundleInstaller(manifest string) ([]byte, error) {
	archivePath := filepath.Join(filepath.Dir(filepath.Dir(manifest)), "release.tar.gz")
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open verified release bundle: %w", err)
	}
	defer func() { _ = file.Close() }()
	zr, err := gzip.NewReader(file)
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	tr := tar.NewReader(zr)
	for count := 0; count < 10000; count++ {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if filepath.Clean(filepath.FromSlash(header.Name)) != "install-microvm-release.sh" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 16<<20 {
			return nil, errors.New("verified release bundle installer is not a bounded regular file")
		}
		data, err := io.ReadAll(io.LimitReader(tr, header.Size+1))
		if err != nil {
			return nil, err
		}
		if int64(len(data)) != header.Size {
			return nil, errors.New("verified release bundle installer is truncated")
		}
		return data, nil
	}
	return nil, errors.New("verified release bundle omitted its installer")
}

func copyVerifiedExecutable(source, target string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 256<<20 {
		return errors.New("verified microvmd binary is not a bounded regular file")
	}
	if err := secureMkdirAll(filepath.Dir(target)); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.OpenFile(target+".tmp", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700) // #nosec G302 -- installed executable is owner-only.
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	written, copyErr := io.Copy(tmp, io.LimitReader(in, 256<<20+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written > 256<<20 {
		return errors.New("verified microvmd binary exceeds size limit")
	}
	return os.Rename(tmp.Name(), target)
}

// Running reports whether an owner-only socket accepts a local connection.
func (*DefaultOperations) Running(ctx context.Context, paths Paths) (bool, error) {
	if err := validateOwnerSocket(paths.Socket); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	dialer := net.Dialer{Timeout: time.Second}
	conn, err := dialer.DialContext(ctx, "unix", paths.Socket)
	if err != nil {
		return false, nil
	}
	_ = conn.Close()
	return true, nil
}

// DaemonInfo queries the process serving the owner-only socket.
func (*DefaultOperations) DaemonInfo(ctx context.Context, paths Paths) (DaemonInfo, error) {
	if err := validateOwnerSocket(paths.Socket); err != nil {
		return DaemonInfo{}, err
	}
	client, err := microvmclient.New("unix://" + paths.Socket)
	if err != nil {
		return DaemonInfo{}, err
	}
	return client.DaemonInfo(ctx)
}

// Start launches the installed daemon with absolute manager paths.
func (*DefaultOperations) Start(_ context.Context, paths Paths) error {
	if !regularFile(paths.DaemonBinary) {
		return errors.New("verified mecatl-microvmd binary is missing")
	}
	log, err := os.OpenFile(filepath.Join(paths.StateDir, "microvmd.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	args := []string{"--state-dir", paths.StateDir, "--socket", paths.Socket, "--config", paths.ConfigFile}
	cmd := scrubbedCommand(exec.Command(paths.DaemonBinary, args...)) // #nosec G204 -- absolute manager-owned executable and separate fixed arguments.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		_ = log.Close()
		return err
	}
	pid := []byte(strconv.Itoa(cmd.Process.Pid) + "\n")
	if err := atomicWrite(filepath.Join(paths.StateDir, "microvmd.pid"), pid); err != nil {
		_ = cmd.Process.Kill()
		_ = log.Close()
		return err
	}
	startIdentity, err := processStartIdentity(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = log.Close()
		return fmt.Errorf("record microvmd process-start identity: %w", err)
	}
	binaryIdentity, err := fileSHA256(paths.DaemonBinary)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = log.Close()
		return err
	}
	record, err := json.Marshal(managedProcessRecord{Schema: managedProcessSchema, PID: cmd.Process.Pid, ProcessIdentity: startIdentity, BinaryIdentity: binaryIdentity, Args: append([]string{paths.DaemonBinary}, args...), Socket: paths.Socket})
	if err != nil {
		_ = cmd.Process.Kill()
		_ = log.Close()
		return err
	}
	if err := atomicWrite(filepath.Join(paths.StateDir, "microvmd.process.json"), append(record, '\n')); err != nil {
		_ = cmd.Process.Kill()
		_ = log.Close()
		return err
	}
	_ = cmd.Process.Release()
	return log.Close()
}

// WaitSocket waits until the daemon publishes an owner-only Unix socket.
func (*DefaultOperations) WaitSocket(ctx context.Context, socket string) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	dialer := net.Dialer{Timeout: 250 * time.Millisecond}
	for {
		if err := validateOwnerSocket(socket); err == nil {
			conn, dialErr := dialer.DialContext(ctx, "unix", socket)
			if dialErr == nil {
				_ = conn.Close()
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Doctor verifies the identity of the daemon actually serving the configured socket
// and returns its structured, line-oriented readiness report.
func (o *DefaultOperations) Doctor(ctx context.Context, paths Paths) (string, error) {
	expected, err := expectedDaemonInfo(paths)
	if err != nil {
		return "", fmt.Errorf("compute expected microvmd identity: %w", err)
	}
	serving, err := o.DaemonInfo(ctx, paths)
	if err != nil {
		return "", fmt.Errorf("query serving microvmd identity: %w", err)
	}
	if !serving.Equal(expected) {
		return "", errors.New("serving microvmd identity does not match installed release, binary, policy, profiles, config, and socket")
	}
	cmd := scrubbedCommand(exec.CommandContext(ctx, paths.DaemonBinary, "--doctor", "--state-dir", paths.StateDir, "--socket", paths.Socket, "--config", paths.ConfigFile)) // #nosec G204 -- exact identity was authenticated above; arguments are fixed manager paths.
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("microvmd readiness checks: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

// Stop signals only the manager-recorded daemon process.
func (*DefaultOperations) Stop(ctx context.Context, paths Paths) error {
	data, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.pid"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return errors.New("invalid managed microvmd pid")
	}
	if err := validateOwnerSocket(paths.Socket); err != nil {
		return fmt.Errorf("refusing to signal recorded daemon without its owner-only socket: %w", err)
	}
	recordData, err := os.ReadFile(filepath.Join(paths.StateDir, "microvmd.process.json"))
	if err != nil {
		return fmt.Errorf("managed microvmd process identity is unavailable; use the user service manager and do not signal the recorded PID: %w", err)
	}
	var record managedProcessRecord
	if err := json.Unmarshal(recordData, &record); err != nil || record.Schema != managedProcessSchema || record.PID != pid {
		return errors.New("managed microvmd process identity is invalid; use the user service manager and do not signal the recorded PID")
	}
	handle, err := openManagedProcess(pid)
	if err != nil {
		return fmt.Errorf("open exact managed microvmd process: %w", err)
	}
	defer func() { _ = handle.close() }()
	if err := validateManagedProcess(record, paths); err != nil {
		return err
	}
	if err := handle.signal(); err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(paths.Socket); errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for microvmd shutdown: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func validateManagedProcess(record managedProcessRecord, paths Paths) error { //nolint:gocyclo // fail-closed cross-platform identity validation is intentionally explicit
	if record.Socket != paths.Socket || len(record.Args) != 7 || record.Args[0] != paths.DaemonBinary || record.Args[1] != "--state-dir" || record.Args[2] != paths.StateDir || record.Args[3] != "--socket" || record.Args[4] != paths.Socket || record.Args[5] != "--config" || record.Args[6] != paths.ConfigFile {
		return errors.New("recorded microvmd launch arguments or socket do not match this manager")
	}
	identity, err := processStartIdentity(record.PID)
	if err != nil {
		return fmt.Errorf("read managed daemon process-start identity: %w", err)
	}
	if identity != record.ProcessIdentity {
		return errors.New("recorded microvmd PID was reused by a different process start")
	}
	if runtime.GOOS == "linux" {
		runningPath := filepath.Join("/proc", strconv.Itoa(record.PID), "exe")
		running, err := os.Readlink(runningPath)
		if err != nil {
			return fmt.Errorf("read managed daemon process identity: %w", err)
		}
		expected, err := filepath.EvalSymlinks(paths.DaemonBinary)
		if err != nil {
			return fmt.Errorf("resolve installed daemon identity: %w", err)
		}
		if strings.TrimSuffix(running, " (deleted)") != expected {
			return errors.New("recorded microvmd PID belongs to a different executable")
		}
		binaryIdentity, err := fileSHA256(runningPath)
		if err != nil || binaryIdentity != record.BinaryIdentity {
			return errors.New("recorded microvmd process has a different binary identity")
		}
		args, err := processArgs(record.PID)
		if err != nil || !equalStrings(args, record.Args) {
			return errors.New("recorded microvmd process has different launch arguments")
		}
		return nil
	}
	if runtime.GOOS == "darwin" {
		output, err := scrubbedCommand(exec.Command("ps", "-p", strconv.Itoa(record.PID), "-o", "command=")).Output()
		if err != nil {
			return fmt.Errorf("read managed daemon process identity: %w", err)
		}
		command := strings.Fields(string(output))
		if !equalStrings(command, record.Args) {
			return errors.New("recorded microvmd process has different launch arguments")
		}
		return nil
	}
	return errors.New("managed daemon stop is unsupported on this platform")
}

func processStartIdentity(pid int) (string, error) {
	if runtime.GOOS == "linux" {
		data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if err != nil {
			return "", err
		}
		closeParen := strings.LastIndexByte(string(data), ')')
		if closeParen < 0 {
			return "", errors.New("malformed process stat")
		}
		fields := strings.Fields(string(data[closeParen+1:]))
		if len(fields) <= 19 {
			return "", errors.New("process stat omits start time")
		}
		bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(bootID)) + ":" + fields[19], nil
	}
	if runtime.GOOS == "darwin" {
		output, err := scrubbedCommand(exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "lstart=")).Output()
		if err != nil {
			return "", err
		}
		identity := strings.Join(strings.Fields(string(output)), " ")
		if identity == "" {
			return "", errors.New("process start identity is empty")
		}
		return identity, nil
	}
	return "", errors.New("process start identity is unsupported on this platform")
}

func processArgs(pid int) ([]string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil, errors.New("process arguments are empty")
	}
	return parts, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func validateOwnerSocket(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return errors.New("microvmd endpoint is not a Unix socket")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return errors.New("microvmd socket is not owner-only")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("microvmd socket is not owned by the current user")
	}
	return nil
}
