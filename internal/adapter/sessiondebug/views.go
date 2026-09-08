package sessiondebug

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const rootScope = "root"

type lineageNode struct {
	ID           session.SessionID
	Kind         session.SessionKind
	Relationship session.SessionRelationship
	Incarnation  string
	State        string
	Edge         string
	Handle       string
	Depth        int
	Inspectable  bool
}

type lineageGraph struct {
	Nodes        []lineageNode
	Available    bool
	Supported    bool
	ScanComplete bool
	Truncated    bool
	Error        string
}

func scopeHandle(root session.SessionID, target *session.Session, edge string) string {
	h := sha256.New()
	_, _ = h.Write([]byte("mecatl.inspect-session.scope/v2\x00"))
	_, _ = h.Write([]byte(root))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(edge))
	_, _ = h.Write([]byte{0})
	if target != nil {
		_, _ = h.Write([]byte(target.ID))
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(target.Kind))
		_, _ = h.Write([]byte{0})
		relationship, _ := json.Marshal(target.Relationship)
		_, _ = h.Write(relationship)
		_, _ = h.Write([]byte{0})
		_, _ = h.Write([]byte(session.DebugTargetFingerprint(target)))
	}
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18])
}

func historyHandle(root, scope session.SessionID, rootIncarnation, scopeIncarnation, source string, index int) string {
	h := sha256.New()
	_, _ = h.Write([]byte("mecatl.inspect-session.history/v2\x00"))
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%d", root, rootIncarnation, scope, scopeIncarnation, source, index)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18])
}

func handleEqual(a, b string) bool {
	return len(a) == len(b) && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func relationshipEqual(a, b session.SessionRelationship) bool {
	if a.ScheduleName != b.ScheduleName || a.OriginSessionID != b.OriginSessionID || a.OriginIncarnation != b.OriginIncarnation || a.ParentSessionID != b.ParentSessionID || a.ParentIncarnation != b.ParentIncarnation || a.CallID != b.CallID || a.TeamID != b.TeamID || a.MemberName != b.MemberName || a.DebugTargetID != b.DebugTargetID || a.DebugTargetIncarnation != b.DebugTargetIncarnation {
		return false
	}
	if a.BranchIndex == nil || b.BranchIndex == nil {
		return a.BranchIndex == nil && b.BranchIndex == nil
	}
	return *a.BranchIndex == *b.BranchIndex
}

func sameOwner(a, b *session.Principal) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.SameIdentity(b)
}

func (t *inspectTool) ownerAccessible(root, candidate *session.Principal) bool {
	return !t.ownershipEnforced || sameOwner(root, candidate)
}

func lineageNodeKey(id session.SessionID, incarnation string) string {
	return string(id) + "\x00" + incarnation
}

func relationEdge(parent session.SessionID, parentIncarnation session.IncarnationID, rec port.SessionLineageRecord) (string, bool) {
	r := rec.Relationship
	switch rec.Kind {
	case session.SessionKindSubagent:
		return "subagent", r.ParentSessionID == parent && r.ParentIncarnation == parentIncarnation
	case session.SessionKindParallelBranch:
		return "parallel", r.ParentSessionID == parent && r.ParentIncarnation == parentIncarnation
	case session.SessionKindTeamMember:
		return "team", r.ParentSessionID == parent && r.ParentIncarnation == parentIncarnation
	case session.SessionKindScheduled:
		return "schedule", r.OriginSessionID == parent && r.OriginIncarnation == parentIncarnation
	default:
		return "", false
	}
}

