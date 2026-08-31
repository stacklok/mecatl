package main

// Daemon hosting (issue #821, Scenario 8 of docs/acceptance/sdk-server-enablers.md).
//
// This file owns everything a SPAWNED LOCAL DAEMON needs that a network daemon
// does not: a UNIX-domain gRPC listener with no TCP port at all, a disabled HTTP
// surface, an atomically-written ready file the parent polls instead of racing a
// connect loop, and an inherited lifetime pipe whose EOF means "the parent died,
// stop".
//
// The pieces are deliberately small pure-ish helpers rather than inline branches
// in serve(): each is the unit an acceptance criterion names, and serve()'s
// cyclomatic complexity is already at the lint gate's edge.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/internal/cliconfig"
)

// staleSocketDialTimeout bounds the liveness probe in reclaimStaleSocket.
//
// The probe is a local connect(2) on a UNIX socket: it either completes
// immediately or fails immediately. The timeout exists only so a pathological
// peer with a full accept backlog cannot wedge startup forever — it is a
// safety bound, not a tuning knob.
const staleSocketDialTimeout = 500 * time.Millisecond

// socketDirMode is the mode used for socket-directory components this process
// creates. Owner-only, because the directory — not a post-bind chmod — is the
// primary defence for socket permissions (AC8.7): a chmod that follows bind
// leaves a real window in which the socket is connectable by anyone the
// directory admits.
const socketDirMode fs.FileMode = 0o700

// readyFileMode is the mode of the ready file. Owner-only: it names a socket
// path a peer could otherwise discover, and there is no reason for anyone but
// the spawning parent to read it.
const readyFileMode fs.FileMode = 0o600

// transportUnix and transportTCP are the two values the ready file's transport
// field can take. They are OPEN STRINGS on a local file, not an enum on a wire —
// the same discipline the harness uses for feature identifiers and stop reasons.
const (
	transportUnix = "unix"
	transportTCP  = "tcp"
)

// minLifetimePipeFD is the lowest file descriptor accepted for
// --lifetime-pipe-fd. 0/1/2 are stdin/stdout/stderr: a parent that passed one
// of those has made a mistake, and treating stdin's EOF as "the parent died"
// would stop the daemon the moment it was started from a non-interactive shell.
const minLifetimePipeFD = 3

// grpcListener is the bound gRPC listener plus the transport facts the ready
// file and the startup logs report. socketPath is empty for a TCP listener.
type grpcListener struct {
	net.Listener
	transport  string
	socketPath string
}

// validateDaemonHosting is the STARTUP validation for the daemon-hosting flags.
// It is pure (no I/O, no side effects) and runs from validateEffectiveConfig, so
// it sees the POST-merge config and rejects before app.Build or any listener
// binds.
//
// The mutual exclusion in AC8.1 is stated in terms of CONFIGURED TCP gRPC, not
// of a non-empty --grpc-addr: --grpc-addr carries a non-empty default, so
// "--grpc-unix-socket implies no TCP port" has to mean "the default is
// suppressed, an explicit request is a contradiction". Silently ignoring an
// explicit --grpc-addr would leave an operator believing a TCP port was open;
// silently opening one would falsify the no-TCP-port guarantee the socket exists
// to provide.
func validateDaemonHosting(cfg config) error {
	// --ready-file and --lifetime-pipe-fd are deliberately INDEPENDENT of the
	// socket: a supervisor may want a readiness barrier and a parent-death signal
	// on an ordinary TCP daemon too. Only the socket carries an exclusion.
	if cfg.grpcUnixSocket != "" {
		if cfg.tcpGRPCConfigured() {
			return errors.New("--grpc-unix-socket and --grpc-addr are mutually exclusive: the socket serves gRPC and opens no TCP port, so an explicitly configured TCP gRPC address is a contradiction; drop one")
		}
		if err := validateUnixSocketPath(cfg.grpcUnixSocket); err != nil {
			return err
		}
	}
	if cfg.lifetimePipeFD != 0 && cfg.lifetimePipeFD < minLifetimePipeFD {
		return fmt.Errorf("--lifetime-pipe-fd %d is not an inherited pipe: %d/%d/%d are stdin/stdout/stderr; pass the descriptor the parent duplicated (>= %d)",
			cfg.lifetimePipeFD, 0, 1, 2, minLifetimePipeFD)
	}
	if cfg.readyFile != "" && !filepath.IsAbs(cfg.readyFile) {
		return fmt.Errorf("--ready-file %q must be an absolute path: it is written after the listeners bind, when the process working directory is not the parent's concern", cfg.readyFile)
	}
	// --perf-mcp rides the admin listener, which an empty --http-addr disables
	// wholesale (AC8.2). Catch the contradiction here rather than mounting an
	// unreachable /mcp.
	if cfg.perfMCP && cfg.httpAddr == "" {
		return errors.New("--perf-mcp requires the admin listener, which an empty --http-addr disables (the HTTP and metrics listeners are disabled together); drop --perf-mcp or set --http-addr")
	}
	return nil
}

