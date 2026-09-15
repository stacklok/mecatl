// Package boxenv implements a Mecatl placement provider backed by Ascii Box.
package boxenv

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	// Kind is the durable EnvironmentKind minted for Ascii Box placements.
	Kind session.EnvironmentKind = "box"

	defaultBaseURL  = "https://ascii.dev/api/box/v1"
	providerVersion = "box-api-v1"
	workspaceRoot   = "/workspace"
	boxWorkspace    = "workspace"
	defaultTTL      = 3600
	defaultReady    = 5 * time.Minute
	defaultPoll     = 2 * time.Second
	maxResponseBody = 8 << 20
)

var (
	// ErrInvalidConfig reports an invalid trusted operator configuration.
	ErrInvalidConfig = errors.New("boxenv: invalid config")
	// ErrPathEscape reports a path that is not confined to the Box workspace.
	ErrPathEscape = errors.New("boxenv: path escapes workspace root")
)

// Config configures an Ascii Box placement provider. APIKey is never persisted
// in an EnvironmentRef or forwarded into a Box. By default Boxes are created
// with noEnv=true so account credentials and secrets stay outside the sandbox.
type Config struct {
	APIKey             string
	BaseURL            string
	Scope              server.PlacementScope
	TTLSeconds         int
	InheritEnvironment bool
	HTTPClient         *http.Client
	ReadyTimeout       time.Duration
	PollInterval       time.Duration
}

// Provider implements server.PlacementProvider and server.PlacementReattacher.
type Provider struct {
	client             *apiClient
	scope              server.PlacementScope
	ttlSeconds         int
	inheritEnvironment bool
	readyTimeout       time.Duration
	pollInterval       time.Duration
}

var _ server.PlacementProvider = (*Provider)(nil)
var _ server.PlacementReattacher = (*Provider)(nil)

// NewProvider constructs a Box placement provider from trusted operator config.
func NewProvider(cfg Config) (*Provider, error) {
	if strings.TrimSpace(cfg.APIKey) == "" || cfg.Scope == "" {
		return nil, fmt.Errorf("%w: APIKey and Scope are required", ErrInvalidConfig)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.Scheme == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid BaseURL", ErrInvalidConfig)
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return nil, fmt.Errorf("%w: BaseURL must use https (http is allowed only for loopback tests)", ErrInvalidConfig)
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	ttl := cfg.TTLSeconds
	if ttl <= 0 {
		ttl = defaultTTL
	}
	ready := cfg.ReadyTimeout
	if ready <= 0 {
		ready = defaultReady
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = defaultPoll
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 70 * time.Second}
	}
	return &Provider{
		client:             &apiClient{base: parsed, apiKey: cfg.APIKey, http: httpClient},
		scope:              cfg.Scope,
		ttlSeconds:         ttl,
		inheritEnvironment: cfg.InheritEnvironment,
		readyTimeout:       ready,
		pollInterval:       poll,
	}, nil
}

