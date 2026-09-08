package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	metadataIndexStateKey  = "mecatl:session-metadata:state"
	metadataIndexReady     = "redis-metadata-index/1"
	metadataIndexStale     = "redis-metadata-index/stale"
	metadataGlobalIndexKey = "mecatl:session-metadata:index:all"
	// metadataOwnerIndexBase is concatenated with an owner scope INSIDE each
	// mutating Lua script below (saveMetadataScript/deleteMetadataScript/
	// conditionalDeleteMetadataScript here, adoptMetadataScript in
	// migration.go) rather than being declared in the script's KEYS[] array.
	// That is fine for a single-node/Sentinel redis.Client (the only client
	// this package constructs today) but is a Redis CLUSTER landmine: a
	// cluster mandates every key a script touches be named in KEYS[] for
	// slot routing/validation, and a dynamically-built key outside KEYS[]
	// raises a cross-slot error the moment this runs against a real
	// cluster. Adding Cluster support later is NOT a client-swap — each of
	// these four scripts needs hash-tagged keys or KEYS-array key building
	// first.
	metadataOwnerIndexBase = "mecatl:session-metadata:index:owner:"
	metadataGenerationKey  = "mecatl:session-metadata:generations"
	// metadataRebuildGenerationKey's INCR is the load-bearing half of the
	// exact-coverage proof in migration.go's verify*Coverage functions:
	// "generation unchanged across the verification window ⇒ no membership
	// drift" holds ONLY because every mutator of metadataGlobalIndexKey/an
	// owner index either INCRs this key in the same script (saveMetadataScript,
	// deleteMetadataScript, conditionalDeleteMetadataScript) or is a
	// deliberate, reviewed exemption (migration.go's adoptMetadataScript,
	// whose writes happen under the exclusive fenced migration lock before
	// the verification window starts). If a FIFTH mutator of those sorted
	// sets is ever added, it must either INCR this key too or be added to
	// this exemption list — otherwise the coverage proof silently degrades
	// to "probably fine."
	metadataRebuildGenerationKey = "mecatl:session-metadata:rebuild-generation"
	metadataGlobalScope          = "redis-v1:all"
	metadataOwnerScopeBase       = "redis-v1:owner:"
	metadataNilOwnerScope        = "redis-v1:owner:none"
	metadataContinuationV1       = "redis-v1."
)

type metadataWorkKind uint8

const (
	metadataWorkLoad metadataWorkKind = iota + 1
	metadataWorkRow
)

func (st *Store) observeMetadataWork(kind metadataWorkKind) {
	if st.metadataWorkObserver != nil {
		st.metadataWorkObserver(kind)
	}
}

// initializeMetadataIndex distinguishes a new indexable store from a legacy
// store without reading any snapshot payload. Legacy records remain loadable,
// but bounded inventory is honestly unavailable until an explicit migration
// has saved every record through the current format.
func initializeMetadataIndex(ctx context.Context, client redis.UniversalClient) error {
	state, err := client.Get(ctx, metadataIndexStateKey).Result()
	if err == nil {
		if state != metadataIndexReady && state != metadataIndexStale {
			return fmt.Errorf("redisstore: unknown metadata index state %q", state)
		}
		return nil
	}
	if !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redisstore: read metadata index state: %w", err)
	}

	state = metadataIndexReady
	var cursor uint64
	for {
		keys, next, scanErr := client.Scan(ctx, cursor, sessionKeyPrefix+"*", 1).Result()
		if scanErr != nil {
			return fmt.Errorf("redisstore: inspect legacy metadata index: %w", scanErr)
		}
		if len(keys) > 0 {
			state = metadataIndexStale
			break
		}
		if next == 0 {
			break
		}
		cursor = next
	}
	if err := client.SetNX(ctx, metadataIndexStateKey, state, 0).Err(); err != nil {
		return fmt.Errorf("redisstore: initialize metadata index state: %w", err)
	}
	return nil
}

var createMetadataScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) ~= 0 then
  return 0
end
redis.call('HSET', KEYS[1],
  'blob', ARGV[1],
  'mtime', ARGV[2],
  'metadata_entry', ARGV[3],
  'metadata_owner', ARGV[5],
  'lineage_key', ARGV[8])
