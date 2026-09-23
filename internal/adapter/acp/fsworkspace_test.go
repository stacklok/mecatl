package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/fstools"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

var acpTestLedgers sync.Map

// testLedger returns a per-fsWorkspace-instance ReadLedger for tests that need
// to record/inspect ledger state directly (the production fsWorkspace carries
// NO ledger of its own — ADR 0281; content and read evidence are independently
// composed at the Environment).
func testLedger(w *fsWorkspace) tool.ReadLedger {
	ledger, _ := acpTestLedgers.LoadOrStore(w, memledger.New())
	return ledger.(tool.ReadLedger)
}

func testRecordRead(ctx context.Context, w *fsWorkspace, path string, version tool.FileVersion) error {
	return testLedger(w).RecordRead(ctx, tool.LedgerKey(w.Root(), path), version)
}

func testRecordedVersion(ctx context.Context, w *fsWorkspace, path string) (tool.FileVersion, bool, error) {
	return testLedger(w).RecordedVersion(ctx, tool.LedgerKey(w.Root(), path))
}

// fsPeer is a scripted ACP CLIENT for fsWorkspace unit tests: it answers the
// agent's outbound fs/read_text_file / fs/write_text_file requests against an
// in-memory buffer map (the editor's "buffers"), so a test can assert that
// Read/Write delegate (and never touch disk) and count the fs/* calls issued.
// It speaks newline-delimited JSON (ndjson) over a pipe, the same as a real editor.
type fsPeer struct {
	t *testing.T

	mu      sync.Mutex
	buffers map[string]string // absolute path -> buffer content

	reads  atomic.Int64 // count of fs/read_text_file requests served
	writes atomic.Int64 // count of fs/write_text_file requests served

	// hang, when true, makes the peer NEVER respond to a request (it reads and
	// drops it), so a test can assert the per-call fs/* timeout fires.
	hang atomic.Bool
	// ambiguousRead returns a structured editor error that is not not-found.
	ambiguousRead atomic.Bool
	// genericReadFailure returns a malformed success payload, making Conn.Call
	// fail with a generic protocol/decode error while leaving the peer write-capable.
	genericReadFailure atomic.Bool
	// readErrorTemplate, when set, is formatted with the requested absolute path
	// and returned as a structured rpcError message.
	readErrorTemplate atomic.Pointer[string]

	toConn   *io.PipeWriter // peer -> conn.r (responses)
	fromConn *bufio.Reader  // conn.w -> peer (the agent's requests)
}

// newFSPeerConn wires a Conn whose outbound Calls are answered by an fsPeer.
// It returns the Conn (already Serve-ing so it can deliver responses), the peer
// (seeded with the given buffers), and a cancel func to stop serving.
func newFSPeerConn(t *testing.T, buffers map[string]string) (*Conn, *fsPeer, context.CancelFunc) {
	t.Helper()
	connReadR, peerWriteW := io.Pipe() // peer writes responses -> conn reads
	peerReadR, connWriteW := io.Pipe() // conn writes requests  -> peer reads
	conn := NewConn(connReadR, connWriteW, nil)
	if buffers == nil {
		buffers = map[string]string{}
	}
	peer := &fsPeer{
		t:        t,
		buffers:  buffers,
		toConn:   peerWriteW,
		fromConn: bufio.NewReader(peerReadR),
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = conn.Serve(ctx) }()
	go peer.loop()
	t.Cleanup(func() {
		cancel()
		_ = peerWriteW.Close()
		_ = connWriteW.Close()
	})
	return conn, peer, cancel
}

// get/set/has manipulate the peer's buffers out of band (simulating the editor's
// own buffer mutations) for the ledger tests.
func (p *fsPeer) get(abs string) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.buffers[abs]
	return v, ok
}

func (p *fsPeer) set(abs, content string) {
	p.mu.Lock()
	p.buffers[abs] = content
	p.mu.Unlock()
}

func (p *fsPeer) setReadErrorTemplate(template string) {
	p.readErrorTemplate.Store(&template)
}

// loop reads the agent's fs/* requests and answers them from the buffer map.
func (p *fsPeer) loop() {
	for {
		raw, err := p.fromConn.ReadBytes('\n')
		line := strings.TrimRight(string(raw), "\r\n")
		if line == "" {
			if err != nil {
				return // EOF / closed pipe on a blank trailing line
			}
			continue // bare blank line between messages
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if uerr := json.Unmarshal([]byte(line), &m); uerr != nil {
			return
		}
		if p.hang.Load() {
			// Read the request but never answer it: the agent's per-call timeout must
			// fire. Keep looping so a later un-hung request could still be served.
			continue
		}
		switch m.Method {
		case methodFSReadTextFile:
			p.reads.Add(1)
			var req fsReadTextFileRequest
			_ = json.Unmarshal(m.Params, &req)
			content, ok := p.get(req.Path)
			if !ok {
				if template := p.readErrorTemplate.Load(); template != nil {
					p.respondErr(m.ID, fmt.Sprintf(*template, req.Path))
					continue
				}
				if p.genericReadFailure.Load() {
					// A malformed success response produces a generic decode/protocol
					// error from Conn.Call without closing this write-capable peer.
					p.respond(m.ID, "temporary read failure")
					continue
				}
				if p.ambiguousRead.Load() {
					// A transport-shaped fault (NOT a clean not-found): exercises the
					// Stat fail-safe path.
					p.respondErr(m.ID, "internal editor error: connection reset")
					continue
				}
				// A clean editor "file does not exist" — the message contains a marker
				// isFSNotFound recognizes, so the agent classifies it as genuinely new.
				p.respondErr(m.ID, fmt.Sprintf("no such file or directory: %s", req.Path))
				continue
			}
			p.respond(m.ID, fsReadTextFileResponse{Content: content})
		case methodFSWriteTextFile:
			p.writes.Add(1)
			var req fsWriteTextFileRequest
			_ = json.Unmarshal(m.Params, &req)
			p.set(req.Path, req.Content)
			p.respond(m.ID, struct{}{})
		default:
			p.respondErr(m.ID, "unexpected method "+m.Method)
		}
	}
}

func (p *fsPeer) respond(id json.RawMessage, result any) {
	raw, _ := json.Marshal(result)
	p.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "result": json.RawMessage(raw)})
}

