package sessiondebug

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"

	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

type continuationProjection struct {
	Value              map[string]any
	NextRow            int
	HasMoreRows        bool
	ProjectionComplete bool
	Basis              string
}

func mapValue(value any) map[string]any {
	body, _ := json.Marshal(value)
	out := map[string]any{}
	_ = json.Unmarshal(body, &out)
	return out
}

func rowsPage[T any](rows []T, offset, limit int) ([]T, int, bool) {
	if offset > len(rows) {
		offset = len(rows)
	}
	end := minInt(offset+limit, len(rows))
	return rows[offset:end], end, end < len(rows)
}

func (t *inspectTool) projectContinuation(ctx context.Context, args inspectArgs, target *session.Session, scope string, graph lineageGraph, scan eventScan) (continuationProjection, error) {
	offset := args.Offset
	if args.Cursor != "" {
		offset = scan.RowOffset
	}
	switch args.View {
	case "status":
		out := mapValue(t.statusView(ctx, target, scope))
		out["lifetime_event_log"] = lifetimeFromScan(scan)
		return continuationProjection{Value: out, ProjectionComplete: true}, nil
	case viewPerformance:
		return performanceFromScan(scan, offset, boundedLimit(args.Limit, maxPerformanceRows)), nil
	case viewNetwork:
		return networkFromScan(scan, target.ID, offset, boundedLimit(args.Limit, maxNetworkRows)), nil
	case viewDelegation:
		return delegationFromScan(scan, target, graph, offset, boundedLimit(args.Limit, maxDelegationRows)), nil
	case viewManifest:
		return manifestFromScan(scan, offset, boundedLimit(args.Limit, maxManifestRows)), nil
	case viewHistory:
		return t.historyFromScan(ctx, target, scope, args, scan)
	default:
		return continuationProjection{}, errors.New("cursor is not accepted for this view")
	}
}

func windowFields(out map[string]any, scan eventScan) {
	out["event_window"] = scan.evidence()
	if scan.ContinuationError != "" {
		out["continuation_error"] = scan.ContinuationError
	}
}

