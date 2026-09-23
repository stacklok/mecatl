package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcp"
	serveradapter "github.com/stacklok/mecatl/internal/adapter/server"
)

var errMCPRuntimeUnavailable = errors.New("mcp runtime is unavailable")

type mcpRuntimeContextKey struct{}

type mcpRuntimePin struct {
	candidate *mcpReconcileCandidate
}

// mcpRuntimeSet owns the immutable current direct MCP runtime and the bounded
// retirement set. Engines only retain a revision tag; operation contexts retain
// the pin that keeps a displaced manager alive.
type mcpRuntimeSet struct {
	mu        sync.Mutex
	current   *mcpReconcileCandidate
	empty     *mcpReconcileCandidate
	pins      map[*mcpReconcileCandidate]int
	retired   []*mcpReconcileCandidate
	deferred  bool
	retry     func()
	closed    bool
	drained   chan struct{}
	drain     sync.Once
	closeDone chan struct{}
}

func newMCPRuntimeSet(retry func()) *mcpRuntimeSet {
	return &mcpRuntimeSet{
		empty: &mcpReconcileCandidate{}, pins: make(map[*mcpReconcileCandidate]int),
		retry: retry, drained: make(chan struct{}), closeDone: make(chan struct{}),
	}
}

func (s *mcpRuntimeSet) setRetry(retry func()) {
	s.mu.Lock()
	s.retry = retry
	s.mu.Unlock()
}

func (s *mcpRuntimeSet) publish(old, candidate *mcpReconcileCandidate) bool {
	if candidate == nil {
		return false
	}
	s.mu.Lock()
	if s.closed || s.current != old {
		s.mu.Unlock()
		candidate.close()
		return false
	}
	if old != nil && s.pins[old] > 0 && len(s.retired) >= maxMCPRetainedRuntimes {
		s.deferred = true
		s.mu.Unlock()
		candidate.close()
		return false
	}
	s.current = candidate
	if old != nil {
		if s.pins[old] == 0 {
			s.mu.Unlock()
			old.close()
			return true
		}
		s.retired = append(s.retired, old)
	}
	s.mu.Unlock()
	return true
}

func (s *mcpRuntimeSet) pin(ctx context.Context) (context.Context, func(), error) {
	if inherited, ok := ctx.Value(mcpRuntimeContextKey{}).(*mcpRuntimePin); ok && inherited != nil && inherited.candidate != nil {
		return ctx, func() {}, nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, errMCPRuntimeUnavailable
	}
	candidate := s.current
	if candidate == nil {
		candidate = s.empty
	}
	s.pins[candidate]++
	s.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { s.release(candidate) }) }
	return context.WithValue(ctx, mcpRuntimeContextKey{}, &mcpRuntimePin{candidate: candidate}), release, nil
}

func (s *mcpRuntimeSet) release(candidate *mcpReconcileCandidate) {
	var closeCandidate *mcpReconcileCandidate
	var retry func()
	s.mu.Lock()
	if n := s.pins[candidate]; n > 1 {
		s.pins[candidate] = n - 1
	} else {
		delete(s.pins, candidate)
		for i, retired := range s.retired {
			if retired == candidate {
				closeCandidate = retired
				s.retired = append(s.retired[:i], s.retired[i+1:]...)
				break
			}
		}
		if closeCandidate != nil && s.deferred && !s.closed {
			s.deferred = false
			retry = s.retry
		}
	}
	if s.closed && len(s.pins) == 0 {
		s.drain.Do(func() { close(s.drained) })
	}
	s.mu.Unlock()
	closeCandidate.close()
	if retry != nil {
		retry()
	}
}

func (s *mcpRuntimeSet) currentRevision() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return 0
	}
	return s.current.generation
}

func (s *mcpRuntimeSet) currentToolNames() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	out := make([]string, 0, len(s.current.tools))
	for _, meta := range s.current.tools {
		out = append(out, meta.Name)
	}
	return out
}

func (s *mcpRuntimeSet) currentResourceServers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		return nil
	}
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for _, resource := range s.current.resources {
		if _, ok := seen[resource.Server]; ok {
			continue
		}
		seen[resource.Server] = struct{}{}
		out = append(out, resource.Server)
	}
	return out
}

