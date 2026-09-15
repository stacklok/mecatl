package microvm

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestMicroVMEnvironments_Scenario6_SameRepoSessionsUseDistinctWorktreesAndVMs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := initScenario6Repository(t, root)
	for _, dir := range []string{filepath.Join(root, "state", "worktrees"), filepath.Join(root, "state", "metadata")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	allocator, err := NewOpaqueIdentityAllocator(filepath.Join(root, "state"), filepath.Join(root, "run"), bytes.NewReader(bytes.Repeat([]byte{0x5a}, 64)))
	if err != nil {
		t.Fatalf("NewOpaqueIdentityAllocator: %v", err)
	}

	type result struct {
		names ResourceNames
		prep  *worktree.Prepared
		err   error
	}
	results := make(chan result, 2)
	for _, sessionID := range []string{"session-prefix-A", "session-prefix-B"} {
		go func() {
			names, allocErr := allocator.AllocateResources(sessionID, "same/repository")
			if allocErr != nil {
				results <- result{err: allocErr}
				return
			}
			prepared, prepErr := worktree.New().Prepare(context.Background(), worktree.Request{Source: source, WorktreePath: names.WorktreePath, MetadataPath: names.MetadataPath, Branch: names.Branch})
			results <- result{names: names, prep: prepared, err: prepErr}
		}()
	}
	first, second := <-results, <-results
	for _, got := range []result{first, second} {
		if got.err != nil {
			t.Fatalf("concurrent create: %v", got.err)
		}
		t.Cleanup(func() { _ = got.prep.Cleanup(context.Background()) })
	}
	if first.names.WorktreePath == second.names.WorktreePath || first.names.MetadataPath == second.names.MetadataPath || first.names.Branch == second.names.Branch || first.names.Identity.EnvironmentID == second.names.Identity.EnvironmentID || first.names.Identity.VMID == second.names.Identity.VMID || first.names.Identity.Endpoint == second.names.Identity.Endpoint || first.names.Identity.Generation == second.names.Identity.Generation {
		t.Fatalf("session resources collided:\nfirst=%+v\nsecond=%+v", first.names, second.names)
	}
	for i, got := range []result{first, second} {
		if err := os.WriteFile(filepath.Join(got.prep.WorktreePath, fmt.Sprintf("session-%d.txt", i)), []byte(got.names.Identity.EnvironmentID), 0o600); err != nil {
			t.Fatalf("write session worktree: %v", err)
		}
		gitDir := strings.TrimSpace(runGit(t, got.prep.WorktreePath, "rev-parse", "--git-dir"))
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(got.prep.WorktreePath, gitDir)
		}
		if samePath(gitDir, runGit(t, []result{first, second}[1-i].prep.WorktreePath, "rev-parse", "--git-dir")) {
			t.Fatalf("session %d shares Git index directory", i)
		}
	}
}