func lifetimeFromScan(scan eventScan) map[string]any {
	out := lifetimeEvidence{Available: scan.StopReason != stopNotConfigured, Authoritative: scan.StopReason != stopNotConfigured, ScanComplete: scan.Complete, RetentionComplete: false, ScannedEvents: scan.EventCount}
	runs := map[string]bool{}
	legacyOpen := false
	for _, ev := range scan.Events {
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
	if scan.StopReason == stopNotConfigured {
		out.Error = errLogNotConfigured
	}
	if scan.StopReason == "read_error" {
		out.Error = errLogReadFailed
	}
	m := mapValue(out)
	m["aggregate_scope"] = "event_window"
	m["runs_scope"] = "distinct_observed_within_window"
	m["runs_additive"] = false
	m["event_window"] = scan.evidence()
	if scan.ContinuationError != "" {
		m["continuation_error"] = scan.ContinuationError
	}
	return m
}

func performanceFromScan(scan eventScan, offset, limit int) continuationProjection {
	totals := performanceTotals{}
	rows := []performanceTurn{}
	for _, ev := range scan.Events {
		switch ev.Type {
		case session.EvTurnEnd:
			if ev.TurnEnd != nil {
				p := ev.TurnEnd
				totals.Usage = totals.Usage.Add(p.Usage)
				totals.DurationMs += p.DurationMs
				rows = append(rows, performanceTurn{Turn: ev.Turn, DurationMs: p.DurationMs, TTFTMs: p.TTFTMs, InterTokenMeanMs: p.InterTokenMeanMs, InterTokenMaxMs: p.InterTokenMaxMs, Usage: p.Usage})
			}
		case session.EvModelRetry:
			totals.Retries++
		case session.EvToolResult:
			if ev.ToolResult != nil && ev.ToolResult.IsError {
				totals.ToolFailures++
			}
		case session.EvResult:
			totals.Stops++
			if ev.Result != nil {
				if totals.StopReasons == nil {
					totals.StopReasons = map[session.StopReason]int{}
				}
				totals.StopReasons[ev.Result.Stop]++
			}
		}
	}
	page, next, more := rowsPage(rows, offset, limit)
	out := mapValue(performanceEvidence{View: viewPerformance, Available: scan.StopReason != stopNotConfigured, Authoritative: false, Complete: false, ScanComplete: scan.Complete, Truncated: !scan.Complete || more, Scanned: scan.EventCount, Totals: totals, Turns: page})
	if scan.StopReason == stopNotConfigured {
		out["error"] = errLogNotConfigured
	}
	out["offset"], out["limit"], out["aggregate_scope"] = offset, limit, "event_window"
	out["row_page"] = rowPageEvidence{Limit: limit, Returned: len(page), ProjectionComplete: true, HasMoreRows: more}
	windowFields(out, scan)
	return continuationProjection{Value: out, NextRow: next, HasMoreRows: more, ProjectionComplete: true}
}

func networkFromScan(scan eventScan, target session.SessionID, offset, limit int) continuationProjection {
	rows := []networkAttemptEvidence{}
	invalid := 0
	for _, ev := range scan.Events {
		if ev.Type != session.EvNetworkAttempt || ev.NetworkAttempt == nil {
			continue
		}
		if !validNetworkAttempt(*ev.NetworkAttempt, target) {
			invalid++
			continue
		}
		rows = append(rows, projectNetworkAttempt(*ev.NetworkAttempt))
	}
	page, next, more := rowsPage(rows, offset, limit)
	gap := scan.GapCount != nil && *scan.GapCount > 0
	out := mapValue(networkEvidence{View: viewNetwork, Available: scan.StopReason != stopNotConfigured, Authoritative: false, Coverage: "failed and policy-interesting attempts observed by the shared resilience wrapper; no request bodies, headers, URLs, raw errors, per-phase DNS/TCP/TLS timing, or successful-attempt timing", SuccessfulAttemptsTimed: false, Complete: scan.Complete && !more && invalid == 0 && !gap, ScanComplete: scan.Complete, Truncated: !scan.Complete || more || invalid > 0 || gap, Offset: offset, Limit: limit, ScannedEvents: scan.EventCount, TotalMatched: len(rows), InvalidOmitted: invalid, Attempts: page})
	out["counts_scope"] = "event_window"
	out["row_page"] = rowPageEvidence{Limit: limit, Returned: len(page), ProjectionComplete: true, HasMoreRows: more}
	windowFields(out, scan)
	if scan.StopReason == stopNotConfigured {
		out["error"] = errLogNotConfigured
	}
	if gap {
		out["error"] = "retained event evidence contains gaps"
	}
	return continuationProjection{Value: out, NextRow: next, HasMoreRows: more, ProjectionComplete: true}
}

func delegationFromScan(scan eventScan, root *session.Session, graph lineageGraph, offset, limit int) continuationProjection {
	byLifetime := map[string]lineageNode{}
	for _, n := range graph.Nodes {
		byLifetime[lineageLifetimeKey(string(n.ID), session.IncarnationID(n.Incarnation))] = n
	}
	results := parentResults(root)
	rows := []delegationRow{}
	for _, ev := range scan.Events {
		rows = append(rows, projectDelegationEvent(ev, byLifetime, results, root)...)
	}
	page, next, more := rowsPage(rows, offset, limit)
	available := scan.StopReason != stopNotConfigured
	errorText := graph.Error
	if !available {
		errorText = errLogNotConfigured
	}
	out := mapValue(delegationEvidence{View: viewDelegation, Authoritative: available, Source: "typed root events joined to direct lineage records", ProjectionComplete: true, ScanComplete: available && scan.Complete && graph.ScanComplete, RetentionComplete: available && graph.Supported && graph.ScanComplete && !graph.Truncated, Offset: offset, Limit: limit, Rows: page, Error: errorText})
	out["row_page"] = rowPageEvidence{Limit: limit, Returned: len(page), ProjectionComplete: true, HasMoreRows: more}
	windowFields(out, scan)
	return continuationProjection{Value: out, NextRow: next, HasMoreRows: more, ProjectionComplete: true, Basis: lineageBasis(graph, root)}
}

func lineageBasis(graph lineageGraph, root *session.Session) string {
	body, _ := json.Marshal(struct {
		Graph    lineageGraph
		Messages []session.Message
	}{graph, root.Conversation.Messages})
	return digestBytes(body)
}

func manifestFromScan(scan eventScan, offset, limit int) continuationProjection {
	rows := []manifestRow{}
	for _, ev := range scan.Events {
		if ev.Type == session.EvRequestManifest && ev.RequestManifest != nil {
			rows = append(rows, projectManifest(*ev.RequestManifest))
		}
	}
	page, next, more := rowsPage(rows, offset, limit)
	out := mapValue(manifestEvidence{View: viewManifest, Available: scan.StopReason != stopNotConfigured, Authoritative: scan.StopReason != stopNotConfigured, Source: "request.manifest events", ProjectionComplete: true, ScanComplete: scan.Complete, RetentionComplete: false, Offset: offset, Limit: limit, Rows: page})
	if scan.StopReason == stopNotConfigured {
		out["error"] = errLogNotConfigured
	}
	out["row_page"] = rowPageEvidence{Limit: limit, Returned: len(page), ProjectionComplete: true, HasMoreRows: more}
	windowFields(out, scan)
	return continuationProjection{Value: out, NextRow: next, HasMoreRows: more, ProjectionComplete: true}
}

//nolint:gocyclo // Source availability follows the accepted history contract.
func (t *inspectTool) historyFromScan(_ context.Context, s *session.Session, scope string, args inspectArgs, scan eventScan) (continuationProjection, error) {
	type source struct {
		HistoryHandle      string `json:"history_handle,omitempty"`
		Source             string `json:"source"`
		Messages           int    `json:"messages"`
		Authoritative      bool   `json:"authoritative"`
		ProjectionComplete bool   `json:"projection_complete"`
		Available          bool   `json:"available"`
		Reason             string `json:"reason,omitempty"`
	}
	base := continuationClaim{Root: t.expectedFingerprint, Owner: base64.RawURLEncoding.EncodeToString(t.expectedOwnerScope[:]), Scope: scope, View: viewHistory}
	snapshot := base
	snapshot.Purpose, snapshot.ID, snapshot.Basis = "snapshot", session.DebugTargetFingerprint(s), session.DebugTargetFingerprint(s)
	rows := []source{}
	if scan.Start == "" {
		handle, err := t.sealContinuation(snapshot)
		if err != nil {
			return continuationProjection{}, errors.New(continuationUnrepresentable)
		}
		rows = append(rows, source{HistoryHandle: handle, Source: "current_snapshot", Messages: len(s.Conversation.Messages), Authoritative: true, ProjectionComplete: true, Available: true})
	}
	legacyDigest := digestBytes(mustJSON(scan.Events))
	for i, positioned := range scan.Positioned {
		if positioned.Event.Type != session.EvCompactionArchive || positioned.Event.CompactionArchive == nil {
			continue
		}
		claim := base
		claim.Purpose, claim.ID, claim.Start, claim.End, claim.Row = "archive", scan.ID, positioned.Before, positioned.Cursor, i
		handle, err := t.sealContinuation(claim)
		if err != nil {
			return continuationProjection{}, errors.New(continuationUnrepresentable)
		}
		rows = append(rows, source{HistoryHandle: handle, Source: sourceArchive, Messages: len(positioned.Event.CompactionArchive.Replaced), Authoritative: true, ProjectionComplete: true, Available: true})
	}
	if !scan.ContinuationSupported {
		for i, ev := range scan.Events {
			if ev.Type == session.EvCompactionArchive && ev.CompactionArchive != nil {
				claim := base
				claim.Purpose, claim.ID, claim.Basis, claim.Row = "legacy_archive", legacyDigest, legacyDigest, i
				handle, err := t.sealContinuation(claim)
				if err != nil {
					return continuationProjection{}, errors.New(continuationUnrepresentable)
				}
				rows = append(rows, source{HistoryHandle: handle, Source: sourceArchive, Messages: len(ev.CompactionArchive.Replaced), Authoritative: true, ProjectionComplete: true, Available: true})
			}
		}
	}
	if scan.Start != "" {
		// Later windows catalogue only archives from their own interval.
	} else if scan.StopReason == "read_error" {
		// Snapshot evidence remains selectable; no partial event-derived source is published.
	} else if scan.StopReason == stopNotConfigured {
		// A configured log is required for retained-event and archive sources.
	} else if scan.HasMore {
		rows = append(rows, source{Source: sourceRetainedReplay, Authoritative: true, Available: false, Reason: "full_replay_window_limit"})
	} else if scan.GapCount != nil && *scan.GapCount > 0 {
		rows = append(rows, source{Source: sourceRetainedReplay, Authoritative: true, Available: false, Reason: "event_gap"})
	} else {
		folded, err := eventsource.Fold(eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Limits: s.Limits, CreatedAt: s.CreatedAt, Kind: s.Kind, Relationship: s.Relationship}, seqEvents(scan.Events))
		if err != nil {
			rows = append(rows, source{Source: sourceRetainedReplay, Authoritative: true, Available: false, Reason: "replay_incomplete"})
		} else {
			claim := base
			if scan.ContinuationSupported {
				claim = t.claimFor(scan, viewHistory, scope, "replay", 0)
			} else {
				claim.Purpose, claim.ID, claim.Basis = "legacy_replay", legacyDigest, legacyDigest
			}
			handle, sealErr := t.sealContinuation(claim)
			if sealErr != nil {
				return continuationProjection{}, errors.New(continuationUnrepresentable)
			}
			rows = append(rows, source{HistoryHandle: handle, Source: sourceRetainedReplay, Messages: len(folded.Conversation.Messages), Authoritative: true, ProjectionComplete: true, Available: true})
		}
	}
	limit := boundedLimit(args.Limit, maxHistoryRows)
	offset := args.Offset
	if args.Cursor != "" {
		offset = scan.RowOffset
	}
	page, next, more := rowsPage(rows, offset, limit)
	out := map[string]any{"view": viewHistory, "scope": scope, "authoritative": true, "scan_complete": scan.Complete, "retention_complete": false, "projection_complete": true, "sources": page, "offset": offset, "limit": limit, "row_page": rowPageEvidence{Limit: limit, Returned: len(page), ProjectionComplete: true, HasMoreRows: more}}
	if scan.StopReason == stopNotConfigured {
		out["error"] = errLogNotConfigured
	}
	if scan.StopReason == "read_error" {
		out["error"] = errLogReadFailed
	}
	windowFields(out, scan)
	basisRows := append([]source(nil), rows...)
	for i := range basisRows {
		basisRows[i].HistoryHandle = ""
	}
	basis := struct {
		Sources  []source `json:"sources"`
		Snapshot string   `json:"snapshot"`
	}{basisRows, digestBytes(mustJSON(s.Conversation.Messages))}
	return continuationProjection{Value: out, NextRow: next, HasMoreRows: more, ProjectionComplete: true, Basis: digestBytes(mustJSON(basis))}, nil
}

