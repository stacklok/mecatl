//go:build e2e

package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// Local spawns ./bin/mecated as a subprocess with fully ephemeral state dirs
// under <repo>/.scratch/e2e-<timestamp>-<rand>/ (HOME, XDG_*, workspace, store, memory,
// user-model, soul, skills fixtures) and loopback TCP listeners on
// harness-picked free ports. Its combined stdout+stderr is captured to the
// artifact dir (mecated.log) — the suite's failure reports and the
// soul-diagnostic spec read it.
//
// LINUX-ONLY: the spawn sets SysProcAttr.Pdeathsig (SIGTERM on parent death),
// so a hard-killed test runner (go test panic, SIGKILL, OOM) takes mecated
// with it instead of orphaning it. Pdeathsig exists only on Linux; the live
// e2e harness is deliberately Linux-only because of it.
//
// mecated has no UNIX-socket listen mode (the TUI's embedded server wires the
// in-process service over a UDS itself; the BINARY listens TCP only — verified
// in cmd/mecated/main.go serve()), so the harness mirrors the daemon reality:
// loopback TCP on free ports. The gRPC/HTTP/metrics ports are picked by binding
// :0, reading the port, and closing — racy in principle, loopback-private in
// practice.
type Local struct {
	Root string // .scratch/e2e-<timestamp>-<rand>

	grpcAddr    string
	httpAddr    string
	metricsAddr string

	cmd     *exec.Cmd
	logPath string // combined stdout+stderr capture (mecated.log)
	logFile *os.File

	// exited is closed by the Wait goroutine started at spawn; after the close,
	// cmd.ProcessState is set. It is the single Wait owner — waitReady's
	// exited-early fast-fail and Close's bounded shutdown both select on it.
	exited chan struct{}

	// extraArgs are appended to mecated's flag list at spawn, AFTER the standard
	// args — for scenario-specific overrides like --websearch=off. They append, so
	// a later flag wins over an earlier same-named one (Go's flag pkg: last wins).
	extraArgs []string

	// stateRoot is the scratch root whose --store-dir / --memory-dir / --workspace
	// this spawn uses. It is Root for an ordinary spawn, and the PRIOR Local's Root
	// for a shared-store second spawn (NewLocalSharingStore): the restart leg of
	// the cloud-native Phase 2 scenario needs a second mecated reading the first's
	// durable store. Home/XDG/artifacts always live under this spawn's own Root.
	stateRoot string

	cli *client.Client
}

// NewLocal builds the fixture tree, spawns mecated, waits for readiness, and
// returns the connected target.
func NewLocal() (*Local, error) {
	return NewLocalWith()
}

// NewLocalWith is NewLocal with extra mecated flags appended (after the standard
// args) — for scenarios that need a per-server override the shared suite target
// cannot provide, e.g. the WebSearch kill switch (`--websearch=off`). Each call
// spawns its OWN mecated against its OWN scratch tree; the caller owns Close.
func NewLocalWith(extraArgs ...string) (*Local, error) {
	root, err := newScratchRoot()
	if err != nil {
		return nil, err
	}
	l := &Local{Root: root, stateRoot: root, extraArgs: extraArgs}
	if err := l.start(); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// NewLocalSharingStore spawns a SECOND mecated whose --store-dir / --memory-dir /
// --workspace point at an EXISTING (prior) Local's state tree, while keeping its
// OWN home/XDG/artifacts under a fresh scratch root. It is the restart leg of the
// cloud-native Phase 2 scenario: after prior.Kill() (a SIGKILL that leaves the
// store on disk), this spawn reads the prior's durable awaiting snapshot and can
// resume it. Extra mecated flags append last, exactly like NewLocalWith.
//
// Normally the prior MUST already be dead (Kill) before this is called — two
// live mecateds over the same JSONL store would race writes. The single
// exception is a deployment that wires a session lease (cloud-native Phase 4):
// then two live spawns over one store are SAFE precisely because the lease
// enforces single-writer (the lease-exclusion spec relies on this). The caller
// owns Close on the returned Local.
//
// It binds the new spawn's state tree to the prior's SHARED tree (prior.stateRoot),
// NOT prior.Root — so chaining (bootstrap → A → B) keeps every spawn on the SAME
// store. Using prior.Root would point a third spawn at the second's OWN (empty)
// root; for an ordinary first spawn prior.stateRoot == prior.Root, so 2-process
// callers are unaffected.
func NewLocalSharingStore(prior *Local, extraArgs ...string) (*Local, error) {
	root, err := newScratchRoot()
	if err != nil {
		return nil, err
	}
	l := &Local{Root: root, stateRoot: prior.stateRoot, extraArgs: extraArgs}
	if err := l.start(); err != nil {
		_ = l.Close()
		return nil, err
	}
	return l, nil
}

// newScratchRoot creates <repo>/.scratch/e2e-<timestamp>-<rand> (the repo-local scratch
// area; never /tmp — house rule).
func newScratchRoot() (string, error) {
	repo, err := RepoRoot()
	if err != nil {
		return "", err
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	root := filepath.Join(repo, ".scratch", "e2e-"+time.Now().Format("20060102-150405")+"-"+hex.EncodeToString(b[:]))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

// RepoRoot walks up from the working directory to the directory containing
// go.mod (go test runs with cwd = the package dir, e2e/).
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate the repo root (no go.mod above the test working dir)")
		}
		dir = parent
	}
}

