package sqlite

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

// Children use the real adapter and private test-owned roots. Pipes provide
// explicit readiness/release barriers; no clean Close runs in killed children.
type processOutput struct {
	mu sync.Mutex
	bytes.Buffer
}

func (o *processOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Buffer.Write(p)
}
func (o *processOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.Buffer.String()
}

type historyProcess struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  *bufio.Scanner
	stderr processOutput
	waited bool
}

func startHistoryProcess(t *testing.T, root string, id history.HistoryID, mode string) *historyProcess {
	t.Helper()
	return startAdapterProcess(t, root, id, mode, "TestHistoryProcessHelper")
}

func startAdapterProcess(t *testing.T, root string, id history.HistoryID, mode, helper string) *historyProcess {
	t.Helper()
	p := &historyProcess{}
	p.cmd = exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+helper+"$", "-test.timeout=30s")
	p.cmd.Env = []string{"MECATL_HISTORY_CHILD=" + mode, "MECATL_HISTORY_ROOT=" + root, "MECATL_HISTORY_ID=" + id.String(), "TMPDIR=" + t.TempDir()}
	var err error
	p.in, err = p.cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	p.lines = bufio.NewScanner(out)
	p.cmd.Stderr = &p.stderr
	if err = p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !p.waited {
			_ = p.cmd.Process.Kill()
			_ = p.cmd.Wait()
		}
		_ = p.in.Close()
	})
	return p
}
func (p *historyProcess) line(t *testing.T) string {
	t.Helper()
	// A child that fails or hangs cannot strand the parent test in a pipe read.
	result := make(chan string, 1)
	go func() {
		if p.lines.Scan() {
			result <- p.lines.Text()
		} else {
			result <- ""
		}
	}()
	select {
	case s := <-result:
		if s == "" {
			t.Fatalf("child ended: %s", p.stderr.String())
		}
		return s
	case <-time.After(15 * time.Second):
		t.Fatal("child protocol timeout")
		return ""
	}
}
func (p *historyProcess) send(t *testing.T) {
	t.Helper()
	if _, err := io.WriteString(p.in, "go\n"); err != nil {
		t.Fatal(err)
	}
}
func (p *historyProcess) wait(t *testing.T) {
	t.Helper()
	err := p.cmd.Wait()
	p.waited = true
	if err != nil {
		t.Fatalf("child: %v\n%s", err, p.stderr.String())
	}
}
func (p *historyProcess) kill(t *testing.T) {
	t.Helper()
	if err := p.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := p.cmd.Wait()
	p.waited = true
	if err == nil {
		t.Fatal("child was not killed")
	}
}

