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

	t.Run("read ledger records and looks up version", func(t *testing.T) {
		ws := newWS(t)
		if err := setFile(ctx, ws, "led.txt", []byte("original")); err != nil {
			t.Fatalf("Write: %v", err)
		}
		// Never recorded -> ok=false (I/O-free lookup).
		if _, ok := ws.RecordedVersion("led.txt"); ok {
			t.Fatal("RecordedVersion before record unexpectedly reported ok=true")
		}
		// Read the version-bearing content and record the adapter-minted version.
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
		ws.RecordRead("led.txt", ver)
		if got, ok := ws.RecordedVersion("led.txt"); !ok || !got.Equal(ver) {
			t.Fatalf("RecordedVersion after record did not return the recorded version (ok=%v)", ok)
		}
		// Change the file behind the ledger's back; RecordedVersion is I/O-free
		// and still returns the RECORDED version (the point of the ledger). A
		// caller detects the change by comparing RecordedVersion against a fresh
		// ReadVersion, NOT by re-reading inside RecordedVersion.
		if err := setFile(ctx, ws, "led.txt", []byte("modified content")); err != nil {
			t.Fatalf("Write change: %v", err)
		}
		if got, ok := ws.RecordedVersion("led.txt"); !ok || !got.Equal(ver) {
			t.Fatalf("RecordedVersion changed after a behind-the-back write (ok=%v); the lookup must be I/O-free", ok)
		}
		// A fresh ReadVersion now mints a DIFFERENT version, so the caller's
		// recorded-vs-current comparison detects the change.
		if _, cur, err := ws.ReadVersion(ctx, "led.txt"); err != nil {
			t.Fatalf("ReadVersion after change: %v", err)
		} else if cur.Equal(ver) {
			t.Fatalf("current version still equals recorded version after a behind-the-back change")
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
