package session

// ProviderModelID is the opaque identity of the provider and model selected by
// the server for one auxiliary model call. It carries no selection, provider
// instance, credential, context-window, or reasoning-effort semantics.
type ProviderModelID struct {
	ProviderID string
	ModelID    string
}
