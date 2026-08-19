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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	migrationJobKeyBase           = "mecatl:session-metadata:migration-job:"
	migrationLockKeyBase          = "mecatl:session-metadata:migration-lock:"
	migrationFenceKeyBase         = "mecatl:session-metadata:migration-fence:"
	migrationInspectionMaxRetries = 3
	migrationScanBatchSize        = 100
)

var errMetadataIndexCoverage = errors.New("redisstore: metadata index coverage mismatch")

var (
	migrationLockTTL       = 10 * time.Minute
	migrationRenewInterval = 3 * time.Minute
)

// InspectSessionMigration explicitly inventories legacy Redis snapshots for the
// authenticated maintenance workflow. Inventory pages never call this path.
func (st *Store) InspectSessionMigration(ctx context.Context) (port.SessionMigrationInspection, error) {
	for range migrationInspectionMaxRetries {
		generation, err := st.rebuildGeneration(ctx)
		if err != nil {
			return port.SessionMigrationInspection{}, err
		}
		inspection, err := st.inspectSessionMigrationGeneration(ctx, generation)
		if err != nil {
			return port.SessionMigrationInspection{}, err
		}
		if st.migrationInspectionObserver != nil {
			st.migrationInspectionObserver()
		}
		after, err := st.rebuildGeneration(ctx)
		if err != nil {
			return port.SessionMigrationInspection{}, err
		}
		if after == generation {
			sort.Slice(inspection.Families, func(i, j int) bool { return inspection.Families[i].Handle < inspection.Families[j].Handle })
			return inspection, nil
		}
	}
	after, err := st.rebuildGeneration(ctx)
	if err != nil {
		return port.SessionMigrationInspection{}, err
	}
	return port.SessionMigrationInspection{
		Available: false, UnavailableReason: "inventory_changed_restart",
		Generation: strconv.FormatInt(after, 10),
	}, nil
}

//nolint:gocyclo // Stable inspection keeps snapshot validation and repair classification in one pass.
func (st *Store) inspectSessionMigrationGeneration(ctx context.Context, generation int64) (port.SessionMigrationInspection, error) {
	state, err := st.client.Get(ctx, metadataIndexStateKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return port.SessionMigrationInspection{}, fmt.Errorf("redisstore: inspect metadata state: %w", err)
	}
	keys, err := st.snapshotKeys(ctx)
	if err != nil {
		return port.SessionMigrationInspection{}, err
	}
	inspection := port.SessionMigrationInspection{
		Available: state != metadataIndexReady, Generation: strconv.FormatInt(generation, 10),
	}
	if state == metadataIndexReady {
		inspection.UnavailableReason = "already_current"
	}
	for _, key := range keys {
		values, readErr := st.client.HMGet(ctx, key, fieldBlob, fieldMtime, fieldMetadataEntry, fieldMetadataOwner).Result()
		if readErr != nil {
			return port.SessionMigrationInspection{}, fmt.Errorf("redisstore: inspect snapshot: %w", readErr)
		}
		if len(values) != 4 || values[0] == nil || values[1] == nil {
			inspection.InvalidFamilies++
			continue
		}
		blob := []byte(toString(values[0]))
		mtimeRaw := toString(values[1])
		inspection.CurrentBytes += int64(len(blob))
		mtimeNS, parseErr := strconv.ParseInt(mtimeRaw, 10, 64)
		sess, decodeErr := sessnap.Unmarshal(blob)
		if parseErr != nil || decodeErr != nil || sessionKey(sess.ID) != key {
			inspection.InvalidFamilies++
			continue
		}
		family := port.SessionMigrationFamily{
			ID: sess.ID, Handle: migrationItemHandle(sess.ID), Fingerprint: snapshotFingerprint(blob, mtimeRaw),
			OwnerKey: migrationOwnerKey(sess.Owner), Kind: sess.Kind, State: sess.State, Bytes: int64(len(blob)),
		}
		member := ""
		if values[2] != nil {
			member = toString(values[2])
		}
		if member == "" {
			inspection.V1Families++
			inspection.ReclaimableBytes += int64(len(blob))
			inspection.Families = append(inspection.Families, family)
			continue
		}
		inspection.V2Families++
		expectedMember, encodeErr := encodeMetadataMember(sessionMetadata(sess, time.Unix(0, mtimeNS).UTC(), int64(len(blob))))
		if encodeErr != nil {
			return port.SessionMigrationInspection{}, encodeErr
		}
		ownerScope := ""
		if sess.Owner != nil {
			ownerScope = metadataOwnerScope(sess.Owner)
		}
		storedOwner := ""
		if values[3] != nil {
			storedOwner = toString(values[3])
		}
		globalScoreErr := st.client.ZScore(ctx, metadataGlobalIndexKey, expectedMember).Err()
		if globalScoreErr != nil && !errors.Is(globalScoreErr, redis.Nil) {
			return port.SessionMigrationInspection{}, fmt.Errorf("redisstore: inspect global metadata coverage: %w", globalScoreErr)
		}
		ownerCovered := true
		if ownerScope != "" {
			ownerScoreErr := st.client.ZScore(ctx, metadataOwnerIndexBase+ownerScope, expectedMember).Err()
			if ownerScoreErr != nil && !errors.Is(ownerScoreErr, redis.Nil) {
				return port.SessionMigrationInspection{}, fmt.Errorf("redisstore: inspect owner metadata coverage: %w", ownerScoreErr)
			}
			ownerCovered = ownerScoreErr == nil
		}
		if member != expectedMember || storedOwner != ownerScope || globalScoreErr != nil || !ownerCovered {
			inspection.Families = append(inspection.Families, family)
		}
	}
	return inspection, nil
}

