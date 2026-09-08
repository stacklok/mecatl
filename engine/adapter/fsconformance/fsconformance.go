// Package fsconformance provides a shared conformance test suite for the
// tool.Workspace interface. Adapters (osfs, memfs, ...) call Run with a factory
// that constructs a fresh workspace, and the suite exercises only the
// tool.Workspace interface.
//
// Importing "testing" in a non-_test.go file is intentional here: this is a
// test-helper package whose sole purpose is to be imported by adapter tests,
// the conventional Go pattern for shared conformance suites (cf. testing/fstest).
package fsconformance

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/tool"
)

// nsOf asserts that ws also implements tool.WorkspaceNamespace, which every
// caller of RunNamespace is expected to satisfy (memfs, osfs, redisstore). A
// workspace that does not implement it (ACP, no-fs) is not a RunNamespace
// candidate — those adapters pin their own honest-refusal contract directly.
func nsOf(t *testing.T, ws tool.Workspace) tool.WorkspaceNamespace {
	t.Helper()
	ns, ok := ws.(tool.WorkspaceNamespace)
	if !ok {
		t.Fatalf("%T does not implement tool.WorkspaceNamespace", ws)
	}
	return ns
}

// setFile is the conformance suite's safe setup mutation. It creates a missing
// file or conditionally replaces the version it just read; it never requires an
// unconditional writer capability on tool.Workspace.
func setFile(ctx context.Context, ws tool.Workspace, path string, data []byte) error {
	_, version, err := ws.ReadVersion(ctx, path)
	if errors.Is(err, fs.ErrNotExist) {
		_, err = ws.CreateFile(ctx, path, data)
		return err
	}
	if err != nil {
		return err
	}
	_, err = ws.ReplaceFile(ctx, path, version, data)
	return err
}