//nolint:gocyclo // The bounded BFS keeps validation states together so no edge bypasses authorization.
func (t *inspectTool) scanLineage(ctx context.Context, root *session.Session) lineageGraph {
	g := lineageGraph{Available: true, ScanComplete: true}
	reader, ok := t.store.(port.SessionLineageReader)
	if ok {
		g.Supported = true
		queue := []lineageNode{{ID: root.ID, Incarnation: string(root.Incarnation()), Depth: 0, Inspectable: true}}
		seen := map[string]bool{lineageNodeKey(root.ID, string(root.Incarnation())): true}
		for len(queue) > 0 && len(g.Nodes) < maxRelatedSessions {
			parent := queue[0]
			queue = queue[1:]
			if parent.Depth >= maxRelatedDepth {
				g.Truncated = true
				g.ScanComplete = false
				continue
			}
			res, err := reader.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: parent.ID, RootIncarnation: session.IncarnationID(parent.Incarnation), Limit: port.MaxSessionLineageRecords})
			if err != nil {
				if errors.Is(err, port.ErrSessionLineageUnsupported) {
					g.Supported = false
				} else {
					g.Error = "lineage index read failed"
					g.ScanComplete = false
				}
				break
			}
			g.Truncated = g.Truncated || res.Truncated
			if res.Truncated {
				g.ScanComplete = false
			}
			for _, rec := range res.Records {
				edge, direct := relationEdge(parent.ID, session.IncarnationID(parent.Incarnation), rec)
				key := lineageNodeKey(rec.ID, rec.Incarnation)
				if !direct || seen[key] || rec.ID == root.ID {
					continue
				}
				seen[key] = true
				n := lineageNode{ID: rec.ID, Kind: rec.Kind, Relationship: rec.Relationship, Incarnation: rec.Incarnation, State: string(rec.State), Edge: edge, Depth: parent.Depth + 1}
				if t.ownershipEnforced && rec.OwnerScope != session.PrincipalScopeHash(root.Owner) {
					n.State = "inaccessible"
				} else if rec.State == port.SessionLineageRetained {
					candidate, loadErr := t.store.Load(ctx, rec.ID)
					if loadErr == nil && candidate.Kind == rec.Kind && relationshipEqual(candidate.Relationship, rec.Relationship) && t.ownerAccessible(root.Owner, candidate.Owner) && candidate.Incarnation() == session.IncarnationID(n.Incarnation) {
						n.Inspectable = true
						n.Handle = scopeHandle(root.ID, candidate, edge)
						queue = append(queue, n)
					} else if loadErr == nil {
						n.State = "inaccessible"
					} else if errors.Is(loadErr, port.ErrSessionNotFound) {
						n.State = "not_retained"
					} else {
						n.State = "unavailable"
						g.ScanComplete = false
					}
				}
				g.Nodes = append(g.Nodes, n)
				if len(g.Nodes) == maxRelatedSessions {
					g.Truncated = true
					g.ScanComplete = false
					break
				}
			}
		}
	}
	// Event evidence supplements indexes that are absent, incomplete, or have not yet
	// observed a just-created child. It never overrides a stronger indexed tombstone.
	t.supplementEventLineage(ctx, root, &g)
	if !ok && t.log == nil {
		g.Available = false
		g.Error = "lineage index and event log are not configured"
	}
	sort.SliceStable(g.Nodes, func(i, j int) bool {
		if g.Nodes[i].Depth != g.Nodes[j].Depth {
			return g.Nodes[i].Depth < g.Nodes[j].Depth
		}
		return g.Nodes[i].Handle < g.Nodes[j].Handle
	})
	return g
}

type expectedChild struct {
	id          session.SessionID
	incarnation session.IncarnationID
	kind        session.SessionKind
	edge        string
	rel         session.SessionRelationship
}

func expectedChildren(ev session.Event, parent session.SessionID, parentIncarnation session.IncarnationID) []expectedChild {
	var out []expectedChild
	if p := ev.Subagent; p != nil && p.ChildID != "" && p.ChildIncarnation.Valid() {
		out = append(out, expectedChild{id: session.SessionID(p.ChildID), incarnation: p.ChildIncarnation, kind: session.SessionKindSubagent, edge: "subagent", rel: session.SessionRelationship{ParentSessionID: parent, ParentIncarnation: parentIncarnation, CallID: session.ToolCallID(p.ParentCallID)}})
	}
	if p := ev.Parallel; p != nil && p.ChildID != "" && p.ChildIncarnation.Valid() {
		i := p.BranchIndex
		out = append(out, expectedChild{id: session.SessionID(p.ChildID), incarnation: p.ChildIncarnation, kind: session.SessionKindParallelBranch, edge: "parallel", rel: session.SessionRelationship{ParentSessionID: parent, ParentIncarnation: parentIncarnation, CallID: session.ToolCallID(p.ParentCallID), BranchIndex: &i}})
	}
	if p := ev.Team; p != nil && p.MemberSessionID != "" && p.MemberIncarnation.Valid() {
		out = append(out, expectedChild{id: session.SessionID(p.MemberSessionID), incarnation: p.MemberIncarnation, kind: session.SessionKindTeamMember, edge: "team", rel: session.SessionRelationship{TeamID: p.TeamID, MemberName: p.Member, ParentSessionID: parent, ParentIncarnation: parentIncarnation}})
	}
	return out
}

