package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	artifactPDFMIMEType          = "application/pdf"
	artifactKeyPrefix            = "mecatl:artifacts:"
	artifactDeletionOutboxKey    = "mecatl:artifacts:delete-outbox"
	artifactActiveUploadPrefix   = "mecatl:artifact-upload-active:"
	artifactForkSourcePrefix     = "mecatl:artifact-fork-source:"
	maxArtifactRecordsPerSession = 1024
	// Stage has a 30-minute request deadline. Redis's own TTL clock provides
	// another five minutes for cancellation and multipart abort to settle.
	artifactActiveUploadSeconds = 35 * 60
)

// ArtifactRecord is small Redis metadata. Object keys and PDF bytes are derived or
// held outside Redis; neither is a field in this record.
type ArtifactRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	MIMEType  string    `json:"mime_type"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	// ArtifactStaging means an object write has been reserved but is not usable.
	ArtifactStaging = "staging"
	// ArtifactReady means an object is usable but not yet snapshot-committed.
	ArtifactReady = "ready"
	// ArtifactCommitted means an authoritative snapshot references this object.
	ArtifactCommitted = "committed"
)

func artifactKey(id session.SessionID) string { return artifactKeyPrefix + string(id) }
func artifactActiveUploadKey(id session.SessionID, artifactID string) string {
	return artifactActiveUploadPrefix + string(id) + ":" + artifactID
}
func artifactForkSourceKey(id session.SessionID) string { return artifactForkSourcePrefix + string(id) }

var artifactReserveScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('HLEN', KEYS[2]) >= tonumber(ARGV[3]) then return -1 end
if redis.call('HSETNX', KEYS[2], ARGV[1], ARGV[2]) == 0 then return -1 end
redis.call('SET', KEYS[3], '1', 'EX', ARGV[4])
return 1
`)

