package main

import (
	"bytes"
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
			want: []string{"Usage: mecatui providers", "embedded mecated", "remote mecated", "Classes, authentication, and custody:", "No-provider recovery:"},
		},
		{
			name: "status",
			args: []string{"mecatui", "providers", "status", "--help"},
			want: []string{"Usage: mecatui providers status [PROVIDER]", "without changing", "never prints credential values", "no provider is ready"},
		},
		{
			name: "setup",
			args: []string{"mecatui", "providers", "setup", "--help"},
			want: []string{"Usage: mecatui providers setup [PROVIDER]", "custom provider", "locally managed credentials", "ToolHive remains externally managed"},
		},
		{
			name: "add",
			args: []string{"mecatui", "providers", "add", "--help"},
			want: []string{"Usage: mecatui providers add PROVIDER [--no-login]", "operator settings", "--no-login saves only the definition", "does not configure remote mecated"},
		},
		{
			name: "login",
			args: []string{"mecatui", "providers", "login", "--help"},
			want: []string{"Usage: mecatui providers login PROVIDER [--no-browser]", "locally managed API key", "OIDC", "never printed"},
		},
		{
			name: "logout",
			args: []string{"mecatui", "providers", "logout", "--help"},
			want: []string{"Usage: mecatui providers logout PROVIDER", "locally managed", "ToolHive credentials are externally managed", "side-effecting local operation"},
		},
		{
			name: "set default",
			args: []string{"mecatui", "providers", "set-default", "--help"},
			want: []string{"Usage: mecatui providers set-default PROVIDER [MODEL]", "embedded mecated", "writes local operator settings", "no provider is usable"},
		},
		{
			name: "remove",
			args: []string{"mecatui", "providers", "remove", "--help"},
			want: []string{"Usage: mecatui providers remove PROVIDER", "explicit confirmation", "locally managed", "does not change a remote mecated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := resolveInvocation(tt.args)
			if res.err != nil {
				t.Fatalf("resolve %v: %v", tt.args, res.err)
			}
			var stdout, stderr bytes.Buffer
			err := runProviderCommandForHelpTest(res, &stdout, &stderr)
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

func runProviderCommandForHelpTest(res invocationResolution, stdout, stderr *bytes.Buffer) error {
	switch res.mode {
	case modeProviderStatus:
		return runProviderStatusCommand(res, stdout, stderr)
	case modeProviderSetup:
		return runProviderSetupCommand(res, stdout, stderr)
	case modeProviderAdd:
		return runProviderAddCommand(res, stdout, stderr)
	case modeProviderCredential:
		return runProviderCredentialCommand(res, stdout, stderr)
	case modeProviderSetDefault:
		return runProviderSetDefaultCommand(res, stdout, stderr)
	case modeProviderRemove:
		return runProviderRemoveCommand(res, stdout, stderr)
	default:
		return errors.New("provider help resolved to a non-provider mode")
	}
}