redis.call('ZADD', KEYS[2], 0, ARGV[3])
if ARGV[5] ~= '' then
  redis.call('ZADD', ARGV[7] .. ARGV[5], 0, ARGV[3])
end
redis.call('HINCRBY', KEYS[3], ARGV[4], 1)
if ARGV[5] ~= '' then
  redis.call('HINCRBY', KEYS[3], ARGV[5], 1)
end
redis.call('HSET', KEYS[5], ARGV[8], ARGV[9])
redis.call('INCR', KEYS[4])
return 1
`)

var saveMetadataScript = redis.NewScript(`
local old_member = redis.call('HGET', KEYS[1], 'metadata_entry')
local old_scope = redis.call('HGET', KEYS[1], 'metadata_owner') or ''
local old_lineage_key = redis.call('HGET', KEYS[1], 'lineage_key')
if not old_lineage_key and old_member then
  old_lineage_key = ARGV[11]
end
if old_lineage_key and old_lineage_key ~= ARGV[8] then
  local lineage = redis.call('HGET', KEYS[5], old_lineage_key)
  if lineage then
    local tombstone, count = string.gsub(lineage, '"State":"retained"', '"State":"pruned"')
    local deleted_count
    tombstone, deleted_count = string.gsub(tombstone, '"DeletedAt":"0001%-01%-01T00:00:00Z"', '"DeletedAt":"' .. ARGV[10] .. '"')
    if count ~= 1 or deleted_count ~= 1 then
      return redis.error_reply('invalid lineage record')
    end
    redis.call('HSET', KEYS[5], old_lineage_key, tombstone)
  end
end
if old_member then
  redis.call('ZREM', KEYS[2], old_member)
  if old_scope ~= '' then
    redis.call('ZREM', ARGV[7] .. old_scope, old_member)
  end
end
redis.call('HSET', KEYS[1],
  'blob', ARGV[1],
  'mtime', ARGV[2],
  'metadata_entry', ARGV[3],
  'metadata_owner', ARGV[5],
  'lineage_key', ARGV[8])
redis.call('ZADD', KEYS[2], 0, ARGV[3])
if ARGV[5] ~= '' then
  redis.call('ZADD', ARGV[7] .. ARGV[5], 0, ARGV[3])
end
redis.call('HINCRBY', KEYS[3], ARGV[4], 1)
if old_scope ~= '' and old_scope ~= ARGV[5] then
  redis.call('HINCRBY', KEYS[3], old_scope, 1)
end
if ARGV[5] ~= '' then
  redis.call('HINCRBY', KEYS[3], ARGV[5], 1)
end
redis.call('HSET', KEYS[5], ARGV[8], ARGV[9])
redis.call('INCR', KEYS[4])
return 1
`)

func sessionMetadata(s *session.Session, modifiedAt time.Time, size int64) port.SessionDiscoveryMeta {
	return port.SessionDiscoveryMeta{
		ID: s.ID, ModifiedAt: modifiedAt, State: s.State, Turns: s.Counters.Turns,
		ModelID: s.ModelID, CreatedAt: s.CreatedAt, Title: s.Title,
		TitleProvenance: s.TitleProvenance, Owner: s.Owner.Clone(), EnvironmentRef: s.EnvironmentRef,
		Kind: s.Kind, Relationship: s.Relationship, EstimatedBytes: size,
	}
}

func saveSnapshotAndMetadata(ctx context.Context, client redis.UniversalClient, s *session.Session, blob []byte, modifiedAt time.Time) error {
	row := sessionMetadata(s, modifiedAt, int64(len(blob)))
	member, err := encodeMetadataMember(row)
	if err != nil {
		return err
	}
	ownerScope := ""
	if s.Owner != nil {
		ownerScope = metadataOwnerScope(s.Owner)
	}
	lineage, err := json.Marshal(redisLineageRecord(s))
	if err != nil {
		return err
	}
	return saveMetadataScript.Run(ctx, client,
		[]string{sessionKey(s.ID), metadataGlobalIndexKey, metadataGenerationKey, metadataRebuildGenerationKey, lineageHashKey},
		blob, modifiedAt.UnixNano(), member, metadataGlobalScope, ownerScope, metadataIndexStateKey,
		metadataOwnerIndexBase, redisLineageKey(s.ID, string(s.Incarnation())), lineage, time.Now().UTC().Format(time.RFC3339Nano), string(s.ID),
	).Err()
}

func createSnapshotAndMetadata(ctx context.Context, client redis.UniversalClient, s *session.Session, blob []byte, modifiedAt time.Time) (bool, error) {
	row := sessionMetadata(s, modifiedAt, int64(len(blob)))
	member, err := encodeMetadataMember(row)
	if err != nil {
		return false, err
	}
	ownerScope := ""
	if s.Owner != nil {
		ownerScope = metadataOwnerScope(s.Owner)
	}
	lineage, err := json.Marshal(redisLineageRecord(s))
	if err != nil {
		return false, err
	}
	created, err := createMetadataScript.Run(ctx, client,
		[]string{sessionKey(s.ID), metadataGlobalIndexKey, metadataGenerationKey, metadataRebuildGenerationKey, lineageHashKey},
		blob, modifiedAt.UnixNano(), member, metadataGlobalScope, ownerScope, metadataIndexStateKey,
		metadataOwnerIndexBase, redisLineageKey(s.ID, string(s.Incarnation())), lineage,
	).Int()
	return created == 1, err
}

var deleteMetadataScript = redis.NewScript(`
local member = redis.call('HGET', KEYS[1], 'metadata_entry')
local owner_scope = redis.call('HGET', KEYS[1], 'metadata_owner') or ''
if member then
  redis.call('ZREM', KEYS[2], member)
  redis.call('HINCRBY', KEYS[3], ARGV[1], 1)
  if owner_scope ~= '' then
    redis.call('ZREM', ARGV[2] .. owner_scope, member)
    redis.call('HINCRBY', KEYS[3], owner_scope, 1)
  end
