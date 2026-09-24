package redisstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/session"
)

const (
	pdfArtifactKeyPrefix    = "mecatl:pdf-artifacts:"
	pdfDeletionOutboxKey    = "mecatl:pdf-artifacts:delete-outbox"
	pdfActiveUploadPrefix   = "mecatl:pdf-upload-active:"
	pdfForkSourcePrefix     = "mecatl:pdf-fork-source:"
	maxPDFRecordsPerSession = 1024
	// Stage has a 30-minute request deadline. Redis's own TTL clock provides
	// another five minutes for cancellation and multipart abort to settle.
	pdfActiveUploadSeconds = 35 * 60
)

// PDFRecord is small Redis metadata. Object keys and PDF bytes are derived or
// held outside Redis; neither is a field in this record.
type PDFRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	// PDFStaging means an object write has been reserved but is not usable.
	PDFStaging = "staging"
	// PDFReady means an object is usable but not yet snapshot-committed.
	PDFReady = "ready"
	// PDFCommitted means an authoritative snapshot references this object.
	PDFCommitted = "committed"
)

func pdfArtifactKey(id session.SessionID) string { return pdfArtifactKeyPrefix + string(id) }
func pdfActiveUploadKey(id session.SessionID, artifactID string) string {
	return pdfActiveUploadPrefix + string(id) + ":" + artifactID
}
func pdfForkSourceKey(id session.SessionID) string { return pdfForkSourcePrefix + string(id) }

var pdfReserveScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('HLEN', KEYS[2]) >= tonumber(ARGV[3]) then return -1 end
if redis.call('HSETNX', KEYS[2], ARGV[1], ARGV[2]) == 0 then return -1 end
redis.call('SET', KEYS[3], '1', 'EX', ARGV[4])
return 1
`)

// ReservePDF writes the staging marker before the caller starts an object
// upload. It refuses a deleted session and caps per-session metadata growth.
func (st *Store) ReservePDF(ctx context.Context, id session.SessionID, record PDFRecord) error {
	if record.ID == "" || record.State != PDFStaging || len(record.Name) == 0 || len(record.Name) > 4*255 {
		return errors.New("pdf artifact: invalid staging metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("pdf artifact: invalid staging metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfReserveScript.Run(ctx, client, []string{sessionKey(id), pdfArtifactKey(id), pdfActiveUploadKey(id, record.ID)}, record.ID, data, maxPDFRecordsPerSession, pdfActiveUploadSeconds).Int()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	if result == 0 {
		return errors.New("pdf artifact: session unavailable")
	}
	if result != 1 {
		return errors.New("pdf artifact: metadata limit reached")
	}
	return nil
}

var pdfReserveForkScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 or redis.call('EXISTS', KEYS[2]) ~= 0 then return 0 end
local outbox_type = redis.call('TYPE', KEYS[6]).ok
if outbox_type ~= 'none' and outbox_type ~= 'zset' then return -2 end
local prior_source = redis.call('GET', KEYS[5])
if prior_source and prior_source ~= ARGV[5] then return -1 end
if redis.call('HLEN', KEYS[3]) >= tonumber(ARGV[3]) then return -1 end
if redis.call('HSETNX', KEYS[3], ARGV[1], ARGV[2]) == 0 then return -1 end
redis.call('SET', KEYS[4], '1', 'EX', ARGV[4])
redis.call('SET', KEYS[5], ARGV[5])
redis.call('ZADD', KEYS[6], 'NX', ARGV[7], ARGV[6])
return 1
`)

