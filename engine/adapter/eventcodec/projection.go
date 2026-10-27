package eventcodec

import (
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

// LegacyNamespace identifies the source namespace for legacy event-log records.
const LegacyNamespace = "eventlog/legacy-v1"

// ProjectRecord prepares one reader/cursor record for public mapping without
// trying to pair it against another source or a partial cursor page. Unproven
// legacy records become historical content rather than canonical activity links.
// Normal canonical payloads remain normal. Archives are independent closed sources.
func ProjectRecord(e session.Event, inc session.IncarnationID, ordinal int) (session.Event, error) {
	p := e.LegacyIdentity()
	if p == nil {
		return session.CloneEventIdentity(e), nil
	}
	if p.Incarnation != "" {
		inc = p.Incarnation
	} else {
		p.Incarnation = inc
	}
	if !inc.Valid() {
		return session.Event{}, fmt.Errorf("eventcodec: missing stable source incarnation")
	}
	if p.Namespace == "" {
		p.Namespace = LegacyNamespace
	}
	// A retained-page index is not an original evidence ordinal.
	if p.Coordinate == "" {
		if e.RunID != "" {
			p.Coordinate = fmt.Sprintf("run:%s/seq:%d", e.RunID, e.Seq)
		} else if p.Ordinal >= 0 {
			p.Coordinate = fmt.Sprintf("event:%d", p.Ordinal)
		}
	}
	coordinate := p.Coordinate
	if coordinate == "" {
		if ordinal < 0 {
			return session.Event{}, fmt.Errorf("eventcodec: missing original source position")
		}
		coordinate = fmt.Sprintf("event:%d", ordinal)
	}
	base, err := Unmarshal(p.Payload)
	if err != nil {
		return session.Event{}, err
	}
	e = base.WithLegacyIdentity(*p)
	h, err := session.HistoricalEvent(e, inc, ordinal)
	if err != nil {
		return session.Event{}, err
	}
	e.Historical = h
	if e.Type == session.EvCompactionArchive && e.CompactionArchive == nil {
		old, err := decodeLegacy(p.Payload)
		if err != nil {
			return session.Event{}, err
		}
		if len(old.CompactionArchive) != 0 && string(old.CompactionArchive) != "null" {
			messages, err := sessnap.DecodeLegacyArchive(old.CompactionArchive, inc, p.Namespace+"/archive/"+coordinate)
			if err != nil {
				return session.Event{}, err
			}
			e.CompactionArchive = &session.CompactionArchivePayload{Replaced: messages}
		}
	}
	return e, nil
}