func TestMicroVMEnvironments_Scenario6_SiblingSessionsCannotMutateEachOther(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	source := initScenario6Repository(t, root)
	for _, dir := range []string{filepath.Join(root, "worktrees"), filepath.Join(root, "metadata")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prepared := make([]*worktree.Prepared, 2)
	for i := range prepared {
		var err error
		prepared[i], err = worktree.New().Prepare(context.Background(), worktree.Request{
			Source: source, WorktreePath: filepath.Join(root, "worktrees", fmt.Sprintf("s%d", i)), MetadataPath: filepath.Join(root, "metadata", fmt.Sprintf("s%d", i)), Branch: fmt.Sprintf("mecatl/s%d", i),
		})
		if err != nil {
			t.Fatalf("Prepare(%d): %v", i, err)
		}
		t.Cleanup(func() { _ = prepared[i].Cleanup(context.Background()) })
		// Simulate the guest mount path while running Git on the host: in the VM
		// /run/mecatl/git-objects resolves to this same host-enforced read-only mount.
		if err := os.WriteFile(filepath.Join(prepared[i].MetadataPath, "objects", "info", "alternates"), []byte(prepared[i].CommonObjectStore+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	a, b := prepared[0], prepared[1]
	if a.CommonObjectStore != b.CommonObjectStore {
		t.Fatalf("object stores differ: %q != %q", a.CommonObjectStore, b.CommonObjectStore)
	}
	for _, p := range prepared {
		mount := p.Mounts[2]
		if mount.HostPath != p.CommonObjectStore || mount.GuestPath != worktree.GuestObjectStore || !mount.ReadOnly {
			t.Fatalf("object-store mount is not shared host-enforced read-only: %+v", mount)
		}
		if got := strings.TrimSpace(runGuestGit(t, p, "cat-file", "-p", "HEAD:tracked.txt")); got != "base" {
			t.Fatalf("shared object read = %q, want base", got)
		}
	}

	if err := os.WriteFile(filepath.Join(a.WorktreePath, "tracked.txt"), []byte("only-a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGuestGit(t, a, "add", "tracked.txt")
	runGuestGit(t, a, "config", "user.session", "a")
	runGuestGit(t, a, "config", "user.name", "Session A")
	runGuestGit(t, a, "config", "user.email", "session-a@example.invalid")
	runGuestGit(t, a, "commit", "-m", "session A")
	if err := os.WriteFile(filepath.Join(a.MetadataPath, "guest-only"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.MetadataPath, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.MetadataPath, "hooks", "pre-commit"), []byte("exit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	if got := strings.TrimSpace(string(mustRead(t, filepath.Join(b.WorktreePath, "tracked.txt")))); got != "base" {
		t.Fatalf("sibling working file changed to %q", got)
	}
	if got := runGuestGit(t, b, "diff", "--cached", "--name-only"); got != "" {
		t.Fatalf("sibling index changed: %q", got)
	}
	if got := runGuestGit(t, b, "config", "--get", "user.session"); got != "" {
		t.Fatalf("sibling config changed: %q", got)
	}
	for _, path := range []string{filepath.Join(b.MetadataPath, "guest-only"), filepath.Join(b.MetadataPath, "hooks", "pre-commit")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("sibling metadata %q was altered: %v", path, err)
		}
	}
	if runGuestGit(t, b, "rev-parse", "HEAD") != runGit(t, source, "rev-parse", "HEAD") {
		t.Fatal("session A commit altered session B's branch ref")
	}
	if runGuestGit(t, b, "rev-parse", "HEAD") == runGuestGit(t, a, "rev-parse", "HEAD") {
		t.Fatal("session A ref mutation was visible in session B")
	}
}

func TestMicroVMEnvironments_Scenario6_ConcurrentResourceNamesAreOpaqueAndConfined(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stateRoot, runtimeRoot := filepath.Join(root, "state"), filepath.Join(root, "run")
	allocator, err := NewOpaqueIdentityAllocator(stateRoot, runtimeRoot, nil)
	if err != nil {
		t.Fatalf("NewOpaqueIdentityAllocator: %v", err)
	}
	inputs := []string{"repo", "repo-1", "../repo", "repo/../../escape", "répo\nname", strings.Repeat("x", 400)}
	var wg sync.WaitGroup
	got := make(chan ResourceNames, len(inputs))
	for _, input := range inputs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			names, allocErr := allocator.AllocateResources("session/../"+input, input)
			if allocErr != nil {
				t.Errorf("AllocateResources(%q): %v", input, allocErr)
				return
			}
			got <- names
		}()
	}
	wg.Wait()
	close(got)

	seen := map[string]bool{}
	for names := range got {
		for _, value := range []string{names.Identity.EnvironmentID, names.Identity.VMID, filepath.Base(names.Identity.Endpoint), filepath.Base(names.WorktreePath), filepath.Base(names.MetadataPath), strings.TrimPrefix(names.Branch, "mecatl/")} {
			if seen[value] {
				t.Fatalf("opaque resource name collided: %q", value)
			}
			seen[value] = true
			if strings.ContainsAny(value, "/\\\n\r") || strings.Contains(value, "..") {
				t.Fatalf("resource name is not opaque: %q", value)
			}
		}
		for path, parent := range map[string]string{names.WorktreePath: filepath.Join(stateRoot, "worktrees"), names.MetadataPath: filepath.Join(stateRoot, "metadata"), names.Identity.Endpoint: runtimeRoot} {
			if !pathWithin(parent, path) {
				t.Fatalf("resource path escaped %q: %q", parent, path)
			}
		}
	}
	if len(seen) != len(inputs)*6 {
		t.Fatalf("allocated %d distinct names, want %d", len(seen), len(inputs)*6)
	}
}

func initScenario6Repository(t *testing.T, root string) string {
	t.Helper()
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "init")
	runGit(t, source, "config", "user.name", "Test")
	runGit(t, source, "config", "user.email", "test@example.invalid")
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "add", "tracked.txt")
	runGit(t, source, "commit", "-m", "base")
	return source
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// config --get uses status 1 for an absent value.
		if len(args) >= 2 && args[0] == "config" && args[1] == "--get" && len(out) == 0 {
			return ""
		}
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func runGuestGit(t *testing.T, prepared *worktree.Prepared, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = prepared.WorktreePath
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0", "GIT_DIR="+prepared.MetadataPath, "GIT_WORK_TREE="+prepared.WorktreePath, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+prepared.CommonObjectStore)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if len(args) >= 2 && args[0] == "config" && args[1] == "--get" && len(out) == 0 {
			return ""
		}
		t.Fatalf("guest git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
func pathWithin(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func samePath(a, b string) bool {
	aa, _ := filepath.Abs(strings.TrimSpace(a))
	bb, _ := filepath.Abs(strings.TrimSpace(b))
	return aa == bb
}
