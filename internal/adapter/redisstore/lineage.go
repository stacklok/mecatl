package redisstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const lineageHashKey = "mecatl:session-lineage:v1"

func redisLineageRecord(s *session.Session) port.SessionLineageRecord {
	return port.SessionLineageRecord{ID: s.ID, Kind: s.Kind, Relationship: s.Relationship, OwnerScope: session.PrincipalScopeHash(s.Owner), Incarnation: string(s.Incarnation()), State: port.SessionLineageRetained}
}

func redisLineageKey(id session.SessionID, incarnation string) string {
	return string(id) + "\x00" + incarnation
}

func readRedisLineage(ctx context.Context, client redis.UniversalClient) (map[string]port.SessionLineageRecord, error) {
	values, err := client.HGetAll(ctx, lineageHashKey).Result()
	if err != nil {
		return nil, err
	}
	rows := make(map[string]port.SessionLineageRecord, len(values))
	for key, body := range values {
		var row port.SessionLineageRecord
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			return nil, fmt.Errorf("redisstore: corrupt lineage index: decode: %w", err)
		}
		if key != redisLineageKey(row.ID, row.Incarnation) && key != string(row.ID) {
			return nil, fmt.Errorf("redisstore: corrupt lineage index: key")
		}
		if !session.IncarnationID(row.Incarnation).Valid() || (row.State != port.SessionLineageRetained && row.State != port.SessionLineagePruned) || session.ValidateSessionMetadata(row.Kind, row.Relationship) != nil || (row.State == port.SessionLineageRetained && !row.DeletedAt.IsZero()) || (row.State == port.SessionLineagePruned && row.DeletedAt.IsZero()) {
			return nil, fmt.Errorf("redisstore: corrupt lineage index: record")
		}
		rows[key] = row
	}
	return rows, nil
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
	rows, err := readRedisLineage(ctx, client)
	if err != nil {
		return port.SessionLineageResult{}, err
	}
	result := port.SessionLineageResult{}
	for _, row := range rows {
		rel := row.Relationship
		if row.ID == query.RootID ||
			rel.ParentSessionID == query.RootID && rel.ParentIncarnation == query.RootIncarnation ||
			rel.OriginSessionID == query.RootID && rel.OriginIncarnation == query.RootIncarnation ||
			rel.DebugTargetID == query.RootID && rel.DebugTargetIncarnation == query.RootIncarnation {
			result.Records = append(result.Records, row)
		}
	}
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