func (st *Store) snapshotKeys(ctx context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		batch, next, err := st.client.Scan(ctx, cursor, sessionKeyPrefix+"*", migrationScanBatchSize).Result()
		if err != nil {
			return nil, fmt.Errorf("redisstore: scan legacy snapshots: %w", err)
		}
		for _, key := range batch {
			seen[key] = struct{}{}
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
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

// adoptMetadataScript is the ONE deliberate exemption from
// metadataRebuildGenerationKey's "every mutator INCRs it" invariant (see the
// doc comment on that constant in metadata_index.go): its writes happen
// under the exclusive fenced migration lock, entirely before the exact-
// coverage verification window opens, so they must NOT bump the generation
// migration itself is about to verify against. It shares
// metadataOwnerIndexBase's Redis-Cluster key-declaration caveat too (same
// file).
var adoptMetadataScript = redis.NewScript(`
if redis.call('GET', KEYS[4]) ~= ARGV[1] then
  return -1
end
if redis.call('HGET', KEYS[1], 'blob') ~= ARGV[2] or redis.call('HGET', KEYS[1], 'mtime') ~= ARGV[3] then
  return 0
end
local old_member = redis.call('HGET', KEYS[1], 'metadata_entry')
local old_scope = redis.call('HGET', KEYS[1], 'metadata_owner') or ''
if old_member then
  redis.call('ZREM', KEYS[2], old_member)
  if old_scope ~= '' then
    redis.call('ZREM', ARGV[7] .. old_scope, old_member)
  end
end
redis.call('HSET', KEYS[1], 'metadata_entry', ARGV[4], 'metadata_owner', ARGV[5])
redis.call('ZADD', KEYS[2], 0, ARGV[4])
if ARGV[5] ~= '' then
  redis.call('ZADD', ARGV[7] .. ARGV[5], 0, ARGV[4])
end
redis.call('HINCRBY', KEYS[3], ARGV[6], 1)
if old_scope ~= '' and old_scope ~= ARGV[5] then
  redis.call('HINCRBY', KEYS[3], old_scope, 1)
end
if ARGV[5] ~= '' then
  redis.call('HINCRBY', KEYS[3], ARGV[5], 1)
end
return 1
`)

// MigrateSessionFamily derives and conditionally installs one metadata row. The
// Lua compare-and-publish prevents a concurrent Save or Delete from being lost.
func (st *Store) MigrateSessionFamily(ctx context.Context, expected port.SessionMigrationFamily) (string, error) {
	acquisition, ok := migrationAcquisitionFromContext(ctx, "")
	if !ok || acquisition.lost.Load() {
		return "", errors.New("redisstore: migration job lock lost")
	}
	key := sessionKey(expected.ID)
	values, err := st.client.HMGet(ctx, key, fieldBlob, fieldMtime).Result()
	if err != nil || len(values) != 2 || values[0] == nil || values[1] == nil {
		return "changed", nil
	}
	blob := []byte(toString(values[0]))
	mtimeRaw := toString(values[1])
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
	member, err := encodeMetadataMember(sessionMetadata(sess, time.Unix(0, mtimeNS).UTC(), int64(len(blob))))
	if err != nil {
		return "", err
	}
	ownerScope := ""
	if sess.Owner != nil {
		ownerScope = metadataOwnerScope(sess.Owner)
	}
	if st.migrationMutationObserver != nil {
		st.migrationMutationObserver()
	}
	result, err := adoptMetadataScript.Run(ctx, st.client,
		[]string{key, metadataGlobalIndexKey, metadataGenerationKey, acquisition.key},
		acquisition.token, blob, mtimeRaw, member, ownerScope, metadataGlobalScope, metadataOwnerIndexBase,
	).Int()
	if err != nil {
		return "", fmt.Errorf("redisstore: adopt metadata row: %w", err)
	}
	if result == -1 {
		acquisition.markLost()
		return "", errors.New("redisstore: migration job lock lost")
	}
	if result != 1 {
		return "changed", nil
	}
	return "", nil
}

type expectedMetadataIndexes struct {
	global map[string]struct{}
	owners map[string]map[string]struct{}
}

func (st *Store) verifyMetadataIndexCoverage(ctx context.Context, generation int64, expectedFamilies int64) (bool, error) {
	before, err := st.rebuildGeneration(ctx)
	if err != nil || before != generation {
		return false, err
	}
	expected, err := st.expectedMetadataIndexes(ctx)
	if err != nil {
		return false, err
	}
	if int64(len(expected.global)) != expectedFamilies {
		return false, errMetadataIndexCoverage
	}
	if err := st.verifySortedSetCoverage(ctx, metadataGlobalIndexKey, expected.global); err != nil {
		return false, err
	}
	if err := st.verifyOwnerIndexCoverage(ctx, expected.owners); err != nil {
		return false, err
	}
	after, err := st.rebuildGeneration(ctx)
	if err != nil {
		return false, err
	}
	return after == generation, nil
}

func (st *Store) expectedMetadataIndexes(ctx context.Context) (expectedMetadataIndexes, error) {
	keys, err := st.snapshotKeys(ctx)
	if err != nil {
		return expectedMetadataIndexes{}, err
	}
	expected := expectedMetadataIndexes{
		global: make(map[string]struct{}, len(keys)),
		owners: make(map[string]map[string]struct{}),
	}
	for _, key := range keys {
		member, ownerScope, deriveErr := st.expectedMetadataMember(ctx, key)
		if deriveErr != nil {
			return expectedMetadataIndexes{}, deriveErr
		}
		expected.global[member] = struct{}{}
		if ownerScope == "" {
			continue
		}
		members := expected.owners[ownerScope]
		if members == nil {
			members = make(map[string]struct{})
			expected.owners[ownerScope] = members
		}
		members[member] = struct{}{}
	}
	return expected, nil
}

func (st *Store) expectedMetadataMember(ctx context.Context, key string) (string, string, error) {
	values, err := st.client.HMGet(ctx, key, fieldBlob, fieldMtime, fieldMetadataEntry, fieldMetadataOwner).Result()
	if err != nil {
		return "", "", fmt.Errorf("redisstore: verify metadata snapshot: %w", err)
	}
	if len(values) != 4 || values[0] == nil || values[1] == nil || values[2] == nil {
		return "", "", errMetadataIndexCoverage
	}
	blob := []byte(toString(values[0]))
	mtimeRaw := toString(values[1])
	mtimeNS, parseErr := strconv.ParseInt(mtimeRaw, 10, 64)
	sess, decodeErr := sessnap.Unmarshal(blob)
	if parseErr != nil || decodeErr != nil || sessionKey(sess.ID) != key {
		return "", "", errMetadataIndexCoverage
	}
	member, err := encodeMetadataMember(sessionMetadata(sess, time.Unix(0, mtimeNS).UTC(), int64(len(blob))))
	if err != nil {
		return "", "", err
	}
	ownerScope := ""
	if sess.Owner != nil {
		ownerScope = metadataOwnerScope(sess.Owner)
	}
	storedOwner := ""
	if values[3] != nil {
		storedOwner = toString(values[3])
	}
	if toString(values[2]) != member || storedOwner != ownerScope {
		return "", "", errMetadataIndexCoverage
	}
	return member, ownerScope, nil
}

func (st *Store) verifyOwnerIndexCoverage(ctx context.Context, expected map[string]map[string]struct{}) error {
	seen := make(map[string]struct{})
	var cursor uint64
	for {
		keys, next, err := st.client.Scan(ctx, cursor, metadataOwnerIndexBase+"*", migrationScanBatchSize).Result()
		if err != nil {
			return fmt.Errorf("redisstore: scan owner metadata indexes: %w", err)
		}
		for _, key := range keys {
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			scope := strings.TrimPrefix(key, metadataOwnerIndexBase)
			members, ok := expected[scope]
			if !ok {
				return errMetadataIndexCoverage
			}
			if err := st.verifySortedSetCoverage(ctx, key, members); err != nil {
				return err
			}
			delete(expected, scope)
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	if len(expected) != 0 {
		return errMetadataIndexCoverage
	}
	return nil
}

func (st *Store) verifySortedSetCoverage(ctx context.Context, key string, expected map[string]struct{}) error {
	remaining := make(map[string]struct{}, len(expected))
	for member := range expected {
		remaining[member] = struct{}{}
	}
	seen := make(map[string]struct{}, len(expected))
	var cursor uint64
	for {
		values, next, err := st.client.ZScan(ctx, key, cursor, "*", migrationScanBatchSize).Result()
		if err != nil {
			return fmt.Errorf("redisstore: scan metadata index: %w", err)
		}
		if len(values)%2 != 0 {
			return errMetadataIndexCoverage
		}
		for i := 0; i < len(values); i += 2 {
			member := values[i]
			score, scoreErr := strconv.ParseFloat(values[i+1], 64)
			if scoreErr != nil || score != 0 {
				return errMetadataIndexCoverage
			}
			if _, duplicate := seen[member]; duplicate {
				continue
			}
			seen[member] = struct{}{}
			if _, ok := remaining[member]; !ok {
				return errMetadataIndexCoverage
			}
			delete(remaining, member)
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	if len(remaining) != 0 {
		return errMetadataIndexCoverage
	}
	return nil
}

var publishMetadataReadyScript = redis.NewScript(`
if redis.call('GET', KEYS[4]) ~= ARGV[1] then
  return -1
end
local generation = redis.call('GET', KEYS[1]) or '0'
if generation ~= ARGV[2] then
  return 0
end
if redis.call('ZCARD', KEYS[3]) ~= tonumber(ARGV[4]) then
  return 0
end
redis.call('SET', KEYS[2], ARGV[3])
return 1
`)

// FinalizeSessionMigrationCoverage proves the exact global and per-owner index
// contents with bounded client-side scans before publishing readiness with a
// constant-work Redis CAS. The CAS rechecks the generation, so a concurrent
// Save or Delete cannot invalidate the proof before publication.
func (st *Store) FinalizeSessionMigrationCoverage(ctx context.Context, generation string, expectedFamilies int64) (bool, error) {
	generationNumber, err := strconv.ParseInt(generation, 10, 64)
	if err != nil || expectedFamilies < 0 {
		return false, errors.New("redisstore: invalid migration coverage")
	}
	acquisition, ok := migrationAcquisitionFromContext(ctx, "")
	if !ok || acquisition.lost.Load() {
		return false, errors.New("redisstore: migration job lock lost")
	}
	covered, err := st.verifyMetadataIndexCoverage(ctx, generationNumber, expectedFamilies)
	if err != nil || !covered {
		return false, err
	}
	if st.migrationMutationObserver != nil {
		st.migrationMutationObserver()
	}
	result, err := publishMetadataReadyScript.Run(ctx, st.client,
		[]string{metadataRebuildGenerationKey, metadataIndexStateKey, metadataGlobalIndexKey, acquisition.key},
		acquisition.token, generation, metadataIndexReady, expectedFamilies,
	).Int()
	if err != nil {
		return false, fmt.Errorf("redisstore: publish metadata index: %w", err)
	}
	if result == -1 {
		acquisition.markLost()
		return false, errors.New("redisstore: migration job lock lost")
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
	acquisition, ok := migrationAcquisitionFromContext(ctx, job.ID)
	if !ok {
		return errors.New("redisstore: migration job lock acquisition not bound")
	}
	if acquisition.lost.Load() {
		return errors.New("redisstore: migration job lock lost")
	}
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("redisstore: encode migration job: %w", err)
	}
	saved, err := saveMigrationJobScript.Run(ctx, st.client,
		[]string{acquisition.key, key}, acquisition.token, data,
	).Int()
	if err != nil {
		return fmt.Errorf("redisstore: save migration job: %w", err)
	}
	if saved != 1 {
		acquisition.markLost()
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

var acquireMigrationLockScript = redis.NewScript(`
local fence = redis.call('INCR', KEYS[2])
local token = tostring(fence) .. ':' .. ARGV[1]
if redis.call('SET', KEYS[1], token, 'NX', 'PX', ARGV[2]) then
  return token
end
return ''
`)

var renewMigrationLockScript = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('PEXPIRE', KEYS[1], ARGV[2])
end
return 0
`)

type migrationAcquisitionContextKey struct{}

type migrationLockAcquisition struct {
	id              string
	key             string
	token           string
	renewCancel     context.CancelFunc
	operationCancel context.CancelFunc
	done            chan struct{}
	lost            atomic.Bool
	once            sync.Once
}

func (a *migrationLockAcquisition) markLost() {
	a.lost.Store(true)
	a.operationCancel()
}

func migrationAcquisitionFromContext(ctx context.Context, id string) (*migrationLockAcquisition, bool) {
	acquisition, ok := ctx.Value(migrationAcquisitionContextKey{}).(*migrationLockAcquisition)
	return acquisition, ok && acquisition != nil && (id == "" || acquisition.id == id)
}

// AcquireSessionMigrationJob binds one fenced acquisition to the returned
// context. Renewal and every checkpoint/release use that exact acquisition.
func (st *Store) AcquireSessionMigrationJob(ctx context.Context, id string) (context.Context, func() error, error) {
	if _, err := migrationJobKey(id); err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	key := migrationLockKeyBase + id
	token, err := acquireMigrationLockScript.Run(ctx, st.client,
		[]string{key, migrationFenceKeyBase + id}, hex.EncodeToString(nonce), migrationLockTTL.Milliseconds(),
	).Text()
	if err != nil {
		return nil, nil, fmt.Errorf("redisstore: acquire migration job lock: %w", err)
	}
	if token == "" {
		return nil, nil, errors.New("redisstore: migration job lock not acquired")
	}
	renewCtx, renewCancel := context.WithCancel(context.Background())
	operationCtx, operationCancel := context.WithCancel(ctx)
	acquisition := &migrationLockAcquisition{
		id: id, key: key, token: token, renewCancel: renewCancel, operationCancel: operationCancel, done: make(chan struct{}),
	}
	go st.renewMigrationLock(renewCtx, acquisition)
	bound := context.WithValue(operationCtx, migrationAcquisitionContextKey{}, acquisition)
	release := func() error {
		var releaseErr error
		acquisition.once.Do(func() {
			acquisition.renewCancel()
			<-acquisition.done
			result, err := releaseMigrationLockScript.Run(context.WithoutCancel(bound), st.client, []string{key}, token).Int()
			acquisition.operationCancel()
			if err != nil {
				releaseErr = err
				return
			}
			if result != 1 {
				acquisition.markLost()
				releaseErr = errors.New("redisstore: migration job lock lost")
			}
		})
		return releaseErr
	}
	return bound, release, nil
}

func (st *Store) renewMigrationLock(ctx context.Context, acquisition *migrationLockAcquisition) {
	defer close(acquisition.done)
	ticker := time.NewTicker(migrationRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewed, err := renewMigrationLockScript.Run(ctx, st.client,
				[]string{acquisition.key}, acquisition.token, migrationLockTTL.Milliseconds(),
			).Int()
			if err != nil || renewed != 1 {
				acquisition.markLost()
				return
			}
		}
	}
}

// CheckSessionMigrationJobOwnership fails closed once this acquisition is lost.
func (st *Store) CheckSessionMigrationJobOwnership(ctx context.Context) error {
	acquisition, ok := ctx.Value(migrationAcquisitionContextKey{}).(*migrationLockAcquisition)
	if !ok || acquisition == nil || acquisition.lost.Load() {
		return errors.New("redisstore: migration job lock lost")
	}
	value, err := st.client.Get(ctx, acquisition.key).Result()
	if err != nil || value != acquisition.token {
		acquisition.markLost()
		return errors.New("redisstore: migration job lock lost")
	}
	return nil
}

var _ port.SessionMigrationStore = (*Store)(nil)
