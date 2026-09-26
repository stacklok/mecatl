package sessiondebug

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"sort"
	"strings"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

const (
	rootScope         = "root"
	scopeHandlePrefix = "v2."
	scopeRefreshError = "scope handle is unsupported, malformed, or stale; refresh related evidence"
)

type scopeClaim struct {
	RootFingerprint string            `json:"root"`
	ID              session.SessionID `json:"id"`
	Incarnation     string            `json:"incarnation"`
	EdgeDigest      string            `json:"edge"`
}

type lineageNode struct {
	ID           session.SessionID
	Kind         session.SessionKind
	Relationship session.SessionRelationship
	OwnerScope   [32]byte
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

func (t *inspectTool) scopeCipher() (cipher.AEAD, error) {
	key := sha256.Sum256([]byte("mecatl.inspect-session.scope-key/v2\x00" + t.expectedFingerprint + "\x00" + base64.RawURLEncoding.EncodeToString(t.expectedOwnerScope[:])))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func lineageEdgeDigest(rec port.SessionLineageRecord) string {
	body, _ := json.Marshal(struct {
		Kind         session.SessionKind
		Relationship session.SessionRelationship
	}{rec.Kind, rec.Relationship})
	sum := sha256.Sum256(body)
	return base64.RawURLEncoding.EncodeToString(sum[:18])
}

func (t *inspectTool) scopeHandle(rec port.SessionLineageRecord) (string, error) {
	claim, err := json.Marshal(scopeClaim{RootFingerprint: t.expectedFingerprint, ID: rec.ID, Incarnation: rec.Incarnation, EdgeDigest: lineageEdgeDigest(rec)})
	if err != nil {
		return "", err
	}
	aead, err := t.scopeCipher()
	if err != nil {
		return "", err
	}
	nonceDigest := sha256.Sum256(append([]byte("mecatl.inspect-session.scope-nonce/v2\x00"), claim...))
	nonce := nonceDigest[:aead.NonceSize()]
	sealed := aead.Seal(nil, nonce, claim, []byte(scopeHandlePrefix))
	return scopeHandlePrefix + base64.RawURLEncoding.EncodeToString(append(append([]byte(nil), nonce...), sealed...)), nil
}

func (t *inspectTool) openScopeHandle(handle string) (scopeClaim, error) {
	if !strings.HasPrefix(handle, scopeHandlePrefix) {
		return scopeClaim{}, errors.New(scopeRefreshError)
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handle, scopeHandlePrefix))
	if err != nil {
		return scopeClaim{}, errors.New(scopeRefreshError)
	}
	aead, err := t.scopeCipher()
	if err != nil || len(sealed) < aead.NonceSize() {
		return scopeClaim{}, errors.New(scopeRefreshError)
	}
	plain, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(scopeHandlePrefix))
	if err != nil {
		return scopeClaim{}, errors.New(scopeRefreshError)
	}
	var claim scopeClaim
	if json.Unmarshal(plain, &claim) != nil || claim.RootFingerprint != t.expectedFingerprint || claim.ID == "" || !session.IncarnationID(claim.Incarnation).Valid() || claim.EdgeDigest == "" {
		return scopeClaim{}, errors.New(scopeRefreshError)
	}
	return claim, nil
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

func relationEdge(parent session.SessionID, parentIncarnation session.IncarnationID, rec port.SessionLineageRecord) (string, bool) {
	r := rec.Relationship
	switch rec.Kind {
	case session.SessionKindSubagent:
		return "subagent", r.ParentSessionID == parent && r.ParentIncarnation == parentIncarnation
	case session.SessionKindParallelBranch:
		return "parallel", r.ParentSessionID == parent && r.ParentIncarnation == parentIncarnation
	case session.SessionKindTeamMember:
		return delegationTeam, r.ParentSessionID == parent && r.ParentIncarnation == parentIncarnation
	case session.SessionKindScheduled:
		return "schedule", r.OriginSessionID == parent && r.OriginIncarnation == parentIncarnation
	default:
		return "", false
	}
}

// scanLineage reads exactly one direct-edge partition. It never recurses or
// supplements missing index evidence from snapshots or event logs.
func (t *inspectTool) scanLineage(ctx context.Context, root *session.Session) lineageGraph {
	g := lineageGraph{Available: true, ScanComplete: true}
	reader, ok := t.store.(port.SessionLineageReader)
	if !ok || !port.SupportsSessionLineage(t.store) {
		g.Available = false
		g.Error = "lineage index is not configured"
		return g
	}
	g.Supported = true
	res, err := reader.ReadSessionLineage(ctx, port.SessionLineageQuery{
		RootID: root.ID, RootIncarnation: root.Incarnation(), Limit: port.MaxSessionLineageRecords,
	})
	if err != nil {
		if errors.Is(err, port.ErrSessionLineageUnsupported) {
			g.Supported = false
			g.Available = false
			g.Error = "lineage index is not configured"
		} else {
			g.Error = "lineage index read failed"
			g.ScanComplete = false
		}
		return g
	}
	g.Truncated = res.Truncated
	g.ScanComplete = !res.Truncated
	for _, rec := range res.Records {
		edge, direct := relationEdge(root.ID, root.Incarnation(), rec)
		if !direct || rec.ID == root.ID {
			continue
		}
		n := lineageNode{ID: rec.ID, Kind: rec.Kind, Relationship: rec.Relationship, OwnerScope: rec.OwnerScope, Incarnation: rec.Incarnation, State: string(rec.State), Edge: edge, Depth: 1}
		if t.ownershipEnforced && rec.OwnerScope != session.PrincipalScopeHash(root.Owner) {
			n.State = "inaccessible"
		} else if rec.State == port.SessionLineageRetained {
			handle, handleErr := t.scopeHandle(rec)
			if handleErr != nil {
				n.State = "unavailable"
				g.ScanComplete = false
				g.Error = "scope handle minting failed"
			} else {
				n.Inspectable = true
				n.Handle = handle
			}
		}
		g.Nodes = append(g.Nodes, n)
	}
	sort.SliceStable(g.Nodes, func(i, j int) bool {
		if g.Nodes[i].ID != g.Nodes[j].ID {
			return g.Nodes[i].ID < g.Nodes[j].ID
		}
		return g.Nodes[i].Incarnation < g.Nodes[j].Incarnation
	})
	return g
}

func selfLineageRecord(ctx context.Context, reader port.SessionLineageReader, id session.SessionID, incarnation session.IncarnationID) (port.SessionLineageRecord, error) {
	result, err := reader.ReadSessionLineage(ctx, port.SessionLineageQuery{RootID: id, RootIncarnation: incarnation, Limit: 1})
	if err != nil || result.Truncated && len(result.Records) == 0 || len(result.Records) != 1 {
		return port.SessionLineageRecord{}, errors.New("scope lineage proof is incomplete or unavailable")
	}
	rec := result.Records[0]
	if rec.ID != id || rec.Incarnation != string(incarnation) {
		return port.SessionLineageRecord{}, errors.New("scope handle is stale or inaccessible")
	}
	return rec, nil
}

func exactLineageRecord(ctx context.Context, reader port.SessionLineageReader, rootID session.SessionID, rootIncarnation session.IncarnationID, recordID session.SessionID, recordIncarnation session.IncarnationID) (port.SessionLineageRecord, error) {
	result, err := reader.ReadSessionLineage(ctx, port.SessionLineageQuery{
		RootID: rootID, RootIncarnation: rootIncarnation,
		RecordID: recordID, RecordIncarnation: recordIncarnation, Limit: 1,
	})
	if err != nil || result.Truncated || len(result.Records) != 1 {
		return port.SessionLineageRecord{}, errors.New("scope lineage proof is incomplete or unavailable")
	}
	rec := result.Records[0]
	if rec.ID != recordID || rec.Incarnation != string(recordIncarnation) {
		return port.SessionLineageRecord{}, errors.New("scope lineage proof is incomplete or unavailable")
	}
	return rec, nil
}

func sameLineageRecord(a, b port.SessionLineageRecord) bool {
	return a.ID == b.ID && a.Kind == b.Kind && relationshipEqual(a.Relationship, b.Relationship) &&
		a.OwnerScope == b.OwnerScope && a.Incarnation == b.Incarnation && a.State == b.State && a.DeletedAt.Equal(b.DeletedAt)
}

// proveScope follows only point record reads from the selected descendant back
// to the authorized root. Every hop is proved twice: by the child's self record
// and by the exact corresponding record in its parent's direct-edge partition.
//
//nolint:gocyclo // The bounded ancestry proof keeps every fail-closed check at the projection gate.
func (t *inspectTool) proveScope(ctx context.Context, root *session.Session, claim scopeClaim) (*session.Session, lineageNode, error) {
	reader, ok := t.store.(port.SessionLineageReader)
	if !ok || !port.SupportsSessionLineage(t.store) || claim.RootFingerprint != t.expectedFingerprint || claim.ID == root.ID {
		return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
	}
	id, incarnation := claim.ID, session.IncarnationID(claim.Incarnation)
	candidate, loadErr := t.store.Load(ctx, claim.ID)
	if loadErr != nil {
		return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
	}
	var selected port.SessionLineageRecord
	var selectedEdge string
	for depth := 1; depth <= maxRelatedDepth; depth++ {
		rec, err := selfLineageRecord(ctx, reader, id, incarnation)
		if err != nil {
			return nil, lineageNode{}, err
		}
		if rec.State != port.SessionLineageRetained || t.ownershipEnforced && rec.OwnerScope != session.PrincipalScopeHash(root.Owner) {
			return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
		}
		parent, parentIncarnation := lineageParent(lineageNode{Relationship: rec.Relationship}), lineageParentIncarnation(lineageNode{Relationship: rec.Relationship})
		edge, direct := relationEdge(parent, parentIncarnation, rec)
		if !direct || parent == "" || !parentIncarnation.Valid() {
			return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
		}
		edgeRecord, err := exactLineageRecord(ctx, reader, parent, parentIncarnation, id, incarnation)
		if err != nil || !sameLineageRecord(rec, edgeRecord) {
			return nil, lineageNode{}, errors.New("scope lineage proof is incomplete or unavailable")
		}
		if depth == 1 {
			if !handleEqual(claim.EdgeDigest, lineageEdgeDigest(rec)) {
				return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
			}
			selected = rec
			selectedEdge = edge
		}
		if parent == root.ID {
			if parentIncarnation != root.Incarnation() {
				return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
			}
			n := lineageNode{ID: selected.ID, Kind: selected.Kind, Relationship: selected.Relationship, Incarnation: selected.Incarnation, State: string(selected.State), Edge: selectedEdge, Depth: depth, Inspectable: true}
			if candidate.ID != selected.ID || candidate.Kind != selected.Kind || candidate.Incarnation() != session.IncarnationID(selected.Incarnation) || !relationshipEqual(candidate.Relationship, selected.Relationship) || !t.ownerAccessible(root.Owner, candidate.Owner) {
				return nil, lineageNode{}, errors.New("scope handle is stale or inaccessible")
			}
			return candidate, n, nil
		}
		id, incarnation = parent, parentIncarnation
	}
	return nil, lineageNode{}, errors.New("scope lineage proof exceeds the maximum depth")
}

func (t *inspectTool) resolveScope(ctx context.Context, root *session.Session, claim *scopeClaim, handle string) (*session.Session, string, *lineageNode, error) {
	if claim == nil {
		return root, rootScope, nil, nil
	}
	candidate, binding, err := t.proveScope(ctx, root, *claim)
	if err != nil {
		return nil, "", nil, err
	}
	binding.Handle = handle
	return candidate, handle, &binding, nil
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

func scopedLineageNodes(g lineageGraph, _ string) []lineageNode {
	return g.Nodes
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

func transcriptFromMessages(messages []session.Message, offset, limit int) transcriptEvidence {
	s := session.New("history", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{}, time.Time{})
	_ = s.SeedHistory(messages)
	return transcriptView(s, offset, limit)
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
