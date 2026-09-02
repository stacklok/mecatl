package main

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/adrg/xdg"

	"github.com/stacklok/mecatl/internal/adapter/clientauth"
)

func TestOAuthProtectedResource_Scenario5_SavedConnect(t *testing.T) {
	oldConfigHome := xdg.ConfigHome
	xdg.ConfigHome = t.TempDir()
	t.Cleanup(func() { xdg.ConfigHome = oldConfigHome })

	confirmed := clientauth.Connection{
		Identity: clientauth.Identity{
			Target: "grpc.example.com:7443", Issuer: "https://issuer.example.com",
			ClientID: "client", Audience: "audience", RedirectURI: "http://127.0.0.1/oauth/callback", Scopes: []string{"openid", "profile"},
		},
		ResourceURL:         "https://api.example.com/service",
		IssuerAddressPolicy: clientauth.IssuerAddressPolicyPublic,
	}
	registry, err := clientauth.OpenRegistry(filepath.Join(xdg.ConfigHome, "mecatl"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Upsert(confirmed); err != nil {
		t.Fatal(err)
	}

	got, err := savedConnection("https://api.example.com/service")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, confirmed) {
		t.Fatalf("saved connect did not use the confirmed tuple: got %#v want %#v", got, confirmed)
	}
}