// validateUnixSocketPath rejects a socket path that cannot work, at startup,
// with a message that names the real constraint.
//
// The length check is the one that matters in practice: sockaddr_un.sun_path is
// a FIXED-SIZE character array (104 bytes on Darwin, 108 on Linux), so an
// overlong path fails inside bind(2) as a bare EINVAL — an opaque failure an
// operator cannot act on. Checking it here turns that into a sentence naming the
// path, its length, and the platform limit.
func validateUnixSocketPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("--grpc-unix-socket %q must be an absolute path: a relative socket path depends on the daemon's working directory, which the spawning parent does not control", path)
	}
	if filepath.Clean(path) != path {
		return fmt.Errorf("--grpc-unix-socket %q must be a clean path (no %q, %q, or trailing separator); the cleaned form is %q", path, ".", "..", filepath.Clean(path))
	}
	// The stored pathname needs a terminating NUL, so the usable length is one
	// less than the array size.
	if len(path) >= sunPathMax {
		return fmt.Errorf("--grpc-unix-socket %q is %d bytes; this platform stores at most %d in sockaddr_un.sun_path — use a shorter directory (bind would otherwise fail with an opaque EINVAL)",
			path, len(path), sunPathMax-1)
	}
	return nil
}

// listenGRPC binds the gRPC listener the configuration selects: a UNIX socket
// when --grpc-unix-socket is set, otherwise the historical TCP bind on
// --grpc-addr. It is the SINGLE bind point, so "the socket opens no TCP port" is
// structural rather than a discipline anyone has to remember.
func listenGRPC(cfg config) (grpcListener, error) {
	if cfg.grpcUnixSocket == "" {
		lis, err := net.Listen("tcp", cfg.grpcAddr)
		if err != nil {
			return grpcListener{}, fmt.Errorf("listen grpc %q: %w", cfg.grpcAddr, err)
		}
		return grpcListener{Listener: lis, transport: transportTCP}, nil
	}
	lis, err := listenUnixSocket(cfg.grpcUnixSocket)
	if err != nil {
		return grpcListener{}, err
	}
	return grpcListener{Listener: lis, transport: transportUnix, socketPath: cfg.grpcUnixSocket}, nil
}

// listenUnixSocket prepares and binds an owner-only gRPC socket at path.
//
// The order is load-bearing:
//
//  1. the parent directory is created owner-only (AC8.7) — the socket inherits
//     its reachability from the directory, and a directory created 0700 has no
//     window at all, unlike a chmod that follows bind;
//  2. a STALE socket left by a dead process is removed, while one a LIVE peer is
//     accepting on refuses the start (AC8.6) — unlinking a live peer's socket
//     silently steals its address and is the failure mode worth engineering
//     against;
//  3. the bind itself runs under an owner-only umask so the socket file is never
//     momentarily group/world-reachable;
//  4. the result is VERIFIED, and only then chmod'ed as a last resort, so a
//     platform that ignores umask for sockets still ends up owner-only.
func listenUnixSocket(path string) (net.Listener, error) {
	if err := ensureSocketDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := reclaimStaleSocket(path); err != nil {
		return nil, err
	}
	lis, err := listenUnixOwnerOnly(path)
	if err != nil {
		return nil, fmt.Errorf("listen grpc unix %q: %w", path, err)
	}
	if err := enforceOwnerOnlySocket(path); err != nil {
		_ = lis.Close()
		return nil, err
	}
	return lis, nil
}

