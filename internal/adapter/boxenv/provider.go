// Package boxenv adapts ASCII Box VMs to mecatl execution environments.
package boxenv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	pathpkg "path"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/nofs"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	// Kind is the open EnvironmentKind label minted by the Box adapter.
	Kind session.EnvironmentKind = "box"

	defaultTTLSeconds = 3600
	noFSID            = "no-fs"
	noFSRevision      = "nofs-v1"
)

// Config configures a Box placement provider. APIKey is secret-shaped and must
// never be persisted or logged. Every Box is created/resumed with noEnv=true,
// so account-level model/GitHub/SSH secrets never enter the guest.
type Config struct {
	APIKey string
	// BaseURL is primarily a test seam. Empty uses the public Box v1 endpoint.
	BaseURL string
	// Scope is the trusted server-owned placement authorization scope.
	Scope server.PlacementScope
	// BoxType is passed to Box create when non-empty (for example "small").
	BoxType string
	// TTLSeconds bounds idle leaked resources. Zero selects one hour.
	TTLSeconds int
	// Workdir is the relative Box working directory shared by Workspace and Shell.
	// Empty selects the Box working directory root (".").
	Workdir string
	// ReadyTimeout bounds create/resume readiness polling. Zero selects two minutes.
	ReadyTimeout time.Duration
	HTTPClient   *http.Client
}

// Provider is an exact, server-scoped Box placement provider. It supports the
// deployment default and the ordinary no-FS attenuation. Worktree selectors are
// deliberately unsupported; Box-native forks belong on EnvironmentForker.
type Provider struct {
	client       *apiClient
	scope        server.PlacementScope
	boxType      string
	ttlSeconds   int
	workdir      string
	readyTimeout time.Duration
	revision     string
}

var (
	_ server.PlacementProvider   = (*Provider)(nil)
	_ server.PlacementReattacher = (*Provider)(nil)
)

// New constructs a Box placement provider. The API key remains only in the
// private HTTP client and never enters EnvironmentRef or placement metadata.
func New(cfg Config) (*Provider, error) {
	if cfg.Scope == "" {
		return nil, errors.New("boxenv: placement scope is required")
	}
	client, err := newAPIClient(cfg.APIKey, cfg.BaseURL, cfg.HTTPClient)
	if err != nil {
		return nil, err
	}
	if cfg.TTLSeconds == 0 {
		cfg.TTLSeconds = defaultTTLSeconds
	}
	if cfg.TTLSeconds < 1 || cfg.TTLSeconds > 2_592_000 {
		return nil, errors.New("boxenv: ttlSeconds must be between 1 and 2592000")
	}
	workdir, err := cleanWorkdir(cfg.Workdir)
	if err != nil {
		return nil, err
	}
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = 2 * time.Minute
	}
	if cfg.ReadyTimeout < time.Second {
		return nil, errors.New("boxenv: ready timeout must be at least one second")
	}
	return &Provider{
		client:       client,
		scope:        cfg.Scope,
		boxType:      cfg.BoxType,
		ttlSeconds:   cfg.TTLSeconds,
		workdir:      workdir,
		readyTimeout: cfg.ReadyTimeout,
		revision:     providerRevision(client.baseURL, cfg.BoxType, cfg.TTLSeconds, workdir),
	}, nil
}

func cleanWorkdir(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") {
		return "", errors.New("boxenv: workdir must be relative")
	}
	clean := pathpkg.Clean(value)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("boxenv: workdir escapes the Box workspace")
	}
	return clean, nil
}

func providerRevision(baseURL, boxType string, ttlSeconds int, workdir string) string {
	// Do not fold the credential into durable identity. Reattachment semantics are
	// pinned to the endpoint and non-secret construction contract only.
	material := fmt.Sprintf("boxenv/v1\n%s\n%s\n%d\n%s\nnoenv=true", baseURL, boxType, ttlSeconds, workdir)
	sum := sha256.Sum256([]byte(material))
	return "box-v1-" + hex.EncodeToString(sum[:8])
}

