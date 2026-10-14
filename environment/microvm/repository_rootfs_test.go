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

func TestRepositoryRootFSMaterializer_ClonesWritableRootFSInIsolation(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	brood := filepath.Join(root, "admitted-brood")
	if err := os.MkdirAll(filepath.Join(brood, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(brood, "etc", "brood-release")
	if err := os.WriteFile(sourceFile, []byte("source\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("brood-release", filepath.Join(brood, "etc", "release-link")); err != nil {
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

	first := filepath.Join(root, "repository-rootfs")
	if err := newRepositoryRootFSMaterializer().Materialize(brood, first, verified.GuestAgent.Path); err != nil {
		t.Fatalf("materialize repository rootfs: %v", err)
	}
	second := filepath.Join(root, "second-rootfs")
	if err := cloneRootFS(first, second); err != nil {
		t.Fatalf("clone materialized rootfs: %v", err)
	}

	for _, rootfs := range []string{brood, first, second} {
		info, err := os.Stat(filepath.Join(rootfs, "etc", "brood-release"))
		if err != nil {
			t.Fatalf("stat %q: %v", rootfs, err)
		}
		if info.Mode().Perm() != 0o755 {
			t.Errorf("mode in %q = %o, want 755", rootfs, info.Mode().Perm())
		}
		link, err := os.Readlink(filepath.Join(rootfs, "etc", "release-link"))
		if err != nil || link != "brood-release" {
			t.Errorf("safe symlink in %q = %q, %v", rootfs, link, err)
		}
	}

	for path, contents := range map[string]string{
		sourceFile: "source changed\n",
		filepath.Join(first, "etc", "brood-release"):  "first changed\n",
		filepath.Join(second, "etc", "brood-release"): "second changed\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatalf("write %q: %v", path, err)
		}
	}
	for path, want := range map[string]string{
		sourceFile: "source changed\n",
		filepath.Join(first, "etc", "brood-release"):  "first changed\n",
		filepath.Join(second, "etc", "brood-release"): "second changed\n",
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Errorf("contents of %q = %q, %v; want %q", path, got, err, want)
		}
	}
}

func TestCloneRootFS_RejectsExistingDestination(t *testing.T) {
	t.Parallel()
	for _, populated := range []bool{false, true} {
		name := "empty"
		if populated {
			name = "populated"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
			for _, path := range []string{source, destination} {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chmod(source, 0o755); err != nil {
				t.Fatal(err)
			}
			if populated {
				if err := os.WriteFile(filepath.Join(destination, "sentinel"), []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := digestTree(destination)
			if err != nil {
				t.Fatal(err)
			}
			if err := cloneRootFS(source, destination); !errors.Is(err, os.ErrExist) {
				t.Errorf("clone into existing destination = %v, want os.ErrExist", err)
			}
			after, err := digestTree(destination)
			if err != nil || after != before {
				t.Errorf("existing destination contents changed: digest %q, %v; want %q", after, err, before)
			}
			info, err := os.Stat(destination)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o700 {
				t.Errorf("existing destination mode = %v, want 0700", info.Mode())
			}
		})
	}
}

func TestCloneRootFS_PreservesModes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source, destination := filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	modes := map[string]os.FileMode{
		".":      os.ModeDir | os.ModeSticky | 0o755,
		"tmp":    os.ModeDir | os.ModeSticky | 0o777,
		"shared": os.ModeDir | os.ModeSetgid | 0o775,
		"tool":   0o755,
		"data":   0o666,
	}
	for name, mode := range modes {
		path := filepath.Join(source, name)
		if name != "." {
			if mode.IsDir() {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("source"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode() != mode {
			t.Fatalf("source mode for %q was not established: %v, %v", name, info, err)
		}
	}
	if err := cloneRootFS(source, destination); err != nil {
		t.Fatal(err)
	}
	for name, want := range modes {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode() != want {
			t.Errorf("mode for %q = %v, want %v", name, info.Mode(), want)
		}
	}
}

func TestCloneRootFS_RejectsEscapingSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := cloneRootFS(source, filepath.Join(root, "destination")); err == nil {
		t.Fatal("clone silently accepted an escaping symlink")
	}
}

func TestCloneRootFS_RejectsInvalidSource(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realSource := filepath.Join(root, "source")
	if err := os.Mkdir(realSource, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceLink := filepath.Join(root, "source-link")
	if err := os.Symlink(realSource, sourceLink); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if err := cloneRootFS(sourceLink, destination); err == nil {
		t.Fatal("clone symlink source succeeded")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed clone created destination: %v", err)
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
