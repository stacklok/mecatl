package mcp

import (
	"context"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCompleteConnectionRejectsMalformedToolCatalog(t *testing.T) {
	for _, name := range []string{"bad\nname", strings.Repeat("x", 257)} {
		t.Run(name, func(t *testing.T) {
			url, remote := newMutableTestServer(t)
			mcpsdk.AddTool(remote, &mcpsdk.Tool{Name: name},
				func(context.Context, *mcpsdk.CallToolRequest, noArgs) (*mcpsdk.CallToolResult, any, error) {
					return &mcpsdk.CallToolResult{}, nil, nil
				})
			s, err := ConnectComplete(t.Context(), ServerConfig{
				Name: "fake", URL: url,
				CandidateBudget: NewCandidateListBudget(32, 32, 1<<20),
			}, &recordingDiag{})
			if s != nil {
				_ = s.Close()
			}
			if err == nil || s != nil {
				t.Fatalf("malformed catalog published: server=%v err=%v", s, err)
			}
		})
	}
}

func TestRemoteToolAuthorityNameValidation(t *testing.T) {
	for _, tc := range []struct {
		name, server, remote string
		valid                bool
	}{
		{"ordinary", "server", "read_file", true},
		{"unicode", "server", "café", true},
		{"exact byte boundary", "s", strings.Repeat("é", 124), true},
		{"over byte boundary", "s", strings.Repeat("é", 124) + "x", false},
		{"empty", "s", "", false},
		{"newline", "s", "read\nfile", false},
		{"nul", "s", "read\x00file", false},
		{"unicode control", "s", "read\u0085file", false},
		{"invalid UTF8", "s", "read\xfffile", false},
		{"server control", "bad\nserver", "read", false},
		{"server invalid UTF8", "bad\xffserver", "read", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newRemoteTool(tc.server, nil, &mcpsdk.Tool{Name: tc.remote})
			if !tc.valid {
				if err == nil || got != nil {
					t.Fatal("malformed authority name admitted into catalog")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Spec().Name != namespacedName(tc.server, tc.remote) || got.remoteName != tc.remote {
				t.Fatal("valid exact-name identity was rewritten")
			}
		})
	}
}
