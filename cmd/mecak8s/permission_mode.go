package main

import (
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
)

// permissionModeHelp is the --permission-mode usage line, shared with the other
// server commands through app.PermissionModeHelp.
var permissionModeHelp = app.PermissionModeHelp(app.PermissionModeHelpOptions{
	DefaultToken: "auto", RootRefusal: true,
})

// permissionModeFlags is the --permission-mode flag surface mecak8s parsed. The
// --posture DEFAULT (auto) is not a use of the alias: only an explicit --posture
// sets PostureSet.
func permissionModeFlags(cfg config) app.PermissionModeFlags {
	return app.PermissionModeFlags{
		PermissionMode: cfg.permissionMode, PermissionModeSet: cfg.permissionModeFlagSet,
		Posture: cfg.posture, PostureSet: cfg.postureFlagSet,
	}
}

// validatePermissionModeFlags fails fast on an unknown --permission-mode token
// and on a --permission-mode combined with the deprecated --posture alias.
func validatePermissionModeFlags(cfg config) error {
	return permissionModeFlags(cfg).Validate()
}

// warnDeprecatedPermissionFlags emits exactly one deprecation WARN when the
// operator passed the deprecated --posture explicitly.
func warnDeprecatedPermissionFlags(diag port.Diagnostics, cfg config) {
	permissionModeFlags(cfg).WarnDeprecated(diag)
}