// Run executes the shared Workspace conformance table against the workspace
// produced by newWS. newWS must return a fresh, isolated workspace each call.
func Run(t *testing.T, newWS func(t *testing.T) tool.Workspace) {
	t.Helper()
	ctx := context.Background()

	t.Run("create-read round trip", func(t *testing.T) {
		ws := newWS(t)
		want := []byte("hello world\n")
		if err := setFile(ctx, ws, "dir/file.txt", want); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := ws.Read(ctx, "dir/file.txt")
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if string(got) != string(want) {
			t.Fatalf("round trip = %q want %q", got, want)
		}
	})

	t.Run("stat", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "a/b.txt", []byte("12345")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		fi, err := ws.Stat(ctx, "a/b.txt")
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if fi.Name != "b.txt" {
			t.Errorf("Name = %q want b.txt", fi.Name)
		}
		if fi.Size != 5 {
			t.Errorf("Size = %d want 5", fi.Size)
		}
		if fi.IsDir {
			t.Errorf("IsDir = true want false")
		}
	})

	t.Run("glob", func(t *testing.T) {
		ws := newWS(t)
		for _, p := range []string{"x/one.go", "x/two.go", "x/three.txt"} {
			if err := setFile(ctx, ws, p, []byte("z")); err != nil {
				t.Fatalf("Write %s: %v", p, err)
			}
		}
		got, err := ws.Glob(ctx, "x/*.go")
		if err != nil {
			t.Fatalf("Glob: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("Glob returned %d matches (%v) want 2", len(got), got)
		}
		for _, m := range got {
			if !strings.HasSuffix(m, ".go") {
				t.Errorf("unexpected glob match %q", m)
			}
		}
	})

	t.Run("glob globstar recurses", func(t *testing.T) {
		ws := newWS(t)
		// Files at varying depths plus a non-.go file. "**/*.go" must match the
		// Go files at every depth and exclude the non-.go file. This pins the
		// "**" globstar capability on every adapter (osfs AND memfs) and prevents
		// the two from diverging again.
		for _, p := range []string{"top.go", "x/y/z.go", "x/notes.md"} {
			if err := setFile(ctx, ws, p, []byte("z")); err != nil {
				t.Fatalf("Write %s: %v", p, err)
			}
		}
		got, err := ws.Glob(ctx, "**/*.go")
		if err != nil {
			t.Fatalf("Glob(**/*.go): %v", err)
		}
		set := map[string]bool{}
		for _, m := range got {
			set[m] = true
		}
		if !set["top.go"] {
			t.Errorf("Glob(**/*.go) = %v, missing top-level top.go", got)
		}
		if !set["x/y/z.go"] {
			t.Errorf("Glob(**/*.go) = %v, missing depth-2 x/y/z.go", got)
		}
		if set["x/notes.md"] {
			t.Errorf("Glob(**/*.go) = %v, surfaced non-Go x/notes.md", got)
		}
	})

	t.Run("path escape rejected", func(t *testing.T) {
		ws := newWS(t)
		escapes := []string{
			"../etc/passwd",
			"../../secret",
			"/etc/passwd",
			"a/../../b",
		}
		for _, p := range escapes {
			if _, err := ws.Read(ctx, p); err == nil {
				t.Errorf("Read(%q) = nil err, want escape rejection", p)
			}
			if err := setFile(ctx, ws, p, []byte("x")); err == nil {
				t.Errorf("Write(%q) = nil err, want escape rejection", p)
			}
			if _, err := ws.Stat(ctx, p); err == nil {
				t.Errorf("Stat(%q) = nil err, want escape rejection", p)
			}
		}
	})

	t.Run("version-bearing reads are stable and content-sensitive", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "led.txt", []byte("original")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		data, ver, err := ws.ReadVersion(ctx, "led.txt")
		if err != nil {
			t.Fatalf("ReadVersion: %v", err)
		}
		if string(data) != "original" {
			t.Fatalf("ReadVersion content = %q want %q", data, "original")
		}
		if _, stable, err := ws.ReadVersion(ctx, "led.txt"); err != nil {
			t.Fatalf("second ReadVersion: %v", err)
		} else if !stable.Equal(ver) {
			t.Fatal("unchanged file returned an unstable FileVersion")
		}
		if err := setFile(ctx, ws, "led.txt", []byte("modified content")); err != nil {
			t.Fatalf("Write change: %v", err)
		}
		if _, cur, err := ws.ReadVersion(ctx, "led.txt"); err != nil {
			t.Fatalf("ReadVersion after change: %v", err)
		} else if cur.Equal(ver) {
			t.Fatal("current version still equals prior version after a content change")
		}
	})

	t.Run("create-only rejects existing path", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "c.txt", []byte("first")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		if _, err := ws.CreateFile(ctx, "c.txt", []byte("second")); err == nil {
			t.Fatal("CreateFile on an existing path = nil err, want a create-conflict error")
		} else if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("CreateFile on existing path err = %v, want an fs.ErrExist-wrapping error", err)
		}
		// A genuinely new path succeeds and returns a version.
		ver, err := ws.CreateFile(ctx, "new.txt", []byte("fresh"))
		if err != nil {
			t.Fatalf("CreateFile new path: %v", err)
		}
		if _, got, err := ws.ReadVersion(ctx, "new.txt"); err != nil {
			t.Fatalf("ReadVersion created path: %v", err)
		} else if !got.Equal(ver) {
			t.Fatal("CreateFile version did not match a ReadVersion of the created file")
		}
	})

	t.Run("concurrent create-only has one winner", func(t *testing.T) {
		ws := newWS(t)
		type outcome struct {
			content string
			version tool.FileVersion
			err     error
		}
		start := make(chan struct{})
		out := make(chan outcome, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for _, content := range []string{"creator-a", "creator-b"} {
			go func() {
				ready.Done()
				<-start
				version, err := ws.CreateFile(ctx, "create-race.txt", []byte(content))
				out <- outcome{content: content, version: version, err: err}
			}()
		}
		ready.Wait()
		close(start)
		first, second := <-out, <-out

		var winner outcome
		switch {
		case first.err == nil && errors.Is(second.err, fs.ErrExist):
			winner = first
		case second.err == nil && errors.Is(first.err, fs.ErrExist):
			winner = second
		default:
			t.Fatalf("concurrent CreateFile outcomes = (%v, %v), want one success and one fs.ErrExist conflict", first.err, second.err)
		}
		data, current, err := ws.ReadVersion(ctx, "create-race.txt")
		if err != nil {
			t.Fatalf("ReadVersion final: %v", err)
		}
		if string(data) != winner.content {
			t.Fatalf("final content = %q, want successful create content %q", data, winner.content)
		}
		if !current.Equal(winner.version) {
			t.Fatal("final version did not match the successful concurrent create")
		}
	})

	t.Run("conditional replace returns new version", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "r.txt", []byte("v1 content")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		_, cur, err := ws.ReadVersion(ctx, "r.txt")
		if err != nil {
			t.Fatalf("ReadVersion: %v", err)
		}
		newVer, err := ws.ReplaceFile(ctx, "r.txt", cur, []byte("v2 content"))
		if err != nil {
			t.Fatalf("ReplaceFile with current version: %v", err)
		}
		if newVer.Equal(cur) {
			t.Fatal("ReplaceFile returned the SAME version; a successful replace mints a new one")
		}
		// The new version matches a fresh ReadVersion of the updated file.
		if _, after, err := ws.ReadVersion(ctx, "r.txt"); err != nil {
			t.Fatalf("ReadVersion after replace: %v", err)
		} else if !after.Equal(newVer) {
			t.Fatal("ReadVersion after replace did not return the ReplaceFile version")
		}
	})

	t.Run("conditional replace rejects stale version", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "s.txt", []byte("original")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		_, ver, err := ws.ReadVersion(ctx, "s.txt")
		if err != nil {
			t.Fatalf("ReadVersion: %v", err)
		}
		// Change the file behind the caller's back so the recorded version is stale.
		if err := setFile(ctx, ws, "s.txt", []byte("changed")); err != nil {
			t.Fatalf("Write change: %v", err)
		}
		_, err = ws.ReplaceFile(ctx, "s.txt", ver, []byte("stale-based"))
		if err == nil {
			t.Fatal("ReplaceFile with a stale version = nil err, want a VersionMismatchError")
		}
		var mismatch *tool.VersionMismatchError
		if !errors.As(err, &mismatch) {
			t.Fatalf("ReplaceFile stale-version err = %v, want *tool.VersionMismatchError", err)
		}
		// The stale replace did not write: the content is still the behind-the-back change.
		data, _, err := ws.ReadVersion(ctx, "s.txt")
		if err != nil {
			t.Fatalf("ReadVersion after rejected replace: %v", err)
		}
		if string(data) != "changed" {
			t.Fatalf("content after rejected stale replace = %q want %q (the stale replace must not have written)", data, "changed")
		}
	})

	t.Run("concurrent conditional replace has one winner", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "race.txt", []byte("base")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		_, base, err := ws.ReadVersion(ctx, "race.txt")
		if err != nil {
			t.Fatalf("ReadVersion: %v", err)
		}

		type outcome struct {
			content string
			version tool.FileVersion
			err     error
		}
		start := make(chan struct{})
		out := make(chan outcome, 2)
		var ready sync.WaitGroup
		ready.Add(2)
		for _, content := range []string{"winner-a", "winner-b"} {
			go func() {
				ready.Done()
				<-start
				version, err := ws.ReplaceFile(ctx, "race.txt", base, []byte(content))
				out <- outcome{content: content, version: version, err: err}
			}()
		}
		ready.Wait()
		close(start)
		first, second := <-out, <-out

		isMismatch := func(err error) bool {
			var mismatch *tool.VersionMismatchError
			return errors.As(err, &mismatch)
		}
		var winner outcome
		switch {
		case first.err == nil && isMismatch(second.err):
			winner = first
		case second.err == nil && isMismatch(first.err):
			winner = second
		default:
			t.Fatalf("concurrent ReplaceFile outcomes = (%v, %v), want one success and one version mismatch", first.err, second.err)
		}
		data, current, err := ws.ReadVersion(ctx, "race.txt")
		if err != nil {
			t.Fatalf("ReadVersion final: %v", err)
		}
		if string(data) != winner.content {
			t.Fatalf("final content = %q, want successful replace content %q", data, winner.content)
		}
		if !current.Equal(winner.version) {
			t.Fatal("final version did not match the successful concurrent replace")
		}
	})

	t.Run("conditional replace rejects missing file", func(t *testing.T) {
		ws := newWS(t)
		if _, err := ws.ReplaceFile(ctx, "absent.txt", tool.FileVersion{}, []byte("x")); err == nil {
			t.Fatal("ReplaceFile on a missing path = nil err, want an fs.ErrNotExist-wrapping error")
		} else if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ReplaceFile on missing path err = %v, want an fs.ErrNotExist-wrapping error", err)
		}
	})

	// A zero FileVersion is NEVER an "any version" sentinel: there is
	// deliberately no wildcard that means "overwrite unconditionally". So
	// ReplaceFile with a zero FileVersion against an EXISTING file must NOT
	// overwrite it — it must fail (a version mismatch, since the existing
	// file's minted version never equals the zero value) and leave the
	// existing content byte-unchanged. This is the load-bearing guard against
	// a caller fabricating an unconditional-overwrite from the zero value.
	t.Run("zero FileVersion never overwrites existing file", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "exists.txt", []byte("original content")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		_, err := ws.ReplaceFile(ctx, "exists.txt", tool.FileVersion{}, []byte("would-be-clobber"))
		if err == nil {
			t.Fatal("ReplaceFile with a zero FileVersion on an existing file = nil err, want a mismatch/error (zero is never an overwrite sentinel)")
		}
		// The content must be unchanged regardless of which error an adapter
		// returns (mismatch is the contract; the point is NO overwrite).
		data, rerr := ws.Read(ctx, "exists.txt")
		if rerr != nil {
			t.Fatalf("Read after zero-version replace: %v", rerr)
		}
		if string(data) != "original content" {
			t.Fatalf("zero FileVersion overwrote the file: content = %q, want %q", data, "original content")
		}
	})
}