func TestHistoryProcessHelper(t *testing.T) {
	mode := os.Getenv("MECATL_HISTORY_CHILD")
	if mode == "" {
		t.Skip("subprocess only")
	}
	id, err := history.ParseHistoryID(os.Getenv("MECATL_HISTORY_ID"))
	if err != nil {
		t.Fatal(err)
	}
	b := newBackend(t, os.Getenv("MECATL_HISTORY_ROOT"))
	input := bufio.NewScanner(os.Stdin)
	gate := func() {
		if !input.Scan() || input.Text() != "go" {
			t.Fatal("release pipe closed")
		}
	}
	switch {
	case strings.HasPrefix(mode, "cas-"):
		n, err := strconv.Atoi(strings.TrimPrefix(mode, "cas-"))
		if err != nil {
			t.Fatal(err)
		}
		c := proposal(1)
		c.Bytes = []byte{0x41, byte(n)}
		c.Ref.CID = contentid.Sum(c.Bytes)
		fmt.Println("ready")
		gate()
		o, err := b.Append(t.Context(), id, history.CommitRef{}, c)
		if err != nil || o.Head.Sequence != 1 || o.Commit != o.Head {
			t.Fatalf("incoherent CAS evidence: %+v %v", o, err)
		}
		fmt.Printf("result %t %s\n", o.Head == c.Ref, o.Head.CID.String())
	case mode == "publish":
		b.afterPublish = func() error { fmt.Println("renamed"); gate(); return nil }
		appendRoot(t, b, id)
		fmt.Println("published")
	case strings.HasPrefix(mode, "attach"):
		// Each is a first attachment. A completed deadline, not a quiet interval,
		// proves all three APIs actually attempted admission while the lock was held.
		for _, op := range []string{"read", "observe", "append"} {
			ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
			switch op {
			case "read":
				p, e := b.Read(ctx, id, readRequest(proposal(1).Ref))
				if !errors.Is(e, context.DeadlineExceeded) || p != nil {
					t.Fatalf("read bypassed barrier: %v %v", p, e)
				}
			case "observe":
				o, e := b.Observe(ctx, id, 0)
				if !errors.Is(e, context.DeadlineExceeded) || o != (history.Observation{}) {
					t.Fatalf("observe bypassed barrier: %+v %v", o, e)
				}
			case "append":
				o, e := b.Append(ctx, id, history.CommitRef{}, proposal(1))
				if !errors.Is(e, context.DeadlineExceeded) || o != (history.Observation{}) {
					t.Fatalf("append bypassed barrier: %+v %v", o, e)
				}
			}
			cancel()
			if len(b.handles) != 0 {
				t.Fatal("failed admission retained attachment")
			}
		}
		fmt.Println("blocked")
		gate()
		if mode != "attach" {
			// Write+search permission still permits SQLite and flock; only opening
			// this directory for stabilization is denied. Require the exact failing
			// os.Open path so an unrelated SQL/setup error cannot satisfy the test.
			want := b.root
			if mode == "attach-final-directory" {
				want = filepath.Join(b.root, id.String())
			}
			page, e := b.Read(t.Context(), id, readRequest(proposal(1).Ref))
			var pathErr *os.PathError
			if page != nil || !errors.Is(e, os.ErrPermission) || !errors.As(e, &pathErr) || pathErr.Path != want || pathErr.Op != "open" {
				t.Fatalf("takeover did not stabilize %s: page=%v err=%v cause=%+v", want, page, e, pathErr)
			}
			if len(b.handles) != 0 {
				t.Fatal("failed stabilization retained a ready handle")
			}
			fmt.Println("stabilization-failed")
			gate()
		}
		// Read must acquire/stabilize even when it finds an initialized empty DB.
		page, e := b.Read(t.Context(), id, readRequest(proposal(1).Ref))
		if e != nil && !errors.Is(e, history.ErrNotFound) {
			t.Fatal(e)
		}
		if e == nil && (len(page) != 1 || page[0].Ref != proposal(1).Ref) {
			t.Fatalf("page: %+v", page)
		}
		if len(b.handles) != 1 {
			t.Fatal("read did not retain ready attachment")
		}
		if errors.Is(e, history.ErrNotFound) {
			appendRoot(t, b, id)
		}
		o, e := b.Append(t.Context(), id, proposal(1).Ref, proposal(2))
		if e != nil || o.Head != proposal(2).Ref {
			t.Fatalf("append after read: %+v %v", o, e)
		}
		fmt.Println("attached")
	case mode == "before-commit" || mode == "after-commit":
		first := appendRoot(t, b, id)
		db := storedDatabase(t, b, id)
		execSQL(t, db, `PRAGMA cache_size=8`)
		// Prime and sync the WAL before targeting a large transaction that spills.
		var commits atomic.Int32
		pause := func() { fmt.Println("commit-boundary"); gate() }
		r := receiptConnector{commits: &commits}
		if mode == "before-commit" {
			r.before = pause
		} else {
			r.after = pause
		}
		installReceipt(t, b, id, r)
		execSQL(t, storedDatabase(t, b, id), `PRAGMA cache_size=8`)
		c := largeProposal(2, 2<<20)
		o, e := b.Append(t.Context(), id, first, c)
		if e != nil || o.Head != c.Ref {
			t.Fatalf("append: %+v %v", o, e)
		}
	default:
		t.Fatalf("unknown child %q", mode)
	}
}

func largeProposal(seq int64, size int) history.ByteCommit {
	// One canonical DRISL byte string; payload schema intentionally belongs above storage.
	header := []byte{0x5a, byte(uint32(size) >> 24), byte(uint32(size) >> 16), byte(uint32(size) >> 8), byte(size)}
	switch {
	case size < 24:
		header = []byte{0x40 | byte(size)}
	case size <= 255:
		header = []byte{0x58, byte(size)}
	case size <= 65535:
		header = []byte{0x59, byte(size >> 8), byte(size)}
	}
	data := make([]byte, len(header)+size)
	copy(data, header)
	return history.ByteCommit{Ref: history.CommitRef{Sequence: seq, CID: contentid.Sum(data)}, Bytes: data}
}

