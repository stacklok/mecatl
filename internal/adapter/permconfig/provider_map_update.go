package permconfig

// ProviderMapUpdate changes exactly one custom providers entry. A nil Definition
// removes Provider; a non-nil Definition adds or replaces it. ExpectedAbsent and
// ExpectedDefinition are optional semantic preconditions checked against the same
// document snapshot that is mutated. OIDCCredentialStore is added only when the
// settings document does not already configure one; ExpectedOIDCCredentialStoreAbsent
// makes that absence a precondition. RemoveOIDCCredentialStore removes that shared
// store only when it still equals the expected inserted value and no remaining OIDC
// provider uses it.
type ProviderMapUpdate struct {
	Provider                          string
	Definition                        *ProviderDefinition
	ExpectedAbsent                    bool
	ExpectedDefinition                *ProviderDefinition
	OIDCCredentialStore               *OIDCCredentialStore
	ExpectedOIDCCredentialStoreAbsent bool
	RemoveOIDCCredentialStore         *OIDCCredentialStore
}
