package virtiofs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

var errFakeReadOnlyMount = errors.New("fake guest: host-read-only mount")

func TestMicroVMEnvironments_Scenario3_WorktreeIsBidirectionallyVisible(t *testing.T) {
	worktreeRoot := canonicalTestTempDir(t)
	metadataRoot := canonicalTestTempDir(t)
	objectsRoot := canonicalTestTempDir(t)
	for _, root := range []string{worktreeRoot, metadataRoot} {
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prepared := &worktree.Prepared{
		WorktreePath:      worktreeRoot,
		MetadataPath:      metadataRoot,
		CommonObjectStore: objectsRoot,
	}

	plan, err := Plan(prepared, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	guest := newFakeGuest()
	if err := plan.Configure(guest); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if !slices.ContainsFunc(guest.calls, func(c apiCall) bool {
		return c.method == "krun_add_virtiofs" && c.guestPath == worktree.GuestWorkspace && !c.readOnly
	}) {
		t.Fatalf("calls = %#v, want read-write /workspace device", guest.calls)
	}
	if !slices.ContainsFunc(guest.calls, func(c apiCall) bool {
		return c.method == "krun_add_virtiofs3" && c.guestPath == worktree.GuestObjectStore && c.readOnly
	}) {
		t.Fatalf("calls = %#v, want host-read-only Git object device", guest.calls)
	}

	if err := plan.PrepareWorkloadAccess(); err != nil {
		t.Fatalf("PrepareWorkloadAccess: %v", err)
	}
	for _, root := range []string{worktreeRoot, metadataRoot} {
		info, err := os.Stat(root)
		if err != nil || info.Mode().Perm()&0o007 != 0 {
			t.Fatalf("read-write mount root %s was widened: mode = %v, %v", root, info.Mode().Perm(), err)
		}
	}
	mounts := plan.GoMicroVMMounts()
	for _, mount := range mounts {
		if mount.OverrideUID != 0 || mount.OverrideGID != 0 {
			t.Fatalf("mount %s uses host-specific ownership overrides: %d:%d", mount.Tag, mount.OverrideUID, mount.OverrideGID)
		}
	}

	if err := guest.WriteFile("/workspace/from-guest.txt", []byte("guest\n")); err != nil {
		t.Fatalf("guest write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(worktreeRoot, "from-guest.txt"))
	if err != nil || string(got) != "guest\n" {
		t.Fatalf("host sees guest write = %q, %v", got, err)
	}

	if err := os.WriteFile(filepath.Join(worktreeRoot, "from-host.txt"), []byte("host\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = guest.ReadFile("/workspace/from-host.txt")
	if err != nil || string(got) != "host\n" {
		t.Fatalf("guest sees host write = %q, %v", got, err)
	}
}

func TestMicroVMEnvironments_Scenario3_SkillAssetsAreExplicitAndReadOnly(t *testing.T) {
	home := canonicalTestTempDir(t)
	secret := filepath.Join(home, ".config", "credentials")
	writeFile(t, secret, []byte("do-not-mount\n"))
	assetSource := filepath.Join(home, ".claude", "skills", "review", "references", "guide.txt")
	writeFile(t, assetSource, []byte("review guide\n"))
	symlinkAsset := filepath.Join(home, "asset-link")
	if err := os.Symlink(secret, symlinkAsset); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializeAssets(filepath.Join(canonicalTestTempDir(t), "linked"), []Asset{{Name: "secret", SourcePath: symlinkAsset}}); err == nil {
		t.Fatal("MaterializeAssets accepted a symlink source")
	}

	materialized, err := MaterializeAssets(filepath.Join(canonicalTestTempDir(t), "materialized"), []Asset{
		{Name: "review/references/guide.txt", SourcePath: assetSource},
	})
	if err != nil {
		t.Fatalf("MaterializeAssets: %v", err)
	}
	prepared := &worktree.Prepared{
		WorktreePath:      canonicalTestTempDir(t),
		MetadataPath:      canonicalTestTempDir(t),
		CommonObjectStore: canonicalTestTempDir(t),
	}
	plan, err := Plan(prepared, materialized)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	guest := newFakeGuest()
	if err := plan.Configure(guest); err != nil {
		t.Fatalf("Configure: %v", err)
	}

	got, err := guest.ReadFile("/run/mecatl/skill-assets/review/references/guide.txt")
	if err != nil || string(got) != "review guide\n" {
		t.Fatalf("guest asset = %q, %v", got, err)
	}
	if err := guest.WriteFile("/run/mecatl/skill-assets/review/references/guide.txt", []byte("changed")); !errors.Is(err, errFakeReadOnlyMount) {
		t.Fatalf("asset write error = %v, want a read-only-mount error", err)
	}
	if _, err := guest.ReadFile("/run/mecatl/skill-assets/.config/credentials"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("arbitrary home config is discoverable: %v", err)
	}
	if slices.ContainsFunc(guest.mounts, func(m configuredMount) bool {
		return m.hostPath == home || m.hostPath == filepath.Dir(home)
	}) {
		t.Fatalf("mounts expose arbitrary home: %#v", guest.mounts)
	}
	if !slices.ContainsFunc(guest.calls, func(c apiCall) bool {
		return c.method == "krun_add_virtiofs3" && c.guestPath == GuestSkillAssets && c.readOnly
	}) {
		t.Fatalf("calls = %#v, want libkrun host-enforced read-only API", guest.calls)
	}
}

func canonicalTestTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("canonicalize temporary directory: %v", err)
	}
	return root
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type apiCall struct {
	method, tag, hostPath, guestPath string
	readOnly                         bool
}

type configuredMount struct {
	hostPath, guestPath string
	readOnly            bool
}

type fakeGuest struct {
	calls   []apiCall
	mounts  []configuredMount
	targets map[string]string
}

func newFakeGuest() *fakeGuest { return &fakeGuest{targets: make(map[string]string)} }

func (g *fakeGuest) SetGuestVirtioFSMount(tag, guestPath string) error {
	g.targets[tag] = guestPath
	return nil
}

func (g *fakeGuest) AddVirtioFS(tag, hostPath string) error {
	guestPath := g.targets[tag]
	g.calls = append(g.calls, apiCall{method: "krun_add_virtiofs", tag: tag, hostPath: hostPath, guestPath: guestPath})
	g.mounts = append(g.mounts, configuredMount{hostPath: hostPath, guestPath: guestPath})
	return nil
}

func (g *fakeGuest) AddVirtioFS3(tag, hostPath string, _ uint64, readOnly bool) error {
	guestPath := g.targets[tag]
	g.calls = append(g.calls, apiCall{method: "krun_add_virtiofs3", tag: tag, hostPath: hostPath, guestPath: guestPath, readOnly: readOnly})
	g.mounts = append(g.mounts, configuredMount{hostPath: hostPath, guestPath: guestPath, readOnly: readOnly})
	return nil
}

func (g *fakeGuest) ReadFile(path string) ([]byte, error) {
	mount, hostPath, ok := g.resolve(path)
	if !ok {
		return nil, os.ErrNotExist
	}
	_ = mount
	return os.ReadFile(hostPath)
}

func (g *fakeGuest) WriteFile(path string, data []byte) error {
	mount, hostPath, ok := g.resolve(path)
	if !ok {
		return os.ErrNotExist
	}
	if mount.readOnly {
		return errFakeReadOnlyMount
	}
	if err := os.MkdirAll(filepath.Dir(hostPath), 0o700); err != nil {
		return err
	}
	return os.WriteFile(hostPath, data, 0o600)
}

func (g *fakeGuest) resolve(path string) (configuredMount, string, bool) {
	for _, mount := range slices.Backward(g.mounts) {
		rel, err := filepath.Rel(mount.guestPath, path)
		if err == nil && rel != ".." && !filepath.IsAbs(rel) && rel != "." && !startsWithDotDot(rel) {
			return mount, filepath.Join(mount.hostPath, rel), true
		}
	}
	return configuredMount{}, "", false
}

func startsWithDotDot(path string) bool {
	return len(path) > 3 && path[:3] == ".."+string(filepath.Separator)
}
