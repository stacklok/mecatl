package main

import (
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/app"
)

// permissionModeHelp is the --permission-mode usage line, shared with the other
// server commands through app.PermissionModeHelp.
var permissionModeHelp = app.PermissionModeHelp(app.PermissionModeHelpOptions{
	DefaultToken: "default", RootRefusal: true, YoloAlias: true,
})

// permissionModeFlags is the --permission-mode flag surface mecated parsed,
// including the deprecated --posture and --yolo aliases.
func permissionModeFlags(cfg config) app.PermissionModeFlags {
	return app.PermissionModeFlags{
		PermissionMode: cfg.permissionMode, PermissionModeSet: cfg.permissionModeFlagSet,
		Posture: cfg.posture, PostureSet: cfg.postureFlagSet,
		YoloSet: cfg.yoloFlagSet,
	}
}

// validatePermissionModeFlags fails fast on an unknown --permission-mode token
// and on a --permission-mode combined with one of its deprecated aliases.
func validatePermissionModeFlags(cfg config) error {
	return permissionModeFlags(cfg).Validate()
}

// warnDeprecatedPermissionFlags emits exactly one deprecation WARN per
// deprecated alias the operator passed.
func warnDeprecatedPermissionFlags(diag port.Diagnostics, cfg config) {
	permissionModeFlags(cfg).WarnDeprecated(diag)
}