func TestIndependentProcessCAS(t *testing.T) {
	root := t.TempDir()
	id := history.NewHistoryID()
	a := startHistoryProcess(t, root, id, "cas-11")
	b := startHistoryProcess(t, root, id, "cas-22")
	if a.line(t) != "ready" || b.line(t) != "ready" {
		t.Fatal("not ready")
	}
	a.send(t)
	b.send(t)
	ra, rb := strings.Fields(a.line(t)), strings.Fields(b.line(t))
	if len(ra) != 3 || len(rb) != 3 || ra[0] != "result" || rb[0] != "result" || ra[1] == rb[1] || ra[2] != rb[2] {
		t.Fatalf("CAS evidence: %v / %v", ra, rb)
	}
	a.wait(t)
	b.wait(t)
	backend := newBackend(t, root)
	o, err := backend.Observe(t.Context(), id, 1)
	if err != nil || o.Head.Sequence != 1 || o.Commit != o.Head || o.Head.CID.String() != ra[2] {
		t.Fatalf("reopen: %+v %v", o, err)
	}
	page, err := backend.Read(t.Context(), id, readRequest(o.Head))
	if err != nil || len(page) != 1 || contentid.Sum(page[0].Bytes) != o.Head.CID {
		t.Fatalf("winner bytes: %v %v", page, err)
	}
}

func TestPublicationProcessBarrier(t *testing.T) {
	for _, scenario := range []string{"publisher-release", "publisher-death", "final-directory", "parent-directory"} {
		t.Run(scenario, func(t *testing.T) {
			failure := scenario == "final-directory" || scenario == "parent-directory"
			if failure && os.Geteuid() == 0 {
				t.Skip("directory-open fault needs an unprivileged UID")
			}
			root := t.TempDir()
			id := history.NewHistoryID()
			publisher := startHistoryProcess(t, root, id, "publish")
			if publisher.line(t) != "renamed" {
				t.Fatal("rename not reached")
			}
			if _, err := os.Stat(filepath.Join(root, id.String(), databaseName)); err != nil {
				t.Fatal(err)
			}
			mode := "attach"
			if failure {
				mode += "-" + scenario
			}
			attacher := startHistoryProcess(t, root, id, mode)
			if attacher.line(t) != "blocked" {
				t.Fatal("admission not blocked")
			}
			if scenario != "publisher-release" {
				publisher.kill(t)
			} else {
				publisher.send(t)
				if publisher.line(t) != "published" {
					t.Fatal("publication failed")
				}
				publisher.wait(t)
			}
			if failure {
				path := root
				if scenario == "final-directory" {
					path = filepath.Join(root, id.String())
				}
				if err := os.Chmod(path, 0300); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0700)
				attacher.send(t)
				if got := attacher.line(t); got != "stabilization-failed" {
					t.Fatalf("failed stabilization admitted history: %s", got)
				}
				if err := os.Chmod(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			attacher.send(t)
			if attacher.line(t) != "attached" {
				t.Fatal("takeover failed")
			}
			attacher.wait(t)
			backend := newBackend(t, root)
			o, e := backend.Observe(t.Context(), id, 2)
			if e != nil || o.Head != proposal(2).Ref {
				t.Fatalf("takeover persisted: %+v %v", o, e)
			}
		})
	}
}

func TestKilledProcessWALRecovery(t *testing.T) {
	for _, mode := range []string{"before-commit", "after-commit"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			id := history.NewHistoryID()
			child := startHistoryProcess(t, root, id, mode)
			if child.line(t) != "commit-boundary" {
				t.Fatal("commit not reached")
			}
			st, err := os.Stat(filepath.Join(root, id.String(), databaseName+"-wal"))
			if err != nil || st.Size() < 1<<20 {
				t.Fatalf("no spilled WAL: %v %v", st, err)
			}
			child.kill(t)
			b := newBackend(t, root)
			want := proposal(1)
			if mode == "after-commit" {
				want = largeProposal(2, 2<<20)
			}
			o, err := b.Observe(t.Context(), id, 2)
			if err != nil || o.Head != want.Ref {
				t.Fatalf("recovery: %+v %v", o, err)
			}
			r := readRequest(want.Ref)
			r.MaxBytes = 3 << 20
			r.MaxCommitBytes = 3 << 20
			page, err := b.Read(t.Context(), id, r)
			if err != nil || len(page) != int(want.Ref.Sequence) || !bytes.Equal(page[len(page)-1].Bytes, want.Bytes) {
				t.Fatalf("recovered bytes: count=%d %v", len(page), err)
			}
			var integrity string
			if err = storedDatabase(t, b, id).QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
				t.Fatalf("integrity: %s %v", integrity, err)
			}
			next := proposal(want.Ref.Sequence + 1)
			if o, err = b.Append(t.Context(), id, want.Ref, next); err != nil || o.Head != next.Ref {
				t.Fatalf("writer lock leaked: %+v %v", o, err)
			}
		})
	}
}