func (s *mcpRuntimeSet) close() {
	s.mu.Lock()
	if s.closed {
		closeDone := s.closeDone
		s.mu.Unlock()
		<-closeDone
		return
	}
	s.closed = true
	if len(s.pins) == 0 {
		s.drain.Do(func() { close(s.drained) })
	}
	drained := s.drained
	s.mu.Unlock()

	<-drained
	s.mu.Lock()
	current := s.current
	retired := append([]*mcpReconcileCandidate(nil), s.retired...)
	s.current = nil
	s.retired = nil
	s.mu.Unlock()
	current.close()
	for _, candidate := range retired {
		candidate.close()
	}
	close(s.closeDone)
}

func mcpRuntimeCandidate(ctx context.Context) *mcpReconcileCandidate {
	pin, _ := ctx.Value(mcpRuntimeContextKey{}).(*mcpRuntimePin)
	if pin == nil {
		return nil
	}
	return pin.candidate
}

func mcpRuntimeRevision(ctx context.Context) uint64 {
	candidate := mcpRuntimeCandidate(ctx)
	if candidate == nil {
		return 0
	}
	return candidate.generation
}

func mcpOperationRevision(ctx context.Context) (uint64, bool) {
	candidate := mcpRuntimeCandidate(ctx)
	if candidate == nil {
		return 0, false
	}
	return candidate.generation, true
}

func pinCatalogAssets(ctx context.Context, assets catalogAssets) (context.Context, catalogAssets, uint64, func(), error) {
	if assets.mcpRuntimes == nil {
		return ctx, assets, 0, func() {}, nil
	}
	pinned, release, err := assets.mcpRuntimes.pin(ctx)
	if err != nil {
		return nil, catalogAssets{}, 0, nil, err
	}
	candidate := mcpRuntimeCandidate(pinned)
	assets.globalMgr = candidate.manager
	return pinned, assets, candidate.generation, release, nil
}

func mcpCatalogAssets(ctx context.Context, assets catalogAssets) catalogAssets {
	if candidate := mcpRuntimeCandidate(ctx); candidate != nil {
		assets.globalMgr = candidate.manager
	}
	return assets
}

func pinMCPRuntimeFactory(assets catalogAssets, build serveradapter.SessionEngineWithToolsFactory) serveradapter.SessionEngineWithToolsFactory {
	return func(ctx context.Context, sel serveradapter.ProviderSelector, specs []mcp.ServerConfig, profile serveradapter.SessionProfile, workspace string, mode session.PermissionMode, sessionTools []tool.Tool) (serveradapter.SessionEngineResult, error) {
		pinned, _, _, release, err := pinCatalogAssets(ctx, assets)
		if err != nil {
			return serveradapter.SessionEngineResult{}, err
		}
		defer release()
		return build(pinned, sel, specs, profile, workspace, mode, sessionTools)
	}
}

func (s *mcpRuntimeSet) withManager(ctx context.Context, use func(context.Context, *mcp.Manager) error) error {
	pinned, release, err := s.pin(ctx)
	if err != nil {
		return err
	}
	defer release()
	candidate := mcpRuntimeCandidate(pinned)
	if candidate == nil || candidate.manager == nil {
		return errMCPRuntimeUnavailable
	}
	return use(pinned, candidate.manager)
}

func (s *mcpRuntimeSet) ListResources(ctx context.Context, server string) (out []mcp.Resource, err error) {
	err = s.withManager(ctx, func(ctx context.Context, manager *mcp.Manager) error {
		out, err = manager.ListResources(ctx, server)
		return err
	})
	return out, err
}

func (s *mcpRuntimeSet) ReadResource(ctx context.Context, server, uri string) (out mcp.ResourceContents, err error) {
	err = s.withManager(ctx, func(ctx context.Context, manager *mcp.Manager) error {
		out, err = manager.ReadResource(ctx, server, uri)
		return err
	})
	return out, err
}

func (s *mcpRuntimeSet) ListPrompts(ctx context.Context, server string) (out []mcp.Prompt, err error) {
	err = s.withManager(ctx, func(ctx context.Context, manager *mcp.Manager) error {
		out, err = manager.ListPrompts(ctx, server)
		return err
	})
	return out, err
}

func (s *mcpRuntimeSet) GetPrompt(ctx context.Context, server, name string, args map[string]string) (out mcp.PromptResult, err error) {
	err = s.withManager(ctx, func(ctx context.Context, manager *mcp.Manager) error {
		out, err = manager.GetPrompt(ctx, server, name, args)
		return err
	})
	return out, err
}

func (s *mcpRuntimeSet) CallTool(ctx context.Context, server, name string, args json.RawMessage) (out mcp.CallResult, err error) {
	err = s.withManager(ctx, func(ctx context.Context, manager *mcp.Manager) error {
		out, err = manager.CallTool(ctx, server, name, args)
		return err
	})
	return out, err
}
