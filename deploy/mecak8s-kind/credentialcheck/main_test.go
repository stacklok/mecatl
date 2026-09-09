package main

import (
	"os"
	"strings"
	"testing"
)

func TestHeadlessCredentialStorage_Scenario5_KindQualificationContract(t *testing.T) {
	data, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"**destructive**", "task mecak8s:kind-keycloak-apply", "900 seconds", "870 seconds", "mock provider", "two mecak8s replicas", "ephemeral local Redis", "start-dev",
		"client-xdg-linux", "--credential-store=file", "--no-browser", "--issuer https://keycloak.mecatl.svc.cluster.local:8443/realms/mecatl", "--client-id mecatui-kind", "--audience mecak8s", "--private-issuer", "--scopes openid,profile,mecak8s:access,offline_access",
		"./bin/mecatui connect mecak8s-mecak8s.mecatl.svc.cluster.local:18080 sessions", "./bin/mecatui logout mecak8s-mecak8s.mecatl.svc.cluster.local:18080", "http://127.0.0.1:18473/oauth/callback", "CHECK=post-login", "CHECK=refresh", "CHECK=post-logout", "logout before cleanup", "No automatic shared-cluster teardown", "not run",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing qualification contract %q", want)
		}
	}
}

func TestQualificationRejectsUnsafeInputs(t *testing.T) {
	for _, check := range []string{"", "unknown", "post-login;echo unsafe"} {
		if err := checkCredentials(check, t.TempDir(), "example.test:443"); err == nil {
			t.Fatal("invalid check accepted")
		}
	}
	if err := checkCredentials("post-login", t.TempDir(), "example.test:443"); err == nil {
		t.Fatal("non-fixture root accepted")
	}
}
