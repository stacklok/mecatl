package redisstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	migrationJobKeyBase  = "mecatl:session-metadata:migration-job:"
	migrationLockKeyBase = "mecatl:session-metadata:migration-lock:"
)

// InspectSessionMigration explicitly inventories legacy Redis snapshots for the
// authenticated maintenance workflow. Inventory pages never call this path.
func (st *Store) InspectSessionMigration(ctx context.Context) (port.SessionMigrationInspection, error) {
	generation, err := st.rebuildGeneration(ctx)
	if err != nil {
		return port.SessionMigrationInspection{}, err
	}
	state, err := st.client.Get(ctx, metadataIndexStateKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return port.SessionMigrationInspection{}, fmt.Errorf("redisstore: inspect metadata state: %w", err)
	}

	keys, err := st.snapshotKeys(ctx)
	if err != nil {
		return port.SessionMigrationInspection{}, err
	}
	inspection := port.SessionMigrationInspection{
		Available:  state != metadataIndexReady,
		Generation: strconv.FormatInt(generation, 10),
	}
	if state == metadataIndexReady {
		inspection.UnavailableReason = "already_current"
	}
	for _, key := range keys {
		values, readErr := st.client.HMGet(ctx, key, fieldBlob, fieldMtime, fieldMetadataEntry).Result()
		if readErr != nil {
			return port.SessionMigrationInspection{}, fmt.Errorf("redisstore: inspect snapshot: %w", readErr)
		}
		if len(values) != 3 || values[0] == nil || values[1] == nil {
			inspection.InvalidFamilies++
			continue
		}
		blob := []byte(redisResultString(values[0]))
		mtimeRaw := redisResultString(values[1])
		inspection.CurrentBytes += int64(len(blob))
		if values[2] != nil && redisResultString(values[2]) != "" {
			inspection.V2Families++
			continue
		}
		_, parseErr := strconv.ParseInt(mtimeRaw, 10, 64)
		sess, decodeErr := sessnap.Unmarshal(blob)
		if parseErr != nil || decodeErr != nil || sessionKey(sess.ID) != key {
			inspection.InvalidFamilies++
			continue
		}
		inspection.V1Families++
		inspection.ReclaimableBytes += int64(len(blob))
		fingerprint := snapshotFingerprint(blob, mtimeRaw)
		inspection.Families = append(inspection.Families, port.SessionMigrationFamily{
			ID: sess.ID, Handle: migrationItemHandle(sess.ID), Fingerprint: fingerprint,
			OwnerKey: migrationOwnerKey(sess.Owner), Kind: sess.Kind, State: sess.State,
			Bytes: int64(len(blob)),
		})
	}
	after, err := st.rebuildGeneration(ctx)
	if err != nil {
		return port.SessionMigrationInspection{}, err
	}
	if after != generation {
		inspection.Generation = strconv.FormatInt(after, 10)
	}
	sort.Slice(inspection.Families, func(i, j int) bool { return inspection.Families[i].Handle < inspection.Families[j].Handle })
	return inspection, nil
}

func (st *Store) snapshotKeys(ctx context.Context) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		batch, next, err := st.client.Scan(ctx, cursor, sessionKeyPrefix+"*", 100).Result()
		if err != nil {
			return nil, fmt.Errorf("redisstore: scan legacy snapshots: %w", err)
		}
		keys = append(keys, batch...)
		if next == 0 {
			break
		}
		cursor = next
	}
	sort.Strings(keys)
	return keys, nil
}

func (st *Store) rebuildGeneration(ctx context.Context) (int64, error) {
	raw, err := st.client.Get(ctx, metadataRebuildGenerationKey).Result()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("redisstore: read rebuild generation: %w", err)
	}
	generation, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("redisstore: invalid rebuild generation")
	}
	return generation, nil
}

func snapshotFingerprint(blob []byte, mtime string) string {
	sum := sha256.Sum256(append(append([]byte{}, blob...), []byte("\x00"+mtime)...))
	return hex.EncodeToString(sum[:])
}

func migrationItemHandle(id session.SessionID) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:16])
}

func migrationOwnerKey(owner *session.Principal) string {
	if owner == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(owner.Issuer + "\x00" + owner.Subject))
	return hex.EncodeToString(sum[:16])
}

var adoptMetadataScript = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'blob') ~= ARGV[1] or redis.call('HGET', KEYS[1], 'mtime') ~= ARGV[2] then
  return 0
end
if redis.call('HGET', KEYS[1], 'metadata_entry') then
  return 0
end
redis.call('HSET', KEYS[1], 'metadata_entry', ARGV[3], 'metadata_owner', ARGV[4])
redis.call('ZADD', KEYS[2], 0, ARGV[3])
if ARGV[4] ~= '' then
  redis.call('ZADD', ARGV[6] .. ARGV[4], 0, ARGV[3])
end
redis.call('HINCRBY', KEYS[3], ARGV[5], 1)
if ARGV[4] ~= '' then
  redis.call('HINCRBY', KEYS[3], ARGV[4], 1)
