package boatenv

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/osfs"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// boundFake binds one sandbox on a fresh fake and returns its workspace,
// runner and host root.
func boundFake(t *testing.T, mutate ...func(*Config)) (*fakeBoatAPI, server.PlacementBinding, string) {
	t.Helper()
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	provider := fakeProvider(t, fake, mutate...)
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = binding.Close() })
	return fake, binding, fake.root(binding.Ref.ID)
}

func writeHostFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stagingLeftovers(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, pattern := range []string{"/tmp/mecatl-boat-stage-*", "/tmp/mecatl-boat-snap-*"} {
		m, _ := filepath.Glob(pattern)
		out = append(out, m...)
	}
	return out
}

// Shell honours the caller's deadline instead of a fixed 60s cap, and the
// runner default applies only without one.
func TestCommandTimeoutFollowsCallerDeadline(t *testing.T) {
	now := time.Unix(1000, 0)
	for _, tc := range []struct {
		name    string
		ctx     func() (context.Context, context.CancelFunc)
		seconds int
		capped  bool
	}{
		{"no deadline uses the runner default", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, defaultCommandSeconds, false},
		{"two minutes", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), now.Add(120*time.Second))
		}, 120, false},
		{"sub-second rounds up", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), now.Add(400*time.Millisecond))
		}, 1, false},
		{"above the API ceiling is capped and reported", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), now.Add(20*time.Minute))
		}, maxCommandSeconds, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := tc.ctx()
			defer cancel()
			if seconds, capped := commandSeconds(ctx, now); seconds != tc.seconds || capped != tc.capped {
				t.Fatalf("commandSeconds = %d, %v; want %d, %v", seconds, capped, tc.seconds, tc.capped)
			}
		})
	}

	client, err := newAPIClient("k", "https://boat.dev/api/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 0 {
		t.Fatalf("default HTTP client timeout = %v; a client-wide timeout cuts long commands", client.http.Timeout)
	}

	fake, binding, _ := boundFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	if _, err := binding.Environment.CommandRunner().Run(ctx, "true"); err != nil {
		t.Fatal(err)
	}
	if got := fake.lastTimeout(); got < 149 || got > 150 {
		t.Fatalf("timeoutSeconds sent = %d, want the caller's 150s deadline", got)
	}

	short, cancelShort := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancelShort()
	_, err = binding.Environment.CommandRunner().Run(short, "sleep 10")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out command error = %v, want context.DeadlineExceeded", err)
	}
}

// A signal-killed process has no exit code; it must never read as success.
func TestSignalKilledCommandIsAFailure(t *testing.T) {
	_, binding, _ := boundFake(t)
	res, err := binding.Environment.CommandRunner().Run(context.Background(), "kill -9 $$")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != -1 || !strings.Contains(res.Stderr, "SIGKILL") {
		t.Fatalf("signal-killed result = %+v, want exit -1 naming SIGKILL", res)
	}

	zero := 0
	if got := resolveCommand(commandResponse{Success: false, ExitCode: &zero}); got.ExitCode != -1 {
		t.Fatalf("success:false with exit 0 resolved to %d, want -1", got.ExitCode)
	}
	if got := resolveCommand(commandResponse{Success: true, ExitCode: &zero}); got.ExitCode != 0 || got.Stderr != "" {
		t.Fatalf("clean success resolved to %+v", got)
	}
}

// Content larger than a command line (128 KiB) and larger than one files-API
// write (5 MiB) round-trips through staging and snapshot reads, and nothing
// is left behind in the guest's /tmp.
func TestLargeFilesRoundTrip(t *testing.T) {
	_, binding, _ := boundFake(t)
	ws := binding.Environment.Workspace()
	ctx := context.Background()
	before := len(stagingLeftovers(t))
	for _, size := range []int{300 << 10, 6<<20 + 123} {
		data := make([]byte, size)
		if _, err := rand.Read(data); err != nil {
			t.Fatal(err)
		}
		name := "big-" + strings.Repeat("x", size%7) + ".bin"
		v1, err := ws.CreateFile(ctx, name, data)
		if err != nil {
			t.Fatalf("CreateFile(%d bytes): %v", size, err)
		}
		got, v2, err := ws.ReadVersion(ctx, name)
		if err != nil || !bytes.Equal(got, data) || !v1.Equal(v2) {
			t.Fatalf("ReadVersion(%d bytes): equal=%v versionEqual=%v err=%v", size, bytes.Equal(got, data), v1.Equal(v2), err)
		}
		replacement := append(append([]byte(nil), data...), 'z')
		if _, err := ws.ReplaceFile(ctx, name, v2, replacement); err != nil {
			t.Fatalf("ReplaceFile(%d bytes): %v", size, err)
		}
		if got, err := ws.Read(ctx, name); err != nil || !bytes.Equal(got, replacement) {
			t.Fatalf("Read after replace (%d bytes): %v", size, err)
		}
	}
	if after := stagingLeftovers(t); len(after) != before {
		t.Fatalf("staging/snapshot files left in /tmp: %v", after)
	}
}