func (p *fsPeer) respondErr(id json.RawMessage, msg string) {
	p.writeFrame(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": msg}})
}

func (p *fsPeer) writeFrame(m any) {
	body, _ := json.Marshal(m)
	body = append(body, '\n')
	if _, err := p.toConn.Write(body); err != nil {
		// The conn may have closed (test teardown); ignore.
		return
	}
}

// newTestFSWorkspace builds an fsWorkspace over a peer-backed Conn, rooted at a
// fresh tempdir, seeding the editor buffers under their ABSOLUTE paths derived
// from rel -> root. It returns the workspace, the peer, and the resolved root.
func newTestFSWorkspace(t *testing.T, files map[string]string) (*fsWorkspace, *fsPeer, string) {
	t.Helper()
	root := t.TempDir()
	// Seed buffers keyed by the absolute path the workspace will compute (the
	// EvalSymlinks-resolved root, which is what osfs.Root() returns).
	conn, peer, _ := newFSPeerConn(t, nil)
	ws, err := newFSWorkspace(conn, "sess-test", root)
	if err != nil {
		t.Fatalf("newFSWorkspace: %v", err)
	}
	for rel, content := range files {
		peer.set(filepath.Join(ws.Root(), rel), content)
	}
	return ws, peer, ws.Root()
}

// --- T1: Read/Write delegate, not disk --------------------------------------

func TestFSWorkspaceReadWriteDelegate(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, map[string]string{"a.txt": "hello"})

	got, err := ws.Read(ctx, "a.txt")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("Read = %q, want %q", got, "hello")
	}
	if peer.reads.Load() != 1 {
		t.Fatalf("expected 1 fs/read, got %d", peer.reads.Load())
	}

	if err := ws.Write(ctx, "a.txt", []byte("world")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if peer.writes.Load() != 1 {
		t.Fatalf("expected 1 fs/write, got %d", peer.writes.Load())
	}
	// The bytes landed in the peer's buffer (delegated), NOT on disk.
	if v, _ := peer.get(filepath.Join(root, "a.txt")); v != "world" {
		t.Fatalf("peer buffer = %q, want %q", v, "world")
	}
	if _, err := os.Stat(filepath.Join(root, "a.txt")); err == nil {
		t.Fatalf("Write touched disk at %s; it must delegate to the editor", filepath.Join(root, "a.txt"))
	}
}

// --- T2: Edit's three invariants over the delegating workspace ---------------

func TestFSWorkspaceEditInvariants(t *testing.T) {
	ctx := context.Background()
	edit := fstools.EditTool{}

	// (i) edit without prior Read -> rejected (invariant #1).
	ws, peer, root := newTestFSWorkspace(t, map[string]string{"f.go": "package x\nvar A = 1\nvar B = 1\n"})
	res := runEdit(t, edit, ws, "f.go", "var A = 1", "var A = 2", false)
	if !res.IsError {
		t.Fatalf("(i) edit without prior read should be rejected")
	}

	// (ii) Read, then the editor buffer changes out of band, then Edit -> rejected.
	if _, ver, err := ws.ReadVersion(ctx, "f.go"); err != nil {
		t.Fatalf("ReadVersion: %v", err)
	} else {
		testRecordRead(ctx, ws, "f.go", ver)
	}
	peer.set(filepath.Join(root, "f.go"), "package x\nvar A = 999\n") // editor edited the buffer
	res = runEdit(t, edit, ws, "f.go", "var A = 1", "var A = 2", false)
	if !res.IsError {
		t.Fatalf("(ii) edit after buffer changed should be rejected (unchanged-since)")
	}

	// (iii) Read then Edit with a unique old_string -> succeeds; write delegated.
	ws2, peer2, root2 := newTestFSWorkspace(t, map[string]string{"f.go": "package x\nvar A = 1\nvar C = 3\n"})
	if _, ver, err := ws2.ReadVersion(ctx, "f.go"); err != nil {
		t.Fatalf("ReadVersion: %v", err)
	} else {
		testRecordRead(ctx, ws2, "f.go", ver)
	}
	res = runEdit(t, edit, ws2, "f.go", "var A = 1", "var A = 2", false)
	if res.IsError {
		t.Fatalf("(iii) unique edit should succeed, got error: %s", res.Content)
	}
	if v, _ := peer2.get(filepath.Join(root2, "f.go")); !strings.Contains(v, "var A = 2") {
		t.Fatalf("(iii) edit did not land in the peer buffer: %q", v)
	}

	// (iv) non-unique old_string without replace_all -> rejected (invariant #3).
	ws3, _, _ := newTestFSWorkspace(t, map[string]string{"f.go": "dup\ndup\n"})
	if _, ver, err := ws3.ReadVersion(ctx, "f.go"); err != nil {
		t.Fatalf("ReadVersion: %v", err)
	} else {
		testRecordRead(ctx, ws3, "f.go", ver)
	}
	res = runEdit(t, edit, ws3, "f.go", "dup", "x", false)
	if !res.IsError {
		t.Fatalf("(iv) non-unique edit without replace_all should be rejected")
	}

	// (v) old_string not found -> rejected (invariant #2, exact match).
	ws4, _, _ := newTestFSWorkspace(t, map[string]string{"f.go": "alpha\n"})
	if _, ver, err := ws4.ReadVersion(ctx, "f.go"); err != nil {
		t.Fatalf("ReadVersion: %v", err)
	} else {
		testRecordRead(ctx, ws4, "f.go", ver)
	}
	res = runEdit(t, edit, ws4, "f.go", "missing", "x", false)
	if !res.IsError {
		t.Fatalf("(v) edit with absent old_string should be rejected")
	}
}