// ReserveForkPDF durably stages a successor copy before its object write.
// The source snapshot must exist and the successor snapshot must not exist yet.
func (st *Store) ReserveForkPDF(ctx context.Context, source, target session.SessionID, record PDFRecord) error {
	if source == "" || target == "" || source == target || record.ID == "" || record.State != PDFStaging || record.CreatedAt.IsZero() || len(record.Name) == 0 || len(record.Name) > 4*255 {
		return errors.New("pdf artifact: invalid fork staging metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("pdf artifact: invalid fork staging metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfReserveForkScript.Run(ctx, client,
		[]string{sessionKey(source), sessionKey(target), pdfArtifactKey(target), pdfActiveUploadKey(target, record.ID), pdfForkSourceKey(target), pdfDeletionOutboxKey},
		record.ID, data, maxPDFRecordsPerSession, pdfActiveUploadSeconds, string(source), string(target), record.CreatedAt.Unix()).Int()
	if err != nil || result != 1 {
		return errors.New("pdf artifact: fork staging unavailable")
	}
	return nil
}

var pdfPublishScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('HEXISTS', KEYS[2], ARGV[1]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('DEL', KEYS[3])
return 1
`)

// PublishPDF makes a validated, fully written object available for lookup.
func (st *Store) PublishPDF(ctx context.Context, id session.SessionID, record PDFRecord) error {
	if record.ID == "" || record.State != PDFReady || record.Size <= 0 || len(record.SHA256) != 64 {
		return errors.New("pdf artifact: invalid metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("pdf artifact: invalid metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfPublishScript.Run(ctx, client, []string{sessionKey(id), pdfArtifactKey(id), pdfActiveUploadKey(id, record.ID)}, record.ID, data).Int()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	if result != 1 {
		return errors.New("pdf artifact: session unavailable")
	}
	return nil
}

var pdfPublishForkScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) ~= 0 then return 0 end
if redis.call('GET', KEYS[3]) ~= ARGV[3] then return 0 end
if redis.call('HEXISTS', KEYS[2], ARGV[1]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
return 1
`)

// PublishForkPDF marks a verified private copy ready before the successor is
// published. Its active marker remains until publication or orphan cleanup.
func (st *Store) PublishForkPDF(ctx context.Context, source, target session.SessionID, record PDFRecord) error {
	if record.ID == "" || record.State != PDFReady || record.Size <= 0 || len(record.SHA256) != 64 {
		return errors.New("pdf artifact: invalid fork metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("pdf artifact: invalid fork metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfPublishForkScript.Run(ctx, client,
		[]string{sessionKey(target), pdfArtifactKey(target), pdfForkSourceKey(target)},
		record.ID, data, string(source)).Int()
	if err != nil || result != 1 {
		return errors.New("pdf artifact: fork publication unavailable")
	}
	return nil
}

var pdfLoadScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return nil end
return redis.call('HGET', KEYS[2], ARGV[1])
`)

// PDFRecordForSession atomically checks that the snapshot still exists before
// resolving metadata. The artifact ID itself grants no authority.
func (st *Store) PDFRecordForSession(ctx context.Context, id session.SessionID, artifactID string) (PDFRecord, bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return PDFRecord{}, false, errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	raw, err := pdfLoadScript.Run(ctx, client, []string{sessionKey(id), pdfArtifactKey(id)}, artifactID).Text()
	if errors.Is(err, redis.Nil) {
		return PDFRecord{}, false, nil
	}
	if err != nil {
		return PDFRecord{}, false, errors.New("pdf artifact: metadata unavailable")
	}
	var record PDFRecord
	if json.Unmarshal([]byte(raw), &record) != nil || record.ID != artifactID {
		return PDFRecord{}, false, errors.New("pdf artifact: corrupt metadata")
	}
	return record, true, nil
}

// CommitPDFRecords marks the IDs referenced by an already-saved prompt. A
// failed marker update is recoverable because the snapshot is authoritative.
func (st *Store) CommitPDFRecords(ctx context.Context, id session.SessionID, artifactIDs []string) error {
	for _, artifactID := range artifactIDs {
		record, ok, err := st.PDFRecordForSession(ctx, id, artifactID)
		if err != nil {
			return err
		}
		if !ok || record.State == PDFStaging {
			return errors.New("pdf artifact: reference unavailable")
		}
		if record.State == PDFCommitted {
			continue
		}
		record.State = PDFCommitted
		data, err := json.Marshal(record)
		if err != nil {
			return errors.New("pdf artifact: invalid metadata")
		}
		client, release, err := st.clients.acquire()
		if err != nil {
			return errors.New("pdf artifact: metadata unavailable")
		}
		result, err := pdfPublishScript.Run(ctx, client, []string{sessionKey(id), pdfArtifactKey(id), pdfActiveUploadKey(id, artifactID)}, artifactID, data).Int()
		release()
		if err != nil {
			return errors.New("pdf artifact: metadata unavailable")
		}
		if result != 1 {
			return errors.New("pdf artifact: session unavailable")
		}
	}
	return nil
}

// PDFReferencedInSnapshot answers conservatively from the authoritative
// snapshot. Artifact IDs are opaque ASCII tokens stored verbatim in the JSON
// projection; a coincidental match retains an object rather than deleting it.
func (st *Store) PDFReferencedInSnapshot(ctx context.Context, id session.SessionID, artifactID string) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	blob, err := client.HGet(ctx, sessionKey(id), fieldBlob).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("pdf artifact: metadata unavailable")
	}
	return bytes.Contains(blob, []byte(artifactID)), nil
}

// PDFSessionExists is used before prefix cleanup. A Redis outage returns an
// error so the caller retries instead of deleting an uncertain live object.
func (st *Store) PDFSessionExists(ctx context.Context, id session.SessionID) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	n, err := client.Exists(ctx, sessionKey(id)).Result()
	if err != nil {
		return false, errors.New("pdf artifact: metadata unavailable")
	}
	return n != 0, nil
}

// PDFPendingForkSource returns the source whose lease protects an unpublished
// successor. The mapping stays durable until publication or prefix cleanup.
func (st *Store) PDFPendingForkSource(ctx context.Context, target session.SessionID) (session.SessionID, bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return "", false, errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	value, err := client.Get(ctx, pdfForkSourceKey(target)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil || value == "" {
		return "", false, errors.New("pdf artifact: fork source unavailable")
	}
	return session.SessionID(value), true, nil
}

var pdfFinishForkScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('EXISTS', KEYS[2]) == 0 then return 1 end
local outbox_type = redis.call('TYPE', KEYS[3]).ok
if outbox_type ~= 'none' and outbox_type ~= 'zset' then return -1 end
redis.call('DEL', KEYS[2])
redis.call('ZREM', KEYS[3], ARGV[1])
return 1
`)

// FinishPDFFork removes prepublication cleanup intent only after an
// authoritative successor snapshot exists.
func (st *Store) FinishPDFFork(ctx context.Context, target session.SessionID) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfFinishForkScript.Run(ctx, client,
		[]string{sessionKey(target), pdfForkSourceKey(target), pdfDeletionOutboxKey}, string(target)).Int()
	if err != nil || result != 1 {
		return errors.New("pdf artifact: fork cleanup intent unavailable")
	}
	return nil
}

// PDFHasActiveStage protects an object write that began before session
// deletion or successor publication. The active key uses Redis's TTL clock,
// avoiding pod clock skew. The metadata hash remains after deletion.
func (st *Store) PDFHasActiveStage(ctx context.Context, id session.SessionID) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	values, err := client.HGetAll(ctx, pdfArtifactKey(id)).Result()
	if err != nil {
		return false, errors.New("pdf artifact: metadata unavailable")
	}
	for _, raw := range values {
		var record PDFRecord
		if json.Unmarshal([]byte(raw), &record) != nil {
			return false, errors.New("pdf artifact: corrupt metadata")
		}
		if record.ID == "" || record.CreatedAt.IsZero() {
			return false, errors.New("pdf artifact: corrupt metadata")
		}
		active, err := client.Exists(ctx, pdfActiveUploadKey(id, record.ID)).Result()
		if err != nil {
			return false, errors.New("pdf artifact: metadata unavailable")
		}
		if active != 0 {
			return true, nil
		}
	}
	return false, nil
}

// PDFRecords iterates one metadata record at a time. The per-session reserve
// limit bounds each hash and the Redis SCAN cursor bounds each fetch.
func (st *Store) PDFRecords(ctx context.Context) iter.Seq2[struct {
	SessionID session.SessionID
	Record    PDFRecord
}, error] {
	return func(yield func(struct {
		SessionID session.SessionID
		Record    PDFRecord
	}, error) bool) {
		client, release, err := st.clients.acquire()
		if err != nil {
			yield(struct {
				SessionID session.SessionID
				Record    PDFRecord
			}{}, errors.New("pdf artifact: metadata unavailable"))
			return
		}
		defer release()
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pdfArtifactKeyPrefix+"*", 100).Result()
			if err != nil {
				yield(struct {
					SessionID session.SessionID
					Record    PDFRecord
				}{}, errors.New("pdf artifact: metadata unavailable"))
				return
			}
			for _, key := range keys {
				if key == pdfDeletionOutboxKey {
					continue
				}
				id := session.SessionID(strings.TrimPrefix(key, pdfArtifactKeyPrefix))
				if id == "" {
					continue
				}
				var fieldCursor uint64
				for {
					fields, following, err := client.HScan(ctx, key, fieldCursor, "*", 100).Result()
					if err != nil {
						yield(struct {
							SessionID session.SessionID
							Record    PDFRecord
						}{}, errors.New("pdf artifact: metadata unavailable"))
						return
					}
					for i := 0; i+1 < len(fields); i += 2 {
						var rec PDFRecord
						if json.Unmarshal([]byte(fields[i+1]), &rec) != nil {
							yield(struct {
								SessionID session.SessionID
								Record    PDFRecord
							}{}, errors.New("pdf artifact: corrupt metadata"))
							return
						}
						if !yield(struct {
							SessionID session.SessionID
							Record    PDFRecord
						}{id, rec}, nil) {
							return
						}
					}
					if following == 0 {
						break
					}
					fieldCursor = following
				}
			}
			if next == 0 {
				return
			}
			cursor = next
		}
	}
}

