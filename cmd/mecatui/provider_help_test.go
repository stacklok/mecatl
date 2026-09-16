package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"testing"
)

func TestProviderHelpCommandsSucceedWithoutProviderOrSideEffects(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "providers",
			args: []string{"mecatui", "providers", "--help"},
			want: []string{"Usage: mecatui providers", "embedded server", "remote mecated server", "never printed", "providers setup"},
		},
		{
			name: "status",
			args: []string{"mecatui", "providers", "status", "--help"},
			want: []string{"Usage: mecatui providers status [PROVIDER]", "provider readiness", "changes nothing", "never prints credential values"},
		},
		{
			name: "setup",
			args: []string{"mecatui", "providers", "setup", "--help"},
			want: []string{"Usage: mecatui providers setup [PROVIDER]", "Configure a provider interactively", "Custom provider IDs", "thv llm"},
		},
		{
			name: "add",
			args: []string{"mecatui", "providers", "add", "--help"},
			want: []string{"Usage: mecatui providers add PROVIDER [--no-login]", "local settings", "--no-login", "does not change a remote mecated"},
		},
		{
			name: "login",
			args: []string{"mecatui", "providers", "login", "--help"},
			want: []string{"Usage: mecatui providers login PROVIDER [--no-browser]", "API key", "OIDC", "never printed"},
		},
		{
			name: "logout",
			args: []string{"mecatui", "providers", "logout", "--help"},
			want: []string{"Usage: mecatui providers logout PROVIDER", "locally stored credentials", "configuration", "Environment credentials are not removed"},
		},
		{
			name: "set default",
			args: []string{"mecatui", "providers", "set-default", "--help"},
			want: []string{"Usage: mecatui providers set-default PROVIDER [MODEL]", "embedded server", "must be ready", "does not change a remote mecated"},
		},
		{
			name: "remove",
			args: []string{"mecatui", "providers", "remove", "--help"},
			want: []string{"Usage: mecatui providers remove PROVIDER", "after confirmation", "locally stored credentials", "does not change a remote mecated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveInvocation(tt.args)
			if res.err != nil {
				t.Fatalf("resolve %v: %v", tt.args, res.err)
			}
			var stdout, stderr bytes.Buffer
			err := runProviderHelpForTest(testProviderCommands(), res, &stdout, &stderr)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("help error = %v, want flag.ErrHelp (main exits successfully)", err)
			}
			if stdout.Len() != 0 {
				t.Fatalf("help wrote stdout %q, want no command output", stdout.String())
			}
			output := stderr.String()
			for _, want := range tt.want {
				if !strings.Contains(output, want) {
					t.Errorf("help output missing %q:\n%s", want, output)
				}
			}
		})
	}
}

func runProviderHelpForTest(commands providerCommands, res invocationResolution, stdout, stderr *bytes.Buffer) error {
	switch res.mode {
	case modeProviderStatus:
		return commands.runStatus(context.Background(), res, stdout, stderr)
	case modeProviderSetup:
		return commands.runSetup(context.Background(), res, stdout, stderr)
	case modeProviderAdd:
		return commands.runAdd(context.Background(), res, stdout, stderr)
	case modeProviderCredential:
		return commands.runCredential(context.Background(), res, stdout, stderr)
	case modeProviderSetDefault:
		return commands.runSetDefault(context.Background(), res, stdout, stderr)
	case modeProviderRemove:
		return commands.runRemove(context.Background(), res, stdout, stderr)
	default:
		return errors.New("provider help resolved to a non-provider mode")
	}
}
