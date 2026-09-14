package permconfig

// DefaultUpdate coherently selects one provider and model.
type DefaultUpdate struct {
	Provider string
	Model    string
}

// PreflightDefaultsUpdateTarget validates an existing target with the same
// private-file and document checks used by UpdateDefaults, without creating files.
func PreflightDefaultsUpdateTarget(path string) error { return preflightDefaultsUpdateTarget(path) }