end
return 1
`)

// MigrateSessionFamily derives and conditionally installs one metadata row. The
// Lua compare-and-publish prevents a concurrent Save or Delete from being lost.
func (st *Store) MigrateSessionFamily(ctx context.Context, expected port.SessionMigrationFamily) (string, error) {
	key := sessionKey(expected.ID)
	values, err := st.client.HMGet(ctx, key, fieldBlob, fieldMtime).Result()
	if err != nil || len(values) != 2 || values[0] == nil || values[1] == nil {
		return "changed", nil
	}
	blob := []byte(redisResultString(values[0]))
	mtimeRaw := redisResultString(values[1])
	if snapshotFingerprint(blob, mtimeRaw) != expected.Fingerprint {
		return "changed", nil
	}
	mtimeNS, err := strconv.ParseInt(mtimeRaw, 10, 64)
	if err != nil {
		return "invalid_snapshot", nil
	}
	sess, err := sessnap.Unmarshal(blob)
	if err != nil || sess.ID != expected.ID {
		return "invalid_snapshot", nil
	}
	member, err := encodeMetadataMember(sessionMetadata(sess, time.Unix(0, mtimeNS), int64(len(blob))))
	if err != nil {
		return "", err
	}
	ownerScope := ""
	if sess.Owner != nil {
		ownerScope = metadataOwnerScope(sess.Owner)
	}
	result, err := adoptMetadataScript.Run(ctx, st.client,
		[]string{key, metadataGlobalIndexKey, metadataGenerationKey},
		blob, mtimeRaw, member, ownerScope, metadataGlobalScope, metadataOwnerIndexBase,
	).Int()
	if err != nil {
		return "", fmt.Errorf("redisstore: adopt metadata row: %w", err)
	}
	if result != 1 {
		return "changed", nil
	}
	return "", nil
}

var publishMetadataReadyScript = redis.NewScript(`
local generation = redis.call('GET', KEYS[1]) or '0'
if generation ~= ARGV[1] then
  return 0
end
for i = 3, #KEYS do
  if redis.call('EXISTS', KEYS[i]) == 0 or not redis.call('HGET', KEYS[i], 'metadata_entry') then
    return 0
  end
end
redis.call('SET', KEYS[2], ARGV[2])
return 1
`)

// FinalizeSessionMigration atomically publishes the derivative index only after
// a stable-generation scan proves every extant snapshot has a metadata row.
func (st *Store) FinalizeSessionMigration(ctx context.Context, generation string) (bool, error) {
	keys, err := st.snapshotKeys(ctx)
	if err != nil {
		return false, err
	}
	scriptKeys := make([]string, 0, len(keys)+2)
	scriptKeys = append(scriptKeys, metadataRebuildGenerationKey, metadataIndexStateKey)
	scriptKeys = append(scriptKeys, keys...)
	result, err := publishMetadataReadyScript.Run(ctx, st.client, scriptKeys, generation, metadataIndexReady).Int()
	if err != nil {
		return false, fmt.Errorf("redisstore: publish metadata index: %w", err)
	}
	return result == 1, nil
}

func migrationJobKey(id string) (string, error) {
	if len(id) != 32 {
		return "", errors.New("invalid migration job handle")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", errors.New("invalid migration job handle")
	}
	return migrationJobKeyBase + id, nil
}

var saveMigrationJobScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) ~= ARGV[1] then
  return 0
end
redis.call('SET', KEYS[2], ARGV[2])
return 1
`)

// SaveSessionMigrationJob durably checkpoints a caller-bound adoption job.
func (st *Store) SaveSessionMigrationJob(ctx context.Context, job port.SessionMigrationJob) error {
	key, err := migrationJobKey(job.ID)
	if err != nil {
		return err
	}
	tokenValue, ok := st.migrationLocks.Load(job.ID)
	if !ok {
		return errors.New("redisstore: migration job lock not held")
	}
	token, ok := tokenValue.(string)
	if !ok {
		return errors.New("redisstore: invalid migration job lock")
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("redisstore: encode migration job: %w", err)
	}
	saved, err := saveMigrationJobScript.Run(ctx, st.client,
		[]string{migrationLockKeyBase + job.ID, key}, token, data,
	).Int()
	if err != nil {
		return fmt.Errorf("redisstore: save migration job: %w", err)
	}
	if saved != 1 {
		return errors.New("redisstore: migration job lock lost")
	}
	return nil
}

// LoadSessionMigrationJob reloads one validated durable adoption checkpoint.
func (st *Store) LoadSessionMigrationJob(ctx context.Context, id string) (port.SessionMigrationJob, error) {
	key, err := migrationJobKey(id)
	if err != nil {
		return port.SessionMigrationJob{}, err
	}
	data, err := st.client.Get(ctx, key).Bytes()
	if err != nil {
		return port.SessionMigrationJob{}, err
	}
	var job port.SessionMigrationJob
	if err := json.Unmarshal(data, &job); err != nil || job.ID != id {
		return port.SessionMigrationJob{}, errors.New("redisstore: invalid migration job")
	}
	return job, nil
}

var releaseMigrationLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

// LockSessionMigrationJob excludes overlapping cross-process job drives.
func (st *Store) LockSessionMigrationJob(ctx context.Context, id string) (func() error, error) {
	if _, err := migrationJobKey(id); err != nil {
		return nil, err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	key := migrationLockKeyBase + id
	locked, err := st.client.SetNX(ctx, key, token, 10*time.Minute).Result()
	if err != nil {
		return nil, fmt.Errorf("redisstore: acquire migration job lock: %w", err)
	}
	if !locked {
		return nil, errors.New("redisstore: migration job lock not acquired")
	}
	st.migrationLocks.Store(id, token)
	return func() error {
		st.migrationLocks.CompareAndDelete(id, token)
		return releaseMigrationLockScript.Run(context.WithoutCancel(ctx), st.client, []string{key}, token).Err()
	}, nil
}

var _ port.SessionMigrationStore = (*Store)(nil)
