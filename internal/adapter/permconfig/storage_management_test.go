package permconfig

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
)

func TestStorageManagementConfigurationIsStrictOperatorTier(t *testing.T) {
	const operatorYAML = `
storage_management:
  version: 1
  principals:
    - issuer: https://idp.example
      subject: storage-admin
`
	res := newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, envWithExplicit("/operator/settings.yaml", operatorYAML))
	got, err := res.OperatorStorageManagement()
	if err != nil {
		t.Fatalf("OperatorStorageManagement: %v", err)
	}
	if got == nil || len(got.Principals) != 1 || got.Principals[0].Issuer != "https://idp.example" || got.Principals[0].Subject != "storage-admin" {
		t.Fatalf("operator storage management = %+v", got)
	}

	for _, body := range []string{
		"storage_management:\n  version: 1\n  unknown: true\n",
		"storage_management:\n  version: 1\n  principals:\n    - issuer: https://idp.example\n      subject: ''\n",
	} {
		res := newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, envWithExplicit("/operator/settings.yaml", body))
		if _, err := res.OperatorStorageManagement(); err == nil {
			t.Fatalf("invalid storage management config accepted: %q", body)
		}
	}

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, operatorYAML)
	project := newWithEnv(Options{Conventional: true, TrustProject: true}, fakeEnv())
	_ = project.Resolve(context.Background(), ws)
	if got, err := project.OperatorStorageManagement(); err != nil || got != nil {
		t.Fatalf("project-tier storage management became authority: got=%+v err=%v", got, err)
	}
}
