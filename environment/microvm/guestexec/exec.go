// Package guestexec implements the bounded microVM guest exec data plane.
package guestexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/environment/microvm/control"
	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

const (
	defaultMaxFrameBytes  uint32 = 64 << 10
	defaultMaxOutputBytes int64  = 4 << 20
	defaultMaxConcurrent         = 8
	defaultCancelGrace           = time.Second
)

var (
	// ErrUnauthenticated reports an invalid, stale, or replayed endpoint credential.
	ErrUnauthenticated = errors.New("microvm exec endpoint is not authenticated")
	// ErrTransport distinguishes a broken data-plane channel from a guest command exit.
	ErrTransport = errors.New("microvm exec transport fault")
	// ErrOutputOverflow reports that the guest command exceeded its output budget.
	ErrOutputOverflow = errors.New("microvm exec output exceeds bound")
	// ErrConcurrencyLimit reports that the environment's exec slots are occupied.
	ErrConcurrencyLimit = errors.New("microvm exec concurrency limit reached")
)

// CommandResult is the engine command result implemented by Runner.
type CommandResult = tool.CommandResult

// Limits are immutable bounds applied independently by both protocol peers.
type Limits struct {
	MaxFrameBytes  uint32
	MaxOutputBytes int64
	MaxConcurrent  int
	CancelGrace    time.Duration
}

func (l Limits) normalized() Limits {
	if l.MaxFrameBytes == 0 {
		l.MaxFrameBytes = defaultMaxFrameBytes
	}
	if l.MaxOutputBytes == 0 {
		l.MaxOutputBytes = defaultMaxOutputBytes
	}
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = defaultMaxConcurrent
	}
	if l.CancelGrace == 0 {
		l.CancelGrace = defaultCancelGrace
	}
	return l
}

func (l Limits) validate() error {
	if l.MaxFrameBytes < 256 || l.MaxFrameBytes > control.DefaultMaxMessageBytes || l.MaxOutputBytes <= 0 || l.MaxConcurrent <= 0 || l.CancelGrace <= 0 {
		return errors.New("invalid microvm exec limits")
	}
	return nil
}

func validBinding(b control.Binding) bool {
	return b.Owner != "" && b.SessionID != "" && b.EnvironmentID != "" && b.Ref != "" && b.Generation != 0
}

// RunnerConfig binds a runner to exactly one environment generation.
type RunnerConfig struct {
	Binding control.Binding
	Client  *control.Client
	Limits  Limits
}

// Runner implements the engine's bound CommandRunner and optional CommandStreamer.
type Runner struct {
	client     *control.Client
	limits     Limits
	authorized bool
}

var (
	_ tool.CommandTemporaryScopeRunner   = (*Runner)(nil)
	_ tool.CommandTemporaryScopeStreamer = (*Runner)(nil)
)

// NewRunner constructs a guest-only bound runner. No error path has a local execution fallback.
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	limits := cfg.Limits.normalized()
	if !validBinding(cfg.Binding) || cfg.Client == nil {
		return nil, errors.New("invalid microvm exec runner configuration")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	return &Runner{client: cfg.Client, limits: limits, authorized: cfg.Client.IsBoundTo(cfg.Binding)}, nil
}

// OutputFrame is one globally ordered guest output chunk.
type OutputFrame struct {
	Channel string
	Data    []byte
}

// Run captures distinct bounded stdout and stderr channels.
func (r *Runner) Run(ctx context.Context, command string) (tool.CommandResult, error) {
	return r.runResult(ctx, command, "")
}

// RunWithTemporaryScope runs with the selected guest-owned temporary storage.
func (r *Runner) RunWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope) (tool.CommandResult, error) {
	return r.runResult(ctx, command, scope)
}

func (r *Runner) runResult(ctx context.Context, command string, scope tool.TemporaryScope) (tool.CommandResult, error) {
	var stdout, stderr limitedBuffer
	stdout.remaining = r.limits.MaxOutputBytes
	stderr.remaining = r.limits.MaxOutputBytes
	exit, err := r.run(ctx, command, scope, func(frame OutputFrame) error {
		var sink io.Writer
		switch frame.Channel {
		case channelStdout:
			sink = &stdout
		case channelStderr:
			sink = &stderr
		default:
			return control.ErrMalformedFrame
		}
		_, writeErr := sink.Write(frame.Data)
		return writeErr
	})
	return tool.CommandResult{Stdout: string(stdout.bytes), Stderr: string(stderr.bytes), ExitCode: exit}, err
}