end
local lineage_key = redis.call('HGET', KEYS[1], 'lineage_key') or ARGV[3]
local lineage = redis.call('HGET', KEYS[8], lineage_key)
if lineage then
  local tombstone, count = string.gsub(lineage, '"State":"retained"', '"State":"pruned"')
  local deleted_count
  tombstone, deleted_count = string.gsub(tombstone, '"DeletedAt":"0001%-01%-01T00:00:00Z"', '"DeletedAt":"' .. ARGV[4] .. '"')
  if count ~= 1 or deleted_count ~= 1 then
    return redis.error_reply('invalid lineage record')
  end
  redis.call('HSET', KEYS[8], lineage_key, tombstone)
end
redis.call('DEL', KEYS[1], KEYS[4], KEYS[5], KEYS[7], KEYS[9])
redis.call('INCR', KEYS[6])
return 1
`)

func deleteSessionAndMetadata(ctx context.Context, client redis.UniversalClient, id session.SessionID) error {
	return deleteMetadataScript.Run(ctx, client,
		[]string{sessionKey(id), metadataGlobalIndexKey, metadataGenerationKey, toolsKey(id), eventsKey(id), metadataRebuildGenerationKey, eventsGenerationKey(id), lineageHashKey, ledgerKey(id)},
		metadataGlobalScope, metadataOwnerIndexBase, string(id), time.Now().UTC().Format(time.RFC3339Nano),
	).Err()
}

var conditionalDeleteMetadataScript = redis.NewScript(`
local member = redis.call('HGET', KEYS[1], 'metadata_entry')
if not member or member ~= ARGV[1] then
  return 0
end
local owner_scope = redis.call('HGET', KEYS[1], 'metadata_owner') or ''
redis.call('ZREM', KEYS[2], member)
redis.call('HINCRBY', KEYS[3], ARGV[2], 1)
if owner_scope ~= '' then
  redis.call('ZREM', ARGV[3] .. owner_scope, member)
  redis.call('HINCRBY', KEYS[3], owner_scope, 1)
end
local lineage_key = redis.call('HGET', KEYS[1], 'lineage_key') or ARGV[4]
local lineage = redis.call('HGET', KEYS[8], lineage_key)
if lineage then
  local tombstone, count = string.gsub(lineage, '"State":"retained"', '"State":"pruned"')
  local deleted_count
  tombstone, deleted_count = string.gsub(tombstone, '"DeletedAt":"0001%-01%-01T00:00:00Z"', '"DeletedAt":"' .. ARGV[5] .. '"')
  if count ~= 1 or deleted_count ~= 1 then
    return redis.error_reply('invalid lineage record')
  end
  redis.call('HSET', KEYS[8], lineage_key, tombstone)