//nolint:gocyclo // Each encrypted history selector has distinct bounded validation.
func (t *inspectTool) selectHistory(ctx context.Context, s *session.Session, scope, handle string, offset, limit int) (any, error) {
	claim, err := t.openClaim(handle, viewHistory, scope)
	if err != nil {
		return nil, errors.New("invalid or stale history handle")
	}
	var source string
	var messages []session.Message
	switch claim.Purpose {
	case "snapshot":
		if claim.Basis != session.DebugTargetFingerprint(s) {
			return nil, errors.New("invalid or stale history handle")
		}
		source, messages = "current_snapshot", s.Conversation.Messages
	case "archive":
		log, ok := t.log.(port.CursorEventLog)
		if !ok {
			return nil, errors.New("invalid or stale history handle")
		}
		count := 0
		for rec, readErr := range log.ReadAfter(ctx, s.ID, claim.Start, port.ReadOptions{Limit: 1, Follow: false}) {
			if readErr != nil || rec.Cursor != claim.End || rec.Kind != port.LogRecordEvent || rec.Event.Type != session.EvCompactionArchive || rec.Event.CompactionArchive == nil {
				return nil, errors.New("invalid or stale history handle")
			}
			count++
			messages = rec.Event.CompactionArchive.Replaced
		}
		if count != 1 {
			return nil, errors.New("invalid or stale history handle")
		}
		source = sourceArchive
	case "legacy_archive", "legacy_replay":
		scan, scanErr := t.scanLegacy(ctx, s.ID)
		if scanErr != nil || digestBytes(mustJSON(scan.Events)) != claim.Basis {
			return nil, errors.New("invalid or stale history handle")
		}
		if claim.Purpose == "legacy_archive" {
			if claim.Row < 0 || claim.Row >= len(scan.Events) || scan.Events[claim.Row].CompactionArchive == nil {
				return nil, errors.New("invalid or stale history handle")
			}
			source, messages = sourceArchive, scan.Events[claim.Row].CompactionArchive.Replaced
		} else {
			folded, foldErr := eventsource.Fold(eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Limits: s.Limits, CreatedAt: s.CreatedAt, Kind: s.Kind, Relationship: s.Relationship}, seqEvents(scan.Events))
			if foldErr != nil {
				return nil, errors.New("invalid or stale history handle")
			}
			source, messages = sourceRetainedReplay, folded.Conversation.Messages
		}
	case "replay":
		log, ok := t.log.(port.CursorEventLog)
		if !ok {
			return nil, errors.New("invalid or stale history handle")
		}
		scan, replayErr := replaySealed(ctx, log, s.ID, claim)
		if replayErr != nil {
			return nil, errors.New("invalid or stale history handle")
		}
		folded, foldErr := eventsource.Fold(eventsource.SessionMeta{ID: s.ID, Mode: s.Mode, EnvironmentRef: s.EnvironmentRef, Limits: s.Limits, CreatedAt: s.CreatedAt, Kind: s.Kind, Relationship: s.Relationship}, seqEvents(scan.Events))
		if foldErr != nil {
			return nil, errors.New("invalid or stale history handle")
		}
		source, messages = sourceRetainedReplay, folded.Conversation.Messages
	default:
		return nil, errors.New("invalid or stale history handle")
	}
	page := transcriptFromMessages(messages, offset, limit)
	return map[string]any{"view": viewHistory, "scope": scope, "history_handle": handle, "source": source, "authoritative": true, "scan_complete": true, "retention_complete": false, "projection_complete": page.Complete, "transcript": page}, nil
}

