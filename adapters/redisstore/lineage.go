package redisstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	lineageIndexStateKey   = "mecatl:session-lineage:state"
	lineageIndexReady      = "redis-lineage-index/2"
	lineageRecordKeyPrefix = "mecatl:session-lineage:v2:records:"
	lineageEdgeKeyPrefix   = "mecatl:session-lineage:v2:edges:"
)

func redisLineageRecord(s *session.Session) port.SessionLineageRecord {
	return port.SessionLineageRecord{ID: s.ID, Kind: s.Kind, Relationship: s.Relationship, OwnerScope: session.PrincipalScopeHash(s.Owner), Incarnation: string(s.Incarnation()), State: port.SessionLineageRetained}
}

func redisLineageKey(id session.SessionID, incarnation string) string {
	return string(id) + "\x00" + incarnation
}

func redisLineageRecordPartition(id session.SessionID) string {
	return lineageRecordKeyPrefix + string(id)
}

func redisLineageEdgePartition(id session.SessionID, incarnation session.IncarnationID) string {
	if id == "" || !incarnation.Valid() {
		return ""
	}
	return lineageEdgeKeyPrefix + redisLineageKey(id, string(incarnation))
}

func redisLineageParentPartition(row port.SessionLineageRecord) string {
	rel := row.Relationship
	switch {
	case rel.ParentSessionID != "":
		return redisLineageEdgePartition(rel.ParentSessionID, rel.ParentIncarnation)
	case rel.OriginSessionID != "":
		return redisLineageEdgePartition(rel.OriginSessionID, rel.OriginIncarnation)
	case rel.DebugTargetID != "":
		return redisLineageEdgePartition(rel.DebugTargetID, rel.DebugTargetIncarnation)
	default:
		return ""
	}
}

func redisLineageOrderPartition(partition string) string { return partition + ":order" }

func redisLineageOrderMember(row port.SessionLineageRecord, edge bool) string {
	state := "1"
	if row.State == port.SessionLineageRetained {
		state = "0"
	}
	field := redisLineageKey(row.ID, row.Incarnation)
	encoded := base64.RawURLEncoding.EncodeToString([]byte(field))
	if edge {
		return string(row.ID) + "\x00" + state + "\x00" + row.Incarnation + "." + encoded
	}
	return state + "\x00" + row.Incarnation + "." + encoded
}

func redisLineageOrderField(member string) (string, error) {
	at := strings.LastIndexByte(member, '.')
	if at < 0 {
		return "", fmt.Errorf("redisstore: corrupt lineage order index")
	}
	field, err := base64.RawURLEncoding.DecodeString(member[at+1:])
	if err != nil {
		return "", fmt.Errorf("redisstore: corrupt lineage order index: %w", err)
	}
	return string(field), nil
}

func initializeLineageIndex(ctx context.Context, client redis.UniversalClient) error {
	state, err := client.Get(ctx, lineageIndexStateKey).Result()
	if err == nil {
		if state != lineageIndexReady {
			return fmt.Errorf("redisstore: unsupported current lineage index state %q", state)
		}
		return nil
	}
	if err != redis.Nil {
		return fmt.Errorf("redisstore: read lineage index state: %w", err)
	}
	for _, prefix := range []string{lineageRecordKeyPrefix, lineageEdgeKeyPrefix} {
		exists, scanErr := keyPrefixExists(ctx, client, prefix)
		if scanErr != nil {
			return fmt.Errorf("redisstore: inspect current lineage index: %w", scanErr)
		}
		if exists {
			return fmt.Errorf("redisstore: current lineage index is missing")
		}
	}
	return client.SetNX(ctx, lineageIndexStateKey, lineageIndexReady, 0).Err()
}

func requireLineageIndex(ctx context.Context, client redis.UniversalClient) error {
	state, err := client.Get(ctx, lineageIndexStateKey).Result()
	if err == nil && state == lineageIndexReady {
		return nil
	}
	if err != nil && err != redis.Nil {
		return fmt.Errorf("redisstore: read lineage index state: %w", err)
	}
	return fmt.Errorf("redisstore: lineage partitions are incomplete")
}

func decodeRedisLineage(key, body string) (port.SessionLineageRecord, error) {
	var row port.SessionLineageRecord
	if err := json.Unmarshal([]byte(body), &row); err != nil {
		return port.SessionLineageRecord{}, fmt.Errorf("redisstore: corrupt lineage index: decode: %w", err)
	}
	if key != redisLineageKey(row.ID, row.Incarnation) && key != string(row.ID) {
		return port.SessionLineageRecord{}, fmt.Errorf("redisstore: corrupt lineage index: key")
	}
	if !session.IncarnationID(row.Incarnation).Valid() || (row.State != port.SessionLineageRetained && row.State != port.SessionLineagePruned) || session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil || (row.State == port.SessionLineageRetained && !row.DeletedAt.IsZero()) || (row.State == port.SessionLineagePruned && row.DeletedAt.IsZero()) {
		return port.SessionLineageRecord{}, fmt.Errorf("redisstore: corrupt lineage index: record")
	}
	return row, nil
}