end
redis.call('DEL', KEYS[1], KEYS[4], KEYS[5], KEYS[7], KEYS[9])
redis.call('INCR', KEYS[6])
return 1
`)

func deleteSessionIfMetadataUnchanged(ctx context.Context, client redis.UniversalClient, expected port.SessionDiscoveryMeta) (bool, error) {
	member, err := encodeMetadataMember(expected)
	if err != nil {
		return false, err
	}
	result, err := conditionalDeleteMetadataScript.Run(ctx, client,
		[]string{sessionKey(expected.ID), metadataGlobalIndexKey, metadataGenerationKey, toolsKey(expected.ID), eventsKey(expected.ID), metadataRebuildGenerationKey, eventsGenerationKey(expected.ID), lineageHashKey, ledgerKey(expected.ID)},
		member, metadataGlobalScope, metadataOwnerIndexBase, string(expected.ID), time.Now().UTC().Format(time.RFC3339Nano),
	).Int()
	return result == 1, err
}

var pageMetadataScript = redis.NewScript(`
local generation = redis.call('HGET', KEYS[2], ARGV[1]) or '0'
if ARGV[2] ~= '' and generation ~= ARGV[2] then
  return redis.error_reply('MECATL_METADATA_CURSOR_RESTART')
end
local count = redis.call('ZCARD', KEYS[1])
local rows = {}
if tonumber(ARGV[4]) > 0 then
  rows = redis.call('ZRANGEBYLEX', KEYS[1], ARGV[3], '+', 'LIMIT', 0, ARGV[4])
end
local result = {generation, tostring(count)}
for _, row in ipairs(rows) do
  table.insert(result, row)
