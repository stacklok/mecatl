package cliconfig

import "github.com/stacklok/mecatl/internal/adapter/permconfig"

// ResolveCommandRunnerConfig loads operator settings and applies them below an explicitly present CLI flag.
func ResolveCommandRunnerConfig(cliValue string, cliSet, conventional bool, explicitFiles []string) (string, error) {
	resolver := permconfig.New(permconfig.Options{Conventional: conventional, ExplicitFiles: explicitFiles})
	return ResolveCommandRunnerShell(resolver, cliValue, cliSet)
}

// ResolveCommandRunnerShell applies operator settings below an explicitly present CLI flag.
func ResolveCommandRunnerShell(resolver *permconfig.Resolver, cliValue string, cliSet bool) (string, error) {
	section, err := resolver.OperatorCommandRunner()
	if err != nil {
		return "", err
	}
	if cliSet || section == nil || !section.ShellSet {
		return cliValue, nil
	}
	return section.Shell, nil
}

// CommandRunnerEnabled applies the unconditional no-shell and empty-shell gates.
func CommandRunnerEnabled(shell string, noShell bool) bool {
	return !noShell && shell != ""
}