// runEdit executes the real EditTool against ws and returns the result.
func runEdit(t *testing.T, edit fstools.EditTool, ws *fsWorkspace, path, oldS, newS string, replaceAll bool) session.ToolResult {
	t.Helper()
	args := map[string]any{"path": path, "old_string": oldS, "new_string": newS}
	if replaceAll {
		args["replace_all"] = true
	}
	raw, _ := json.Marshal(args)
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: ws.Root()}, ws, testLedger(ws), nil)
	res, err := edit.Execute(context.Background(), session.ToolCall{ID: "c1", Name: "Edit", Args: raw}, env)
	if err != nil {
		t.Fatalf("edit returned hard error: %v", err)
	}
	return res
}

// --- T3: buffer-keyed ledger -------------------------------------------------

func TestFSWorkspaceBufferKeyedLedger(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, map[string]string{"a.txt": "v1"})

	// Record the buffer version via ReadVersion (no I/O inside RecordRead).
	_, ver, err := ws.ReadVersion(ctx, "a.txt")
	if err != nil {
		t.Fatalf("ReadVersion: %v", err)
	}
	if err := testRecordRead(ctx, ws, "a.txt", ver); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	if got, ok, err := testRecordedVersion(ctx, ws, "a.txt"); err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	} else if !ok {
		t.Fatalf("RecordedVersion not recorded")
	} else if !got.Equal(ver) {
		t.Fatal("RecordedVersion did not return the recorded version")
	}

	// Mutate the editor buffer out of band -> a fresh ReadVersion mints a
	// different version, so the recorded-vs-current comparison detects the change.
	peer.set(filepath.Join(root, "a.txt"), "v2")
	if _, cur, err := ws.ReadVersion(ctx, "a.txt"); err != nil {
		t.Fatalf("ReadVersion after buffer change: %v", err)
	} else if cur.Equal(ver) {
		t.Fatalf("expected the current buffer version to differ after the peer mutated it")
	}

	// A never-recorded path is not in the ledger (ok=false), not an error.
	if _, ok, err := testRecordedVersion(ctx, ws, "never.txt"); err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	} else if ok {
		t.Fatalf("never-recorded path: ok=%v, want false", ok)
	}
}

// RecordRead and RecordedVersion are pure ledger operations backed by
// tool.LedgerKey (the comprehensive convergence table lives in
// engine/tool/ledgerkey_test.go). This pins the ACP adapter's WIRING: the
// ledger methods issue NO editor RPCs even when the root pathname is replaced
// by a symlink (a perturbation that would change any absPath/confineSymlinks-
// based key), because tool.LedgerKey is purely lexical.
func TestFSWorkspaceLedgerKeyIsLexicalAndIOFree(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, nil)
	moved := root + "-moved"
	if err := os.Rename(root, moved); err != nil {
		t.Fatalf("Rename(root): %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, root); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	first := tool.NewFileVersion("first")
	if err := testRecordRead(ctx, ws, "dir/../one.txt", first); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	got, ok, err := testRecordedVersion(ctx, ws, filepath.Join(ws.Root(), "one.txt"))
	if err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	}
	if !ok || !got.Equal(first) {
		t.Fatalf("relative record / absolute lookup did not converge (ok=%v)", ok)
	}
	second := tool.NewFileVersion("second")
	if err := testRecordRead(ctx, ws, filepath.Join(ws.Root(), "two.txt"), second); err != nil {
		t.Fatalf("RecordRead: %v", err)
	}
	got, ok, err = testRecordedVersion(ctx, ws, "two.txt")
	if err != nil {
		t.Fatalf("RecordedVersion: %v", err)
	}
	if !ok || !got.Equal(second) {
		t.Fatalf("absolute record / relative lookup did not converge (ok=%v)", ok)
	}
	if reads, writes := peer.reads.Load(), peer.writes.Load(); reads != 0 || writes != 0 {
		t.Fatalf("ledger methods issued editor calls: reads=%d writes=%d", reads, writes)
	}
}

// --- T4: Grep/Glob hybrid -> disk results, no fs/* calls ---------------------