end
return result
`)

func (st *Store) pageSessionMetadata(ctx context.Context, client redis.UniversalClient, request port.SessionMetadataPageRequest) (port.SessionMetadataPage, error) {
	if request.Limit < 0 {
		return port.SessionMetadataPage{}, fmt.Errorf("redisstore: metadata page limit must be non-negative")
	}
	if err := requireMetadataIndex(ctx, client); err != nil {
		return port.SessionMetadataPage{}, err
	}
	scope, indexKey := metadataScopeAndKey(request)
	minimum, expectedGeneration, err := metadataPageBoundary(request.Cursor, scope)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	result, err := pageMetadataScript.Run(ctx, client, []string{indexKey, metadataGenerationKey},
		scope, expectedGeneration, minimum, request.Limit+1).Result()
	if err != nil {
		if strings.Contains(err.Error(), "MECATL_METADATA_CURSOR_RESTART") {
			return port.SessionMetadataPage{}, port.ErrSessionMetadataCursorRestart
		}
		return port.SessionMetadataPage{}, fmt.Errorf("redisstore: page metadata: %w", err)
	}
	return st.decodeMetadataPage(result, request, scope)
}

func requireMetadataIndex(ctx context.Context, client redis.UniversalClient) error {
	state, err := client.Get(ctx, metadataIndexStateKey).Result()
	if err == nil && state == metadataIndexReady {
		return nil
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("redisstore: read metadata index state: %w", err)
	}
	return fmt.Errorf("redisstore: legacy metadata index is stale: %w", port.ErrSessionMetadataPagingUnsupported)
}

func metadataPageBoundary(cursor *port.SessionMetadataCursor, scope string) (string, string, error) {
	if cursor == nil {
		return "-", "", nil
	}
	member, ok := decodeMetadataContinuation(cursor.Continuation)
	if !ok || cursor.Scope != scope {
		return "", "", port.ErrSessionMetadataCursorRestart
	}
	row, err := decodeMetadataMember(member)
	if err != nil || row.ID != cursor.ID || !row.ModifiedAt.Equal(cursor.ModifiedAt) {
		return "", "", port.ErrSessionMetadataCursorRestart
	}
	return "(" + member, cursor.Generation, nil
}

func (st *Store) decodeMetadataPage(result any, request port.SessionMetadataPageRequest, scope string) (port.SessionMetadataPage, error) {
	values, ok := result.([]any)
	if !ok || len(values) < 2 {
		return port.SessionMetadataPage{}, fmt.Errorf("redisstore: invalid metadata page result")
	}
	generation := toString(values[0])
	total, err := strconv.Atoi(toString(values[1]))
	if err != nil {
		return port.SessionMetadataPage{}, fmt.Errorf("redisstore: invalid metadata count: %w", err)
	}
	members := values[2:]
	hasMore := len(members) > request.Limit
	if hasMore {
		members = members[:request.Limit]
	}
	rows, err := st.decodeMetadataRows(members, request)
	if err != nil {
		return port.SessionMetadataPage{}, err
	}
	page := port.SessionMetadataPage{Sessions: rows, TotalCount: total}
	if hasMore && len(rows) > 0 {
		last := rows[len(rows)-1]
		lastMember := toString(values[1+len(rows)])
		page.NextCursor = &port.SessionMetadataCursor{
			ModifiedAt: last.ModifiedAt, ID: last.ID, Generation: generation, Scope: scope,
			Continuation: encodeMetadataContinuation(lastMember),
		}
	}
	return page, nil
}

func (st *Store) decodeMetadataRows(members []any, request port.SessionMetadataPageRequest) ([]port.SessionDiscoveryMeta, error) {
	rows := make([]port.SessionDiscoveryMeta, 0, len(members))
	for _, value := range members {
		st.observeMetadataWork(metadataWorkRow)
		row, err := decodeMetadataMember(toString(value))
		if err != nil {
			return nil, fmt.Errorf("redisstore: corrupt metadata index: %w", err)
		}
		if request.OwnershipEnforced && (request.Owner == nil || !request.Owner.SameIdentity(row.Owner)) {
			return nil, fmt.Errorf("redisstore: metadata owner index contains foreign row")
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func metadataScopeAndKey(request port.SessionMetadataPageRequest) (string, string) {
	if !request.OwnershipEnforced {
		return metadataGlobalScope, metadataGlobalIndexKey
	}
	if request.Owner == nil {
		return metadataNilOwnerScope, metadataOwnerIndexBase + "none"
	}
	scope := metadataOwnerScope(request.Owner)
	return scope, metadataOwnerIndexBase + scope
}

func metadataOwnerScope(owner *session.Principal) string {
	encoded, _ := json.Marshal([2]string{owner.Issuer, owner.Subject})
	sum := sha256.Sum256(encoded)
	return metadataOwnerScopeBase + hex.EncodeToString(sum[:])
}

func encodeMetadataMember(row port.SessionDiscoveryMeta) (string, error) {
	encoded, err := json.Marshal(row)
	if err != nil {
		return "", fmt.Errorf("redisstore: encode metadata: %w", err)
	}
	inverse := ^uint64(row.ModifiedAt.UnixNano())
	return fmt.Sprintf("%016x:%s!%s", inverse, hex.EncodeToString([]byte(row.ID)), base64.RawURLEncoding.EncodeToString(encoded)), nil
}

func decodeMetadataMember(member string) (port.SessionDiscoveryMeta, error) {
	if len(member) < 18 || member[16] != ':' {
		return port.SessionDiscoveryMeta{}, errors.New("invalid member framing")
	}
	payloadAt := strings.IndexByte(member[17:], '!')
	if payloadAt < 0 {
		return port.SessionDiscoveryMeta{}, errors.New("invalid member framing")
	}
	payloadAt += 17
	id, err := hex.DecodeString(member[17:payloadAt])
	if err != nil {
		return port.SessionDiscoveryMeta{}, errors.New("invalid member id")
	}
	encoded, err := base64.RawURLEncoding.DecodeString(member[payloadAt+1:])
	if err != nil {
		return port.SessionDiscoveryMeta{}, errors.New("invalid member payload")
	}
	var row port.SessionDiscoveryMeta
	if err := json.Unmarshal(encoded, &row); err != nil {
		return port.SessionDiscoveryMeta{}, errors.New("invalid member metadata")
	}
	if string(row.ID) != string(id) {
		return port.SessionDiscoveryMeta{}, errors.New("member id mismatch")
	}
	return row, nil
}

func encodeMetadataContinuation(member string) string {
	return metadataContinuationV1 + base64.RawURLEncoding.EncodeToString([]byte(member))
}

func decodeMetadataContinuation(token string) (string, bool) {
	if !strings.HasPrefix(token, metadataContinuationV1) {
		return "", false
	}
	encoded := strings.TrimPrefix(token, metadataContinuationV1)
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	return string(raw), err == nil && encoded != "" && encodeMetadataContinuation(string(raw)) == token
}
