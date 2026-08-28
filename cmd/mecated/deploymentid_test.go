package main

import (
	"strings"
	"testing"
)

// TestValidateDeploymentID pins the --deployment-id bound (ADR 0248).
//
// The label is echoed verbatim to every authenticated caller and rendered by
// clients we do not control, so it is validated at STARTUP — where the operator
// is present and the message is actionable — rather than sanitised at every
// point that renders it.
func TestValidateDeploymentID(t *testing.T) {
	cases := []struct {
		name    string
		id      string
		wantErr bool
		// reason names what the case defends, so a failure says why it matters.
		reason string
	}{
		{
			name:   "empty is the default and always valid",
			id:     "",
			reason: "the overwhelming majority of deployments have no label; absence is never fabricated",
		},
		{
			name:   "ordinary operator label",
			id:     "eu-west-1 staging",
			reason: "spaces are printable and this is the shape operators actually use",
		},
		{
			name:   "at the length bound",
			id:     strings.Repeat("a", maxDeploymentIDLen),
			reason: "the bound is inclusive",
		},
		{
			name:    "over the length bound",
			id:      strings.Repeat("a", maxDeploymentIDLen+1),
			wantErr: true,
			reason:  "the label rides every GetServerInfo, which the SDK polls on its status heartbeat",
		},
		{
			name:    "newline would forge a log line",
			id:      "prod\nFATAL: compromised",
			wantErr: true,
			reason:  "a label that can inject a line break can forge structure in anything that logs it",
		},
		{
			name:    "carriage return",
			id:      "prod\rstaging",
			wantErr: true,
			reason:  "CR overwrites a rendered line the same way LF forges one",
		},
		{
			name:    "terminal escape sequence",
			id:      "prod\x1b[31mDANGER",
			wantErr: true,
			reason:  "mecatui renders this label in an alt-screen TUI; ESC must never reach it",
		},
		{
			name:    "NUL byte",
			id:      "prod\x00",
			wantErr: true,
			reason:  "a NUL truncates the value in any C-string consumer downstream",
		},
		{
			name:    "invalid UTF-8",
			id:      "prod\xe2",
			wantErr: true,
			reason:  "a protobuf string field REJECTS invalid UTF-8 at marshal time and would kill the response",
		},
		{
			name:    "whitespace only",
			id:      "   ",
			wantErr: true,
			reason:  "a blank label is indistinguishable from unset to a reader; omitting the flag is the honest form",
		},
		{
			name:    "unicode line separator",
			id:      "prod staging",
			wantErr: true,
			reason:  "U+2028 is a line break to a JS client even though it is not \\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDeploymentID(tc.id)
			if tc.wantErr && err == nil {
				t.Fatalf("validateDeploymentID(%q) = nil, want an error: %s", tc.id, tc.reason)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("validateDeploymentID(%q) = %v, want nil: %s", tc.id, err, tc.reason)
			}
		})
	}
}

// TestValidateDeploymentIDRejectionNamesTheFlag checks the operator-facing
// message identifies the flag. A startup refusal that does not name what to fix
// is a worse failure than the one it prevents.
func TestValidateDeploymentIDRejectionNamesTheFlag(t *testing.T) {
	err := validateDeploymentID("bad\nvalue")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "--deployment-id") {
		t.Errorf("error %q does not name --deployment-id; an operator cannot act on it", err)
	}
}