//nolint:gocyclo // Typed fallback events intentionally share one bounded, fail-closed traversal.
func (t *inspectTool) supplementEventLineage(ctx context.Context, root *session.Session, g *lineageGraph) {
	if t.log == nil {
		return
	}
	seen := map[string]bool{lineageNodeKey(root.ID, string(root.Incarnation())): true}
	for _, n := range g.Nodes {
		seen[lineageNodeKey(n.ID, n.Incarnation)] = true
	}
	queue := []lineageNode{{ID: root.ID, Incarnation: string(root.Incarnation())}}
	for len(queue) > 0 && len(g.Nodes) < maxRelatedSessions {
		parent := queue[0]
		queue = queue[1:]
		if parent.Depth >= maxRelatedDepth {
			g.Truncated = true
			g.ScanComplete = false
			continue
		}
		scanned := 0
		parallelCounts := map[string]int{}
		parallelSeen := map[string]map[int]bool{}
		teamRoster := map[string]map[string]bool{}
		teamSeen := map[string]map[string]bool{}
		for ev, err := range t.log.Read(ctx, parent.ID) {
			if err != nil {
				g.ScanComplete = false
				if g.Error == "" {
					g.Error = "event lineage read failed"
				}
				break
			}
			if scanned == maxLineageEventScan {
				g.Truncated = true
				g.ScanComplete = false
				break
			}
			scanned++
			if p := ev.Schedule; p != nil && p.Kind == "skipped" {
				g.Nodes = append(g.Nodes, lineageNode{Kind: session.SessionKindScheduled, Edge: "schedule", Depth: parent.Depth + 1, State: "absent"})
			}
			if p := ev.Parallel; p != nil {
				if ev.Type == session.EvParallelStart {
					parallelCounts[p.ParentCallID] = p.BranchCount
				}
				if ev.Type == session.EvParallelBranch && p.ChildID != "" {
					if parallelSeen[p.ParentCallID] == nil {
						parallelSeen[p.ParentCallID] = map[int]bool{}
					}
					parallelSeen[p.ParentCallID][p.BranchIndex] = true
				}
			}
			if p := ev.Team; p != nil {
				if ev.Type == session.EvTeamStart {
					if teamRoster[p.ParentCallID] == nil {
						teamRoster[p.ParentCallID] = map[string]bool{}
					}
					for _, member := range p.Roster {
						teamRoster[p.ParentCallID][member.Name] = true
					}
				}
				if p.MemberSessionID != "" {
					if teamSeen[p.ParentCallID] == nil {
						teamSeen[p.ParentCallID] = map[string]bool{}
					}
					teamSeen[p.ParentCallID][p.Member] = true
				}
			}
			for _, expected := range expectedChildren(ev, parent.ID, session.IncarnationID(parent.Incarnation)) {
				key := lineageNodeKey(expected.id, string(expected.incarnation))
				if seen[key] {
					continue
				}
				seen[key] = true
				n := lineageNode{ID: expected.id, Kind: expected.kind, Relationship: expected.rel, Incarnation: string(expected.incarnation), Edge: expected.edge, Depth: parent.Depth + 1, State: "not_retained"}
				candidate, err := t.store.Load(ctx, expected.id)
				if err == nil {
					if candidate.Kind != expected.kind || candidate.Incarnation() != expected.incarnation || !relationshipEqual(candidate.Relationship, expected.rel) || !t.ownerAccessible(root.Owner, candidate.Owner) {
						n.State = "inaccessible"
					} else {
						n.Incarnation = string(candidate.Incarnation())
						n.State = "retained"
						n.Inspectable = true
						n.Handle = scopeHandle(root.ID, candidate, expected.edge)
						queue = append(queue, n)
					}
				} else if !errors.Is(err, port.ErrSessionNotFound) {
					n.State = "unavailable"
					g.ScanComplete = false
				}
				g.Nodes = append(g.Nodes, n)
				if len(g.Nodes) == maxRelatedSessions {
					g.Truncated = true
					g.ScanComplete = false
					return
				}
			}
		}
		for call, count := range parallelCounts {
			for i := 0; i < count; i++ {
				if !parallelSeen[call][i] {
					g.Nodes = append(g.Nodes, lineageNode{Kind: session.SessionKindParallelBranch, Edge: "parallel", Depth: parent.Depth + 1, State: "never_produced"})
				}
			}
		}
		for call, roster := range teamRoster {
			for member := range roster {
				if !teamSeen[call][member] {
					g.Nodes = append(g.Nodes, lineageNode{Kind: session.SessionKindTeamMember, Edge: "team", Depth: parent.Depth + 1, State: "never_produced"})
				}
			}
		}
	}
}

