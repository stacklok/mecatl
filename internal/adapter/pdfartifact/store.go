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
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

const (
	// MaxPDFBytes is the maximum accepted size of one private PDF artifact.
	MaxPDFBytes       = 20 << 20
	stagedLifetime    = 24 * time.Hour
	cleanupGrace      = 5 * time.Minute
	maxUploadDuration = 30 * time.Minute
)

var (
	// ErrNotFound means the artifact is unavailable to this session.
	ErrNotFound = fmt.Errorf("%w: artifact not found", server.ErrNotFound)
	// ErrInvalidPDF means the supplied PDF or its metadata is invalid.
	ErrInvalidPDF = fmt.Errorf("%w: invalid PDF artifact", server.ErrInvalidArgument)
	// ErrStorage means private artifact storage is unavailable.
	ErrStorage = fmt.Errorf("%w: artifact storage unavailable", server.ErrInternal)
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

var _ server.ArtifactLifecycle = (*Store)(nil)

// New binds Redis metadata and private objects into one artifact lifecycle.
func New(metadata *redisstore.Store, objects ObjectStore) *Store {
	return &Store{metadata: metadata, objects: objects, now: time.Now}
}

func objectPrefix(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	return "artifacts/v1/" + hex.EncodeToString(sum[:]) + "/"
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

// Stage validates and writes one PDF after reserving durable staging metadata.
func (st *Store) Stage(ctx context.Context, id session.SessionID, name, mimeType string, source io.Reader) (server.Artifact, error) {
	if st == nil || st.metadata == nil || st.objects == nil {
		return server.Artifact{}, ErrStorage
	}
	if !safeName(name) || mimeType != "application/pdf" || source == nil {
		return server.Artifact{}, ErrInvalidPDF
	}
	if err := ctx.Err(); err != nil {
		return server.Artifact{}, err
	}
	uploadCtx, cancel := context.WithTimeout(ctx, maxUploadDuration)
	defer cancel()
	artifactID, err := newID()
	if err != nil {
		return server.Artifact{}, err
	}
	created := st.now().UTC()
	record := redisstore.ArtifactRecord{ID: artifactID, Name: name, MIMEType: mimeType, State: redisstore.ArtifactStaging, CreatedAt: created}
	if err := st.metadata.ReserveArtifact(uploadCtx, id, record); err != nil {
		return server.Artifact{}, ErrStorage
	}
	key := objectKey(id, artifactID)
	validator := &validatingReader{source: source, ctx: uploadCtx, digest: sha256.New()}
	if err := st.objects.Put(uploadCtx, key, validator); err != nil {
		// Keep the staging marker for a restart-safe retry if object cleanup fails.
		st.cleanupFailedStage(ctx, id, key, artifactID)
		if ctx.Err() != nil {
			return server.Artifact{}, ctx.Err()
		}
		if validator.inputFailure != nil {
			return server.Artifact{}, ErrInvalidPDF
		}
		return server.Artifact{}, ErrStorage
	}
	if err := validator.finish(); err != nil {
		st.cleanupFailedStage(ctx, id, key, artifactID)
		return server.Artifact{}, err
	}
	record.Size = validator.size
	record.SHA256 = hex.EncodeToString(validator.digest.Sum(nil))
	record.State = redisstore.ArtifactReady
	if err := st.metadata.PublishArtifact(uploadCtx, id, record); err != nil {
		st.cleanupFailedStage(ctx, id, key, artifactID)
		return server.Artifact{}, ErrStorage
	}
	return server.Artifact{ID: artifactID, Name: name, MIMEType: mimeType, Size: record.Size, SHA256: record.SHA256}, nil
}

func (st *Store) cleanupFailedStage(ctx context.Context, id session.SessionID, key, artifactID string) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if st.objects.Delete(cleanupCtx, key) == nil {
		_ = st.metadata.DeleteArtifactRecord(cleanupCtx, id, artifactID)
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

// Resolve finds a usable PDF reference owned by the given live session.
func (st *Store) Resolve(ctx context.Context, id session.SessionID, artifactID string) (server.Artifact, error) {
	if st == nil || st.metadata == nil {
		return server.Artifact{}, ErrStorage
	}
	if !validID(artifactID) {
		return server.Artifact{}, ErrNotFound
	}
	record, ok, err := st.metadata.ArtifactRecordForSession(ctx, id, artifactID)
	if err != nil {
		return server.Artifact{}, ErrStorage
	}
	if !ok || (record.State != redisstore.ArtifactReady && record.State != redisstore.ArtifactCommitted) {
		return server.Artifact{}, ErrNotFound
	}
	if record.State == redisstore.ArtifactReady && st.now().Sub(record.CreatedAt) >= stagedLifetime {
		// A snapshot may have committed the reference just before a Redis marker
		// update failed. The authoritative snapshot protects that object.
		referenced, err := st.metadata.ArtifactReferencedInSnapshot(ctx, id, artifactID)
		if err != nil {
			return server.Artifact{}, ErrStorage
		}
		if !referenced {
			return server.Artifact{}, ErrNotFound
		}
	}
	return server.Artifact{ID: record.ID, Name: record.Name, MIMEType: record.MIMEType, Size: record.Size, SHA256: record.SHA256}, nil
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

// Open resolves a session-owned PDF and opens its private object reader.
func (st *Store) Open(ctx context.Context, id session.SessionID, artifactID string) (server.Artifact, io.ReadCloser, error) {
	meta, err := st.Resolve(ctx, id, artifactID)
	if err != nil {
		return server.Artifact{}, nil, err
	}
	reader, err := st.objects.Open(ctx, objectKey(id, artifactID))
	if err != nil {
		return server.Artifact{}, nil, ErrStorage
	}
	return meta, reader, nil
}

// CommitPrompt marks snapshot-referenced PDFs durable after a successful save.
func (st *Store) CommitPrompt(ctx context.Context, id session.SessionID, artifactIDs []string) error {
	for _, artifactID := range artifactIDs {
		if _, err := st.Resolve(ctx, id, artifactID); err != nil {
			return err
		}
	}
	if err := st.metadata.CommitArtifactRecords(ctx, id, artifactIDs); err != nil {
		return ErrStorage
	}
	if err := st.metadata.FinishArtifactFork(ctx, id); err != nil {
		return ErrStorage
	}
	return nil
}

// CopyFork stages each distinct referenced PDF before copying its bytes into
// the unpublished successor's private namespace, then rewrites nested parts.
func (st *Store) CopyFork(ctx context.Context, source, target session.SessionID, history []session.Message) ([]session.Message, error) {
	if st == nil || st.metadata == nil || st.objects == nil || source == "" || target == "" || source == target {
		return nil, ErrStorage
	}
	// The whole copy must finish before the 35-minute Redis active-write keys
	// expire, even if history contains many references.
	ctx, cancel := context.WithTimeout(ctx, maxUploadDuration)
	defer cancel()
	ids := artifactHistoryIDs(history)
	if len(ids) == 0 {
		return session.CloneMessages(history), nil
	}
	copied := make(map[string]server.Artifact, len(ids))
	for _, id := range ids {
		meta, err := st.Resolve(ctx, source, id)
		if err != nil {
			return nil, err
		}
		newArtifactID, err := newID()
		if err != nil {
			return nil, err
		}
		record := redisstore.ArtifactRecord{
			ID: newArtifactID, Name: meta.Name, MIMEType: meta.MIMEType, Size: meta.Size, SHA256: meta.SHA256,
			State: redisstore.ArtifactStaging, CreatedAt: st.now().UTC(),
		}
		if err := st.metadata.ReserveForkArtifact(ctx, source, target, record); err != nil {
			return nil, ErrStorage
		}
		if err := st.copyForkObject(ctx, source, target, meta, newArtifactID); err != nil {
			return nil, err
		}
		record.State = redisstore.ArtifactReady
		if err := st.metadata.PublishForkArtifact(ctx, source, target, record); err != nil {
			return nil, ErrStorage
		}
		copied[id] = server.Artifact{ID: newArtifactID, Name: meta.Name, MIMEType: meta.MIMEType, Size: meta.Size, SHA256: meta.SHA256}
	}
	return rewriteForkArtifactParts(history, copied)
}

func artifactHistoryIDs(history []session.Message) []string {
	seen := make(map[string]struct{})
	var ids []string
	add := func(id string) {
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	for _, message := range history {
		for _, part := range message.Parts {
			if part.Kind == session.MediaPDF && part.BlockKind == "" {
				add(part.ArtifactID)
			}
		}
		if message.ToolResult != nil {
			for _, part := range message.ToolResult.Parts {
				if part.BlockKind == session.BlockArtifact {
					add(part.ArtifactID)
				}
			}
		}
	}
	return ids
}

type forkCopyReader struct {
	io.Reader
	size int64
}

func (r *forkCopyReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.size += int64(n)
	return n, err
}

func (st *Store) copyForkObject(ctx context.Context, source, target session.SessionID, meta server.Artifact, newID string) error {
	reader, err := st.objects.Open(ctx, objectKey(source, meta.ID))
	if err != nil {
		return ErrStorage
	}
	defer func() { _ = reader.Close() }()
	digest := sha256.New()
	copyReader := &forkCopyReader{Reader: io.TeeReader(io.LimitReader(reader, meta.Size+1), digest)}
	if err := st.objects.Put(ctx, objectKey(target, newID), copyReader); err != nil {
		return ErrStorage
	}
	if copyReader.size != meta.Size || hex.EncodeToString(digest.Sum(nil)) != meta.SHA256 {
		return ErrStorage
	}
	return nil
}

func rewriteForkArtifactParts(history []session.Message, copied map[string]server.Artifact) ([]session.Message, error) {
	out := session.CloneMessages(history)
	for i, message := range out {
		if len(message.Parts) != 0 {
			out[i].Parts = slices.Clone(message.Parts)
			for j, part := range out[i].Parts {
				if part.Kind != session.MediaPDF || part.BlockKind != "" {
					continue
				}
				meta, ok := copied[part.ArtifactID]
				if !ok {
					return nil, ErrStorage
				}
				updated, err := session.NewPDFContent(meta.ID, meta.Name, meta.Size, meta.SHA256)
				if err != nil {
					return nil, ErrStorage
				}
				out[i].Parts[j] = updated
			}
		}
		if message.ToolResult != nil {
			result := *message.ToolResult
			result.Parts = slices.Clone(result.Parts)
			for j, part := range result.Parts {
				if part.BlockKind != session.BlockArtifact {
					continue
				}
				meta, ok := copied[part.ArtifactID]
				if !ok {
					return nil, ErrStorage
				}
				updated, err := session.NewArtifactBlock(meta.ID, meta.Name, meta.MIMEType, meta.Size, meta.SHA256)
				if err != nil {
					return nil, ErrStorage
				}
				result.Parts[j] = updated
			}
			out[i].ToolResult = &result
		}
	}
	return out, nil
}

// DiscardUnpublished safely removes a failed successor's private objects only
// when no authoritative snapshot for that successor has been published.
func (st *Store) DiscardUnpublished(ctx context.Context, id session.SessionID) error {
	exists, err := st.metadata.ArtifactSessionExists(ctx, id)
	if err != nil {
		return ErrStorage
	}
	if exists {
		return nil
	}
	if err := st.deletePrefix(ctx, id); err != nil {
		return err
	}
	return st.metadata.FinishArtifactDeletion(ctx, id)
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
	ids, err := st.metadata.ArtifactDeletionBatch(ctx, now)
	if err != nil {
		return ErrStorage
	}
	if err := st.reconcileDeletions(ctx, ids); err != nil {
		return err
	}
	return st.reconcileRecords(ctx, now)
}

func (st *Store) reconcileDeletions(ctx context.Context, ids []session.SessionID) error {
	for _, id := range ids {
		exists, err := st.metadata.ArtifactSessionExists(ctx, id)
		if err != nil {
			return ErrStorage
		}
		if exists {
			if err := st.metadata.FinishArtifactFork(ctx, id); err != nil {
				return ErrStorage
			}
			continue
		}
		recentWrite, err := st.metadata.ArtifactHasActiveStage(ctx, id)
		if err != nil {
			return ErrStorage
		}
		if recentWrite {
			continue
		}
		forkSource, pendingFork, err := st.metadata.ArtifactPendingForkSource(ctx, id)
		if err != nil {
			return ErrStorage
		}
		if st.LeaseActive == nil {
			continue
		}
		if pendingFork {
			active, err := st.LeaseActive(ctx, forkSource)
			if err != nil {
				return ErrStorage
			}
			if active {
				continue
			}
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
		if err := st.metadata.FinishArtifactDeletion(ctx, id); err != nil {
			return ErrStorage
		}
	}
	return nil
}

func (st *Store) reconcileRecords(ctx context.Context, now time.Time) error {
	for entry, err := range st.metadata.ArtifactRecords(ctx) {
		if err != nil {
			return ErrStorage
		}
		if err := st.reconcileRecord(ctx, now, entry.SessionID, entry.Record); err != nil {
			return err
		}
	}
	return nil
}

func (st *Store) reconcileRecord(ctx context.Context, now time.Time, id session.SessionID, record redisstore.ArtifactRecord) error {
	if !validID(record.ID) {
		return ErrStorage
	}
	if now.Sub(record.CreatedAt) < stagedLifetime+cleanupGrace || record.State == redisstore.ArtifactCommitted {
		return nil
	}
	exists, err := st.metadata.ArtifactSessionExists(ctx, id)
	if err != nil {
		return ErrStorage
	}
	if !exists || st.LeaseActive == nil {
		// Absent sessions are handled by the deletion outbox. A live session
		// without a lease check is retained conservatively.
		return nil
	}
	active, err := st.LeaseActive(ctx, id)
	if err != nil {
		return ErrStorage
	}
	if active {
		return nil
	}
	referenced, err := st.metadata.ArtifactReferencedInSnapshot(ctx, id, record.ID)
	if err != nil {
		return ErrStorage
	}
	if referenced {
		if record.State == redisstore.ArtifactReady {
			_ = st.metadata.CommitArtifactRecords(ctx, id, []string{record.ID})
		}
		return nil
	}
	if err := st.objects.Delete(ctx, objectKey(id, record.ID)); err != nil {
		return ErrStorage
	}
	if err := st.metadata.DeleteArtifactRecord(ctx, id, record.ID); err != nil {
		return ErrStorage
	}
	return nil
}
