package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
)

func TestLogoutOutputIsSecretFreeAndHonestAboutPartialState(t *testing.T) {
	const access = "access-super-secret"
	const refresh = "refresh-super-secret"
	result := clientauth.LogoutResult{
		Target: "gateway.example:443", Entries: 1, RevocationsFailed: 1,
		Issues: []clientauth.LogoutIssue{{Identity: clientauth.Identity{Issuer: "https://issuer.example", ClientID: "mecatui"}, Stage: "credential_changed"}},
	}
	var out bytes.Buffer
	writeLogoutResult(&out, result)
	got := out.String()
	if strings.Contains(got, access) || strings.Contains(got, refresh) {
		t.Fatalf("logout output exposed token material: %q", got)
	}
	for _, want := range []string{"incomplete", "metadata was retained", "revocation was unavailable", "credential_changed"} {
		if !strings.Contains(got, want) {
			t.Errorf("logout output %q does not contain %q", got, want)
		}
	}
}

func TestLogoutOutputOmitsZeroMissingCredentialCount(t *testing.T) {
	cases := []struct {
		name   string
		result clientauth.LogoutResult
		want   string
	}{
		{
			name:   "single credential",
			result: clientauth.LogoutResult{Target: "example.com:443", Entries: 1, CredentialsDeleted: 1, RegistryDeleted: true},
			want:   "removed saved login for example.com:443 (1 credential removed)\n",
		},
		{
			name:   "multiple credentials",
			result: clientauth.LogoutResult{Target: "example.com:443", Entries: 2, CredentialsDeleted: 2, RegistryDeleted: true},
			want:   "removed saved login for example.com:443 (2 credentials removed)\n",
		},
		{
			name:   "missing credential is reported",
			result: clientauth.LogoutResult{Target: "example.com:443", Entries: 1, CredentialsDeleted: 1, CredentialsMissing: 1, RegistryDeleted: true},
			want:   "removed saved login for example.com:443 (1 credential removed, 1 already absent)\n",
		},
		{
			name:   "all credentials already absent",
			result: clientauth.LogoutResult{Target: "example.com:443", Entries: 1, CredentialsDeleted: 0, CredentialsMissing: 1, RegistryDeleted: true},
			want:   "removed saved login for example.com:443 (0 credentials removed, 1 already absent)\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			writeLogoutResult(&out, tc.result)
			got := out.String()
			if got != tc.want {
				t.Fatalf("logout output = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLogoutPublicIssuerUsesManagedPublicPolicy(t *testing.T) {
	client, owned, err := logoutIssuerClient(context.Background(), clientauth.Connection{
		Identity: clientauth.Identity{Issuer: "https://8.8.8.8"}, IssuerAddressPolicy: clientauth.IssuerAddressPolicyPublic,
	})
	if err != nil || client == nil || !owned {
		t.Fatalf("public logout issuer client = (%v, %t, %v), want managed client", client, owned, err)
	}
	client.CloseIdleConnections()
}
