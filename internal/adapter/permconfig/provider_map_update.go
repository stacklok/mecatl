package permconfig

// ProviderMapUpdate changes exactly one custom providers entry. A nil Definition
// removes Provider; a non-nil Definition adds or replaces it.
type ProviderMapUpdate struct {
	Provider   string
	Definition *ProviderDefinition
}
