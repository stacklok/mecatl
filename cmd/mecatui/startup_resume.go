package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

// startupResumeSource is the pure startup read surface. Both methods are
// ownership-checked server reads; neither enters the run funnel.
type startupResumeSource interface {
	client.SessionLister
	client.SessionTranscripter
	client.SessionGetter
}

const startupReasonNotFound = client.CapabilityReasonUnknown

type startupResumeError struct {
	Reason client.CapabilityReason
	text   string
}

func (e *startupResumeError) Error() string { return e.text }

func startupResumeConfig(ctx context.Context, source startupResumeSource, cfg config) (*client.ResumeSelection, string, error) {
	resume, err := resolveStartupResume(ctx, source, cfg.resumeID, cfg.resumeLatest)
	if err != nil {
		return nil, "", err
	}
	if resume != nil {
		return resume, resume.Snapshot.Workspace, nil
	}
	return nil, cfg.workspace, nil
}

func resolveStartupResume(ctx context.Context, source startupResumeSource, exactID string, latest bool) (*client.ResumeSelection, error) {
	if exactID == "" && !latest {
		return nil, nil
	}
	if exactID != "" {
		return loadExactStartupResume(ctx, source, exactID)
	}
	rows, err := source.ListSessions(ctx)
	if err != nil {
		return nil, &startupResumeError{Reason: client.CapabilityReasonUnknown, text: "could not list resumable chats; retry or start without a resume flag"}
	}

	// Do not trust transport ordering here: the wire promises this key, but sorting
	// again makes selection deterministic for custom clients and unit fixtures.
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ModifiedAt != rows[j].ModifiedAt {
			return rows[i].ModifiedAt > rows[j].ModifiedAt
		}
		return rows[i].ID < rows[j].ID
	})
	for _, row := range rows {
		if !startupResumeEligible(row, true) {
			continue
		}
		selection, loadErr := loadStartupResume(ctx, source, row, true)
		if loadErr == nil {
			return selection, nil
		}
		// Latest means newest with an available authoritative transcript. A
		// pruned/corrupt row is advisory inventory, so continue to the next row.
	}
	return nil, &startupResumeError{Reason: startupReasonNotFound, text: "no eligible resumable chat was found; use --resume with an exact ID or start a new chat"}
}

func loadExactStartupResume(ctx context.Context, source startupResumeSource, id string) (*client.ResumeSelection, error) {
	snapshot, err := source.GetSession(ctx, id)
	if err != nil {
		return nil, &startupResumeError{Reason: startupReasonNotFound, text: "session not found; check the exact ID or use --resume-latest"}
	}
	transcript, err := source.GetSessionTranscript(ctx, id)
	if err != nil || !transcript.Complete || transcript.SessionID != id {
		return nil, &startupResumeError{Reason: client.CapabilityReasonTranscriptUnavailable, text: "the authoritative transcript is unavailable; retry or start without a resume flag"}
	}
	row := client.SessionListItem{
		ID: id, State: snapshot.State, Workspace: snapshot.Workspace, CreatedAt: snapshot.CreatedAt,
		Title: snapshot.Title, Kind: transcript.Kind, Relationship: transcript.Relationship,
		Capabilities: client.SessionInventoryCapabilities{
			PublicChat: transcript.Kind == client.SessionKindMain && snapshot.State != "awaiting",
			Inspect:    true, AuthoritativeTranscript: true, ActivityReplay: transcript.Activity.Available,
		},
	}
	if transcript.Kind != client.SessionKindMain {
		row.ReasonCode = client.CapabilityReasonInspectOnlyKind
	} else if snapshot.State == "awaiting" {
		row.ReasonCode = client.CapabilityReasonAwaitingApproval
	}
	if !startupResumeEligible(row, false) {
		return nil, &startupResumeError{Reason: row.ReasonCode, text: capabilityStartupGuidance(row.ReasonCode)}
	}
	return &client.ResumeSelection{Row: row, Transcript: transcript, Snapshot: snapshot}, nil
}

func startupResumeEligible(row client.SessionListItem, latest bool) bool {
	if row.Kind != client.SessionKindMain || !row.Capabilities.PublicChat {
		return false
	}
	if row.State == "awaiting" {
		return false
	}
	// Exact adoption may display a crash-orphaned running transcript; only the
	// ordinary first-prompt funnel can prove it stale. Latest is conservative and
	// never guesses among running rows.
	return !latest || row.State != "running"
}

func loadStartupResume(ctx context.Context, source startupResumeSource, row client.SessionListItem, latest bool) (*client.ResumeSelection, error) {
	if !startupResumeEligible(row, latest) {
		reason := row.ReasonCode
		if reason == "" {
			reason = client.CapabilityReasonUnknown
		}
		return nil, &startupResumeError{Reason: reason, text: capabilityStartupGuidance(reason)}
	}
	transcript, err := source.GetSessionTranscript(ctx, row.ID)
	if err != nil || !transcript.Complete || transcript.SessionID != row.ID {
		return nil, &startupResumeError{Reason: client.CapabilityReasonTranscriptUnavailable, text: "the authoritative transcript is unavailable; retry or start without a resume flag"}
	}
	snapshot, err := source.GetSession(ctx, row.ID)
	if err != nil {
		return nil, &startupResumeError{Reason: client.CapabilityReasonTranscriptUnavailable, text: "the authoritative session metadata is unavailable; retry or start without a resume flag"}
	}
	row.State = snapshot.State
	row.Workspace = snapshot.Workspace
	row.CreatedAt = snapshot.CreatedAt
	row.Title = snapshot.Title
	return &client.ResumeSelection{Row: row, Transcript: transcript, Snapshot: snapshot}, nil
}

func capabilityStartupGuidance(reason client.CapabilityReason) string {
	switch reason {
	case client.CapabilityReasonInspectOnlyKind:
		return "that session is inspect-only and cannot be continued as a chat"
	case client.CapabilityReasonAwaitingApproval:
		return "that chat is awaiting approval and cannot be adopted at startup"
	case client.CapabilityReasonActiveElsewhere:
		return "that chat is currently active; retry after its run finishes"
	case client.CapabilityReasonTranscriptUnavailable:
		return "the authoritative transcript is unavailable; retry or start without a resume flag"
	default:
		return fmt.Sprintf("that session cannot be continued (%s)", client.CapabilityReasonUnknown)
	}
}
