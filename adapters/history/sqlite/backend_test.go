package sqlite

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func newBackend(t *testing.T, root string) *Backend {
	t.Helper()
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := b.Close(); err != nil {
			t.Error(err)
		}
	})
	return b
}
func proposal(seq int64) history.ByteCommit {
	data := []byte{0x41, byte(seq)}
	return history.ByteCommit{Ref: history.CommitRef{Sequence: seq, CID: contentid.Sum(data)}, Bytes: data}
}
func appendRoot(t *testing.T, b *Backend, id history.HistoryID) history.CommitRef {
	t.Helper()
	c := proposal(1)
	o, err := b.Append(t.Context(), id, history.CommitRef{}, c)
	if err != nil || o.Head != c.Ref {
		t.Fatalf("append root: %+v %v", o, err)
	}
	return c.Ref
}
func readRequest(ref history.CommitRef) history.ByteReadRequest {
	return history.ByteReadRequest{ReadRequest: history.ReadRequest{Through: ref, MaxCommits: 10}, MaxBytes: 100, MaxCommitBytes: 100}
}

func TestMissingReadsLeaveNoFiles(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	id := history.NewHistoryID()
	if o, err := b.Observe(t.Context(), id, 0); err != nil || o != (history.Observation{}) {
		t.Fatalf("%+v %v", o, err)
	}
	if p, err := b.Read(t.Context(), id, readRequest(proposal(1).Ref)); !errors.Is(err, history.ErrNotFound) || p != nil {
		t.Fatalf("%v %v", p, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%v err=%v", entries, err)
	}
}

func TestReopenExactBytesAndPrivateNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "?mode=ro #&", "nested", "history root")
	b := newBackend(t, root)
	id := history.NewHistoryID()
	ref := appendRoot(t, b, id)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b = newBackend(t, root)
	page, err := b.Read(t.Context(), id, readRequest(ref))
	if err != nil || len(page) != 1 || !bytes.Equal(page[0].Bytes, proposal(1).Bytes) {
		t.Fatalf("%v %v", page, err)
	}
	for _, path := range []string{root, filepath.Join(root, id.String()), filepath.Join(root, id.String(), databaseName)} {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0077 != 0 {
			t.Fatalf("nonprivate %s: %v", path, st.Mode())
		}
	}
}