// Bind atomically resolves one server-owned placement choice. Default creates a
// fresh Box. The returned Close archives that provisional live VM; persisted
// EnvironmentRef remains resumable and Reattach brings the exact Box back.
func (p *Provider) Bind(ctx context.Context, req server.PlacementBindRequest) (server.PlacementBinding, error) {
	if p == nil || p.client == nil || req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	switch {
	case req.Selector.IsNoFS():
		return p.bindNoFS()
	case req.Selector.IsDefault():
		return p.create(ctx)
	default:
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
}

// Reattach resolves only the exact persisted Box identity. It never follows the
// current default or fabricates a replacement when the box is gone/stale.
func (p *Provider) Reattach(ctx context.Context, req server.PlacementReattachRequest) (server.PlacementBinding, error) {
	if p == nil || p.client == nil || req.Scope != p.scope {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if req.Ref == noFSRef() {
		return p.bindNoFS()
	}
	if req.Ref.Kind != Kind || req.Ref.ID == "" {
		return server.PlacementBinding{}, server.ErrPlacementNotFound
	}
	if req.Ref.Revision != p.revision {
		return server.PlacementBinding{}, server.ErrPlacementStale
	}
	readyCtx, cancel := context.WithTimeout(ctx, p.readyTimeout)
	defer cancel()
	if err := p.client.ensureReady(readyCtx, req.Ref.ID); err != nil {
		var statusErr *apiStatusError
		if errors.As(err, &statusErr) && statusErr.status == http.StatusNotFound {
			return server.PlacementBinding{}, server.ErrPlacementNotFound
		}
		return server.PlacementBinding{}, fmt.Errorf("%w: %v", server.ErrPlacementUnavailable, err)
	}
	return p.binding(req.Ref), nil
}

func (p *Provider) create(ctx context.Context) (server.PlacementBinding, error) {
	created, err := p.client.createBox(ctx, p.boxType, p.ttlSeconds)
	if err != nil {
		return server.PlacementBinding{}, fmt.Errorf("%w: %v", server.ErrPlacementUnavailable, err)
	}
	ref := session.EnvironmentRef{Kind: Kind, ID: created.ID, Revision: p.revision}
	readyCtx, cancel := context.WithTimeout(ctx, p.readyTimeout)
	defer cancel()
	if err := p.client.ensureReady(readyCtx, created.ID); err != nil {
		// Best-effort cleanup of a partially provisioned resource.
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		_ = p.client.stopBox(cleanupCtx, created.ID)
		cleanupCancel()
		return server.PlacementBinding{}, fmt.Errorf("%w: %v", server.ErrPlacementUnavailable, err)
	}
	binding := p.binding(ref)
	binding.Close = func() error {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return p.client.stopBox(closeCtx, ref.ID)
	}
	return binding, nil
}

func (p *Provider) binding(ref session.EnvironmentRef) server.PlacementBinding {
	ws := &workspace{client: p.client, boxID: ref.ID, workdir: p.workdir}
	runner := &runner{client: p.client, boxID: ref.ID, workdir: p.workdir, root: ws.Root()}
	env := tool.MustEnvironment(ref, ws, memledger.New(), runner)
	return server.PlacementBinding{
		Environment: env,
		Ref:         ref,
		Metadata: server.PlacementMetadata{
			Kind:     string(Kind),
			Label:    "Box remote environment",
			Revision: ref.Revision,
		},
	}
}

func (*Provider) bindNoFS() (server.PlacementBinding, error) {
	ref := noFSRef()
	env, err := tool.NewEnvironment(ref, nofs.New(), memledger.New(), nil)
	if err != nil {
		return server.PlacementBinding{}, server.ErrPlacementUnavailable
	}
	return server.PlacementBinding{
		Environment: env,
		Ref:         ref,
		Metadata:    server.PlacementMetadata{Kind: string(session.EnvKindNoFS), Label: "No filesystem", Revision: ref.Revision},
	}, nil
}

func noFSRef() session.EnvironmentRef {
	return session.EnvironmentRef{Kind: session.EnvKindNoFS, ID: noFSID, Revision: noFSRevision}
}