// ensureSocketDir makes sure the socket's parent directory exists and is
// owner-only.
//
// A directory this process CREATES is created 0700 (every missing component:
// os.MkdirAll applies the mode to each one it makes). A directory that already
// exists is left ALONE — chmod'ing an operator's /tmp, XDG runtime dir, or
// systemd RuntimeDirectory would be a far worse outcome than the risk it closes
// — but a group/world-reachable one draws a WARN naming the directory, because
// the operator is the only party who can fix it and the socket's own 0600 mode
// is then the sole remaining defence.
func ensureSocketDir(dir string) error {
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if mkErr := os.MkdirAll(dir, socketDirMode); mkErr != nil {
			return fmt.Errorf("create socket directory %q: %w", dir, mkErr)
		}
		return nil
	case err != nil:
		return fmt.Errorf("stat socket directory %q: %w", dir, err)
	case !info.IsDir():
		return fmt.Errorf("socket directory %q exists and is not a directory", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		slog.Warn("gRPC socket directory is reachable beyond its owner; the socket's own 0600 mode is the only remaining restriction — prefer an owner-only directory (0700) for a spawned daemon",
			"dir", dir, "mode", info.Mode().Perm().String())
	}
	return nil
}

// reclaimStaleSocket removes a socket file left behind by a process that died
// without unlinking it, and REFUSES to start when a live peer is still
// accepting on it (AC8.6).
//
// The distinction cannot be made from the filesystem: a UNIX socket inode looks
// identical whether or not anyone is listening. So the test is behavioural — dial
// it. A successful connect proves a live peer; ECONNREFUSED proves the inode has
// no listener and is safe to unlink. Anything that is not a socket at all is
// refused outright rather than removed: silently deleting an operator's regular
// file or directory because a flag pointed at it is not a cleanup, it is data
// loss.
func reclaimStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat socket path %q: %w", path, err)
	}
	if info.Mode()&fs.ModeSocket == 0 {
		return fmt.Errorf("--grpc-unix-socket %q exists and is not a socket (mode %s); refusing to remove it", path, info.Mode().String())
	}
	conn, dialErr := net.DialTimeout("unix", path, staleSocketDialTimeout)
	if dialErr == nil {
		_ = conn.Close()
		return fmt.Errorf("--grpc-unix-socket %q is live: another process is accepting on it. Refusing to start — removing it would silently steal that daemon's address. Stop it, or choose a different socket path", path)
	}
	// The socket vanished between the Lstat and the dial. Nothing to reclaim.
	if errors.Is(dialErr, fs.ErrNotExist) {
		return nil
	}
	// ONLY a refused connect proves the inode has no listener. Every other dial
	// failure — a timeout because the peer's accept backlog is full, EACCES, a
	// descriptor limit — is AMBIGUOUS, and reading ambiguity as "stale" would
	// unlink a live daemon's socket: exactly the outcome this function exists to
	// prevent, reached by the back door. So the ambiguous cases refuse the start.
	if !errors.Is(dialErr, syscall.ECONNREFUSED) {
		return fmt.Errorf("--grpc-unix-socket %q already exists and could not be probed (%v): refusing to start, because removing a socket a live process may still be accepting on would silently steal its address. Remove it yourself if you are sure it is stale", path, dialErr)
	}
	if rmErr := os.Remove(path); rmErr != nil {
		return fmt.Errorf("remove stale socket %q (the connect was refused, so nothing is accepting on it): %w", path, rmErr)
	}
	slog.Info("removed a stale gRPC socket left by a dead process (the connect was refused, so nothing was accepting on it)", "socket", path)
	return nil
}