func TestFSWorkspaceGrepGlobHybrid(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, nil)

	// Write a file to DISK directly (the composed osfs view searches disk).
	if err := os.WriteFile(filepath.Join(root, "code.go"), []byte("package x\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatalf("seed disk file: %v", err)
	}

	matches, err := ws.Grep(ctx, "func Hello", "")
	if err != nil {
		t.Fatalf("Grep: %v", err)
	}
	if len(matches) != 1 || matches[0].Path != "code.go" {
		t.Fatalf("Grep returned %+v, want one match in code.go", matches)
	}

	globbed, err := ws.Glob(ctx, "*.go")
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(globbed) != 1 || globbed[0] != "code.go" {
		t.Fatalf("Glob returned %v, want [code.go]", globbed)
	}

	// Grep/Glob must NOT issue any fs/* calls (they read disk).
	if peer.reads.Load() != 0 || peer.writes.Load() != 0 {
		t.Fatalf("Grep/Glob issued fs/* calls: reads=%d writes=%d", peer.reads.Load(), peer.writes.Load())
	}
}

// --- T5: concurrency — N concurrent Reads race-clean -------------------------

func TestFSWorkspaceConcurrentReads(t *testing.T) {
	ctx := context.Background()
	files := map[string]string{}
	for i := 0; i < 16; i++ {
		files[fmt.Sprintf("f%d.txt", i)] = fmt.Sprintf("content-%d", i)
	}
	ws, _, _ := newTestFSWorkspace(t, files)

	var wg sync.WaitGroup
	errs := make(chan error, len(files))
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rel := fmt.Sprintf("f%d.txt", i)
			want := fmt.Sprintf("content-%d", i)
			got, err := ws.Read(ctx, rel)
			if err != nil {
				errs <- fmt.Errorf("read %s: %w", rel, err)
				return
			}
			if string(got) != want {
				errs <- fmt.Errorf("read %s = %q, want %q", rel, got, want)
			}
			// Also exercise the ledger concurrently: ReadVersion + RecordRead +
			// RecordedVersion round-trip under concurrency.
			_, ver, rerr := ws.ReadVersion(ctx, rel)
			if rerr != nil {
				errs <- fmt.Errorf("ReadVersion %s: %w", rel, rerr)
				return
			}
			if err := testRecordRead(ctx, ws, rel, ver); err != nil {
				errs <- fmt.Errorf("RecordRead %s: %w", rel, err)
				return
			}
			if got, ok, err := testRecordedVersion(ctx, ws, rel); err != nil || !ok || !got.Equal(ver) {
				errs <- fmt.Errorf("ledger %s did not return the recorded version (ok=%v, err=%v)", rel, ok, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// --- path confinement --------------------------------------------------------

func TestFSWorkspacePathConfinement(t *testing.T) {
	ctx := context.Background()
	ws, peer, _ := newTestFSWorkspace(t, nil)

	for _, bad := range []string{"../escape.txt", "../../etc/passwd", "/etc/passwd", "a/../../b"} {
		if _, err := ws.Read(ctx, bad); err == nil {
			t.Errorf("Read(%q) should be rejected as an escape", bad)
		}
		if err := ws.Write(ctx, bad, []byte("x")); err == nil {
			t.Errorf("Write(%q) should be rejected as an escape", bad)
		}
	}
	// None of the escapes should have reached the editor.
	if peer.reads.Load() != 0 || peer.writes.Load() != 0 {
		t.Fatalf("an escaping path was delegated: reads=%d writes=%d", peer.reads.Load(), peer.writes.Load())
	}
}

// --- buffer-only Stat -> Write read-before-overwrite gate engages -----------

// A file that exists ONLY as an unsaved editor buffer (present in the peer,
// absent on disk) must Stat as EXISTING, so the real WriteTool's
// read-before-overwrite gate engages: a Write WITHOUT a prior Read is refused;
// after Read (+unchanged) it succeeds and lands in the buffer.
func TestFSWorkspaceBufferOnlyStatGatesWrite(t *testing.T) {
	ctx := context.Background()
	// Seed the file as a buffer ONLY (not on disk).
	ws, peer, root := newTestFSWorkspace(t, map[string]string{"buf.txt": "original"})
	abs := filepath.Join(root, "buf.txt")
	if _, err := os.Stat(abs); err == nil {
		t.Fatalf("precondition: %s must NOT exist on disk", abs)
	}

	// Stat must report EXISTS (via the buffer probe), not not-exist.
	info, err := ws.Stat(ctx, "buf.txt")
	if err != nil {
		t.Fatalf("Stat of buffer-only file: %v (want exists)", err)
	}
	if info.IsDir || info.Size != int64(len("original")) {
		t.Fatalf("Stat synthesized FileInfo = %+v, want a regular file of size %d", info, len("original"))
	}

	write := fstools.WriteTool{}

	// (a) Write WITHOUT a prior Read -> refused (read-before-overwrite engages).
	res := runWrite(t, write, ws, "buf.txt", "clobbered")
	if !res.IsError {
		t.Fatalf("Write to an unsaved buffer without a prior Read must be refused; got success")
	}
	if v, _ := peer.get(abs); v != "original" {
		t.Fatalf("the buffer was clobbered despite the gate: %q", v)
	}

	// (b) Read then Write -> succeeds, lands in the buffer.
	if _, ver, err := ws.ReadVersion(ctx, "buf.txt"); err != nil {
		t.Fatalf("ReadVersion: %v", err)
	} else {
		testRecordRead(ctx, ws, "buf.txt", ver)
	}
	res = runWrite(t, write, ws, "buf.txt", "clobbered")
	if res.IsError {
		t.Fatalf("Write after Read should succeed, got error: %s", res.Content)
	}
	if v, _ := peer.get(abs); v != "clobbered" {
		t.Fatalf("Write did not land in the buffer: %q", v)
	}
}

// A genuinely new file (absent on disk AND in the editor) Stats as not-exist, so
// Write is allowed without a prior read.
func TestFSWorkspaceGenuinelyNewStatNotExist(t *testing.T) {
	ctx := context.Background()
	ws, _, _ := newTestFSWorkspace(t, nil) // empty: peer returns a clean not-found
	if _, err := ws.Stat(ctx, "new.txt"); err == nil {
		t.Fatalf("Stat of a genuinely-new file should report not-exist")
	}
}

// An AMBIGUOUS fs/read fault (not a clean not-found) on a disk-absent file must
// fail SAFE: Stat reports EXISTS so the write-before-overwrite gate engages.
func TestFSWorkspaceAmbiguousReadStatFailsSafe(t *testing.T) {
	ctx := context.Background()
	ws, peer, _ := newTestFSWorkspace(t, nil)
	peer.ambiguousRead.Store(true)
	info, err := ws.Stat(ctx, "mystery.txt")
	if err != nil {
		t.Fatalf("ambiguous read should fail safe to EXISTS, got error: %v", err)
	}
	if info.IsDir {
		t.Fatalf("fail-safe FileInfo should be a regular file: %+v", info)
	}
}

// A model-controlled path containing a not-found marker must not turn a generic
// unstructured decode/protocol failure into permission to create.
func TestFSWorkspaceCreateDoesNotClassifyPathTextAsNotFound(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, nil)
	peer.genericReadFailure.Store(true)
	const path = "not found.txt"

	if _, err := ws.CreateFile(ctx, path, []byte("must not be written")); err == nil {
		t.Fatal("CreateFile succeeded after a generic temporary read failure")
	}
	if got := peer.writes.Load(); got != 0 {
		t.Fatalf("CreateFile sent %d write request(s), want 0 after ambiguous read", got)
	}
	if _, ok := peer.get(filepath.Join(root, path)); ok {
		t.Fatal("CreateFile populated the editor buffer after ambiguous read")
	}
}

func TestFSWorkspaceCreateRejectsStructuredErrorsEchoingNotFoundPath(t *testing.T) {
	for _, message := range []string{
		"permission denied for %s",
		"cannot read %q: unavailable",
	} {
		t.Run(message, func(t *testing.T) {
			ws, peer, root := newTestFSWorkspace(t, nil)
			peer.setReadErrorTemplate(message)
			const path = "not found.txt"
			if _, err := ws.CreateFile(context.Background(), path, []byte("must not be written")); err == nil {
				t.Fatal("CreateFile succeeded after a structured non-absence error")
			}
			if got := peer.writes.Load(); got != 0 {
				t.Fatalf("CreateFile sent %d write request(s), want 0", got)
			}
			if _, ok := peer.get(filepath.Join(root, path)); ok {
				t.Fatal("CreateFile populated the editor buffer")
			}
		})
	}
}

func TestFSWorkspaceCreateAcceptsAnchoredNotFoundMessagesWithPath(t *testing.T) {
	messages := []string{
		"not found: %s",
		"file not found: %q",
		"no such file or directory: %s",
		"no such file or directory: '%s'",
		"%q does not exist",
		"ENOENT: %s",
		"ENOENT: no such file or directory, open %q",
		"open: no such file or directory: %q",
		"error: file not found: %s",
		"not found: `%s`",
	}
	for i, message := range messages {
		t.Run(message, func(t *testing.T) {
			ws, peer, root := newTestFSWorkspace(t, nil)
			peer.setReadErrorTemplate(message)
			path := fmt.Sprintf("missing-%d.txt", i)
			content := []byte("created")
			if _, err := ws.CreateFile(context.Background(), path, content); err != nil {
				t.Fatalf("CreateFile rejected clean not-found message: %v", err)
			}
			if got := peer.writes.Load(); got != 1 {
				t.Fatalf("CreateFile sent %d write request(s), want 1", got)
			}
			if got, ok := peer.get(filepath.Join(root, path)); !ok || got != string(content) {
				t.Fatalf("created buffer = %q, %v; want %q, true", got, ok, content)
			}
		})
	}
}

// runWrite executes the real WriteTool against ws and returns the result.
func runWrite(t *testing.T, write fstools.WriteTool, ws *fsWorkspace, path, content string) session.ToolResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"path": path, "content": content})
	env := tool.MustEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: ws.Root()}, ws, testLedger(ws), nil)
	res, err := write.Execute(context.Background(), session.ToolCall{ID: "w1", Name: "Write", Args: raw}, env)
	if err != nil {
		t.Fatalf("write returned hard error: %v", err)
	}
	return res
}