func boundContinuationProjection(projection *continuationProjection, view string, offset int) {
	key := map[string]string{viewPerformance: "turns", viewNetwork: "attempts", viewDelegation: "rows", viewManifest: "rows", viewHistory: "sources"}[view]
	if key == "" {
		return
	}
	rows, ok := projection.Value[key].([]any)
	if !ok {
		body, _ := json.Marshal(projection.Value[key])
		_ = json.Unmarshal(body, &rows)
	}
	for len(rows) > 0 && !fitsEvidence(projection.Value) {
		if len(rows) > 1 {
			rows = rows[:len(rows)-1]
			projection.NextRow--
			projection.HasMoreRows = true
		} else {
			rows = rows[:0]
			projection.Value["omitted_rows"] = []map[string]any{{"index": offset, "reason": "response_bound"}}
			projection.ProjectionComplete = false
			projection.HasMoreRows = true
		}
		projection.Value[key] = rows
		if page, ok := projection.Value["row_page"].(rowPageEvidence); ok {
			page.Returned = len(rows)
			page.ProjectionComplete = projection.ProjectionComplete
			page.HasMoreRows = projection.HasMoreRows
			projection.Value["row_page"] = page
		}
	}
}

func digestBytes(body []byte) string { sum := sha256Sum(body); return sum }
func sha256Sum(body []byte) string {
	h := sha256.New()
	_, _ = h.Write(body)
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil)[:18])
}
func mustJSON(v any) []byte { body, _ := json.Marshal(v); return body }
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