func (t *inspectTool) validScopedSession(root, candidate *session.Session, n lineageNode) bool {
	if candidate == nil || candidate.ID != n.ID || candidate.Kind != n.Kind ||
		!relationshipEqual(candidate.Relationship, n.Relationship) ||
		!t.ownerAccessible(root.Owner, candidate.Owner) ||
		candidate.Incarnation() != session.IncarnationID(n.Incarnation) {
		return false
	}
	edge, direct := relationEdge(lineageParent(n), lineageParentIncarnation(n), port.SessionLineageRecord{Kind: candidate.Kind, Relationship: candidate.Relationship})
	return direct && edge == n.Edge
}

func (t *inspectTool) resolveScope(ctx context.Context, root *session.Session, g lineageGraph, handle string) (*session.Session, string, *lineageNode, error) {
	if handle == "" {
		return root, rootScope, nil, nil
	}
	for _, n := range g.Nodes {
		if !n.Inspectable || !handleEqual(handle, n.Handle) {
			continue
		}
		candidate, err := t.store.Load(ctx, n.ID)
		if err != nil || !t.validScopedSession(root, candidate, n) {
			return nil, "", nil, errors.New("scope handle is stale or inaccessible")
		}
		return candidate, handle, &n, nil
	}
	return nil, "", nil, errors.New("invalid, stale, or inaccessible scope handle")
}

type relatedEvidence struct {
	View              string       `json:"view"`
	Scope             string       `json:"scope"`
	Available         bool         `json:"available"`
	Supported         bool         `json:"lineage_reader_supported"`
	ScanComplete      bool         `json:"scan_complete"`
	RetentionComplete bool         `json:"retention_complete"`
	Truncated         bool         `json:"truncated"`
	Error             string       `json:"error,omitempty"`
	Offset            int          `json:"offset"`
	Limit             int          `json:"limit"`
	NextOffset        *int         `json:"next_offset,omitempty"`
	Rows              []relatedRow `json:"rows"`
}
type relatedRow struct {
	Handle string              `json:"scope_handle,omitempty"`
	Kind   session.SessionKind `json:"kind"`
	Edge   string              `json:"edge"`
	Depth  int                 `json:"depth"`
	Status string              `json:"status"`
}

func lineageParent(n lineageNode) session.SessionID {
	if n.Relationship.ParentSessionID != "" {
		return n.Relationship.ParentSessionID
	}
	return n.Relationship.OriginSessionID
}

func lineageParentIncarnation(n lineageNode) session.IncarnationID {
	if n.Relationship.ParentSessionID != "" {
		return n.Relationship.ParentIncarnation
	}
	return n.Relationship.OriginIncarnation
}