// --- symlink escape rejected, never delegated -------------------------------

// A symlink INSIDE the workspace root that points OUTSIDE it must be rejected by
// absPath's best-effort symlink re-confinement, so Read/Write never delegate a
// path that resolves out of root.
func TestFSWorkspaceSymlinkEscapeRejected(t *testing.T) {
	ctx := context.Background()
	// outside is a sibling of the workspace root, containing a secret.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatalf("seed secret: %v", err)
	}

	ws, peer, root := newTestFSWorkspace(t, nil)
	// Create an in-root symlink to the outside secret (as `ln -s` via Shell would).
	link := filepath.Join(root, "evil")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}
	// Also a symlinked DIRECTORY component leaving the root.
	linkDir := filepath.Join(root, "escapedir")
	if err := os.Symlink(outside, linkDir); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	for _, bad := range []string{"evil", "escapedir/secret.txt"} {
		if _, err := ws.Read(ctx, bad); err == nil {
			t.Errorf("Read(%q) through an escaping symlink should be rejected", bad)
		}
		if err := ws.Write(ctx, bad, []byte("x")); err == nil {
			t.Errorf("Write(%q) through an escaping symlink should be rejected", bad)
		}
	}
	if peer.reads.Load() != 0 || peer.writes.Load() != 0 {
		t.Fatalf("an escaping symlink was delegated: reads=%d writes=%d", peer.reads.Load(), peer.writes.Load())
	}
}