// Workspace files must never shadow the helper's own imports.
func TestHelperIgnoresWorkspaceModules(t *testing.T) {
	_, binding, root := boundFake(t)
	for _, mod := range []string{"json.py", "base64.py", "hashlib.py", "os.py"} {
		writeHostFile(t, root, mod, "raise SystemExit('hijacked by workspace module')\n")
	}
	writeHostFile(t, root, "plain.txt", "safe\n")
	if got, err := binding.Environment.Workspace().Read(context.Background(), "plain.txt"); err != nil || string(got) != "safe\n" {
		t.Fatalf("Read with shadowing modules present = %q, %v", got, err)
	}
}

// Symlink semantics match the osfs workspace, checked against osfs itself on
// the very same directory.
func TestSymlinkSemanticsMatchOSFS(t *testing.T) {
	_, binding, root := boundFake(t)
	ws := binding.Environment.Workspace()
	ns := ws.(tool.WorkspaceNamespace)
	ctx := context.Background()
	outside := t.TempDir()
	writeHostFile(t, outside, "secret.txt", "top secret\n")
	writeHostFile(t, root, "target.txt", "hello\n")
	for link, target := range map[string]string{
		"link.txt": "target.txt",
		"evil":     filepath.Join(outside, "secret.txt"),
		"up":       outside,
		"dangling": "missing.txt",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Skipf("symlinks unsupported: %v", err)
		}
	}

	if got, err := ws.Read(ctx, "link.txt"); err != nil || string(got) != "hello\n" {
		t.Fatalf("Read through in-workspace link = %q, %v", got, err)
	}
	if info, err := ws.Stat(ctx, "link.txt"); err != nil || info.Name != "link.txt" {
		t.Fatalf("Stat(link.txt) = %+v, %v; want the requested name", info, err)
	}
	for _, p := range []string{"evil", "up/secret.txt"} {
		if _, err := ws.Read(ctx, p); !errors.Is(err, ErrPathEscape) {
			t.Errorf("Read(%q) = %v, want ErrPathEscape", p, err)
		}
		if _, err := ws.CreateFile(ctx, p, []byte("pwned")); !errors.Is(err, ErrPathEscape) && !errors.Is(err, fs.ErrExist) {
			t.Errorf("CreateFile(%q) = %v, want a refusal", p, err)
		}
		if _, err := ws.ReplaceFile(ctx, p, tool.FileVersion{}, []byte("pwned")); !errors.Is(err, ErrPathEscape) {
			t.Errorf("ReplaceFile(%q) = %v, want ErrPathEscape", p, err)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(data) != "top secret\n" {
		t.Fatalf("outside file modified: %q", data)
	}

	entries, err := ns.ReadDir(ctx, ".")
	if err != nil {
		t.Fatalf("ReadDir with a dangling link = %v, want a listing", err)
	}
	var sawLink bool
	for _, e := range entries {
		if e.Name == "link.txt" {
			sawLink = e.Mode&fs.ModeSymlink != 0
		}
	}
	if !sawLink {
		t.Fatalf("ReadDir(.) = %+v, want link.txt with fs.ModeSymlink", entries)
	}

	if err := ns.Rename(ctx, "link.txt", "moved-link.txt"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "moved-link.txt")); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("Rename moved the target instead of the link: %v", err)
	}
	if err := ns.Remove(ctx, "moved-link.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "target.txt")); err != nil {
		t.Fatalf("Remove(link) deleted the target: %v", err)
	}
	if err := ns.Remove(ctx, "dangling"); err != nil {
		t.Fatalf("Remove(dangling link) = %v", err)
	}

	local, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"*", "**/*", "**"} {
		got, err := ws.Glob(ctx, pattern)
		if err != nil {
			t.Fatalf("Glob(%q): %v", pattern, err)
		}
		want, err := local.Glob(ctx, pattern)
		if err != nil {
			t.Fatalf("osfs Glob(%q): %v", pattern, err)
		}
		if !reflect.DeepEqual(nonNil(got), nonNil(want)) {
			t.Errorf("Glob(%q) = %v, osfs = %v", pattern, got, want)
		}
	}
	for _, pathGlob := range []string{"", "**/*"} {
		hits, err := ws.Grep(ctx, "top secret", pathGlob)
		if err != nil || len(hits) != 0 {
			t.Errorf("Grep(pathGlob=%q) followed an escaping link: %v, %v", pathGlob, hits, err)
		}
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// Glob and Grep agree with the osfs workspace, the production sibling, on the
// same tree for the pattern forms models actually send.
func TestGlobAndGrepMatchOSFS(t *testing.T) {
	_, binding, root := boundFake(t)
	ws := binding.Environment.Workspace()
	ctx := context.Background()
	files := map[string]string{
		"a.ts":                "export const x = 1\n",
		"b.tsx":               "\treturn <div/>\n",
		"src/main.go":         "package main\n\nfunc main() {\n\treturn\n}\n",
		"src/a/one.go":        "// Kelvin K and Straße\nvar HTTPError = 1\n",
		"src/b/two.go":        "x := 42 ٣\n  return x\n",
		"src/sub/deep/x.txt":  "page one\fpage two\nwindows line\r\nlast",
		"docs/readme.md":      "colour color colr\nfoo bar foobar\n",
		"bin/blob.bin":        "needle\x00binary",
		"empty.txt":           "",
		"src/sub/.hidden.txt": "hidden return\n",
	}
	for rel, content := range files {
		writeHostFile(t, root, rel, content)
	}
	local, err := osfs.NewWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	for _, pattern := range []string{
		"**/*.{ts,tsx}", "/src/**/*.go", "./src/*.go", "src/**", "**", "*", "[^a]*",
		"src/{a,b}/*.go", "**/sub/**", "nonexistent/*", "src/*/", "**/*.go", "*.ts",
	} {
		got, gotErr := ws.Glob(ctx, pattern)
		want, wantErr := local.Glob(ctx, pattern)
		if (gotErr == nil) != (wantErr == nil) || !reflect.DeepEqual(nonNil(got), nonNil(want)) {
			t.Errorf("Glob(%q) = %v, %v; osfs = %v, %v", pattern, got, gotErr, want, wantErr)
		}
	}

	for _, tc := range []struct{ pattern, pathGlob string }{
		{`[[:space:]]+return`, ""}, {`\d+`, ""}, {`\w+Error`, ""}, {`(?i)HTTPERROR`, ""},
		{`\bfoo\b`, ""}, {`^func `, ""}, {`two$`, ""}, {`\p{Lu}\w+`, ""}, {`colou?r`, ""},
		{`(?i)k`, "src/**"}, {`page`, ""}, {`line\r`, ""}, {`needle`, ""}, {`^$`, "*.txt"},
		{`(?i)STRASSE|straße`, "**/*.go"}, {`[^\x00-\x7f]`, ""}, {`return`, "src/**/*.go"},
		{`x{2,}|4{1,2}`, ""}, {`(?s)one.`, ""}, {`\Qx :=\E`, ""},
	} {
		got, gotErr := ws.Grep(ctx, tc.pattern, tc.pathGlob)
		want, wantErr := local.Grep(ctx, tc.pattern, tc.pathGlob)
		if (gotErr == nil) != (wantErr == nil) || !reflect.DeepEqual(grepKeys(got), grepKeys(want)) {
			t.Errorf("Grep(%q, %q) = %v, %v; osfs = %v, %v", tc.pattern, tc.pathGlob, grepKeys(got), gotErr, grepKeys(want), wantErr)
		}
	}
	for _, bad := range []string{`a(?=b)`, `(unclosed`, `\`} {
		if _, err := ws.Grep(ctx, bad, ""); err == nil || !strings.Contains(err.Error(), "invalid grep pattern") {
			t.Errorf("Grep(%q) = %v, want the RE2 compile error", bad, err)
		}
	}
}

// grepKeys compares path, line and text; osfs keeps a line's raw bytes and so
// does the helper for valid UTF-8, which is all this corpus contains.
func grepKeys(matches []tool.GrepMatch) []string {
	out := []string{}
	for _, m := range matches {
		out = append(out, m.Path+":"+strconv.Itoa(m.Line)+":"+m.Text)
	}
	return out
}

// The grep match cap and file budget match osfs.
func TestGrepBudgetsMatchOSFS(t *testing.T) {
	_, binding, root := boundFake(t)
	ws := binding.Environment.Workspace()
	writeHostFile(t, root, "many.txt", strings.Repeat("hit\n", 500))
	hits, err := ws.Grep(context.Background(), "hit", "")
	if err != nil || len(hits) != grepMatchLimit {
		t.Fatalf("Grep cap = %d, %v; want %d", len(hits), err, grepMatchLimit)
	}
}

// A non-default workdir is created on first use: the live API rejects a cwd
// that does not exist.
func TestNonDefaultWorkdirIsCreated(t *testing.T) {
	_, binding, root := boundFake(t, func(c *Config) { c.Workdir = "projects/app" })
	if _, err := binding.Environment.Workspace().CreateFile(context.Background(), "a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "projects", "app", "a.txt")); err != nil || string(data) != "x" {
		t.Fatalf("file under workdir = %q, %v", data, err)
	}
}

// A sandbox archived behind the adapter's back (TTL, operator) is resumed and
// the refused command retried once; the command never ran twice.
func TestArchivedSandboxIsResumedOnConflict(t *testing.T) {
	fake, binding, root := boundFake(t)
	fake.setState(fake.sandboxes[binding.Ref.ID], "archived")
	res, err := binding.Environment.CommandRunner().Run(context.Background(), "echo run >> ran.txt")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("command after external archive = %+v, %v", res, err)
	}
	if fake.resumeCount() != 1 {
		t.Fatalf("resumes = %d, want 1", fake.resumeCount())
	}
	if data, _ := os.ReadFile(filepath.Join(root, "ran.txt")); string(data) != "run\n" {
		t.Fatalf("command ran %q times, want exactly once", data)
	}
}

// API errors carry the server's reason instead of a bare status code.
func TestAPIErrorsCarryTheServerReason(t *testing.T) {
	_, binding, _ := boundFake(t)
	w := binding.Environment.Workspace().(*workspace)
	_, err := w.sandbox.client.runCommand(context.Background(), w.sandbox.id, "no/such/dir", "true")
	if err == nil || !strings.Contains(err.Error(), "cwd must be an existing directory") {
		t.Fatalf("error = %v, want the server's reason", err)
	}
}

// The client never follows a redirect, so the bearer credential cannot be
// replayed to another host or over plain HTTP.
func TestRedirectsAreRefused(t *testing.T) {
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("Authorization") != ""
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client, err := newAPIClient("secret-key", origin.URL, origin.Client())
	if err != nil {
		t.Fatal(err)
	}
	var status *apiStatusError
	if err := client.me(context.Background()); !errors.As(err, &status) || status.status != http.StatusTemporaryRedirect {
		t.Fatalf("me() through a redirect = %v, want the redirect refused", err)
	}
	if leaked {
		t.Fatal("credential sent to the redirect target")
	}
}

// Reattach resolves a gone sandbox honestly and never resumes on its own.
func TestReattachOfMissingSandbox(t *testing.T) {
	fake, binding, _ := boundFake(t)
	provider := fakeProvider(t, fake)
	gone := binding.Ref
	gone.ID = "sbx-does-not-exist"
	if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: gone, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("reattach of a missing sandbox = %v, want ErrPlacementNotFound", err)
	}
	fake.setState(fake.sandboxes[binding.Ref.ID], "deleted")
	if _, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: binding.Ref, Scope: "test"}); !errors.Is(err, server.ErrPlacementNotFound) {
		t.Fatalf("reattach of a deleted sandbox = %v, want ErrPlacementNotFound", err)
	}
}

// Two bindings of one archived sandbox may race to resume it; the loser's
// conflict is not a failure.
func TestConcurrentResumeIsTolerated(t *testing.T) {
	requireLocalHelper(t)
	fake := newFakeBoatAPI(t)
	provider := fakeProvider(t, fake)
	var conflicts int
	inner := fake.server.Config.Handler
	fake.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/resume") && conflicts == 0 {
			conflicts++
			// Another caller's resume won: the sandbox is already coming up.
			fake.mu.Lock()
			for _, sb := range fake.sandboxes {
				sb.state = "idle"
			}
			fake.mu.Unlock()
			apiError(w, http.StatusConflict, "resume_in_progress", "sandbox is already resuming")
			return
		}
		inner.ServeHTTP(w, r)
	})
	binding, err := provider.Bind(context.Background(), server.PlacementBindRequest{
		Selector: server.DefaultPlacement(), Scope: "test", Operation: server.PlacementOperationCreate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.Close(); err != nil {
		t.Fatal(err)
	}
	fake.setState(fake.sandboxes[binding.Ref.ID], "archived")
	rebound, err := provider.Reattach(context.Background(), server.PlacementReattachRequest{Ref: binding.Ref, Scope: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rebound.Environment.CommandRunner().Run(context.Background(), "true"); err != nil || conflicts != 1 {
		t.Fatalf("run after a lost resume race = %v (conflicts=%d)", err, conflicts)
	}
}