// Bind provisions the deployment default or returns the no-filesystem attenuation.
// Worktree selectors are intentionally not fabricated: Box-native fork/merge can
// be added only when Mecatl has an explicit remote worktree selection contract.
func (p *Provider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if p == nil || p.client == nil || req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	switch {
	case req.Selector.IsNoFS():
		return noFSBinding()
	case req.Selector.IsDefault():
		box, err := p.client.createBox(ctx, createBoxRequest{
			TTLSeconds: p.ttlSeconds,
			NoEnv:      !p.inheritEnvironment,
		})
		if err != nil {
			return server.PlacementBinding{}, fmt.Errorf("%w: create Box: %v", server.ErrPlacementUnavailable, err)
		}
		if err := p.prepare(ctx, box.ID); err != nil {
			_ = p.client.stopBox(context.WithoutCancel(ctx), box.ID)
			return server.PlacementBinding{}, fmt.Errorf("%w: prepare Box: %v", server.ErrPlacementUnavailable, err)
		}
		return p.binding(box.ID), nil
	default:
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
}

// Reattach resumes and rebinds the exact persisted Box identity. Unknown,
// stale, or mismatched refs fail loudly; this never creates a replacement Box.
func (p *Provider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if p == nil || p.client == nil || req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if req.Ref == noFSRef() {
		return noFSBinding()
	}
	if req.Ref.Kind != Kind || req.Ref.Revision != providerVersion || req.Ref.ID == "" {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	box, err := p.client.getBox(ctx, req.Ref.ID)
	if err != nil {
		if isStatus(err, http.StatusNotFound) {
			return server.PlacementBinding{}, server.ErrPlacementNotFound
		}
		return server.PlacementBinding{}, fmt.Errorf("%w: get Box: %v", server.ErrPlacementUnavailable, err)
	}
	if strings.EqualFold(box.State, "stopped") || strings.EqualFold(box.State, "archived") {
		if err := p.client.resumeBox(ctx, box.ID); err != nil {
			return server.PlacementBinding{}, fmt.Errorf("%w: resume Box: %v", server.ErrPlacementUnavailable, err)
		}
	}
	if err := p.prepare(ctx, box.ID); err != nil {
		return server.PlacementBinding{}, fmt.Errorf("%w: prepare Box: %v", server.ErrPlacementUnavailable, err)
	}
	return p.binding(box.ID), nil
}

func (p *Provider) prepare(ctx context.Context, id string) error {
	if _, err := p.waitReady(ctx, id); err != nil {
		return err
	}
	// Box file APIs address "workspace/..." while Mecatl reports /workspace.
	// Make those two views the same namespace for Shell as well.
	const command = `mkdir -p workspace; if [ ! -L /workspace ] && ! findmnt -rn --target /workspace >/dev/null 2>&1; then sudo rmdir /workspace 2>/dev/null || true; fi; if [ ! -e /workspace ]; then sudo ln -s "$PWD/workspace" /workspace 2>/dev/null || true; fi; if [ ! -L /workspace ] && ! findmnt -rn --target /workspace >/dev/null 2>&1; then sudo mkdir -p /workspace 2>/dev/null && sudo mount --bind "$PWD/workspace" /workspace 2>/dev/null || true; fi; touch /workspace/.mecatl-box-check && test -e workspace/.mecatl-box-check && rm -f /workspace/.mecatl-box-check workspace/.mecatl-box-check`
	result, err := p.client.command(ctx, id, commandRequest{Command: command, TimeoutSeconds: 10})
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("workspace initialization exited %d: %s", result.ExitCode, bounded(strings.TrimSpace(result.Stderr), 256))
	}
	return nil
}

func (p *Provider) waitReady(ctx context.Context, id string) (boxInfo, error) {
	deadline := time.Now().Add(p.readyTimeout)
	for {
		box, err := p.client.getBox(ctx, id)
		if err != nil {
			return boxInfo{}, err
		}
		switch strings.ToLower(box.State) {
		case "ready", "idle", "running":
			return box, nil
		case "error", "failed", "deleted":
			return boxInfo{}, fmt.Errorf("box %s entered state %q", id, box.State)
		}
		if time.Now().After(deadline) {
			return boxInfo{}, fmt.Errorf("timed out waiting for Box %s; last state %q", id, box.State)
		}
		timer := time.NewTimer(p.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return boxInfo{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *Provider) binding(id string) server.PlacementBinding {
	ref := session.EnvironmentRef{Kind: Kind, ID: id, Revision: providerVersion}
	ws := &Workspace{client: p.client, boxID: id}
	runner := &Runner{client: p.client, boxID: id}
	env := tool.MustEnvironment(ref, ws, memledger.New(), runner)
	return server.PlacementBinding{
		Environment: env,
		Ref:         ref,
		Metadata:    server.PlacementMetadata{Kind: string(Kind), Label: "Ascii Box", Revision: providerVersion},
	}
}

func noFSRef() session.EnvironmentRef {
	return session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: "no-fs", Revision: "nofs-v1"}
}

func noFSBinding() (server.PlacementBinding, error) {
	ref := noFSRef()
	env, err := tool.NewEnvironment(ref, nofs.New(), memledger.New(), nil)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{Environment: env, Ref: ref, Metadata: server.PlacementMetadata{Label: "No filesystem"}}, nil
}