func readRedisLineagePartition(ctx context.Context, client redis.UniversalClient, key string, limit int) ([]port.SessionLineageRecord, bool, error) {
	if limit <= 0 {
		count, err := client.ZCard(ctx, redisLineageOrderPartition(key)).Result()
		return nil, count > 0, err
	}
	members, err := client.ZRange(ctx, redisLineageOrderPartition(key), 0, int64(limit)).Result()
	if err != nil {
		return nil, false, err
	}
	more := len(members) > limit
	if more {
		members = members[:limit]
	}
	fields := make([]string, len(members))
	for i, member := range members {
		fields[i], err = redisLineageOrderField(member)
		if err != nil {
			return nil, false, err
		}
	}
	if len(fields) == 0 {
		return nil, more, nil
	}
	values, err := client.HMGet(ctx, key, fields...).Result()
	if err != nil {
		return nil, false, err
	}
	rows := make([]port.SessionLineageRecord, 0, len(values))
	for i, value := range values {
		body, ok := value.(string)
		if !ok {
			return nil, false, fmt.Errorf("redisstore: incomplete lineage partition")
		}
		row, decodeErr := decodeRedisLineage(fields[i], body)
		if decodeErr != nil {
			return nil, false, decodeErr
		}
		rows = append(rows, row)
	}
	return rows, more, nil
}

func lineageDirectlyRelated(row port.SessionLineageRecord, query port.SessionLineageQuery) bool {
	rel := row.Relationship
	return rel.ParentSessionID == query.RootID && rel.ParentIncarnation == query.RootIncarnation ||
		rel.OriginSessionID == query.RootID && rel.OriginIncarnation == query.RootIncarnation ||
		rel.DebugTargetID == query.RootID && rel.DebugTargetIncarnation == query.RootIncarnation
}

func readExactRedisLineage(ctx context.Context, client redis.UniversalClient, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	key := redisLineageKey(query.RecordID, string(query.RecordIncarnation))
	body, err := client.HGet(ctx, redisLineageEdgePartition(query.RootID, query.RootIncarnation), key).Result()
	if err == redis.Nil {
		return port.SessionLineageResult{}, nil
	}
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	row, err := decodeRedisLineage(key, body)
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	if row.ID != query.RootID && !lineageDirectlyRelated(row, query) {
		return port.SessionLineageResult{}, nil
	}
	return port.SessionLineageResult{Records: []port.SessionLineageRecord{row}}, nil
}

// ReadSessionLineage returns deterministic direct edges without loading snapshots.
func (st *Store) ReadSessionLineage(ctx context.Context, query port.SessionLineageQuery) (port.SessionLineageResult, error) {
	if err := port.ValidateSessionLineageQuery(query); err != nil {
		return port.SessionLineageResult{}, err
	}
	client, release, err := st.clients.acquire()
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	defer release()
	if err := requireLineageIndex(ctx, client); err != nil {
		return port.SessionLineageResult{}, err
	}
	if query.RecordID != "" {
		return readExactRedisLineage(ctx, client, query)
	}
	rows, moreRecords, err := readRedisLineagePartition(ctx, client, redisLineageRecordPartition(query.RootID), query.Limit)
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	more := moreRecords
	if !more {
		remaining := query.Limit - len(rows)
		edges, moreEdges, edgeErr := readRedisLineagePartition(ctx, client, redisLineageEdgePartition(query.RootID, query.RootIncarnation), remaining)
		if edgeErr != nil {
			return port.SessionLineageResult{}, edgeErr
		}
		rows = append(rows, edges...)
		more = moreEdges
	}
	result := port.SessionLineageResult{Records: rows, Truncated: more}
	sort.Slice(result.Records, func(i, j int) bool {
		a, b := result.Records[i], result.Records[j]
		if (a.ID == query.RootID) != (b.ID == query.RootID) {
			return a.ID == query.RootID
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		if a.State != b.State {
			return a.State == port.SessionLineageRetained
		}
		return a.Incarnation < b.Incarnation
	})
	if len(result.Records) > query.Limit {
		result.Records = result.Records[:query.Limit]
		result.Truncated = true
	}
	return result, nil
}