func scopedLineageNodes(g lineageGraph, scope string) []lineageNode {
	if scope == rootScope {
		return g.Nodes
	}
	var selected session.SessionID
	for _, n := range g.Nodes {
		if n.Handle != "" && handleEqual(n.Handle, scope) {
			selected = n.ID
			break
		}
	}
	if selected == "" {
		return nil
	}
	parents := make(map[session.SessionID]session.SessionID, len(g.Nodes))
	for _, n := range g.Nodes {
		parents[n.ID] = lineageParent(n)
	}
	var out []lineageNode
	for _, n := range g.Nodes {
		for parent := lineageParent(n); parent != ""; parent = parents[parent] {
			if parent == selected {
				out = append(out, n)
				break
			}
		}
	}
	return out
}

func (*inspectTool) relatedView(g lineageGraph, scope string, offset, requested int) relatedEvidence {
	limit := boundedLimit(requested, maxActivityRows)
	out := relatedEvidence{View: "related", Scope: scope, Available: g.Available, Supported: g.Supported, ScanComplete: g.ScanComplete, RetentionComplete: g.Supported && g.ScanComplete && !g.Truncated, Truncated: g.Truncated, Error: g.Error, Offset: offset, Limit: limit, Rows: []relatedRow{}}
	nodes := scopedLineageNodes(g, scope)
	for i := offset; i < len(nodes) && len(out.Rows) < limit; i++ {
		n := nodes[i]
		out.Rows = append(out.Rows, relatedRow{n.Handle, n.Kind, n.Edge, n.Depth, n.State})
	}
	if offset+len(out.Rows) < len(nodes) {
		n := offset + len(out.Rows)
		out.NextOffset = &n
		out.Truncated = true
		out.ScanComplete = false
		out.RetentionComplete = false
	}
	return out
}

type lifetimeEvidence struct {
	Available         bool          `json:"available"`
	Authoritative     bool          `json:"authoritative"`
	ScanComplete      bool          `json:"scan_complete"`
	RetentionComplete bool          `json:"retention_complete"`
	ScannedEvents     int           `json:"scanned_events"`
	Runs              int           `json:"runs"`
	Turns             int           `json:"turns"`
	Usage             session.Usage `json:"usage"`
	ToolCalls         int           `json:"tool_calls"`
	ToolResults       int           `json:"tool_results"`
	ToolFailures      int           `json:"tool_failures"`
	Error             string        `json:"error,omitempty"`
}

func (t *inspectTool) lifetimeView(ctx context.Context, id session.SessionID) lifetimeEvidence {
	out := lifetimeEvidence{Available: t.log != nil}
	if t.log == nil {
		out.Error = errLogNotConfigured
		return out
	}
	out.Authoritative = true
	out.ScanComplete = true
	runs := map[string]bool{}
	legacyOpen := false
	for ev, err := range t.log.Read(ctx, id) {
		if err != nil {
			out.Error = errLogReadFailed
			out.ScanComplete = false
			break
		}
		if out.ScannedEvents == maxPerformanceScan {
			out.ScanComplete = false
			break
		}
		out.ScannedEvents++
		if ev.RunID != "" {
			runs[ev.RunID] = true
		} else if ev.Schedule == nil {
			legacyOpen = true
		}
		switch ev.Type {
		case session.EvTurnEnd:
			if ev.TurnEnd != nil {
				out.Turns++
				out.Usage = out.Usage.Add(ev.TurnEnd.Usage)
			}
		case session.EvToolCall:
			out.ToolCalls++
		case session.EvToolResult:
			out.ToolResults++
			if ev.ToolResult != nil && ev.ToolResult.IsError {
				out.ToolFailures++
			}
		case session.EvResult:
			if ev.RunID == "" && legacyOpen {
				out.Runs++
				legacyOpen = false
			}
		}
	}
	if legacyOpen {
		out.Runs++
	}
	out.Runs += len(runs)
	out.RetentionComplete = false
	return out
}