// ReserveArtifact writes the staging marker before the caller starts an object
// upload. It refuses a deleted session and caps per-session metadata growth.
func (st *Store) ReserveArtifact(ctx context.Context, id session.SessionID, record ArtifactRecord) error {
	if record.ID == "" || record.State != ArtifactStaging || record.MIMEType != artifactPDFMIMEType || len(record.Name) == 0 || len(record.Name) > 4*255 {
		return errors.New("artifact: invalid staging metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("artifact: invalid staging metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	result, err := artifactReserveScript.Run(ctx, client, []string{sessionKey(id), artifactKey(id), artifactActiveUploadKey(id, record.ID)}, record.ID, data, maxArtifactRecordsPerSession, artifactActiveUploadSeconds).Int()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	if result == 0 {
		return errors.New("artifact: session unavailable")
	}
	if result != 1 {
		return errors.New("artifact: metadata limit reached")
	}
	return nil
}

var artifactReserveForkScript = redis.NewScript(`
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

// ReserveForkArtifact durably stages a successor copy before its object write.
// The source snapshot must exist and the successor snapshot must not exist yet.
func (st *Store) ReserveForkArtifact(ctx context.Context, source, target session.SessionID, record ArtifactRecord) error {
	if source == "" || target == "" || source == target || record.ID == "" || record.State != ArtifactStaging || record.MIMEType != artifactPDFMIMEType || record.CreatedAt.IsZero() || len(record.Name) == 0 || len(record.Name) > 4*255 {
		return errors.New("artifact: invalid fork staging metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("artifact: invalid fork staging metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	result, err := artifactReserveForkScript.Run(ctx, client,
		[]string{sessionKey(source), sessionKey(target), artifactKey(target), artifactActiveUploadKey(target, record.ID), artifactForkSourceKey(target), artifactDeletionOutboxKey},
		record.ID, data, maxArtifactRecordsPerSession, artifactActiveUploadSeconds, string(source), string(target), record.CreatedAt.Unix()).Int()
	if err != nil || result != 1 {
		return errors.New("artifact: fork staging unavailable")
	}
	return nil
}

var artifactPublishScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('HEXISTS', KEYS[2], ARGV[1]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
redis.call('DEL', KEYS[3])
return 1
`)

// PublishArtifact makes a validated, fully written object available for lookup.
func (st *Store) PublishArtifact(ctx context.Context, id session.SessionID, record ArtifactRecord) error {
	if record.ID == "" || record.State != ArtifactReady || record.MIMEType != artifactPDFMIMEType || record.Size <= 0 || len(record.SHA256) != 64 {
		return errors.New("artifact: invalid metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("artifact: invalid metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	result, err := artifactPublishScript.Run(ctx, client, []string{sessionKey(id), artifactKey(id), artifactActiveUploadKey(id, record.ID)}, record.ID, data).Int()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	if result != 1 {
		return errors.New("artifact: session unavailable")
	}
	return nil
}

var artifactPublishForkScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) ~= 0 then return 0 end
if redis.call('GET', KEYS[3]) ~= ARGV[3] then return 0 end
if redis.call('HEXISTS', KEYS[2], ARGV[1]) == 0 then return 0 end
redis.call('HSET', KEYS[2], ARGV[1], ARGV[2])
return 1
`)

// PublishForkArtifact marks a verified private copy ready before the successor is
// published. Its active marker remains until publication or orphan cleanup.
func (st *Store) PublishForkArtifact(ctx context.Context, source, target session.SessionID, record ArtifactRecord) error {
	if record.ID == "" || record.State != ArtifactReady || record.MIMEType != artifactPDFMIMEType || record.Size <= 0 || len(record.SHA256) != 64 {
		return errors.New("artifact: invalid fork metadata")
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("artifact: invalid fork metadata")
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	result, err := artifactPublishForkScript.Run(ctx, client,
		[]string{sessionKey(target), artifactKey(target), artifactForkSourceKey(target)},
		record.ID, data, string(source)).Int()
	if err != nil || result != 1 {
		return errors.New("artifact: fork publication unavailable")
	}
	return nil
}

var artifactLoadScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return nil end
return redis.call('HGET', KEYS[2], ARGV[1])
`)

// ArtifactRecordForSession atomically checks that the snapshot still exists before
// resolving metadata. The artifact ID itself grants no authority.
func (st *Store) ArtifactRecordForSession(ctx context.Context, id session.SessionID, artifactID string) (ArtifactRecord, bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return ArtifactRecord{}, false, errors.New("artifact: metadata unavailable")
	}
	defer release()
	raw, err := artifactLoadScript.Run(ctx, client, []string{sessionKey(id), artifactKey(id)}, artifactID).Text()
	if errors.Is(err, redis.Nil) {
		return ArtifactRecord{}, false, nil
	}
	if err != nil {
		return ArtifactRecord{}, false, errors.New("artifact: metadata unavailable")
	}
	var record ArtifactRecord
	if json.Unmarshal([]byte(raw), &record) != nil || record.ID != artifactID || record.MIMEType != artifactPDFMIMEType {
		return ArtifactRecord{}, false, errors.New("artifact: corrupt metadata")
	}
	return record, true, nil
}

// CommitArtifactRecords marks the IDs referenced by an already-saved prompt. A
// failed marker update is recoverable because the snapshot is authoritative.
func (st *Store) CommitArtifactRecords(ctx context.Context, id session.SessionID, artifactIDs []string) error {
	for _, artifactID := range artifactIDs {
		record, ok, err := st.ArtifactRecordForSession(ctx, id, artifactID)
		if err != nil {
			return err
		}
		if !ok || record.State == ArtifactStaging {
			return errors.New("artifact: reference unavailable")
		}
		if record.State == ArtifactCommitted {
			continue
		}
		record.State = ArtifactCommitted
		data, err := json.Marshal(record)
		if err != nil {
			return errors.New("artifact: invalid metadata")
		}
		client, release, err := st.clients.acquire()
		if err != nil {
			return errors.New("artifact: metadata unavailable")
		}
		result, err := artifactPublishScript.Run(ctx, client, []string{sessionKey(id), artifactKey(id), artifactActiveUploadKey(id, artifactID)}, artifactID, data).Int()
		release()
		if err != nil {
			return errors.New("artifact: metadata unavailable")
		}
		if result != 1 {
			return errors.New("artifact: session unavailable")
		}
	}
	return nil
}

// ArtifactReferencedInSnapshot checks typed PDF parts in the authoritative snapshot.
// A corrupt snapshot is an error, so reconciliation cannot delete uncertain data.
func (st *Store) ArtifactReferencedInSnapshot(ctx context.Context, id session.SessionID, artifactID string) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, errors.New("artifact: metadata unavailable")
	}
	defer release()
	blob, err := client.HGet(ctx, sessionKey(id), fieldBlob).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("artifact: metadata unavailable")
	}
	restored, err := sessnap.Unmarshal(blob)
	if err != nil || restored.ID != id {
		return false, errors.New("artifact: corrupt snapshot")
	}
	for _, message := range restored.Conversation.Messages {
		if message.Role == session.RoleUser {
			for _, part := range message.Parts {
				if part.Kind == session.MediaPDF && part.ArtifactID == artifactID {
					return true, nil
				}
			}
		}
		if message.Role == session.RoleTool && message.ToolResult != nil {
			for _, part := range message.ToolResult.Parts {
				if part.BlockKind == session.BlockArtifact && part.ArtifactID == artifactID {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

// ArtifactSessionExists is used before prefix cleanup. A Redis outage returns an
// error so the caller retries instead of deleting an uncertain live object.
func (st *Store) ArtifactSessionExists(ctx context.Context, id session.SessionID) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, errors.New("artifact: metadata unavailable")
	}
	defer release()
	n, err := client.Exists(ctx, sessionKey(id)).Result()
	if err != nil {
		return false, errors.New("artifact: metadata unavailable")
	}
	return n != 0, nil
}

// ArtifactPendingForkSource returns the source whose lease protects an unpublished
// successor. The mapping stays durable until publication or prefix cleanup.
func (st *Store) ArtifactPendingForkSource(ctx context.Context, target session.SessionID) (session.SessionID, bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return "", false, errors.New("artifact: metadata unavailable")
	}
	defer release()
	value, err := client.Get(ctx, artifactForkSourceKey(target)).Result()
	if errors.Is(err, redis.Nil) {
		return "", false, nil
	}
	if err != nil || value == "" {
		return "", false, errors.New("artifact: fork source unavailable")
	}
	return session.SessionID(value), true, nil
}

var artifactFinishForkScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
if redis.call('EXISTS', KEYS[2]) == 0 then return 1 end
local outbox_type = redis.call('TYPE', KEYS[3]).ok
if outbox_type ~= 'none' and outbox_type ~= 'zset' then return -1 end
redis.call('DEL', KEYS[2])
redis.call('ZREM', KEYS[3], ARGV[1])
return 1
`)

// FinishArtifactFork removes prepublication cleanup intent only after an
// authoritative successor snapshot exists.
func (st *Store) FinishArtifactFork(ctx context.Context, target session.SessionID) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	result, err := artifactFinishForkScript.Run(ctx, client,
		[]string{sessionKey(target), artifactForkSourceKey(target), artifactDeletionOutboxKey}, string(target)).Int()
	if err != nil || result != 1 {
		return errors.New("artifact: fork cleanup intent unavailable")
	}
	return nil
}

// ArtifactHasActiveStage protects an object write that began before session
// deletion or successor publication. The active key uses Redis's TTL clock,
// avoiding pod clock skew. The metadata hash remains after deletion.
func (st *Store) ArtifactHasActiveStage(ctx context.Context, id session.SessionID) (bool, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return false, errors.New("artifact: metadata unavailable")
	}
	defer release()
	values, err := client.HGetAll(ctx, artifactKey(id)).Result()
	if err != nil {
		return false, errors.New("artifact: metadata unavailable")
	}
	for _, raw := range values {
		var record ArtifactRecord
		if json.Unmarshal([]byte(raw), &record) != nil {
			return false, errors.New("artifact: corrupt metadata")
		}
		if record.ID == "" || record.CreatedAt.IsZero() {
			return false, errors.New("artifact: corrupt metadata")
		}
		active, err := client.Exists(ctx, artifactActiveUploadKey(id, record.ID)).Result()
		if err != nil {
			return false, errors.New("artifact: metadata unavailable")
		}
		if active != 0 {
			return true, nil
		}
	}
	return false, nil
}

// ArtifactRecords iterates one metadata record at a time. The per-session reserve
// limit bounds each hash and the Redis SCAN cursor bounds each fetch.
func (st *Store) ArtifactRecords(ctx context.Context) iter.Seq2[struct {
	SessionID session.SessionID
	Record    ArtifactRecord
}, error] {
	return func(yield func(struct {
		SessionID session.SessionID
		Record    ArtifactRecord
	}, error) bool) {
		type entry = struct {
			SessionID session.SessionID
			Record    ArtifactRecord
		}
		client, release, err := st.clients.acquire()
		if err != nil {
			yield(entry{}, errors.New("artifact: metadata unavailable"))
			return
		}
		defer release()
		scan := client.Scan(ctx, 0, artifactKeyPrefix+"*", 100).Iterator()
		for scan.Next(ctx) {
			key := scan.Val()
			if key == artifactDeletionOutboxKey {
				continue
			}
			id := session.SessionID(strings.TrimPrefix(key, artifactKeyPrefix))
			if id == "" {
				continue
			}
			fields := client.HScan(ctx, key, 0, "*", 100).Iterator()
			for fields.Next(ctx) {
				field := fields.Val()
				if !fields.Next(ctx) {
					if fields.Err() != nil {
						yield(entry{}, errors.New("artifact: metadata unavailable"))
					} else {
						yield(entry{}, errors.New("artifact: corrupt metadata"))
					}
					return
				}
				var rec ArtifactRecord
				if json.Unmarshal([]byte(fields.Val()), &rec) != nil || rec.ID != field || rec.MIMEType != artifactPDFMIMEType {
					yield(entry{}, errors.New("artifact: corrupt metadata"))
					return
				}
				if !yield(entry{id, rec}, nil) {
					return
				}
			}
			if fields.Err() != nil {
				yield(entry{}, errors.New("artifact: metadata unavailable"))
				return
			}
		}
		if scan.Err() != nil {
			yield(entry{}, errors.New("artifact: metadata unavailable"))
		}
	}
}

var artifactDeleteRecordScript = redis.NewScript(`
redis.call('HDEL', KEYS[1], ARGV[1])
redis.call('DEL', KEYS[2])
return 1
`)

// DeleteArtifactRecord removes unreferenced metadata after its private object is gone.
func (st *Store) DeleteArtifactRecord(ctx context.Context, id session.SessionID, artifactID string) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	if err := artifactDeleteRecordScript.Run(ctx, client, []string{artifactKey(id), artifactActiveUploadKey(id, artifactID)}, artifactID).Err(); err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	return nil
}

// ArtifactDeletionBatch returns only outbox members older than the age grace.
func (st *Store) ArtifactDeletionBatch(ctx context.Context, now time.Time) ([]session.SessionID, error) {
	client, release, err := st.clients.acquire()
	if err != nil {
		return nil, errors.New("artifact: metadata unavailable")
	}
	defer release()
	values, err := client.ZRangeByScore(ctx, artifactDeletionOutboxKey, &redis.ZRangeBy{Min: "-inf", Max: strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10), Count: 100}).Result()
	if err != nil {
		return nil, errors.New("artifact: metadata unavailable")
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

// FinishArtifactDeletion clears metadata and durable cleanup intent after the
// private prefix is gone and the authoritative snapshot is absent.
func (st *Store) FinishArtifactDeletion(ctx context.Context, id session.SessionID) error {
	client, release, err := st.clients.acquire()
	if err != nil {
		return errors.New("artifact: metadata unavailable")
	}
	defer release()
	result, err := pdfFinishDeletionScript.Run(ctx, client,
		[]string{sessionKey(id), artifactKey(id), artifactForkSourceKey(id), artifactDeletionOutboxKey}, string(id)).Int()
	if err != nil || result != 1 {
		return errors.New("artifact: metadata unavailable")
	}
	return nil
}
