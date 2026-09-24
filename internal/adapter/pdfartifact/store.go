// Package pdfartifact stores private session-owned PDF objects outside Redis.
// Redis retains only bounded metadata, staging state, and cleanup intent.
package pdfartifact

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	MaxPDFBytes       = 20 << 20
	stagedLifetime    = 24 * time.Hour
	cleanupGrace      = 5 * time.Minute
	maxUploadDuration = 30 * time.Minute
)

var (
	ErrNotFound   = fmt.Errorf("%w: PDF artifact not found", server.ErrNotFound)
	ErrInvalidPDF = fmt.Errorf("%w: invalid PDF artifact", server.ErrInvalidArgument)
	ErrStorage    = fmt.Errorf("%w: PDF artifact storage unavailable", server.ErrInternal)
)

// ObjectStore writes and reads private immutable objects. Implementations must
// abort unfinished writes when ctx is cancelled or source returns an error.
type ObjectStore interface {
	Put(context.Context, string, io.Reader) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
	ListPrefix(context.Context, string) ([]string, error)
}

// Store implements the server's lifecycle using one shared Redis adapter and
// an S3-compatible object store. The Redis adapter owns atomic delete intent.
type Store struct {
	metadata *redisstore.Store
	objects  ObjectStore
	now      func() time.Time
	// LeaseActive is installed by composition. Without it, reconciliation
	// conservatively keeps unreferenced objects of still-live sessions.
	LeaseActive func(context.Context, session.SessionID) (bool, error)
}

var _ server.PDFArtifactLifecycle = (*Store)(nil)

func New(metadata *redisstore.Store, objects ObjectStore) *Store {
	return &Store{metadata: metadata, objects: objects, now: time.Now}
}

func objectPrefix(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	return "pdf/v1/" + hex.EncodeToString(sum[:]) + "/"
}

func objectKey(id session.SessionID, artifactID string) string { return objectPrefix(id) + artifactID }

func newID() (string, error) {
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", ErrStorage
	}
	return hex.EncodeToString(random[:]), nil
}