// RunNamespace executes the shared tool.WorkspaceNamespace conformance table
// against the workspace produced by newWS. newWS must return a fresh, isolated
// workspace each call, and the workspace it returns must implement
// tool.WorkspaceNamespace (memfs, osfs, redisstore — the POSIX-like namespace
// adapters); a workspace that does not (ACP, no-fs) pins its own contract
// directly rather than through this shared suite. Namespace operations are
// deliberately NOT part of RunNamespace's setup path (setFile stays content-only
// via CreateFile/ReplaceFile); every case here exercises ReadDir/Remove/Rename/
// CopyFile end to end so adapters cannot silently diverge on error-wrapping or
// on the "directories are derived from file paths" contract.
func RunNamespace(t *testing.T, newWS func(t *testing.T) tool.Workspace) {
	t.Helper()
	ctx := context.Background()

	t.Run("ReadDir empty root", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		entries, err := ns.ReadDir(ctx, ".")
		if err != nil {
			t.Fatalf("ReadDir(root) on an empty workspace: %v", err)
		}
		if len(entries) != 0 {
			t.Fatalf("ReadDir(root) on an empty workspace = %v, want empty", entries)
		}
	})

	t.Run("ReadDir lists immediate children sorted, derives subdirectories from paths", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		for _, p := range []string{"b.txt", "a.txt", "sub/nested.txt"} {
			if err := setFile(ctx, ws, p, []byte("x")); err != nil {
				t.Fatalf("setFile %s: %v", p, err)
			}
		}
		entries, err := ns.ReadDir(ctx, ".")
		if err != nil {
			t.Fatalf("ReadDir(root): %v", err)
		}
		if len(entries) != 3 {
			t.Fatalf("ReadDir(root) = %d entries, want 3 (a.txt, b.txt, sub/): %+v", len(entries), entries)
		}
		// Sorted by name: a.txt, b.txt, sub.
		names := []string{entries[0].Name, entries[1].Name, entries[2].Name}
		want := []string{"a.txt", "b.txt", "sub"}
		for i := range want {
			if names[i] != want[i] {
				t.Fatalf("ReadDir(root) names = %v, want sorted %v", names, want)
			}
		}
		var dirEntry *tool.FileInfo
		for i := range entries {
			if entries[i].Name == "sub" {
				dirEntry = &entries[i]
			}
		}
		if dirEntry == nil || !dirEntry.IsDir {
			t.Fatalf("ReadDir(root) sub entry = %+v, want IsDir=true", dirEntry)
		}
		// One level down: only nested.txt.
		subEntries, err := ns.ReadDir(ctx, "sub")
		if err != nil {
			t.Fatalf("ReadDir(sub): %v", err)
		}
		if len(subEntries) != 1 || subEntries[0].Name != "nested.txt" || subEntries[0].IsDir {
			t.Fatalf("ReadDir(sub) = %+v, want one file entry nested.txt", subEntries)
		}
	})

	t.Run("ReadDir on a missing directory is fs.ErrNotExist", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if _, err := ns.ReadDir(ctx, "missing"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("ReadDir(missing) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
	})

	t.Run("ReadDir on a regular file is refused, not silently listed", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "plain.txt", []byte("x")); err != nil {
			t.Fatalf("setFile: %v", err)
		}
		if _, err := ns.ReadDir(ctx, "plain.txt"); err == nil {
			t.Fatal("ReadDir(a regular file) = nil err, want a refusal")
		}
	})

	t.Run("Remove deletes a regular file", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "gone.txt", []byte("x")); err != nil {
			t.Fatalf("setFile: %v", err)
		}
		if err := ns.Remove(ctx, "gone.txt"); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, err := ws.Read(ctx, "gone.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Read after Remove err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
	})

	t.Run("Remove a missing path is fs.ErrNotExist", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := ns.Remove(ctx, "missing.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Remove(missing) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
	})

	// Removal is NEVER recursive: a non-empty directory is refused with
	// tool.ErrDirectoryNotEmpty, and the contained file survives untouched.
	t.Run("Remove a non-empty directory is refused non-recursively", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "sub/keep.txt", []byte("still here")); err != nil {
			t.Fatalf("setFile: %v", err)
		}
		if err := ns.Remove(ctx, "sub"); !errors.Is(err, tool.ErrDirectoryNotEmpty) {
			t.Fatalf("Remove(non-empty dir) err = %v, want errors.Is(_, tool.ErrDirectoryNotEmpty)", err)
		}
		data, err := ws.Read(ctx, "sub/keep.txt")
		if err != nil {
			t.Fatalf("Read sub/keep.txt after refused Remove: %v", err)
		}
		if string(data) != "still here" {
			t.Fatalf("Remove(non-empty dir) mutated content: %q", data)
		}
	})

	// What happens once a directory's LAST file is removed is deliberately NOT
	// asserted here: it is the one point where derived-directory adapters
	// (memfs, redisstore — an empty directory does not exist, per the
	// tool.WorkspaceNamespace doc-comment) and real-directory adapters (osfs —
	// the physical, now-empty directory survives) legitimately diverge. Each
	// adapter pins its own version of this case directly.

	t.Run("Rename moves a regular file without overwriting an existing destination", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "old.txt", []byte("payload")); err != nil {
			t.Fatalf("setFile old.txt: %v", err)
		}
		if err := setFile(ctx, ws, "taken.txt", []byte("occupant")); err != nil {
			t.Fatalf("setFile taken.txt: %v", err)
		}
		if err := ns.Rename(ctx, "old.txt", "taken.txt"); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("Rename onto an existing destination err = %v, want errors.Is(_, fs.ErrExist)", err)
		}
		// The refused rename must not have moved or clobbered anything.
		if data, err := ws.Read(ctx, "old.txt"); err != nil || string(data) != "payload" {
			t.Fatalf("source after refused Rename: data=%q err=%v, want payload/nil", data, err)
		}
		if data, err := ws.Read(ctx, "taken.txt"); err != nil || string(data) != "occupant" {
			t.Fatalf("destination after refused Rename: data=%q err=%v, want occupant/nil", data, err)
		}

		if err := ns.Rename(ctx, "old.txt", "new.txt"); err != nil {
			t.Fatalf("Rename to a free destination: %v", err)
		}
		if _, err := ws.Read(ctx, "old.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("source after successful Rename err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
		data, err := ws.Read(ctx, "new.txt")
		if err != nil || string(data) != "payload" {
			t.Fatalf("destination after successful Rename: data=%q err=%v, want payload/nil", data, err)
		}
	})

	t.Run("Rename a missing source is fs.ErrNotExist", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := ns.Rename(ctx, "missing.txt", "dest.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Rename(missing source) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
	})

	// Rename(source, source) — the source and destination are the SAME path — is
	// a no-clobber case like any other existing destination, and must classify
	// identically whether the shared path exists or is missing: an EXISTING
	// same-path rename is rejected as an existing destination (fs.ErrExist), a
	// MISSING same-path rename is rejected as a missing source (fs.ErrNotExist).
	// Neither case may silently succeed as a no-op.
	t.Run("Rename(source, source) rejects consistently for an existing file and a missing path", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "self.txt", []byte("unchanged")); err != nil {
			t.Fatalf("setFile: %v", err)
		}
		if err := ns.Rename(ctx, "self.txt", "self.txt"); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("Rename(self.txt, self.txt) existing err = %v, want errors.Is(_, fs.ErrExist)", err)
		}
		if data, err := ws.Read(ctx, "self.txt"); err != nil || string(data) != "unchanged" {
			t.Fatalf("self.txt after refused self-rename: data=%q err=%v, want unchanged/nil", data, err)
		}
		if err := ns.Rename(ctx, "missing-self.txt", "missing-self.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Rename(missing-self.txt, missing-self.txt) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
	})

	// A destination whose PARENT path component is an existing regular file
	// (not a directory) must be rejected for both CopyFile and Rename, and the
	// refusal must not disturb the blocking file, the source, or plant anything
	// under the rejected destination.
	t.Run("CopyFile and Rename reject a destination whose parent component is an existing file", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "blocker.txt", []byte("blocking content")); err != nil {
			t.Fatalf("setFile blocker.txt: %v", err)
		}
		if err := setFile(ctx, ws, "copy-src.txt", []byte("copy source")); err != nil {
			t.Fatalf("setFile copy-src.txt: %v", err)
		}
		if err := setFile(ctx, ws, "rename-src.txt", []byte("rename source")); err != nil {
			t.Fatalf("setFile rename-src.txt: %v", err)
		}

		if _, err := ns.CopyFile(ctx, "copy-src.txt", "blocker.txt/dest.txt"); err == nil {
			t.Fatal("CopyFile onto a destination whose parent is an existing file = nil err, want rejection")
		}
		if err := ns.Rename(ctx, "rename-src.txt", "blocker.txt/dest2.txt"); err == nil {
			t.Fatal("Rename onto a destination whose parent is an existing file = nil err, want rejection")
		}

		// Neither rejected mutation disturbed anything: blocker.txt is untouched,
		// both sources survive, and no path landed "under" the blocking file.
		if data, err := ws.Read(ctx, "blocker.txt"); err != nil || string(data) != "blocking content" {
			t.Fatalf("blocker.txt after rejected CopyFile/Rename: data=%q err=%v, want blocking content/nil", data, err)
		}
		if data, err := ws.Read(ctx, "copy-src.txt"); err != nil || string(data) != "copy source" {
			t.Fatalf("copy-src.txt after rejected CopyFile: data=%q err=%v, want copy source/nil", data, err)
		}
		if data, err := ws.Read(ctx, "rename-src.txt"); err != nil || string(data) != "rename source" {
			t.Fatalf("rename-src.txt after rejected Rename: data=%q err=%v, want rename source/nil (must not have moved)", data, err)
		}
	})

	// Renaming a directory moves the WHOLE subtree: every file under the old
	// prefix reappears, byte-identical, under the new prefix.
	t.Run("Rename moves a whole directory subtree", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "src/a.txt", []byte("alpha")); err != nil {
			t.Fatalf("setFile src/a.txt: %v", err)
		}
		if err := setFile(ctx, ws, "src/nested/b.txt", []byte("beta")); err != nil {
			t.Fatalf("setFile src/nested/b.txt: %v", err)
		}
		if err := ns.Rename(ctx, "src", "dst"); err != nil {
			t.Fatalf("Rename(dir): %v", err)
		}
		if _, err := ws.Read(ctx, "src/a.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("old subtree survived Rename: err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
		if data, err := ws.Read(ctx, "dst/a.txt"); err != nil || string(data) != "alpha" {
			t.Fatalf("dst/a.txt after Rename: data=%q err=%v, want alpha/nil", data, err)
		}
		if data, err := ws.Read(ctx, "dst/nested/b.txt"); err != nil || string(data) != "beta" {
			t.Fatalf("dst/nested/b.txt after Rename: data=%q err=%v, want beta/nil", data, err)
		}
	})

	t.Run("CopyFile copies content to a new destination without disturbing the source", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "src.txt", []byte("original")); err != nil {
			t.Fatalf("setFile: %v", err)
		}
		newVer, err := ns.CopyFile(ctx, "src.txt", "dup.txt")
		if err != nil {
			t.Fatalf("CopyFile: %v", err)
		}
		if data, err := ws.Read(ctx, "src.txt"); err != nil || string(data) != "original" {
			t.Fatalf("source after CopyFile: data=%q err=%v, want original/nil (Copy must not remove/alter the source)", data, err)
		}
		data, ver, err := ws.ReadVersion(ctx, "dup.txt")
		if err != nil || string(data) != "original" {
			t.Fatalf("destination after CopyFile: data=%q err=%v, want original/nil", data, err)
		}
		if !ver.Equal(newVer) {
			t.Fatal("CopyFile's returned version did not match a ReadVersion of the new destination")
		}
	})

	t.Run("CopyFile never overwrites an existing destination", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if err := setFile(ctx, ws, "src.txt", []byte("fresh")); err != nil {
			t.Fatalf("setFile src.txt: %v", err)
		}
		if err := setFile(ctx, ws, "dest.txt", []byte("occupant")); err != nil {
			t.Fatalf("setFile dest.txt: %v", err)
		}
		if _, err := ns.CopyFile(ctx, "src.txt", "dest.txt"); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("CopyFile onto an existing destination err = %v, want errors.Is(_, fs.ErrExist)", err)
		}
		data, err := ws.Read(ctx, "dest.txt")
		if err != nil || string(data) != "occupant" {
			t.Fatalf("destination after refused CopyFile: data=%q err=%v, want occupant/nil (no clobber)", data, err)
		}
	})

	t.Run("CopyFile a missing source is fs.ErrNotExist", func(t *testing.T) {
		ws := newWS(t)
		ns := nsOf(t, ws)
		if _, err := ns.CopyFile(ctx, "missing.txt", "dest.txt"); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("CopyFile(missing source) err = %v, want errors.Is(_, fs.ErrNotExist)", err)
		}
	})
}
