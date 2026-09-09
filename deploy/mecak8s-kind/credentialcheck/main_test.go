package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
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

func TestQualificationTaskWiring(t *testing.T) {
	data, err := os.ReadFile("../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Tasks map[string]map[string]any `yaml:"tasks"`
	}
	if err := yaml.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	task := file.Tasks["kind-credential-storage-check"]
	if task == nil {
		t.Fatal("qualification task missing")
	}
	delete(task, "desc")
	var want map[string]any
	if err := yaml.Unmarshal([]byte(`requires:
  vars: [CHECK, ROOT, TARGET]
env:
  CHECK: '{{.CHECK}}'
  ROOT: '{{.ROOT}}'
  TARGET: '{{.TARGET}}'
cmds:
  - go run ./deploy/mecak8s-kind/credentialcheck
`), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(task, want) {
		t.Fatal("qualification task changed required inputs, env-only passing, command, or added orchestration")
	}
	data, err = os.ReadFile("../../../Taskfile.yml")
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Includes map[string]any `yaml:"includes"`
	}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	var include any
	if err := yaml.Unmarshal([]byte("taskfile: deploy/mecak8s-kind/Taskfile.yml\ndir: .\n"), &include); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(root.Includes["mecak8s"], include) {
		t.Fatal("qualification task not exposed from repository root as mecak8s")
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