// An in-root symlink pointing to an in-root target is allowed (it does not
// escape) — confinement must not be over-broad.
func TestFSWorkspaceInRootSymlinkAllowed(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, nil)
	target := filepath.Join(root, "real.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed target: %v", err)
	}
	link := filepath.Join(root, "alias.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// Seed the buffer at the link's real resolved path so the read returns content.
	peer.set(filepath.Join(root, "alias.txt"), "hi")
	if _, err := ws.Read(ctx, "alias.txt"); err != nil {
		t.Fatalf("Read of an in-root symlink should be allowed, got: %v", err)
	}
}

// --- per-call fs/* timeout fires against a non-responsive editor ------------

func TestFSWorkspaceCallTimeout(t *testing.T) {
	ws, peer, _ := newTestFSWorkspace(t, map[string]string{"a.txt": "x"})
	// Shrink the per-call bound and make the peer never respond.
	ws.callTimeout = 100 * time.Millisecond
	peer.hang.Store(true)

	done := make(chan error, 1)
	go func() {
		_, err := ws.Read(context.Background(), "a.txt")
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Read against a hung editor should time out, got nil error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Read error = %v, want a deadline-exceeded timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read did not return within the bound; the per-call timeout did not fire")
	}

	// Write must also be bounded.
	go func() {
		done <- ws.Write(context.Background(), "a.txt", []byte("y"))
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Write error = %v, want a deadline-exceeded timeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not return within the bound")
	}
}

// guard against a flaky timeout: ensure a Read completes promptly when the peer
// responds normally (the timeout is an upper bound, not a delay).
func TestFSWorkspaceReadTimeoutSane(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, _ := newTestFSWorkspace(t, map[string]string{"a.txt": "x"})
	if _, err := ws.Read(ctx, "a.txt"); err != nil {
		t.Fatalf("Read: %v", err)
	}
}

// --- absolute in-root path acceptance (issue #154 / ADR 0047 parity) ---------

// TestFSWorkspaceAbsoluteInRootReadWriteStat pins that an ABSOLUTE in-root path
// is now accepted by the ACP workspace's Read/Write/Stat (the absPath change had
// ZERO coverage). The ACP workspace delegates Read/Write through the editor
// buffer map keyed by the absolute path absPath computes; Stat delegates to the
// composed osfs view, which accepts the same absolute in-root form. Content must
// round-trip across the two path forms (write by absolute, read by relative and
// vice versa).
func TestFSWorkspaceAbsoluteInRootReadWriteStat(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, map[string]string{"rel.go": "package main\n"})

	// Read by the ABSOLUTE in-root alias of a buffer seeded by relative path.
	abs := filepath.Join(root, "rel.go")
	got, err := ws.Read(ctx, abs)
	if err != nil {
		t.Fatalf("Read(absolute in-root): %v", err)
	}
	if string(got) != "package main\n" {
		t.Errorf("Read(absolute) = %q, want %q", got, "package main\n")
	}

	// Write by ABSOLUTE in-root path, read back by RELATIVE.
	if err := ws.Write(ctx, abs, []byte("package main // edited\n")); err != nil {
		t.Fatalf("Write(absolute in-root): %v", err)
	}
	if v, _ := peer.get(abs); v != "package main // edited\n" {
		t.Fatalf("peer buffer after absolute Write = %q, want edited", v)
	}
	got, err = ws.Read(ctx, "rel.go")
	if err != nil {
		t.Fatalf("Read(relative after absolute Write): %v", err)
	}
	if string(got) != "package main // edited\n" {
		t.Errorf("content after absolute Write = %q", got)
	}

	// Stat by ABSOLUTE in-root path must succeed (delegated to the osfs view,
	// which now accepts in-root absolutes); the file exists on disk because the
	// osfs view is rooted at the same tempdir — but the ACP workspace does NOT
	// write to disk, so seed a disk file Stat will see.
	disk := filepath.Join(root, "disk.txt")
	if err := os.WriteFile(disk, []byte("on disk\n"), 0o644); err != nil {
		t.Fatalf("seed disk file: %v", err)
	}
	fi, err := ws.Stat(ctx, disk)
	if err != nil {
		t.Fatalf("Stat(absolute in-root disk file): %v", err)
	}
	if fi.Name != "disk.txt" {
		t.Errorf("Stat(absolute).Name = %q, want disk.txt", fi.Name)
	}

	// An OUT-of-root ABSOLUTE path (a sibling tempdir) is still rejected.
	other := filepath.Join(t.TempDir(), "other", "file")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	if err := os.WriteFile(other, []byte("out"), 0o644); err != nil {
		t.Fatalf("write other: %v", err)
	}
	for _, bad := range []string{"/etc/passwd", other} {
		if _, err := ws.Read(ctx, bad); err == nil {
			t.Errorf("Read(%q) should be rejected as an escape", bad)
		}
		if err := ws.Write(ctx, bad, []byte("x")); err == nil {
			t.Errorf("Write(%q) should be rejected as an escape", bad)
		}
	}
}