// RunStreaming writes stdout and stderr frames to out in guest delivery order.
func (r *Runner) RunStreaming(ctx context.Context, command string, out io.Writer) (int, error) {
	return r.run(ctx, command, "", func(frame OutputFrame) error {
		n, err := out.Write(frame.Data)
		if err == nil && n != len(frame.Data) {
			err = io.ErrShortWrite
		}
		return err
	})
}

// RunStreamingWithTemporaryScope streams with the selected guest-owned temporary storage.
func (r *Runner) RunStreamingWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope, out io.Writer) (int, error) {
	return r.run(ctx, command, scope, func(frame OutputFrame) error {
		n, err := out.Write(frame.Data)
		if err == nil && n != len(frame.Data) {
			err = io.ErrShortWrite
		}
		return err
	})
}

// RunFrames delivers stdout and stderr chunks with their channel in global guest order.
func (r *Runner) RunFrames(ctx context.Context, command string, receive func(OutputFrame) error) (int, error) {
	return r.RunFramesWithTemporaryScope(ctx, command, "", receive)
}

// RunFramesWithTemporaryScope preserves channel framing with the selected guest-owned temporary storage.
func (r *Runner) RunFramesWithTemporaryScope(ctx context.Context, command string, scope tool.TemporaryScope, receive func(OutputFrame) error) (int, error) {
	if receive == nil {
		return 0, errors.New("microvm exec frame receiver is nil")
	}
	return r.run(ctx, command, scope, receive)
}

