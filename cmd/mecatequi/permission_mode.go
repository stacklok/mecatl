package main

import (
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
)

// permissionModeHelp is the --permission-mode usage line, shared with the other
// server commands through app.PermissionModeHelp. mecatequi runs one session and
// has no root/sandbox refusal.
var permissionModeHelp = app.PermissionModeHelp(app.PermissionModeHelpOptions{
	DefaultToken: "default", OneShot: true,
})

// permissionModeFlags is the --permission-mode flag surface mecatequi parsed.
func permissionModeFlags(f flags) app.PermissionModeFlags {
	return app.PermissionModeFlags{
		PermissionMode: f.permissionMode, PermissionModeSet: f.permissionModeFlagSet,
		Posture: f.posture, PostureSet: f.postureFlagSet,
	}
}

// validatePermissionModeFlags fails fast on an unknown --permission-mode token
// and on a --permission-mode combined with the deprecated --posture alias.
func validatePermissionModeFlags(f flags) error {
	return permissionModeFlags(f).Validate()
}

// warnDeprecatedPermissionFlags emits exactly one deprecation WARN when the
// operator passed the deprecated --posture explicitly.
func warnDeprecatedPermissionFlags(diag port.Diagnostics, f flags) {
	permissionModeFlags(f).WarnDeprecated(diag)
}
