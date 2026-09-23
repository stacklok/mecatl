package boatenv

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

func TestProviderLifecycleAndWorkspace(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	provider := fakeProvider(t, fake)

	ctx := context.Background()
	binding, err := provider.Bind(ctx, server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Ref.Kind != Kind || !binding.Ref.Valid() {
		t.Fatalf("ref = %+v", binding.Ref)
	}
	if got := fake.lastCreateNoEnv(); !got {
		t.Fatal("Boat create did not force noEnv=true")
	}

	ws := binding.Environment.Workspace()
	if ws == nil {
		t.Fatal("nil workspace")
	}
	runner := binding.Environment.CommandRunner()
	if runner == nil {
		t.Fatal("nil runner")
	}
	bound, ok := runner.(interface{ BoundWorkspaceRoot() string })
	if !ok {
		t.Fatal("runner does not expose its bound workspace root")
	}
	if bound.BoundWorkspaceRoot() != ws.Root() {
		t.Fatalf("runner/workspace affinity mismatch: runner=%q workspace=%q", bound.BoundWorkspaceRoot(), ws.Root())
	}

	v1, err := ws.CreateFile(ctx, "dir/a.txt", []byte("one\ntwo\n"))
	if err != nil {
		t.Fatal(err)
	}
	data, readVersion, err := ws.ReadVersion(ctx, "dir/a.txt")
	if err != nil || string(data) != "one\ntwo\n" || !v1.Equal(readVersion) {
		t.Fatalf("read = %q, versionEqual=%v, err=%v", data, v1.Equal(readVersion), err)
	}
	if _, err := ws.ReplaceFile(ctx, "dir/a.txt", tool.NewFileVersion("stale"), []byte("bad")); err == nil {
		t.Fatal("stale replace succeeded")
	} else {
		var mismatch *tool.VersionMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("stale replace error = %T %v", err, err)
		}
	}
	v2, err := ws.ReplaceFile(ctx, "dir/a.txt", readVersion, []byte("two\nneedle\n"))
	if err != nil || v2.Equal(readVersion) {
		t.Fatalf("replace version=%v err=%v", v2, err)
	}

	if _, err := ws.CreateFile(ctx, "dir/a.txt", []byte("duplicate")); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("duplicate create = %v", err)
	}
	if _, err := ws.Read(ctx, "../escape"); err == nil {
		t.Fatal("path escape accepted")
	}

	ns, ok := ws.(tool.WorkspaceNamespace)
	if !ok {
		t.Fatal("Boat workspace does not expose namespace operations")
	}
	if _, err := ns.CopyFile(ctx, "dir/a.txt", "dir/b.txt"); err != nil {
		t.Fatal(err)
	}
	entries, err := ns.ReadDir(ctx, "dir")
	if err != nil || len(entries) != 2 {
		t.Fatalf("readdir = %+v, err=%v", entries, err)
	}
	matches, err := ws.Grep(ctx, "needle", "**/*.txt")
	if err != nil || len(matches) != 2 {
		t.Fatalf("grep = %+v, err=%v", matches, err)
	}
	paths, err := ws.Glob(ctx, "**/*.txt")
	if err != nil || len(paths) != 2 {
		t.Fatalf("glob = %+v, err=%v", paths, err)
	}
	if err := ns.Rename(ctx, "dir/b.txt", "dir/c.txt"); err != nil {
		t.Fatal(err)
	}
	if err := ns.Remove(ctx, "dir/c.txt"); err != nil {
		t.Fatal(err)
	}

	if _, err := runner.Run(ctx, "printf shell > shell.txt"); err != nil {
		t.Fatal(err)
	}
	if got, err := ws.Read(ctx, "shell.txt"); err != nil || string(got) != "shell" {
		t.Fatalf("shell/read affinity = %q, %v", got, err)
	}

	if binding.Close == nil {
		t.Fatal("binding has no provisional close")
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	// Close schedules archival instead of archiving: the run that follows
	// CreateSession must find the sandbox warm.
	if state, ttl := fake.state(binding.Ref.ID), fake.lastTTL(); state != "idle" || ttl != min(closeGraceSeconds, 60) {
		t.Fatalf("after close: state=%q ttl=%d, want a running sandbox with the grace TTL", state, ttl)
	}
	// Let the grace expire, as the service would if nothing reattached.
	fake.setState(fake.sandboxes[binding.Ref.ID], "archived")

	rebound, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: binding.Ref, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	// Reattach alone must not wake (and bill) the sandbox; Root() is derived
	// from the ref without contacting the guest.
	if state := fake.state(binding.Ref.ID); state != "archived" || fake.resumeCount() != 0 {
		t.Fatalf("reattach resumed eagerly: state=%q resumes=%d", state, fake.resumeCount())
	}
	if rebound.Environment.Workspace().Root() != ws.Root() {
		t.Fatalf("reattached root = %q, want %q", rebound.Environment.Workspace().Root(), ws.Root())
	}
	if got, err := rebound.Environment.Workspace().Read(ctx, "shell.txt"); err != nil || string(got) != "shell" {
		t.Fatalf("reattached content = %q, %v", got, err)
	}
	if state := fake.state(binding.Ref.ID); state != "idle" || fake.resumeCount() != 1 {
		t.Fatalf("first operation after reattach: state=%q resumes=%d, want one resume", state, fake.resumeCount())
	}
	if ttl := fake.resumes[0].TTLSeconds; ttl != 60 {
		t.Fatalf("resume ttlSeconds = %d, want the configured 60 so an idle sandbox is re-archived", ttl)
	}

	stale := binding.Ref
	stale.Revision = "old"
	if _, err := provider.Reattach(ctx, server.PlacementReattachRequest{Ref: stale, Scope: "test"}); !errors.Is(err, server.ErrPlacementStale) {
		t.Fatalf("stale reattach = %v", err)
	}
}

func TestConcurrentReplaceHasOneWinner(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	provider := fakeProvider(t, fake)
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = binding.Close() }()
	ws := binding.Environment.Workspace()
	v, err := ws.CreateFile(context.Background(), "race.txt", []byte("base"))
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	for _, text := range []string{"left", "right"} {
		text := text
		go func() {
			<-start
			_, err := ws.ReplaceFile(context.Background(), "race.txt", v, []byte(text))
			results <- err
		}()
	}
	close(start)
	var success, conflict int
	for range 2 {
		err := <-results
		if err == nil {
			success++
			continue
		}
		var mismatch *tool.VersionMismatchError
		if errors.As(err, &mismatch) {
			conflict++
		} else {
			t.Fatalf("unexpected replace error: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestNoFSAndConfigurationGuards(t *testing.T) {
	fake := newFakeBoatAPI(t)
	provider := fakeProvider(t, fake)
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{Selector: server.NoFSPlacement(), Scope: "test", Operation: server.PlacementOperationCreate})
	if err != nil || binding.Ref.Kind != session.EnvKindNoFS || binding.Environment.CommandRunner() != nil {
		t.Fatalf("no-fs binding=%+v err=%v", binding.Ref, err)
	}
	if fake.createCount() != 0 {
		t.Fatal("no-fs binding provisioned a sandbox")
	}

	if _, err := New(Config{Scope: "test"}); err == nil {
		t.Fatal("missing API key accepted")
	}
	if _, err := New(Config{APIKey: "secret", Scope: "test", Workdir: "../escape"}); err == nil {
		t.Fatal("escaping workdir accepted")
	}
}

func requireLocalHelper(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("local Boat helper contract fixture requires a POSIX host")
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required to exercise the same helper used inside a Boat sandbox")
	}
}