func (l *Local) dir(parts ...string) string {
	return filepath.Join(append([]string{l.Root}, parts...)...)
}

// stateDir resolves a path under the SHARED state tree (l.stateRoot) — the
// --store-dir / --memory-dir / --workspace lane. For an ordinary spawn stateRoot
// == Root, so it is identical to dir(); for a NewLocalSharingStore second spawn
// it points at the prior Local's tree.
func (l *Local) stateDir(parts ...string) string {
	return filepath.Join(append([]string{l.stateRoot}, parts...)...)
}

// sharesState reports whether this spawn reuses a prior Local's state tree (the
// shared-store second spawn). When true, start() must NOT re-write fixtures or
// re-init git over the shared workspace — the prior already laid them out and the
// store holds live snapshots.
func (l *Local) sharesState() bool { return l.stateRoot != l.Root }

// start writes fixtures, spawns the daemon, and waits for readiness.
func (l *Local) start() error {
	repo, err := RepoRoot()
	if err != nil {
		return err
	}
	bin := filepath.Join(repo, "bin", "mecated")
	if _, err := os.Stat(bin); err != nil {
		return fmt.Errorf("bin/mecated not found (%v): run `task build` first (task e2e depends on it)", err)
	}

	// Process-private dirs always live under this spawn's OWN Root (home/XDG keep
	// provider detection hermetic; artifacts/log are per-process). The state lane
	// (workspace/store/memory/usermodel/soul) lives under stateRoot — its own Root
	// for an ordinary spawn, the prior Local's Root for a shared-store second
	// spawn.
	for _, d := range []string{"home", "xdg-config", "xdg-state", "xdg-cache", "artifacts"} {
		if err := os.MkdirAll(l.dir(d), 0o755); err != nil {
			return err
		}
	}
	if !l.sharesState() {
		// First spawn over a fresh state tree: create the state dirs, write the
		// fixtures, and git-init the workspace. A shared-store second spawn SKIPS
		// all of this — the prior already laid the tree out and its store holds the
		// live (awaiting) snapshot the restart leg resumes.
		for _, d := range []string{"workspace", "store", "memory", "usermodel", "soul", "lease"} {
			if err := os.MkdirAll(l.stateDir(d), 0o755); err != nil {
				return err
			}
		}
		if err := WriteFixtures(repo, l.stateRoot); err != nil {
			return fmt.Errorf("write fixtures: %w", err)
		}
		if err := initWorkspaceGit(l.stateDir("workspace")); err != nil {
			return fmt.Errorf("git-init workspace: %w", err)
		}
	}

	// Reserve all three ports AT ONCE (holding the listeners open together) so
	// the OS hands back three DISTINCT ports. Allocating them one-at-a-time with
	// a close between calls lets the OS re-assign the same just-freed ephemeral
	// port to the next bind — which collided http==metrics in CI and crashed the
	// daemon at startup ("bind: address already in use"). This is the only spawn
	// path, and the lease spec spawns three daemons per run, widening that window.
	ports, err := freePorts(3)
	if err != nil {
		return err
	}
	l.grpcAddr = "127.0.0.1:" + strconv.Itoa(ports[0])
	l.httpAddr = "127.0.0.1:" + strconv.Itoa(ports[1])
	l.metricsAddr = "127.0.0.1:" + strconv.Itoa(ports[2])

	args := []string{
		"serve",
		"--grpc-addr", l.grpcAddr,
		"--http-addr", l.httpAddr,
		"--metrics-addr", l.metricsAddr,
		"--workspace", l.stateDir("workspace"),
		"--model", DefaultModel(),
		"--store-dir", l.stateDir("store"),
		"--memory-dir", l.stateDir("memory"),
		"--user-model-dir", l.stateDir("usermodel"),
		"--soul-file", l.stateDir("soul", "soul.md"),
		// Skills come from the conventional locations under the FAKE HOME /
		// workspace (fixtures.go laid them out): ~/.claude/skills (user-global
		// lane) and <workspace>/.claude/skills (project lane, trust-gated).
		"--skills-conventional",
		// Honour the project tier (project skills, agent defs, rules, and any
		// project allows) — the workspace is harness-authored, so it is trusted
		// by construction. The rules-untrusted spec overrides this with an
		// appended --trust-project=false (Go's flag pkg: last wins) to prove
		// the project tier is withheld on an untrusted workspace.
		"--trust-project",
		// CLI-scope permission config: allows for the ask-floor tools the
		// scenarios exercise (Skill/Parallel/Team). Everything else keeps the
		// production posture; the driver auto-DENIES unexpected asks.
		"--permission-config", l.stateDir("permissions.yaml"),
		// Budgets: the shared runaway brakes. Reality check (run 1): a single
		// turn's input with the full catalog is ~5-6k tokens, so the originally
		// planned 4000 tripped at the FIRST turn boundary and forced every
		// multi-turn scenario to stop=budget. Raised 20k→50k after live haiku
		// drifted verbose enough (incl. occasional wasted no-progress turns) that
		// multi-turn + cross-restart scenarios (approve-after-kill accumulates
		// usage across the SIGKILL) intermittently tripped the 20k rail. 50k/60k
		// keep a meaningful runaway brake (~8-9 full-catalog turns) while letting
		// the 2-3-turn scenarios end naturally. This value is a safety rail, not a
		// cost control — the scenarios are bounded by their turn count, not the
		// budget, so raising it bills nothing extra.
		"--max-run-tokens", envOr("MECATL_E2E_MAX_RUN_TOKENS", "50000"),
		"--max-team-tokens", envOr("MECATL_E2E_MAX_TEAM_TOKENS", "60000"),
		// Hermeticity: never adopt MCP servers from the developer's running
		// ToolHive workloads.
		"--toolhive=false",
	}
	// Scenario-specific overrides, appended last (Go's flag pkg: last wins).
	args = append(args, l.extraArgs...)

	// One combined stdout+stderr capture — mecated logs to stderr, but stdout
	// is folded in too so nothing the daemon prints is lost.
	l.logPath = l.dir("artifacts", "mecated.log")
	l.logFile, err = os.Create(l.logPath)
	if err != nil {
		return err
	}

	cmd := exec.Command(bin, args...)
	cmd.Stdout = l.logFile
	cmd.Stderr = l.logFile
	// Orphan prevention (Linux-only — see the type doc): if the test runner is
	// hard-killed (go test -timeout panic, SIGKILL), the kernel SIGTERMs mecated.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	// A minimal, explicit environment: the fake HOME/XDG tree, PATH (git, shell),
	// and the OpenRouter key. Deliberately NOT os.Environ(): OPENAI_API_KEY /
	// ANTHROPIC_API_KEY in the developer's shell would flip provider detection.
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + l.dir("home"),
		"XDG_CONFIG_HOME=" + l.dir("xdg-config"),
		"XDG_STATE_HOME=" + l.dir("xdg-state"),
		"XDG_CACHE_HOME=" + l.dir("xdg-cache"),
		"OPENROUTER_API_KEY=" + os.Getenv("OPENROUTER_API_KEY"),
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("spawn mecated: %w", err)
	}
	l.cmd = cmd
	// The ONE Wait call, started at spawn: without it cmd.ProcessState is never
	// populated, so any "did it crash?" check is dead code. waitReady and Close
	// both observe the exit through this channel.
	l.exited = make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(l.exited)
	}()

	cli, err := client.Dial(client.DialConfig{Server: l.grpcAddr})
	if err != nil {
		return err
	}
	l.cli = cli
	if err := l.waitReady(60 * time.Second); err != nil {
		return fmt.Errorf("mecated did not become ready: %w\n--- mecated log tail ---\n%s", err, l.LogTail(4096))
	}
	return nil
}

