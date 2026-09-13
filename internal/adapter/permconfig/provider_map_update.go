package permconfig

// ProviderMapUpdate changes exactly one custom providers entry. A nil Definition
// removes Provider; a non-nil Definition adds or replaces it. OIDCCredentialStore
// is added only when the settings document does not already configure one.
type ProviderMapUpdate struct {
	Provider            string
	Definition          *ProviderDefinition
	OIDCCredentialStore *OIDCCredentialStore
}
