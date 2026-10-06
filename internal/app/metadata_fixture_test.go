package app

// These fixtures inject immutable observations for pure resolver tests only.
// Attempt/publication tests must exercise providerDiscovery.request instead.
func newMetadataFixture() *liveMetaStore {
	owner := &providerDiscovery{}
	owner.view.Store(&discoverySnapshot{providers: map[string]discoveryProvider{}})
	return &liveMetaStore{owner: owner}
}

func (s *liveMetaStore) setMetadataFixture(rows map[string][]modelEntry) {
	view := &discoverySnapshot{providers: make(map[string]discoveryProvider)}
	for pid, models := range rows {
		view.providers[pid] = discoveryProvider{eligible: true, observations: cloneModelEntries(models), outcome: providerStatus{State: statusOK}}
	}
	s.owner.view.Store(view)
}

func (s *liveMetaStore) setEligibilityFixture(providers []string) {
	view := &discoverySnapshot{providers: make(map[string]discoveryProvider)}
	for _, pid := range providers {
		view.providers[pid] = discoveryProvider{eligible: true}
	}
	s.owner.view.Store(view)
}
