package cliconfig

import "strings"

// NormalizeLegacyNoBash maps the undocumented legacy shell-disabling flag to
// its canonical spelling before a command's FlagSet parses it.
func NormalizeLegacyNoBash(args []string) []string {
	out := append([]string(nil), args...)
	for i, arg := range out {
		switch {
		case arg == "--no-bash":
			out[i] = "--no-shell"
		case strings.HasPrefix(arg, "--no-bash="):
			out[i] = "--no-shell=" + strings.TrimPrefix(arg, "--no-bash=")
		}
	}
	return out
}
