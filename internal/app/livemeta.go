package app

import "github.com/stacklok/mecatl/internal/adapter/permconfig"

// liveMetaStore is a read-only resolver view. It is bound once, before bootstrap,
// to the discovery owner; it owns no metadata, settlement or publication state.
type liveMetaStore struct{ owner *providerDiscovery }

func newLiveMetaStore() *liveMetaStore { return &liveMetaStore{} }

func (s *liveMetaStore) current() *discoverySnapshot {
	if s == nil {
		return nil
	}
	return s.owner.snapshot()
}

func (s *liveMetaStore) lookup(pid, model string) (modelEntry, bool) {
	return s.current().lookup(pid, model)
}

const (
	maxLiveContextLimit = permconfig.MaxContextWindowTokens
	maxLiveOutputLimit  = 512000
)

func clampLive(v, limit int) int { return min(v, limit) }

func (s *liveMetaStore) outputLimitFor(pid, model string) int {
	if m, ok := s.lookup(pid, model); ok && m.OutputLimit > 0 {
		return clampLive(m.OutputLimit, maxLiveOutputLimit)
	}
	if pid == providerAnthropic || pid == providerToolhiveAnthropic {
		return anthropicOutputLimit(model)
	}
	return 0
}

func (s *liveMetaStore) contextWindowFor(pid, model string) int {
	value, _ := s.knownWindow(pid, model)
	return value
}

func (s *liveMetaStore) knownWindow(pid, model string) (int, bool) {
	if m, ok := s.lookup(pid, model); ok && m.ContextLimit > 0 {
		return clampLive(m.ContextLimit, maxLiveContextLimit), true
	}
	value := catalogContextWindow(metadataCatalogProviderID(pid), model)
	return value, value > 0
}

func (reg *providerRegistry) windowResolver(cfg Config, pid, model string) func() int {
	return func() int {
		result := resolveModelWindow(cfg, reg.meta.current(), pid, model)
		if result.tokens > 0 {
			return result.tokens
		}
		return defaultContextWindowTokens
	}
}

func (reg *providerRegistry) echoWindowResolver(cfg Config, pid, model string) func() int {
	return func() int { return resolveModelWindow(cfg, reg.meta.current(), pid, model).tokens }
}

func (s *liveMetaStore) modalitiesFor(pid, model string) ([]string, bool) {
	if m, ok := s.lookup(pid, model); ok && m.InputModalities != nil {
		return m.InputModalities, true
	}
	return nil, false
}

func (s *liveMetaStore) thinkingFor(pid, model string) (adaptive, enabled, known bool) {
	if m, ok := s.lookup(pid, model); ok && m.Thinking.Known {
		return m.Thinking.Adaptive, m.Thinking.Enabled, true
	}
	return false, false, false
}