type historyCatalog struct {
	View               string          `json:"view"`
	Scope              string          `json:"scope"`
	Authoritative      bool            `json:"authoritative"`
	ScanComplete       bool            `json:"scan_complete"`
	RetentionComplete  bool            `json:"retention_complete"`
	ProjectionComplete bool            `json:"projection_complete"`
	Error              string          `json:"error,omitempty"`
	Sources            []historySource `json:"sources"`
}
type historySource struct {
	HistoryHandle      string `json:"history_handle"`
	Source             string `json:"source"`
	Messages           int    `json:"messages"`
	Authoritative      bool   `json:"authoritative"`
	ProjectionComplete bool   `json:"projection_complete"`
}

func (t *inspectTool) historyView(ctx context.Context, s *session.Session, scope, selected string, offset, limit int) (any, error) {
	sources := []struct {
		name          string
		messages      []session.Message
		authoritative bool
		projection    bool
	}{{"current_snapshot", s.Conversation.Messages, true, true}}
	complete := true
	errText := ""
	if t.log != nil {
		events, err := readEvents(ctx, t.log, s.ID, maxPerformanceScan)
		if err != nil {
			complete = false
			errText = errLogReadFailed
		} else {
			for _, ev := range events {
				if ev.Type == session.EvCompactionArchive && ev.CompactionArchive != nil {
					sources = append(sources, struct {
						name          string
						messages      []session.Message
						authoritative bool
						projection    bool
					}{"compaction_archive", ev.CompactionArchive.Replaced, true, true})
				}
			}
			folded, foldErr := eventsource.Fold(eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Limits: s.Limits, CreatedAt: s.CreatedAt, Kind: s.Kind, Relationship: s.Relationship}, seqEvents(events))
			if foldErr == nil {
				sources = append(sources, struct {
					name          string
					messages      []session.Message
					authoritative bool
					projection    bool
				}{"retained_event_history", folded.Conversation.Messages, true, true})
			} else {
				sources = append(sources, struct {
					name          string
					messages      []session.Message
					authoritative bool
					projection    bool
				}{"retained_event_history", nil, true, false})
				complete = false
				if errText == "" {
					errText = "retained events do not form a complete replay"
				}
			}
		}
	} else {
		complete = false
		errText = errLogNotConfigured
	}
	catalog := historyCatalog{View: "history", Scope: scope, Authoritative: true, ScanComplete: complete, RetentionComplete: false, ProjectionComplete: true, Error: errText, Sources: []historySource{}}
	for i, src := range sources {
		h := historyHandle(t.target, s.ID, t.expectedFingerprint, session.DebugTargetFingerprint(s), src.name, i)
		catalog.Sources = append(catalog.Sources, historySource{h, src.name, len(src.messages), src.authoritative, src.projection})
		if selected != "" && handleEqual(selected, h) {
			page := transcriptFromMessages(src.messages, offset, limit)
			return struct {
				View               string             `json:"view"`
				Scope              string             `json:"scope"`
				HistoryHandle      string             `json:"history_handle"`
				Source             string             `json:"source"`
				Authoritative      bool               `json:"authoritative"`
				ScanComplete       bool               `json:"scan_complete"`
				RetentionComplete  bool               `json:"retention_complete"`
				ProjectionComplete bool               `json:"projection_complete"`
				Transcript         transcriptEvidence `json:"transcript"`
			}{"history", scope, h, src.name, src.authoritative, complete, false, src.projection && page.Complete, page}, nil
		}
	}
	if selected != "" {
		return nil, errors.New("invalid or stale history handle")
	}
	return catalog, nil
}

func transcriptFromMessages(messages []session.Message, offset, limit int) transcriptEvidence {
	s := session.New("history", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Time{})
	_ = s.SeedHistory(messages)
	return transcriptView(s, offset, limit)
}

func readEvents(ctx context.Context, log port.EventLog, id session.SessionID, scanLimit int) ([]session.Event, error) {
	out := []session.Event{}
	for ev, err := range log.Read(ctx, id) {
		if err != nil {
			return out, err
		}
		if len(out) == scanLimit {
			return out, errors.New("event scan bound reached")
		}
		out = append(out, ev)
	}
	return out, nil
}
func seqEvents(events []session.Event) iter.Seq2[session.Event, error] {
	return func(yield func(session.Event, error) bool) {
		for _, ev := range events {
			if !yield(ev, nil) {
				return
			}
		}
	}
}