// enforceOwnerOnlySocket verifies the bound socket is owner-only and, if the
// platform did not honour the bind-time umask, narrows it.
//
// This is the BACKSTOP, not the mechanism: by the time it runs the socket is
// already bound and therefore already connectable, so a chmod here cannot close
// the window it appears to close. The umask in listenUnixOwnerOnly and the 0700
// directory are what actually do that. Keeping the check means a platform whose
// umask does not apply to sockets still converges, and the failure is loud.
func enforceOwnerOnlySocket(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat bound socket %q: %w", path, err)
	}
	if info.Mode().Perm()&0o077 == 0 {
		return nil
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict socket %q to its owner (bound with mode %s): %w", path, info.Mode().Perm().String(), err)
	}
	return nil
}

// readyDoc is the ready file's document (AC8.3).
//
// The field set is a deliberate ALLOWLIST, and the reason it is short is AC8.4:
// the file is the only startup artefact a parent process reads mechanically, so
// anything that lands in it lands in whatever that parent logs, ships, or
// attaches to a bug report. Descriptive fields are therefore sourced from the
// GetCompatibilityInfo projection — already vetted for exposure to an
// authenticated caller — rather than read out of the raw config, where the next
// field someone adds might be a token.
//
// It deliberately omits capabilities, authentication, and TLS detail, matching
// the ADR 0245 privacy boundary that keeps configuration out of GetServerInfo. A
// client that wants the operator-enabled capability set has an RPC for it, and
// it is authenticated; the ready file is not.
type readyDoc struct {
	// Schema names the document format so a parent can fail cleanly on a future
	// version instead of misreading an added field.
	Schema string `json:"schema"`
	// PID is the daemon's process id, so a parent that lost its child handle can
	// still signal it.
	PID int `json:"pid"`
	// Transport is "unix" or "tcp": which listener the gRPC address below names.
	Transport string `json:"transport"`
	// GRPCAddress is the dialable gRPC address — the socket path for "unix", the
	// bound host:port for "tcp". It reports the listener's OWN address, so a
	// ":0" port arrives resolved.
	GRPCAddress string `json:"grpc_address"`
	// SocketPath repeats the socket path as its own field for a parent that
	// wants it without branching on transport. Empty for TCP.
	SocketPath string `json:"socket_path,omitempty"`
	// HTTPAddress is the bound HTTP/SSE address, omitted entirely when the HTTP
	// listener is disabled — absence is the honest signal, not an empty string
	// that reads like a bind to every interface.
	HTTPAddress string `json:"http_address,omitempty"`
	// APIMajor is the wire-contract major from the compatibility projection, so
	// a parent can refuse an incompatible daemon before its first RPC.
	APIMajor int32 `json:"api_major"`
	// Features are the build's feature identifiers, verbatim from the same
	// registry GetCompatibilityInfo serves.
	Features []string `json:"features,omitempty"`
	// Deployment is the operator-set --deployment-id label, empty by default and
	// never derived from the host.
	Deployment string `json:"deployment,omitempty"`
}

// readyDocSchema versions the ready file.
const readyDocSchema = "mecated-ready/1"