// waitReady retries CreateSession (the first real RPC; it also exercises the
// provider registry) until it succeeds, then closes the probe session. A
// mecated that exits during readiness fails IMMEDIATELY (the spawn-time Wait
// goroutine closes l.exited and sets ProcessState) instead of burning the
// full timeout.
func (l *Local) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-l.exited:
			return fmt.Errorf("mecated exited during readiness: %v", l.cmd.ProcessState)
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		id, _, _, err := l.cli.CreateSession(ctx, client.ModeFromString("default"), client.ModelSelection{})
		if err == nil {
			_ = l.cli.CloseSession(ctx, id)
			cancel()
			return nil
		}
		cancel()
		lastErr = err
		time.Sleep(250 * time.Millisecond)
	}
	return lastErr
}

// freePorts reserves n distinct free loopback ports. It binds n listeners on
// :0 SIMULTANEOUSLY, reads each assigned port, then releases them all — so the
// OS cannot hand the same ephemeral port to two of them (which it can, and did
// in CI, when ports are allocated one-at-a-time with a close between calls).
// The bind→read→close→hand-to-daemon window is still racy in principle against
// OTHER processes (loopback-private, and FlakeAttempts covers the rare case),
// but it can no longer collide a single daemon's own ports against each other.
func freePorts(n int) ([]int, error) {
	lns := make([]net.Listener, 0, n)
	defer func() {
		for _, ln := range lns {
			_ = ln.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		lns = append(lns, ln)
		ports = append(ports, ln.Addr().(*net.TCPAddr).Port)
	}
	return ports, nil
}

// initWorkspaceGit makes the workspace a real git repo with one commit, so the
// subagent/team/parallel worktree-fork machinery has a base to fork.
func initWorkspaceGit(ws string) error {
	cmds := [][]string{
		{"git", "init", "-q"},
		{"git", "add", "README.md", "FRUIT.txt", "compaction-input.txt", ".claude"},
		{"git", "-c", "user.name=mecatl-e2e", "-c", "user.email=e2e@mecatl.invalid", "commit", "-q", "-m", "e2e fixture workspace"},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = ws
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ws, "GIT_CONFIG_NOSYSTEM=1"}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %v (%s)", c, err, out)
		}
	}
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// --- Target interface ---