// TestFSWorkspaceAbsPathUnit is the direct unit test of the changed resolver:
// an in-root absolute is accepted and returned cleaned; an out-of-root absolute
// and a relative-escape are rejected. Drives absPath without the editor conn.
func TestFSWorkspaceAbsPathUnit(t *testing.T) {
	ws, _, root := newTestFSWorkspace(t, nil)

	in := filepath.Join(root, "sub", "file.go")
	got, err := ws.absPath(in)
	if err != nil {
		t.Fatalf("absPath(in-root absolute): %v", err)
	}
	if got != in {
		t.Errorf("absPath(in-root absolute) = %q, want %q", got, in)
	}
	// The relative form must canonicalize to the SAME absolute key.
	relGot, err := ws.absPath(filepath.ToSlash(filepath.Join("sub", "file.go")))
	if err != nil {
		t.Fatalf("absPath(relative): %v", err)
	}
	if relGot != got {
		t.Errorf("absPath(relative) = %q, want %q (same as absolute)", relGot, got)
	}

	for _, bad := range []string{
		"/etc/passwd",
		filepath.Join(t.TempDir(), "sibling"),
		"../escape",
		"a/../../b",
	} {
		if _, err := ws.absPath(bad); err == nil {
			t.Errorf("absPath(%q) should be rejected as an escape", bad)
		}
	}
}

// TestFSWorkspaceLedgerCrossForm pins the I/O-free lexical key: an ordinary
// absolute <root>/<rel> and relative <rel> share one entry through Clean/Rel,
// without absPath, filesystem inspection, or an editor RPC. A concrete adapter
// Write through either form mints a fresh version that no longer matches the
// recorded one. Physical symlink aliases may conservatively miss.
func TestFSWorkspaceLedgerCrossForm(t *testing.T) {
	ctx := context.Background()
	ws, _, root := newTestFSWorkspace(t, map[string]string{"led.txt": "original"})
	rel := "led.txt"
	abs := filepath.Join(root, "led.txt")

	// Read by ABSOLUTE, look up by RELATIVE → recorded version matches.
	_, absVer, err := ws.ReadVersion(ctx, abs)
	if err != nil {
		t.Fatalf("ReadVersion(abs): %v", err)
	}
	if err := testRecordRead(ctx, ws, abs, absVer); err != nil {
		t.Fatalf("RecordRead(abs): %v", err)
	}
	got, ok, err := testRecordedVersion(ctx, ws, rel)
	if err != nil {
		t.Fatalf("RecordedVersion(rel): %v", err)
	}
	if !ok {
		t.Fatalf("read abs / lookup rel: not recorded")
	}
	if !got.Equal(absVer) {
		t.Fatal("read abs / lookup rel returned a different version")
	}
	// Mutate by RELATIVE → a fresh ReadVersion by ABSOLUTE mints a different version.
	if err := ws.Write(ctx, rel, []byte("mutated")); err != nil {
		t.Fatalf("Write(rel) mutation: %v", err)
	}
	if _, cur, err := ws.ReadVersion(ctx, abs); err != nil {
		t.Fatalf("ReadVersion(abs) after mutation: %v", err)
	} else if cur.Equal(absVer) {
		t.Fatalf("after relative mutation, current abs version still equals recorded")
	}

	// Reverse: read by RELATIVE, look up by ABSOLUTE → recorded version matches.
	_, relVer, err := ws.ReadVersion(ctx, rel)
	if err != nil {
		t.Fatalf("ReadVersion(rel): %v", err)
	}
	if err := testRecordRead(ctx, ws, rel, relVer); err != nil {
		t.Fatalf("RecordRead(rel): %v", err)
	}
	got, ok, err = testRecordedVersion(ctx, ws, abs)
	if err != nil {
		t.Fatalf("RecordedVersion(abs): %v", err)
	}
	if !ok {
		t.Fatalf("read rel / lookup abs: not recorded")
	}
	if !got.Equal(relVer) {
		t.Fatal("read rel / lookup abs returned a different version")
	}
	// Mutate by ABSOLUTE → a fresh ReadVersion by RELATIVE mints a different version.
	if err := ws.Write(ctx, abs, []byte("mutated again")); err != nil {
		t.Fatalf("Write(abs) mutation: %v", err)
	}
	if _, cur, err := ws.ReadVersion(ctx, rel); err != nil {
		t.Fatalf("ReadVersion(rel) after mutation: %v", err)
	} else if cur.Equal(relVer) {
		t.Fatalf("after absolute mutation, current rel version still equals recorded")
	}
}

