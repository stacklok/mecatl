package eventcodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/stacklok/mecatl/engine/session"
)

// ErrSkipLegacyRecord means a recognized, well-formed old event cannot be safely
// converted. Unmarshal and UnmarshalFormat return a zero Event with this error.
// Readers may omit the event, but must advance its physical cursor and preserve
// the gap; a filtered stream is not complete evidence for reconstruction/learning.
// Malformed records, unknown payloads and schema/format mismatches are errors,
// not skips. Current schema-2 records never return this sentinel.
var ErrSkipLegacyRecord = errors.New("eventcodec: skip unsupported legacy record")

func decodeLegacyEvent(data []byte) (session.Event, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return session.Event{}, err
	}
	// Validate known payload shapes before deciding to skip. These values remain
	// local: provider-era references must never escape as canonical invocation IDs.
	var old struct {
		session.Event
		ToolCall *struct {
			ID, Name string
			Args     json.RawMessage
			ItemID   string `json:"item_id"`
		}
	}
	if err := json.Unmarshal(data, &old); err != nil {
		return session.Event{}, err
	}
	skip := false
	switch old.Type {
	case session.EvSessionInit, session.EvSessionTitle, session.EvModelRetry,
		session.EvTurnStart, session.EvTurnEnd, session.EvMessageDelta,
		session.EvReasoningDelta, session.EvToolProgress, session.EvCompaction,
		session.EvNoProgress, session.EvProviderRoute, session.EvRecoverNotice,
		session.EvRequestManifest, session.EvNetworkAttempt,
		session.EvPlanContinuationFailed, session.EvResult, session.EvUserPrompt,
		session.EvScheduleFired, session.EvScheduleSkipped, session.EvScheduleFailed,
		session.EvSteer:
	case session.EvToolCall, session.EvToolResult, session.EvToolResultAvailable,
		session.EvPermissionAsk, session.EvPermissionRetract, session.EvApproval,
		session.EvHook, session.EvAuthorizationRequired, session.EvAuthorizationResolved,
		session.EvCompactionArchive, session.EvSubagentStart, session.EvSubagentTool,
		session.EvSubagentEnd, session.EvTeamStart, session.EvTeamMember,
		session.EvTeamTasks, session.EvTeamFindings, session.EvTeamEnd,
		session.EvParallelStart, session.EvParallelBranch, session.EvParallelEnd:
		skip = true
	default:
		return session.Event{}, fmt.Errorf("eventcodec: unknown legacy event type %q", old.Type)
	}
	// Use an allowlist, not removal of a few dangerous fields: an attachment on
	// an otherwise harmless event type must not smuggle old IDs into current data.
	for key, raw := range fields {
		switch strings.ToLower(key) {
		case "type", "seq", "turn", "text", "title", "modelretry", "requestmanifest",
			"networkattempt", "plancontinuationfailure", "result", "turnend",
			"userprompt", "runid", "actor", "schedule", "steer":
		case "toolcall", "toolresult", "ask", "hook", "approval", "authorization",
			"subagent", "team", "parallel", "compactionarchive":
			if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				skip = true
			}
		default:
			return session.Event{}, fmt.Errorf("eventcodec: unknown legacy payload field %q", key)
		}
	}
	if skip {
		return session.Event{}, ErrSkipLegacyRecord
	}
	return old.Event, nil
}
