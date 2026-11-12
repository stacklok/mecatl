package executionenv

import "slices"

// Execution capability names shared by templates, requests, and admitted sets.
const (
	CapabilityFilesystem        = "filesystem"
	CapabilityForegroundCommand = "foreground-command"
)

// TemplateCapabilities is every template's declared set until templates
// declare their own.
func TemplateCapabilities() []string {
	return []string{CapabilityFilesystem, CapabilityForegroundCommand}
}

// ValidAdmittedCapabilities reports whether v is a present admitted set:
// non-empty, known names only, and no duplicates.
func ValidAdmittedCapabilities(v []string) bool {
	if len(v) == 0 {
		return false
	}
	for i, name := range v {
		if name != CapabilityFilesystem && name != CapabilityForegroundCommand {
			return false
		}
		if slices.Contains(v[:i], name) {
			return false
		}
	}
	return true
}