// TestFSWorkspaceLedgerNotBlockedByParkedRPC proves capability separation
// (ADR 0208, ADR 0294): an Environment-selected ReadLedger is independent of
// ACP's RPC CAS mutex (callMu), so a parked RPC mutation holding callMu never
// blocks ledger RecordRead/RecordedVersion. AC3.8 pins this separation.
func TestFSWorkspaceLedgerNotBlockedByParkedRPC(t *testing.T) {
	ctx := context.Background()
	ws, _, _ := newTestFSWorkspace(t, nil)

	// Park callMu as a CreateFile/ReplaceFile would while it holds an in-flight
	// RPC. The ledger must still be writable/readable.
	ws.callMu.Lock()
	defer ws.callMu.Unlock()

	ver := tool.NewFileVersion("parked")
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := testRecordRead(ctx, ws, "a.txt", ver); err != nil {
			t.Errorf("RecordRead: %v", err)
		}
		if got, ok, err := testRecordedVersion(ctx, ws, "a.txt"); err != nil || !ok || !got.Equal(ver) {
			t.Errorf("RecordedVersion while callMu is parked = (ok=%v, got=%v, err=%v), want the recorded version", ok, got, err)
		}
		// A second record on a different path and a lookup of the first both
		// succeed without touching callMu.
		if err := testRecordRead(ctx, ws, "b.txt", tool.NewFileVersion("second")); err != nil {
			t.Errorf("RecordRead(b.txt): %v", err)
		}
		if _, ok, err := testRecordedVersion(ctx, ws, "b.txt"); err != nil || !ok {
			t.Errorf("RecordedVersion(b.txt) not recorded while callMu is parked (err=%v)", err)
		}
		if got, ok, err := testRecordedVersion(ctx, ws, "a.txt"); err != nil || !ok || !got.Equal(ver) {
			t.Errorf("RecordedVersion(a.txt) after b record = (ok=%v, got=%v, err=%v), want the first recorded version", ok, got, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ledger RecordRead/RecordedVersion blocked behind a parked RPC holding callMu — the mutexes must be independent")
	}
}

// --- ADR 0315: fsWorkspace namespace operations ------------------------------

// TestADR_0315_FSWorkspace_ReadDir_DelegatesToLocal pins that ReadDir uses the
// confined LOCAL disk view (fsWorkspace.local, an osfs.Workspace), per the
// documented limitation that ACP exposes no directory-listing RPC and cannot
// enumerate unsaved buffer-only files.
func TestADR_0315_FSWorkspace_ReadDir_DelegatesToLocal(t *testing.T) {
	ctx := context.Background()
	ws, _, root := newTestFSWorkspace(t, nil)
	// Write directly to disk (bypassing the peer buffer entirely) so ReadDir can
	// only see this file if it consults the LOCAL disk view, not the buffers.
	if err := os.WriteFile(filepath.Join(root, "ondisk.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed on-disk file: %v", err)
	}
	entries, err := ws.ReadDir(ctx, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "ondisk.txt" {
		t.Fatalf("ReadDir = %+v, want [ondisk.txt] (the local disk view)", entries)
	}
}

// TestADR_0315_FSWorkspace_Remove_Unsupported pins that Remove is UNSUPPORTED
// (ACP has no delete RPC and mutating local disk would bypass the editor's
// authoritative buffer), returning tool.ErrFileOperationUnsupported.
func TestADR_0315_FSWorkspace_Remove_Unsupported(t *testing.T) {
	ws, _, _ := newTestFSWorkspace(t, map[string]string{"a.txt": "hello"})
	err := ws.Remove(context.Background(), "a.txt")
	if !errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("Remove error = %v, want errors.Is(_, tool.ErrFileOperationUnsupported)", err)
	}
}

// TestADR_0315_FSWorkspace_Rename_Unsupported pins that Rename is UNSUPPORTED
// for the same reason as Remove (no rename RPC; local-disk mutation would
// bypass the editor's authoritative buffers).
func TestADR_0315_FSWorkspace_Rename_Unsupported(t *testing.T) {
	ws, _, _ := newTestFSWorkspace(t, map[string]string{"a.txt": "hello"})
	err := ws.Rename(context.Background(), "a.txt", "b.txt")
	if !errors.Is(err, tool.ErrFileOperationUnsupported) {
		t.Fatalf("Rename error = %v, want errors.Is(_, tool.ErrFileOperationUnsupported)", err)
	}
}

// TestADR_0315_FSWorkspace_CopyFile_IsBufferAware pins that CopyFile reads the
// EDITOR BUFFER (via Read, i.e. fs/read_text_file), not disk — so a source
// whose in-memory buffer has diverged from its on-disk content is copied with
// the buffer's content, honoring the editor's authoritative view the same way
// Read/Write already do.
func TestADR_0315_FSWorkspace_CopyFile_IsBufferAware(t *testing.T) {
	ctx := context.Background()
	ws, peer, root := newTestFSWorkspace(t, map[string]string{"src.txt": "buffer-content"})
	// The on-disk content (if any existed) would differ; there is deliberately
	// no on-disk file here — CopyFile must succeed purely from the buffer.
	if _, err := os.Stat(filepath.Join(root, "src.txt")); !os.IsNotExist(err) {
		t.Fatalf("precondition: src.txt must not exist on disk, stat err = %v", err)
	}
	ver, err := ws.CopyFile(ctx, "src.txt", "dst.txt")
	if err != nil {
		t.Fatalf("CopyFile: %v", err)
	}
	if ver.Equal(tool.FileVersion{}) {
		t.Fatal("CopyFile returned a zero-value FileVersion")
	}
	got, ok := peer.get(filepath.Join(root, "dst.txt"))
	if !ok || got != "buffer-content" {
		t.Fatalf("peer buffer for dst.txt = (%q, %v), want (\"buffer-content\", true)", got, ok)
	}
}

// TestADR_0315_FSWorkspace_CopyFile_NoClobber pins that CopyFile refuses to
// overwrite an existing destination buffer, mirroring the shared Copy tool's
// no-clobber contract (it delegates to CreateFile, the create-only mutation).
func TestADR_0315_FSWorkspace_CopyFile_NoClobber(t *testing.T) {
	ctx := context.Background()
	ws, _, _ := newTestFSWorkspace(t, map[string]string{
		"src.txt": "source",
		"dst.txt": "already-there",
	})
	if _, err := ws.CopyFile(ctx, "src.txt", "dst.txt"); err == nil {
		t.Fatal("CopyFile(src.txt, dst.txt) = nil error, want a no-clobber refusal (dst.txt already exists)")
	}
}