func safeName(name string) bool {
	if !utf8.ValidString(name) || name == "." || name == ".." {
		return false
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > 255 {
		return false
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func (st *Store) Stage(ctx context.Context, id session.SessionID, name string, source io.Reader) (server.PDFArtifact, error) {
	if st == nil || st.metadata == nil || st.objects == nil {
		return server.PDFArtifact{}, ErrStorage
	}
	if !safeName(name) || source == nil {
		return server.PDFArtifact{}, ErrInvalidPDF
	}
	if err := ctx.Err(); err != nil {
		return server.PDFArtifact{}, err
	}
	uploadCtx, cancel := context.WithTimeout(ctx, maxUploadDuration)
	defer cancel()
	artifactID, err := newID()
	if err != nil {
		return server.PDFArtifact{}, err
	}
	created := st.now().UTC()
	record := redisstore.PDFRecord{ID: artifactID, Name: name, State: redisstore.PDFStaging, CreatedAt: created}
	if err := st.metadata.ReservePDF(uploadCtx, id, record); err != nil {
		return server.PDFArtifact{}, ErrStorage
	}
	key := objectKey(id, artifactID)
	validator := &validatingReader{source: source, ctx: uploadCtx, digest: sha256.New()}
	if err := st.objects.Put(uploadCtx, key, validator); err != nil {
		// Keep the staging marker for a restart-safe retry if object cleanup fails.
		st.cleanupFailedStage(ctx, id, key, artifactID)
		if ctx.Err() != nil {
			return server.PDFArtifact{}, ctx.Err()
		}
		if validator.inputFailure != nil {
			return server.PDFArtifact{}, ErrInvalidPDF
		}
		return server.PDFArtifact{}, ErrStorage
	}
	if err := validator.finish(); err != nil {
		st.cleanupFailedStage(ctx, id, key, artifactID)
		return server.PDFArtifact{}, err
	}
	record.Size = validator.size
	record.SHA256 = hex.EncodeToString(validator.digest.Sum(nil))
	record.State = redisstore.PDFReady
	if err := st.metadata.PublishPDF(uploadCtx, id, record); err != nil {
		st.cleanupFailedStage(ctx, id, key, artifactID)
		return server.PDFArtifact{}, ErrStorage
	}
	return server.PDFArtifact{ID: artifactID, Name: name, Size: record.Size, SHA256: record.SHA256}, nil
}

func (st *Store) cleanupFailedStage(ctx context.Context, id session.SessionID, key, artifactID string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if st.objects.Delete(cleanupCtx, key) == nil {
		_ = st.metadata.DeletePDFRecord(cleanupCtx, id, artifactID)
	}
}

// validatingReader keeps only the first five bytes, a final 1024-byte ring,
// and a SHA-256 digest while the object writer applies natural backpressure.
type validatingReader struct {
	source       io.Reader
	ctx          context.Context
	digest       hash.Hash
	size         int64
	head         [5]byte
	headSize     int
	tail         [1024]byte
	tailSize     int
	seenEOF      bool
	noProgress   int
	inputFailure error
}

func (v *validatingReader) Read(p []byte) (int, error) {
	if err := v.ctx.Err(); err != nil {
		return 0, err
	}
	if v.size > MaxPDFBytes {
		v.inputFailure = ErrInvalidPDF
		return 0, ErrInvalidPDF
	}
	if len(p) > MaxPDFBytes+1-int(v.size) {
		p = p[:MaxPDFBytes+1-int(v.size)]
	}
	n, err := v.source.Read(p)
	if n > 0 {
		v.noProgress = 0
		v.size += int64(n)
		_, _ = v.digest.Write(p[:n])
		if v.headSize < len(v.head) {
			v.headSize += copy(v.head[v.headSize:], p[:n])
		}
		switch {
		case n >= len(v.tail):
			copy(v.tail[:], p[n-len(v.tail):n])
			v.tailSize = len(v.tail)
		case v.tailSize+n <= len(v.tail):
			v.tailSize += copy(v.tail[v.tailSize:], p[:n])
		default:
			drop := v.tailSize + n - len(v.tail)
			copy(v.tail[:], v.tail[drop:v.tailSize])
			copy(v.tail[v.tailSize-drop:], p[:n])
			v.tailSize = len(v.tail)
		}
		if v.size > MaxPDFBytes {
			v.inputFailure = ErrInvalidPDF
			return n, ErrInvalidPDF
		}
	}
	if n == 0 && err == nil {
		v.noProgress++
		if v.noProgress >= 100 {
			v.inputFailure = ErrInvalidPDF
			return 0, io.ErrNoProgress
		}
	}
	if errors.Is(err, io.EOF) {
		v.seenEOF = true
	} else if err != nil {
		v.inputFailure = ErrInvalidPDF
	}
	return n, err
}

func (v *validatingReader) finish() error {
	if !v.seenEOF || v.size == 0 || v.size > MaxPDFBytes || string(v.head[:]) != "%PDF-" || !bytes.Contains(v.tail[:v.tailSize], []byte("%%EOF")) {
		return ErrInvalidPDF
	}
	return nil
}

func (st *Store) Resolve(ctx context.Context, id session.SessionID, artifactID string) (server.PDFArtifact, error) {
	if st == nil || st.metadata == nil {
		return server.PDFArtifact{}, ErrStorage
	}
	if !validID(artifactID) {
		return server.PDFArtifact{}, ErrNotFound
	}
	record, ok, err := st.metadata.PDFRecordForSession(ctx, id, artifactID)
	if err != nil {
		return server.PDFArtifact{}, ErrStorage
	}
	if !ok || (record.State != redisstore.PDFReady && record.State != redisstore.PDFCommitted) {
		return server.PDFArtifact{}, ErrNotFound
	}
	if record.State == redisstore.PDFReady && st.now().Sub(record.CreatedAt) >= stagedLifetime {
		// A snapshot may have committed the reference just before a Redis marker
		// update failed. The authoritative snapshot protects that object.
		referenced, err := st.metadata.PDFReferencedInSnapshot(ctx, id, artifactID)
		if err != nil {
			return server.PDFArtifact{}, ErrStorage
		}
		if !referenced {
			return server.PDFArtifact{}, ErrNotFound
		}
	}
	return server.PDFArtifact{ID: record.ID, Name: record.Name, Size: record.Size, SHA256: record.SHA256}, nil
}

func validID(id string) bool {
	if len(id) != 48 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}

func (st *Store) Open(ctx context.Context, id session.SessionID, artifactID string) (server.PDFArtifact, io.ReadCloser, error) {
	meta, err := st.Resolve(ctx, id, artifactID)
	if err != nil {
		return server.PDFArtifact{}, nil, err
	}
	reader, err := st.objects.Open(ctx, objectKey(id, artifactID))
	if err != nil {
		return server.PDFArtifact{}, nil, ErrStorage
	}
	return meta, reader, nil
}

func (st *Store) CommitPrompt(ctx context.Context, id session.SessionID, artifactIDs []string) error {
	for _, artifactID := range artifactIDs {
		if _, err := st.Resolve(ctx, id, artifactID); err != nil {
			return err
		}
	}
	if err := st.metadata.CommitPDFRecords(ctx, id, artifactIDs); err != nil {
		return ErrStorage
	}
	return nil
}

// CopyFork is completed with the engine's PDF content fields by the fork
// integration task. Returning an error prevents publishing a broken successor.
func (st *Store) CopyFork(context.Context, session.SessionID, session.SessionID, []session.Message) ([]session.Message, error) {
	return nil, ErrStorage
}

// DiscardUnpublished safely removes a failed successor's private objects only
// when no authoritative snapshot for that successor has been published.
func (st *Store) DiscardUnpublished(ctx context.Context, id session.SessionID) error {
	exists, err := st.metadata.PDFSessionExists(ctx, id)
	if err != nil {
		return ErrStorage
	}
	if exists {
		return nil
	}
	if err := st.deletePrefix(ctx, id); err != nil {
		return err
	}
	return st.metadata.FinishPDFDeletion(ctx, id)
}

func (st *Store) deletePrefix(ctx context.Context, id session.SessionID) error {
	keys, err := st.objects.ListPrefix(ctx, objectPrefix(id))
	if err != nil {
		return ErrStorage
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, objectPrefix(id)) {
			return ErrStorage
		}
		if err := st.objects.Delete(ctx, key); err != nil {
			return ErrStorage
		}
	}
	return nil
}

// Reconcile drains old deletion intent first. It repairs marker-update failure
// against the snapshot, then reclaims expired unreferenced uploads only when
// composition supplies a lease check and the session is inactive.
func (st *Store) Reconcile(ctx context.Context) error {
	if st == nil || st.metadata == nil || st.objects == nil {
		return ErrStorage
	}
	now := st.now()
	ids, err := st.metadata.PDFDeletionBatch(ctx, now)
	if err != nil {
		return ErrStorage
	}
	for _, id := range ids {
		exists, err := st.metadata.PDFSessionExists(ctx, id)
		if err != nil {
			return ErrStorage
		}
		if exists {
			continue
		}
		recentWrite, err := st.metadata.PDFHasActiveStage(ctx, id)
		if err != nil {
			return ErrStorage
		}
		if recentWrite {
			continue
		}
		if st.LeaseActive == nil {
			continue
		}
		active, err := st.LeaseActive(ctx, id)
		if err != nil {
			return ErrStorage
		}
		if active {
			continue
		}
		if err := st.deletePrefix(ctx, id); err != nil {
			return err
		}
		if err := st.metadata.FinishPDFDeletion(ctx, id); err != nil {
			return ErrStorage
		}
	}
	for entry, err := range st.metadata.PDFRecords(ctx) {
		if err != nil {
			return ErrStorage
		}
		if !validID(entry.Record.ID) {
			return ErrStorage
		}
		if now.Sub(entry.Record.CreatedAt) < stagedLifetime+cleanupGrace {
			continue
		}
		if entry.Record.State == redisstore.PDFCommitted {
			continue
		}
		exists, err := st.metadata.PDFSessionExists(ctx, entry.SessionID)
		if err != nil {
			return ErrStorage
		}
		if exists && st.LeaseActive == nil {
			continue
		}
		if exists {
			active, err := st.LeaseActive(ctx, entry.SessionID)
			if err != nil {
				return ErrStorage
			}
			if active {
				continue
			}
			referenced, err := st.metadata.PDFReferencedInSnapshot(ctx, entry.SessionID, entry.Record.ID)
			if err != nil {
				return ErrStorage
			}
			if referenced {
				if entry.Record.State == redisstore.PDFReady {
					_ = st.metadata.CommitPDFRecords(ctx, entry.SessionID, []string{entry.Record.ID})
				}
				continue
			}
		}
		if err := st.objects.Delete(ctx, objectKey(entry.SessionID, entry.Record.ID)); err != nil {
			return ErrStorage
		}
		if err := st.metadata.DeletePDFRecord(ctx, entry.SessionID, entry.Record.ID); err != nil {
			return ErrStorage
		}
	}
	return nil
}