func TestInitialCreateAcrossInstances(t *testing.T) {
	root := t.TempDir()
	id := history.NewHistoryID()
	const n = 12
	backends := make([]*Backend, n)
	for i := range backends {
		backends[i] = newBackend(t, root)
	}
	var wg sync.WaitGroup
	results := make(chan history.Observation, n)
	errs := make(chan error, n)
	for i, b := range backends {
		wg.Go(func() {
			c := proposal(1)
			c.Bytes = []byte{0x41, byte(i)}
			c.Ref.CID = contentid.Sum(c.Bytes)
			o, err := b.Append(t.Context(), id, history.CommitRef{}, c)
			results <- o
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var winner history.CommitRef
	for o := range results {
		if winner == (history.CommitRef{}) {
			winner = o.Head
		}
		if o.Head != winner || o.Commit != winner || winner.Validate() != nil {
			t.Fatalf("incoherent: %+v", o)
		}
	}
	db, err := backends[0].database(t.Context(), id, false)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM commits`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count %d: %v", count, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 {
		t.Fatalf("publication artifacts: %v %v", entries, err)
	}
}

func TestPublicationAdmissionAndRetry(t *testing.T) {
	root := t.TempDir()
	publisher := newBackend(t, root)
	reader := newBackend(t, root)
	id := history.NewHistoryID()
	published, release := make(chan struct{}), make(chan struct{})
	releasePublisher := sync.OnceFunc(func() { close(release) })
	defer releasePublisher()
	publisher.afterPublish = func() error { close(published); <-release; return nil }
	result := make(chan error, 1)
	go func() { _, err := publisher.Append(t.Context(), id, history.CommitRef{}, proposal(1)); result <- err }()
	<-published
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	if o, err := reader.Observe(ctx, id, 0); !errors.Is(err, context.DeadlineExceeded) || o != (history.Observation{}) {
		t.Fatalf("bypassed publication barrier: %+v %v", o, err)
	}
	// A blocked history must not hold a backend-wide I/O mutex.
	appendRoot(t, reader, history.NewHistoryID())
	releasePublisher()
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if o, err := reader.Observe(t.Context(), id, 1); err != nil || o.Head != proposal(1).Ref {
		t.Fatalf("retry: %+v %v", o, err)
	}
}

func TestInterruptedPublicationStabilized(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	id := history.NewHistoryID()
	failure := errors.New("publication interrupted")
	b.afterPublish = func() error { return failure }
	if o, err := b.Append(t.Context(), id, history.CommitRef{}, proposal(1)); !errors.Is(err, failure) || o != (history.Observation{}) {
		t.Fatalf("%+v %v", o, err)
	}
	reader := newBackend(t, root)
	if o, err := reader.Observe(t.Context(), id, 0); err != nil || o != (history.Observation{}) {
		t.Fatalf("empty published database: %+v %v", o, err)
	}
	appendRoot(t, reader, id)
}

func TestUnknownFinalNeverReplaced(t *testing.T) {
	for _, original := range [][]byte{nil, []byte("not a SQLite database")} {
		t.Run(string(original), func(t *testing.T) {
			root := t.TempDir()
			b := newBackend(t, root)
			id := history.NewHistoryID()
			dir := filepath.Join(root, id.String())
			if err := os.Mkdir(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, databaseName)
			if err := os.WriteFile(path, original, 0600); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if o, err := b.Append(t.Context(), id, history.CommitRef{}, proposal(1)); err == nil || o != (history.Observation{}) {
					t.Fatalf("%+v %v", o, err)
				}
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("unknown replaced: %q %v", got, err)
			}
			if len(b.handles) != 0 {
				t.Fatal("failed attachment retained")
			}
		})
	}
}

func TestCloseDrainsAdmissionAndIsIdempotent(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	appendRoot(t, b, id)
	_, done, err := b.admit(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	for {
		b.mu.Lock()
		closing := b.closing
		b.mu.Unlock()
		if closing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if o, err := b.Observe(t.Context(), id, 0); !errors.Is(err, os.ErrClosed) || o != (history.Observation{}) {
		t.Fatalf("admitted after Close: %+v %v", o, err)
	}
	select {
	case <-closed:
		t.Fatal("did not drain")
	default:
	}
	done()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if err := b.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestLocalOpeningWaitCancellation(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	id := history.NewHistoryID()
	lock := flock.New(filepath.Join(root, id.String()+".lock"))
	if err := lock.Lock(); err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	first := make(chan error, 1)
	go func() { _, err := b.Append(firstCtx, id, history.CommitRef{}, proposal(1)); first <- err }()
	for {
		b.mu.Lock()
		opening := b.handles[id] != nil
		b.mu.Unlock()
		if opening {
			break
		}
		time.Sleep(time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if o, err := b.Append(ctx, id, history.CommitRef{}, proposal(1)); !errors.Is(err, context.DeadlineExceeded) || o != (history.Observation{}) {
		t.Fatalf("local wait: %+v %v", o, err)
	}
	cancelFirst()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatal(err)
	}
	appendRoot(t, b, id)
}

func TestCloseDuringFirstOpen(t *testing.T) {
	b := newBackend(t, t.TempDir())
	id := history.NewHistoryID()
	published, release := make(chan struct{}), make(chan struct{})
	releasePublisher := sync.OnceFunc(func() { close(release) })
	defer releasePublisher()
	b.afterPublish = func() error { close(published); <-release; return nil }
	appended := make(chan error, 1)
	go func() { _, err := b.Append(t.Context(), id, history.CommitRef{}, proposal(1)); appended <- err }()
	<-published
	closed := make(chan error, 1)
	go func() { closed <- b.Close() }()
	for {
		b.mu.Lock()
		closing := b.closing
		b.mu.Unlock()
		if closing {
			break
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-closed:
		t.Fatal("Close bypassed an opening operation")
	default:
	}
	releasePublisher()
	if err := <-appended; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if s := b.handles[id].db.Stats(); s.OpenConnections != 0 {
		t.Fatalf("leaked handle: %+v", s)
	}
}
