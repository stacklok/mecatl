package pdfartifact

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

type memoryObjects struct{ data map[string][]byte }

func (m *memoryObjects) Put(ctx context.Context, key string, source io.Reader) error {
	data, err := io.ReadAll(source)
	if err != nil {
		return err
	}
	m.data[key] = data
	return nil
}
func (m *memoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	data, ok := m.data[key]
	if !ok {
		return nil, errors.New("missing object")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (m *memoryObjects) Delete(_ context.Context, key string) error { delete(m.data, key); return nil }
func (m *memoryObjects) ListPrefix(_ context.Context, prefix string) ([]string, error) {
	var keys []string
	for key := range m.data {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func TestPDFArtifactStorage_StageOpenAndReject(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	redis, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = redis.Close() })
	const owner session.SessionID = "pdf-owner"
	const other session.SessionID = "pdf-other"
	for _, id := range []session.SessionID{owner, other} {
		if err := redis.Save(t.Context(), session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
			t.Fatal(err)
		}
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	storage := New(redis, objects)
	pdf := []byte("%PDF-1.7\nfixture\n%%EOF")
	meta, err := storage.Stage(t.Context(), owner, "report.pdf", bytes.NewReader(pdf))
	if err != nil {
		t.Fatalf("Stage: %v", err)
	}
	if meta.ID == "" || meta.Name != "report.pdf" || meta.Size != int64(len(pdf)) {
		t.Fatalf("metadata = %+v", meta)
	}
	hash := sha256.Sum256(pdf)
	if meta.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatalf("SHA256 = %q", meta.SHA256)
	}
	got, reader, err := storage.Open(t.Context(), owner, meta.ID)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer reader.Close()
	if got != meta {
		t.Fatalf("Open metadata = %+v, want %+v", got, meta)
	}
	all, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(all, pdf) {
		t.Fatalf("Open bytes = %q, %v", all, err)
	}
	if _, err := storage.Resolve(t.Context(), other, meta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-session Resolve = %v", err)
	}
	if _, err := storage.Stage(t.Context(), owner, "../escape.pdf", bytes.NewReader(pdf)); err == nil {
		t.Fatal("unsafe name accepted")
	}
	if _, err := storage.Stage(t.Context(), owner, "bad.pdf", strings.NewReader("%PDF-1.7\nmissing eof")); err == nil {
		t.Fatal("missing EOF accepted")
	}
	if _, err := storage.Stage(t.Context(), owner, "large.pdf", io.LimitReader(io.MultiReader(strings.NewReader("%PDF-1.7"), strings.NewReader(strings.Repeat("x", MaxPDFBytes+1))), MaxPDFBytes+1)); !errors.Is(err, ErrInvalidPDF) {
		t.Fatalf("oversized PDF = %v, want invalid-input classification", err)
	}
	if len(objects.data) != 1 {
		t.Fatalf("objects after rejected stages = %d, want 1", len(objects.data))
	}
	count := 0
	for _, err := range redis.PDFRecords(t.Context()) {
		if err != nil {
			t.Fatal(err)
		}
		count++
	}
	if count != 1 {
		t.Fatalf("Redis metadata after rejected stages = %d records, want 1", count)
	}
}

type heldObjects struct {
	memoryObjects
	started chan struct{}
	release chan struct{}
}

func (h *heldObjects) Put(ctx context.Context, key string, source io.Reader) error {
	close(h.started)
	select {
	case <-h.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return h.memoryObjects.Put(ctx, key, source)
}

func TestPDFArtifactStorage_InFlightUploadSurvivesOutboxPass(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	const id session.SessionID = "in-flight-owner"
	if err := metadata.Save(t.Context(), session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	objects := &heldObjects{memoryObjects: memoryObjects{data: make(map[string][]byte)}, started: make(chan struct{}), release: make(chan struct{})}
	storage := New(metadata, objects)
	clock := time.Now().UTC()
	storage.now = func() time.Time { return clock }
	storage.LeaseActive = func(context.Context, session.SessionID) (bool, error) { return false, nil }
	stageDone := make(chan error, 1)
	go func() {
		_, err := storage.Stage(context.Background(), id, "slow.pdf", strings.NewReader("%PDF-1.7\n%%EOF"))
		stageDone <- err
	}()
	<-objects.started
	if err := metadata.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	active, err := metadata.PDFHasActiveStage(t.Context(), id)
	if err != nil || !active {
		t.Fatalf("durable upload guard after session deletion = %v, %v", active, err)
	}
	clock = clock.Add(6 * time.Minute)
	if err := storage.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err := metadata.PDFDeletionBatch(t.Context(), clock)
	if err != nil || len(pending) != 1 {
		t.Fatalf("outbox while upload active = %v, %v, want pending", pending, err)
	}
	close(objects.release)
	if err := <-stageDone; err == nil {
		t.Fatal("upload published after session deletion")
	}
	active, err = metadata.PDFHasActiveStage(t.Context(), id)
	if err != nil || active {
		t.Fatalf("durable upload guard after settlement = %v, %v", active, err)
	}
	if err := storage.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	pending, err = metadata.PDFDeletionBatch(t.Context(), clock)
	if err != nil || len(pending) != 0 {
		t.Fatalf("outbox after settled upload = %v, %v", pending, err)
	}
	if len(objects.data) != 0 {
		t.Fatalf("objects after settlement = %d", len(objects.data))
	}
}

func TestPDFArtifactStorage_ReconcileExpiryAndDeletion(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	const id session.SessionID = "cleanup-owner"
	sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := metadata.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	storage := New(metadata, objects)
	clock := time.Now().UTC()
	storage.now = func() time.Time { return clock }
	storage.LeaseActive = func(context.Context, session.SessionID) (bool, error) { return true, nil }
	artifact, err := storage.Stage(t.Context(), id, "unused.pdf", strings.NewReader("%PDF-1.7\n%%EOF"))
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(25 * time.Hour)
	if _, err := storage.Resolve(t.Context(), id, artifact.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired upload Resolve = %v", err)
	}
	if err := storage.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 1 {
		t.Fatal("active lease lost staged object")
	}
	storage.LeaseActive = func(context.Context, session.SessionID) (bool, error) { return false, nil }
	if err := storage.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 0 {
		t.Fatal("expired unreferenced object survived reconciliation")
	}
	artifact, err = storage.Stage(t.Context(), id, "keep.pdf", strings.NewReader("%PDF-1.7\n%%EOF"))
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.RecordUserPrompt("artifact "+artifact.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(25 * time.Hour)
	if _, err := storage.Resolve(t.Context(), id, artifact.ID); err != nil {
		t.Fatalf("snapshot-protected upload unavailable: %v", err)
	}
	if err := storage.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 1 {
		t.Fatal("snapshot reference did not protect object")
	}
	if err := metadata.Delete(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(6 * time.Minute)
	if err := storage.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 0 {
		t.Fatal("deleted session object survived outbox reconciliation")
	}
	pending, err := metadata.PDFDeletionBatch(t.Context(), clock.Add(6*time.Minute))
	if err != nil || len(pending) != 0 {
		t.Fatalf("outbox after cleanup = %v, %v", pending, err)
	}
}