func (l *Local) Client() *client.Client { return l.cli }
func (l *Local) Workspace() string      { return l.stateDir("workspace") }
func (l *Local) MetricsURL() string     { return "http://" + l.metricsAddr + "/metrics" }
func (l *Local) IsLocal() bool          { return true }

// HTTPAddr is the host:port the daemon's HTTP/SSE listener is bound to (the
// --http-addr it was spawned with). The HTTP approve client (ApproveOverHTTP)
// posts to it; a shared-store second spawn exposes ITS OWN listener here.
func (l *Local) HTTPAddr() string { return l.httpAddr }

func (l *Local) StateDir(kind StateKind) string {
	switch kind {
	case StateWorkspace:
		// The state lane resolves under the SHARED tree so a side-effect read finds
		// the file no matter which Local (first or shared-store second) is queried.
		return l.stateDir("workspace")
	case StateMemory:
		return l.stateDir("memory")
	case StateUserModel:
		return l.stateDir("usermodel")
	case StateStore:
		return l.stateDir("store")
	case StateLease:
		return l.stateDir("lease")
	case StateArtifacts:
		// Artifacts are PER-PROCESS (this spawn's own log/transcripts).
		return l.dir("artifacts")
	}
	return ""
}

// LogTail returns up to the last n bytes of the captured mecated combined
// stdout+stderr log (mecated.log).
func (l *Local) LogTail(n int) string {
	data, err := os.ReadFile(l.logPath)
	if err != nil {
		return ""
	}
	if len(data) > n {
		data = data[len(data)-n:]
	}
	return string(data)
}

// Kill SIGKILLs the daemon and reaps it (waits on the spawn-time exit channel),
// WITHOUT tearing the state tree down — it is the "disposable process" death the
// cloud-native Phase 2 restart leg needs: a SECOND mecated (NewLocalSharingStore)
// reads this one's durable store after it dies. Unlike Close it does NOT
// SIGTERM-first (no graceful drain — a real abrupt death) and leaves the scratch
// tree fully intact (the store + the awaiting snapshot must survive).
//
// It closes this spawn's own gRPC conn and log file (process-private resources)
// but removes nothing on disk. The exit is observed via the single Wait owner
// (the spawn-time goroutine that closes l.exited).
func (l *Local) Kill() error {
	var errs []error
	if l.cli != nil {
		errs = append(errs, l.cli.Close())
		l.cli = nil
	}
	if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Process.Kill() // SIGKILL: no graceful drain
		<-l.exited               // reap (ProcessState set by the spawn-time Wait)
	}
	if l.logFile != nil {
		errs = append(errs, l.logFile.Close())
		l.logFile = nil
	}
	return errors.Join(errs...)
}

// Close SIGTERMs the daemon, waits (bounded), then SIGKILLs. The scratch tree
// is left in place — the artifacts ARE the deliverable of a failed run. The
// exit is observed via the spawn-time Wait goroutine (the single Wait owner).
// Calling Close after Kill is safe: the conn/log are already closed (nil-guarded)
// and the process is already reaped.
func (l *Local) Close() error {
	var errs []error
	if l.cli != nil {
		errs = append(errs, l.cli.Close())
	}
	if l.cmd != nil && l.cmd.Process != nil {
		_ = l.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-l.exited:
		case <-time.After(10 * time.Second):
			_ = l.cmd.Process.Kill()
			<-l.exited
		}
	}
	if l.logFile != nil {
		errs = append(errs, l.logFile.Close())
	}
	return errors.Join(errs...)
}
