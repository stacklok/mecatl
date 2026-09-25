package main

import (
	"errors"
	"fmt"
	"io"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/app"
)

// permission_mode.go is mecatui's half of the ADR 0365 --permission-mode flag.
// A token names an exact (posture, session mode) pair. The posture half goes to
// the embedded server's composition (app.Config.PermissionMode); the session half
// is the mode this client requests for the sessions it creates. The deprecated
// --mode, --posture, and --yolo keep resolving exactly as before and each emit one
// pre-launch deprecation WARN.

// parsePermissionModeFlag parses an explicit --permission-mode into cfg and
// rejects a combination with the deprecated aliases. It runs at parse time so an
// unknown token fails fast on every transport.
func parsePermissionModeFlag(cfg *config) error {
	if cfg.permissionModeFlagSet {
		tok, err := app.ParsePermissionMode(cfg.permissionMode)
		if err != nil {
			return fmt.Errorf("--permission-mode: %w", err)
		}
		cfg.permissionModeToken = tok
	}
	return validatePermissionModeFlags(*cfg)
}

// validatePermissionModeFlags enforces the flag-surface rules: --permission-mode
// replaces --mode, --posture, and --yolo rather than combining with them, and
// under `mecatui connect` a token whose posture half is not strict is refused,
// because the posture belongs to the remote server (mirroring --posture, which
// connect rejects outright). --trust-project is not deprecated and still folds.
func validatePermissionModeFlags(c config) error {
	if !c.permissionModeFlagSet {
		return nil
	}
	var aliases []string
	if c.modeFlagSet {
		aliases = append(aliases, "--mode")
	}
	if c.postureFlagSet {
		aliases = append(aliases, "--posture")
	}
	if c.yoloFlagSet {
		aliases = append(aliases, "--yolo")
	}
	if len(aliases) > 0 {
		return fmt.Errorf("--permission-mode cannot be combined with %s; pass one: --permission-mode alone replaces the deprecated flags", joinFlagNames(aliases))
	}
	tok := c.permissionModeToken
	if c.transportMode == modeConnect && tok.Name != "" && tok.Posture != app.PostureStrict {
		return errors.New("--permission-mode " + tok.Name + " sets posture " + tok.Posture.String() +
			", which belongs to the remote server; mecatui connect cannot change it. The server operator must change mecated's configuration and restart it. " +
			"To choose the mode of this client's new sessions, pass plan, default, or accept-edits")
	}
	return nil
}

func joinFlagNames(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " or " + names[1]
	default:
		out := ""
		for i, n := range names {
			switch {
			case i == len(names)-1:
				out += ", or " + n
			case i > 0:
				out += ", " + n
			default:
				out += n
			}
		}
		return out
	}
}

// requestedSessionMode is the session mode this client asks for when it creates a
// session, in the TUI spelling. serverDefault is true when neither
// --permission-mode nor --mode was given: the create then carries no mode, so the
// server's own default applies (embedded: the operator's permissionMode: key; connect:
// the remote server's default). Without that key, and on an older server, the
// server's default is "default", which is exactly what mecatui requested before.
func (c config) requestedSessionMode() (mode string, serverDefault bool) {
	switch {
	case c.permissionModeFlagSet:
		return client.ModeString(client.ModeFromString(string(c.permissionModeToken.SessionMode))), false
	case c.modeFlagSet:
		return c.mode, false
	default:
		return "", true
	}
}

// deprecatedPermissionFlagWarnings returns one WARN line per deprecated alias the
// operator typed (--mode, --posture, --yolo), each naming --permission-mode and,
// when one exists, the single token that reproduces the combination.
func deprecatedPermissionFlagWarnings(c config) []string {
	if c.permissionModeFlagSet {
		// Combined use is a startup error, so no deprecation line is needed.
		return nil
	}
	if !c.modeFlagSet && !c.postureFlagSet && !c.yoloFlagSet {
		return nil
	}
	hint := "no single token names this combination; it still resolves as before"
	posture := app.ResolveAliasPosture(app.ParsePosture(c.posture), c.allowAllTools, c.trustProject)
	mode := c.mode
	if !c.modeFlagSet {
		mode = client.ModeDefaultString
	}
	if tok, ok := app.PermissionModeForPair(posture, sessionModeFromTUI(mode)); ok {
		hint = "use --permission-mode " + tok.Name
	}
	var out []string
	add := func(flagName string) {
		out = append(out, "mecatui: WARNING: "+flagName+" is deprecated and will be removed; use --permission-mode instead ("+hint+")")
	}
	if c.modeFlagSet {
		add("--mode")
	}
	if c.postureFlagSet {
		add("--posture")
	}
	if c.yoloFlagSet {
		add("--yolo")
	}
	return out
}

// warnDeprecatedPermissionFlags prints the deprecation WARNs on the pre-launch
// stderr path (before the alternate screen starts), like warnEmbeddedPosture.
func warnDeprecatedPermissionFlags(w io.Writer, c config) {
	for _, line := range deprecatedPermissionFlagWarnings(c) {
		_, _ = fmt.Fprintln(w, line)
	}
}

// sessionModeFromTUI maps a TUI mode spelling to the session value app's table
// uses. It goes through the client's spelling grammar first, so every accepted
// --mode spelling maps the same way it does on the wire.
func sessionModeFromTUI(mode string) session.PermissionMode {
	switch client.ModeString(client.ModeFromString(mode)) {
	case "plan":
		return session.ModePlan
	case "accept-edits":
		return session.ModeAccept
	default:
		return session.ModeDefault
	}
}