// writeReadyFile writes doc to path ATOMICALLY (AC8.3).
//
// Atomic means: a temporary file in the SAME DIRECTORY (so the rename is a
// same-filesystem operation and cannot fail with EXDEV), fully written and
// fsync'ed, then renamed over the target. A parent polling the path therefore
// only ever observes the complete document or nothing at all — never a truncated
// prefix it would have to distinguish from a malformed one.
//
// The temp file is created 0600 and the final file 0600 (readyFileMode): the
// rename preserves the temp's mode, so there is no chmod-after-publish window.
//
// The file is deliberately NEVER REMOVED — not on shutdown, not on error.
// Removing it on a graceful exit but not on a SIGKILL would be a guarantee no
// parent could rely on, so it is not offered at all: a parent must treat the
// file as possibly stale either way, which is what the pid field is for. A
// restart over the same path overwrites it atomically.
func writeReadyFile(path string, doc readyDoc) error {
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ready file: %w", err)
	}
	body = append(body, '\n')

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".mecated-ready-*")
	if err != nil {
		return fmt.Errorf("create ready-file temp in %q: %w", dir, err)
	}
	tmpName := tmp.Name()
	// Any failure from here removes the temp: a leftover dot-file in the
	// parent's runtime directory is litter it never asked for.
	defer func() {
		if tmpName != "" {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(readyFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict ready-file temp %q: %w", tmpName, err)
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write ready-file temp %q: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync ready-file temp %q: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close ready-file temp %q: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("publish ready file %q: %w", path, err)
	}
	tmpName = "" // renamed: nothing left to clean up
	return nil
}

// lifetimePipe is the inherited parent-liveness descriptor. A zero value means
// no pipe was configured and every method is inert.
type lifetimePipe struct {
	file *os.File
	// closed fires (by closing) when the read side sees EOF or an error: the
	// parent is gone.
	closed chan struct{}
}

// openLifetimePipe adopts the inherited descriptor named by --lifetime-pipe-fd
// and starts watching it (AC8.5).
//
// The contract with the parent is the simplest one that survives a CRASH rather
// than only a clean exit: the parent holds the write end and never writes. It
// does not have to remember to signal anything — if it dies for any reason, the
// kernel closes its descriptors, the read end sees EOF, and this daemon stops
// through the ordinary shutdown path. A parent that is merely finished with the
// daemon closes the write end deliberately and gets the same result.
//
// fd 0 means "not configured" and returns an inert value with a nil error.
//
// A descriptor that is NOT actually open is a startup ERROR rather than an
// immediate EOF. The difference matters for anyone debugging it: a bad fd read
// as EOF means "the parent died", so the daemon would start, publish its ready
// file, and vanish milliseconds later with nothing but a WARN — a far harder
// failure to diagnose than a refusal naming the flag.
func openLifetimePipe(fd int) (lifetimePipe, error) {
	if fd == 0 {
		return lifetimePipe{}, nil
	}
	f := os.NewFile(uintptr(fd), fmt.Sprintf("lifetime-pipe-fd-%d", fd))
	if _, err := f.Stat(); err != nil {
		return lifetimePipe{}, fmt.Errorf("--lifetime-pipe-fd %d is not an open descriptor in this process (%w): the parent must pass the pipe's READ end as an inherited fd", fd, err)
	}
	p := lifetimePipe{file: f, closed: make(chan struct{})}
	go p.watch()
	return p, nil
}

// Closed returns the channel that closes when the parent's write end goes away.
// A nil channel (no pipe configured) blocks forever in a select, which is
// exactly the inert behaviour a select case wants.
func (p lifetimePipe) Closed() <-chan struct{} { return p.closed }

// Enabled reports whether a lifetime pipe was configured.
func (p lifetimePipe) Enabled() bool { return p.file != nil }

// watch drains the pipe until EOF or an error.
//
// It READS rather than merely waiting for readability so that a parent which
// does write something (a heartbeat, a stray byte) is tolerated instead of
// spinning. Anything the parent sends is discarded: this descriptor is a
// liveness signal, never a control channel — treating bytes on it as commands
// would hand an unauthenticated local writer a way to steer the daemon.
func (p lifetimePipe) watch() {
	defer close(p.closed)
	buf := make([]byte, 64)
	for {
		n, err := p.file.Read(buf)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				slog.Warn("lifetime pipe read failed; treating it as parent exit", "err", err)
			}
			return
		}
		if n == 0 {
			return
		}
	}
}

// Close releases the descriptor. It does NOT join the watcher goroutine: the
// goroutine is parked in a blocking read, and on a normal shutdown the process
// is about to exit anyway. See the ADR 0027 List 1 row for this resource.
func (p lifetimePipe) Close() {
	if p.file != nil {
		_ = p.file.Close()
	}
}

// listenerIsNetworkBoundary reports whether an API listener puts the harness on
// a network, which is what decides workspace authority (ADR 0237).
//
// The three cases are genuinely different and collapsing them was the bug this
// helper exists to prevent:
//
//   - DISABLED (empty address): not a boundary. There is no listener.
//   - UNIX SOCKET: not a boundary. Reachability is filesystem permission on a
//     path this host owns — strictly narrower than loopback TCP, which any local
//     process may connect to.
//   - TCP: a boundary unless it is loopback, per the existing fail-closed gate.
func listenerIsNetworkBoundary(addr string, unixSocket bool) bool {
	if unixSocket || addr == "" {
		return false
	}
	return !cliconfig.IsLoopbackAddr(addr)
}
