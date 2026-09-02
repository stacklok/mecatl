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

func TestLogoutPublicIssuerUsesManagedPublicPolicy(t *testing.T) {
	client, owned, err := logoutIssuerClient(context.Background(), clientauth.Connection{
		Identity: clientauth.Identity{Issuer: "https://8.8.8.8"}, IssuerAddressPolicy: clientauth.IssuerAddressPolicyPublic,
	})
	if err != nil || client == nil || !owned {
		t.Fatalf("public logout issuer client = (%v, %t, %v), want managed client", client, owned, err)
	}
	client.CloseIdleConnections()
}