func (r *Runner) run(ctx context.Context, command string, scope tool.TemporaryScope, receive func(OutputFrame) error) (int, error) {
	if !r.authorized {
		return 0, ErrUnauthenticated
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var received int64
	var final responseFrame
	decodeFrame := func(payload json.RawMessage) error {
		var response responseFrame
		if err := json.Unmarshal(payload, &response); err != nil {
			return control.ErrMalformedFrame
		}
		if response.Type != frameOutput {
			return control.ErrMalformedFrame
		}
		received += int64(len(response.Data))
		if received > r.limits.MaxOutputBytes {
			return ErrOutputOverflow
		}
		if response.Channel != channelStdout && response.Channel != channelStderr {
			return control.ErrMalformedFrame
		}
		if err := receive(OutputFrame{Channel: response.Channel, Data: response.Data}); err != nil {
			return fmt.Errorf("%w: consume guest output: %v", ErrTransport, err)
		}
		return nil
	}
	err := r.client.Stream(ctx, control.ServiceExec, "run", requestFrame{Command: command, TemporaryScope: scope}, decodeFrame, &final)
	if err != nil {
		var remote *control.RemoteError
		if errors.As(err, &remote) {
			return final.ExitCode, responseError(ctx, remote.Code)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return final.ExitCode, ctxErr
		}
		if errors.Is(err, control.ErrFrameTooLarge) || errors.Is(err, control.ErrMalformedFrame) || errors.Is(err, ErrOutputOverflow) {
			return final.ExitCode, err
		}
		return final.ExitCode, fmt.Errorf("%w: guest request: %v", ErrTransport, err)
	}
	if final.Type != frameExit {
		return final.ExitCode, control.ErrMalformedFrame
	}
	return final.ExitCode, nil
}

func responseError(ctx context.Context, code string) error {
	switch code {
	case codeUnauthenticated:
		return ErrUnauthenticated
	case codeOutputOverflow:
		return ErrOutputOverflow
	case codeConcurrency:
		return ErrConcurrencyLimit
	case codeCancelled:
		if err := ctx.Err(); err != nil {
			return err
		}
		return context.Canceled
	default:
		return fmt.Errorf("%w: guest error %q", ErrTransport, code)
	}
}

// WorkloadIdentity is the dedicated guest UID/GID used for model-controlled commands.
type WorkloadIdentity struct {
	UID uint32
	GID uint32
}

const workloadID uint32 = 65532

// RuntimeContract is the explicit process environment restored when a rootfs is
// supplied directly and therefore carries no OCI configuration.
type RuntimeContract struct {
	Identity    WorkloadIdentity
	Home        string
	Path        string
	Workdir     string
	Environment map[string]string
}

// DefaultRuntimeContract returns the repository VM's fixed workload contract.
func DefaultRuntimeContract() RuntimeContract {
	const home = "/home/guest"
	return RuntimeContract{
		Identity: DefaultWorkloadIdentity(),
		Home:     home,
		Path:     "/usr/lib/go/bin:" + home + "/go/bin:" + home + "/.local/bin:" + home + "/.cargo/bin:/usr/local/bin:/usr/bin:/bin",
		Workdir:  worktree.GuestWorkspace,
		Environment: map[string]string{
			"GOCACHE":          home + "/.cache/go-build",
			"GOMODCACHE":       home + "/go/pkg/mod",
			"PIP_CACHE_DIR":    home + "/.cache/pip",
			"npm_config_cache": home + "/.cache/node",
			"CARGO_HOME":       home + "/.cargo",
		},
	}
}

// CacheDirectories returns the declared writable cache roots in stable order.
func (c RuntimeContract) CacheDirectories() []string {
	result := make([]string, 0, len(c.Environment))
	for _, name := range []string{"CARGO_HOME", "GOCACHE", "GOMODCACHE", "PIP_CACHE_DIR", "npm_config_cache"} {
		if path := c.Environment[name]; path != "" {
			result = append(result, path)
		}
	}
	return result
}

func (c RuntimeContract) commandEnvironment() []string {
	env := []string{"HOME=" + c.Home, "PATH=" + c.Path}
	names := make([]string, 0, len(c.Environment))
	for name := range c.Environment {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		env = append(env, name+"="+c.Environment[name])
	}
	return env
}

// DefaultWorkloadIdentity returns the fixed unprivileged production identity.
func DefaultWorkloadIdentity() WorkloadIdentity {
	return WorkloadIdentity{UID: workloadID, GID: workloadID}
}

// ServerConfig configures a guest endpoint. Production execution is fixed at /workspace.
type ServerConfig struct {
	Binding          control.Binding
	Limits           Limits
	Shell            string
	WorkspaceRoot    string
	GitDirectory     string
	WorkloadIdentity WorkloadIdentity
	RuntimeContract  RuntimeContract
}

// GuestServer executes bounded commands inside the authenticated guest connection.
type GuestServer struct {
	limits   Limits
	shell    string
	root     string
	gitDir   string
	identity WorkloadIdentity
	runtime  RuntimeContract
	slots    chan struct{}
	onStart  func(string)
}

// NewGuestServer constructs the guest exec endpoint.
func NewGuestServer(cfg ServerConfig) (*GuestServer, error) {
	limits := cfg.Limits.normalized()
	if !validBinding(cfg.Binding) {
		return nil, errors.New("microvm exec binding is invalid")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	shell := cfg.Shell
	if shell == "" {
		shell = "/bin/sh"
	}
	root := cfg.WorkspaceRoot
	if root == "" {
		root = worktree.GuestWorkspace
	}
	gitDir := cfg.GitDirectory
	if gitDir == "" {
		gitDir = worktree.GuestMetadata
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(gitDir) {
		return nil, errors.New("microvm exec workspace and Git directory must be absolute")
	}
	identity := cfg.WorkloadIdentity
	if identity.UID == 0 || identity.GID == 0 {
		return nil, errors.New("microvm exec workload identity must be unprivileged")
	}
	runtimeContract := cfg.RuntimeContract
	if runtimeContract.Home == "" && runtimeContract.Path == "" && runtimeContract.Workdir == "" && runtimeContract.Environment == nil {
		runtimeContract = DefaultRuntimeContract()
		runtimeContract.Identity = identity
	}
	if runtimeContract.Identity != identity || runtimeContract.Home == "" || runtimeContract.Path == "" || runtimeContract.Workdir == "" {
		return nil, errors.New("microvm exec runtime contract is incomplete or has the wrong identity")
	}
	return &GuestServer{
		limits: limits, shell: shell, root: root, gitDir: gitDir, identity: identity, runtime: runtimeContract,
		slots: make(chan struct{}, limits.MaxConcurrent),
	}, nil
}

type frameType string

const (
	frameOutput frameType = "output"
	frameExit   frameType = "exit"
	frameError  frameType = "error"

	channelStdout = "stdout"
	channelStderr = "stderr"

	codeUnauthenticated = "unauthenticated"
	codeOutputOverflow  = "output_overflow"
	codeConcurrency     = "concurrency_limit"
	codeCancelled       = "cancelled"
	codeExecution       = "execution_fault"
)

type requestFrame struct {
	Command        string              `json:"command"`
	TemporaryScope tool.TemporaryScope `json:"temporary_scope,omitempty"`
}

type responseFrame struct {
	Type     frameType `json:"type"`
	Channel  string    `json:"channel,omitempty"`
	Data     []byte    `json:"data,omitempty"`
	ExitCode int       `json:"exit_code,omitempty"`
	Code     string    `json:"code,omitempty"`
}

type limitedBuffer struct {
	bytes     []byte
	remaining int64
}

func (b *limitedBuffer) Write(value []byte) (int, error) {
	if int64(len(value)) > b.remaining {
		return 0, ErrOutputOverflow
	}
	b.bytes = append(b.bytes, value...)
	b.remaining -= int64(len(value))
	return len(value), nil
}