var pdfDeleteRecordScript = redis.NewScript(`
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('DEL', KEYS[2])
return 1
`)

// DeletePDFRecord removes unreferenced metadata after its private object is gone.
func (st *Store) DeletePDFRecord(ctx context.Context, id session.SessionID, artifactID string) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	if err := pdfDeleteRecordScript.Run(ctx, client, []string{pdfArtifactKey(id), pdfActiveUploadKey(id, artifactID)}, artifactID).Err(); err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	return nil
}

// PDFDeletionBatch returns only outbox members older than the age grace.
func (st *Store) PDFDeletionBatch(ctx context.Context, now time.Time) ([]session.SessionID, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return nil, errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	values, err := client.ZRangeByScore(ctx, pdfDeletionOutboxKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10), Count: 100}).Result()
	if err != nil {
		return nil, errors.New("pdf artifact: metadata unavailable")
	}
	out := make([]session.SessionID, len(values))
	for i, value := range values {
		out[i] = session.SessionID(value)
	}
	return out, nil
}

var pdfFinishDeletionScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) ~= 0 then return 0 end
local outbox_type = redis.call('TYPE', KEYS[4]).ok
if outbox_type ~= 'none' and outbox_type ~= 'zset' then return -1 end
redis.call('DEL', KEYS[2], KEYS[3])
redis.call('ZREM', KEYS[4], ARGV[1])
return 1
`)

// FinishPDFDeletion clears metadata and durable cleanup intent after the
// private prefix is gone and the authoritative snapshot is absent.
func (st *Store) FinishPDFDeletion(ctx context.Context, id session.SessionID) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("pdf artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfFinishDeletionScript.Run(ctx, client,
		[]string{sessionKey(id), pdfArtifactKey(id), pdfForkSourceKey(id), pdfDeletionOutboxKey}, string(id)).Int()
	if err != nil || result != 1 {
		return errors.New("pdf artifact: metadata unavailable")
	}
	return nil
}
