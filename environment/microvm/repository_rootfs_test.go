package microvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/guestexec"
)

func TestRepositoryRootFSMaterializer_ClonesStaticRootFSAndInjectsGuestAgentOnce(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	brood := filepath.Join(root, "admitted-brood")
	if err := os.MkdirAll(filepath.Join(brood, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(brood, "etc", "brood-release"), []byte("direct admitted bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactSources := filepath.Join(root, "artifact-sources")
	if err := os.Mkdir(artifactSources, 0o700); err != nil {
		t.Fatal(err)
	}
	requests, resolver := testArtifactSet(t, artifactSources, nil, "")
	verified, _, err := NewProvisioner(NewVerifiedCache(filepath.Join(root, "verified-cache")), resolver, testPolicy("guest-policy-v1", nil), nil, nil).Verify(t.Context(), requests)
	if err != nil {
		t.Fatalf("independently verify guest-agent release artifact: %v", err)
	}
	guestBytes := []byte("guest-agent-complete")

	materializer := newRepositoryRootFSMaterializer()
	var prepared []string
	destination := filepath.Join(root, "repository-rootfs")
	materializer.prepareOwnership = func(_ context.Context, gotRoot, relative string) error {
		if gotRoot != destination {
			t.Fatalf("ownership root = %q, want %q", gotRoot, destination)
		}
		prepared = append(prepared, relative)
		return nil
	}
	if err := materializer.Materialize(brood, destination, verified.GuestAgent.Path); err != nil {
		t.Fatalf("materialize repository rootfs: %v", err)
	}
	if want := []string{filepath.Join("home", "guest"), "workspace"}; !slices.Equal(prepared, want) {
		t.Fatalf("ownership targets = %q, want only %q", prepared, want)
	}
	for _, target := range prepared {
		if target == "." || strings.HasPrefix(target, "etc") || strings.HasPrefix(target, "usr") {
			t.Fatalf("ownership preparation escaped writable runtime trees: %q", target)
		}
	}
	got, err := os.ReadFile(filepath.Join(destination, guestAgentInstallPath))
	if err != nil || string(got) != string(guestBytes) {
		t.Fatalf("injected guest agent = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(brood, guestAgentInstallPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("admitted Brood rootfs was mutated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destination, "etc", "mecatl", "guest-agent.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("static repository rootfs contains per-boot guest configuration: %v", err)
	}
	if err := materializer.Materialize(brood, filepath.Join(root, "second-repository-rootfs"), verified.GuestAgent.Path); !errors.Is(err, errRepositoryRootFSMaterialized) {
		t.Fatalf("second primitive materialization = %v, want errRepositoryRootFSMaterialized", err)
	}
	if _, err := os.Stat(filepath.Join(root, "second-repository-rootfs")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("second materialization created rootfs bytes: %v", err)
	}
}

func TestMicroVMRedesign_Scenario4_GuestRuntimeContractIsExplicit(t *testing.T) {
	t.Parallel()
	contract := guestexec.DefaultRuntimeContract()
	identity := guestexec.DefaultWorkloadIdentity()
	if contract.Identity != identity || identity.UID != 65532 || identity.GID != 65532 {
		t.Fatalf("workload identity = %+v contract=%+v, want 65532:65532", identity, contract.Identity)
	}
	if contract.Home != "/home/guest" || contract.Workdir != "/workspace" {
		t.Fatalf("HOME/workdir = %q/%q", contract.Home, contract.Workdir)
	}
	for _, required := range []string{"/usr/lib/go/bin", "/home/guest/go/bin", "/home/guest/.cargo/bin", "/home/guest/.local/bin"} {
		if !strings.Contains(":"+contract.Path+":", ":"+required+":") {
			t.Errorf("PATH %q omits Brood/toolchain path %q", contract.Path, required)
		}
	}
	wantCaches := map[string]string{
		"GOCACHE":          "/home/guest/.cache/go-build",
		"GOMODCACHE":       "/home/guest/go/pkg/mod",
		"PIP_CACHE_DIR":    "/home/guest/.cache/pip",
		"npm_config_cache": "/home/guest/.cache/node",
		"CARGO_HOME":       "/home/guest/.cargo",
	}
	for name, path := range wantCaches {
		if contract.Environment[name] != path {
			t.Errorf("%s = %q, want %q", name, contract.Environment[name], path)
		}
	}

	rootfs := filepath.Join(t.TempDir(), "rootfs")
	if err := os.Mkdir(rootfs, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := establishGuestRuntimeContract(rootfs, contract); err != nil {
		t.Fatalf("establish guest runtime contract: %v", err)
	}
	for _, guestPath := range append([]string{contract.Home, contract.Workdir}, contract.CacheDirectories()...) {
		info, err := os.Stat(filepath.Join(rootfs, filepath.FromSlash(strings.TrimPrefix(guestPath, "/"))))
		if err != nil || !info.IsDir() || info.Mode().Perm()&0o200 == 0 {
			t.Errorf("writable runtime directory %q = mode %v, %v", guestPath, infoMode(info), err)
		}
	}
}

func infoMode(info os.FileInfo) os.FileMode {
	if info == nil {
		return 0
	}
	return info.Mode().Perm()
}
