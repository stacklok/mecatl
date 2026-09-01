package cliconfig

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
)

const defaultLogLevel = "info"

// NewTextLogger constructs the text logger used by command roots. It deliberately
// does not install a default logger; cmd/ owns that process-global operation.
// warning, when non-empty, is emitted after the logger is configured.
func NewTextLogger(w io.Writer, level slog.Level, warning string) *slog.Logger {
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level}))
	if warning != "" {
		logger.Warn(warning)
	}
	return logger
}

// LogLevelFlags is the shared binding for the command roots' --log-level flag.
// The roots own the logger and install it after parsing; this helper only parses
// the small, deliberately closed level vocabulary.
type LogLevelFlags struct {
	value *string
}

// RegisterLogLevelFlag adds --log-level to fs. Invalid values are intentionally
// accepted so a typo cannot make a daemon fail to start; Resolve reports a
// warning for the root to emit after it has installed the configured logger.
func RegisterLogLevelFlag(fs *flag.FlagSet) *LogLevelFlags {
	f := &LogLevelFlags{value: new(string)}
	fs.StringVar(f.value, "log-level", defaultLogLevel,
		"minimum log level: debug, info (default), warn, or error")
	return f
}

// Resolve returns the configured slog level and, for an invalid value, the one
// warning the command root should emit. Matching is exact and case-sensitive.
func (f *LogLevelFlags) Resolve() (slog.Level, string) {
	raw := defaultLogLevel
	if f != nil && f.value != nil {
		raw = *f.value
	}
	level, ok := ParseLogLevel(raw)
	if ok {
		return level, ""
	}
	return slog.LevelInfo, fmt.Sprintf("invalid --log-level %q; using info (accepted: debug, info, warn, error)", raw)
}

// ParseLogLevel maps the exact supported command-line tokens to slog levels.
// The boolean is false for every other token, including the empty string.
func ParseLogLevel(value string) (slog.Level, bool) {
	switch value {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}
