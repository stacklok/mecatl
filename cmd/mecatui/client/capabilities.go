package client

import (
	"slices"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

// Capabilities is the proto-free mirror of mecatlv1.ServerCapabilities: which
// optional features the connected server has enabled. The ui renders honest
// affordances from it (advertise only reachable features; explain empty
// inventories as "not enabled" vs "enabled but empty") WITHOUT importing proto.
// All-false is the safe default (an older server omits the field).
type Capabilities struct {
	// MCPConnectorStatus gates the broker-local connector inventory. It is separate
	// from MCP: broker-only deployments deliberately expose no direct resources or prompts.
	MCPConnectorStatus bool
	// MCPRefresh gates explicit direct/global source reconciliation.
	MCPRefresh    bool
	MCP           bool
	SlashCommands bool
	Memory        bool
	Skills        bool
	Teams         bool
	Agents        bool
	// Shell reports availability of the canonical Shell tool.
	Shell bool
	// ExecutionTemplates advertises the enabled authenticated catalog.
	ExecutionTemplates bool
	// ExecutionFiles and BuiltInShell describe this bound session, not server-wide
	// availability and not model media capability.
	ExecutionFiles bool
	BuiltInShell   bool
	// Soul / UserModel report whether the server has a soul source / user-model store
	// wired. They gate the /soul and /usermodel read-only inspection panels.
	Soul      bool
	UserModel bool
	// ModelSelection is true when >=1 provider is available (ListModels would return
	// at least one model). It gates the /models picker: an old server (field absent →
	// false) hides the command, same mechanism as Soul/UserModel.
	ModelSelection bool
	// Image/Audio report whether the wired provider consumes that media kind. They
	// gate the @-mention file-attach UX: a client refuses to send a part the
	// server's provider cannot read (an old server with no field → false → degrade).
	Image bool
	Audio bool
	// Posture is the SERVER-WIDE operator posture tier ("strict"/"trusted"/"auto"/
	// "yolo"). It bounds which modes the ui's mode cycle may switch to. NOT
	// session state. Empty on an older server.
	Posture string
	// SessionPermissionModes reports the session_permission_modes feature: the
	// five modes are session-scoped (auto and yolo are session modes, and a lower
	// session mode drops the posture's allow-all). An older server without it
	// applies its auto/yolo posture process-wide whatever the session mode, so the
	// ui must not present a session mode as the effective permission mode there.
	SessionPermissionModes bool
	// RefusedModeRecovery reports the refused_mode_recovery feature: a stored
	// session parked on an approval in a mode the server no longer admits
	// accepts SetMode to an admitted, strictly lower mode, which cancels the
	// pending turn without running it. An older server refuses that SetMode
	// mid-turn.
	RefusedModeRecovery bool
	// ProjectTrust is "trusted" or "untrusted" for the server's launch
	// workspace, CHROME ONLY: the ui's header badge. Empty on an older server.
	ProjectTrust string
	// SubagentAskReviewer is the server's subagent ask reviewer state ("on",
	// "off", "inert"), CHROME ONLY: shown by /posture. Empty on an older server.
	SubagentAskReviewer string
	// Worktrees is true when a WorktreeLister is wired (ListWorktrees may return a
	// non-empty list for a real git repo). It gates the /worktrees overlay — the
	// first-class operator workflow for binding a session to an EXISTING sibling
	// git worktree (issue #102). An older server, or a no-FS/cloud server with no
	// lister, yields false, so the overlay is honestly absent.
	Worktrees bool
	// Scheduling is true when a ScheduleStore is reachable on the server (the
	// ScheduleService RPCs are functional). Gates the /schedule overlay. An older
	// server (field absent → false) hides the overlay. Independent of the scheduler
	// tick loop: the overlay can create/inspect/pause/resume/fire-now on any
	// store-backed server; auto-firing on a cadence is the server's tick loop
	// (ON by default on a store-backed server; `--no-scheduler` opts out).
	Scheduling        bool
	Reflection        bool
	LearningProposals bool
	LearnedSkills     bool
	StorageHealth     bool
	StorageCleanup    bool
	// ManualDream is nil when an older server does not expose the capability object.
	// A non-nil value keeps /dream discoverable even when both targets are unavailable,
	// so the overlay can explain the target-specific reasons.
	ManualDream *ManualDreamCapabilities
	// Steer is true when the server's engine arms the mid-run steer inbox
	// (steer-while-running, issue #512): a `steer` frame on the bidi Converse stream
	// then drains at the next turn boundary and the authoritative steer / steer.outcome
	// events echo back. When false (the operator disabled it, or an older server with
	// no field → proto3 default false), the ui keeps the client-side terminal
	// merge-queue (issue #228) byte-identical — it never sends a steer frame the
	// server would only ack too_late.
	Steer bool
	// ManualCompaction gates the bodyless out-of-band session compaction action.
	// Older servers leave it false, hiding /compact.
	ManualCompaction bool
	// SessionDebug advertises dedicated no-filesystem sessions bound to one stored
	// target. False is fail-closed for older servers.
	SessionDebug bool
	// DebugMCP gates explicit selected global MCP mounts for debug sessions.
	DebugMCP bool
	// WorkspaceEnrollment gates the bundled MCP workspace-enrollment flow.
	WorkspaceEnrollment bool
	// SessionMediaPresent records that the wire carried SessionCapabilities even
	// when both media bools are false. The UI uses it to distinguish an explicit
	// text-only snapshot from an older server that omitted the message.
	SessionMediaPresent bool
}

// capabilitiesWithSessionMediaFrom overlays the selected session's media support while
// retaining server-wide feature bits. A nil session capability is an older-server
// response, so the global media values remain unchanged.
func capabilitiesWithSessionMediaFrom(caps Capabilities, sessionCaps *mecatlv1.SessionCapabilities) Capabilities {
	if sessionCaps != nil {
		caps.Image = sessionCaps.GetImage()
		caps.Audio = sessionCaps.GetAudio()
		caps.ExecutionFiles = sessionCaps.GetExecutionFiles()
		caps.BuiltInShell = sessionCaps.GetBuiltInShell()
		caps.SessionMediaPresent = true
	}
	return caps
}

// SessionPermissionModesFeature is the compatibility feature identifier for
// session-scoped permission modes.
const SessionPermissionModesFeature = "session_permission_modes"

// RefusedModeRecoveryFeature is the compatibility feature identifier for the
// SetMode recovery of a stored approval parked in a refused mode.
const RefusedModeRecoveryFeature = "refused_mode_recovery"

// compatibilityCapabilitiesFrom maps a compatibility response: the capability
// message plus the feature bits the ui consumes.
func compatibilityCapabilitiesFrom(resp *mecatlv1.GetCompatibilityInfoResponse) Capabilities {
	caps := capabilitiesFrom(resp.GetCapabilities())
	caps.SessionPermissionModes = slices.Contains(resp.GetFeatures(), SessionPermissionModesFeature)
	caps.RefusedModeRecovery = slices.Contains(resp.GetFeatures(), RefusedModeRecoveryFeature)
	return caps
}

// capabilitiesFrom maps a proto ServerCapabilities (nil-safe) to the plain
// struct. A nil message (older server) yields the all-false zero value.
func capabilitiesFrom(c *mecatlv1.ServerCapabilities) Capabilities {
	if c == nil {
		return Capabilities{}
	}
	return Capabilities{
		MCPConnectorStatus:  c.GetMcpConnectorStatus(),
		MCPRefresh:          c.GetMcpRefresh(),
		MCP:                 c.GetMcp(),
		SlashCommands:       c.GetSlashCommands(),
		Memory:              c.GetMemory(),
		Skills:              c.GetSkills(),
		Teams:               c.GetTeams(),
		Agents:              c.GetAgents(),
		Shell:               c.GetShell(),
		ExecutionTemplates:  c.GetExecutionTemplates(),
		Soul:                c.GetSoul(),
		UserModel:           c.GetUserModel(),
		ModelSelection:      c.GetModelSelection(),
		Image:               c.GetImage(),
		Audio:               c.GetAudio(),
		Posture:             c.GetPosture(),
		ProjectTrust:        projectTrustFrom(c),
		SubagentAskReviewer: c.GetSubagentAskReviewer(),
		Worktrees:           c.GetWorktrees(),
		Scheduling:          c.GetScheduling(),
		Reflection:          c.GetReflection(),
		LearningProposals:   c.GetLearningProposals(),
		LearnedSkills:       c.GetLearnedSkills(),
		StorageHealth:       c.GetStorageHealth(),
		StorageCleanup:      c.GetStorageCleanup(),
		ManualDream:         manualDreamCapabilitiesFrom(c.GetManualDream()),
		Steer:               c.GetSteer(),
		ManualCompaction:    c.GetManualCompaction(),
		SessionDebug:        c.GetSessionDebug(),
		DebugMCP:            c.GetDebugMcp(),
		WorkspaceEnrollment: c.GetWorkspaceEnrollment(),
	}
}

// Project-trust spellings for Capabilities.ProjectTrust.
const (
	ProjectTrusted   = "trusted"
	ProjectUntrusted = "untrusted"
)

// projectTrustFrom maps the optional project_trusted echo; unset (an older
// server) stays "" so the client shows no trust indicator.
func projectTrustFrom(c *mecatlv1.ServerCapabilities) string {
	switch {
	case c.ProjectTrusted == nil:
		return ""
	case c.GetProjectTrusted():
		return ProjectTrusted
	default:
		return ProjectUntrusted
	}
}

func manualDreamCapabilitiesFrom(c *mecatlv1.ManualDreamCapabilities) *ManualDreamCapabilities {
	if c == nil {
		return nil
	}
	return &ManualDreamCapabilities{
		ProjectMemory: dreamTargetCapabilityFrom(c.GetProjectMemory()),
		UserModel:     dreamTargetCapabilityFrom(c.GetUserModel()),
	}
}

func dreamTargetCapabilityFrom(c *mecatlv1.DreamTargetCapability) DreamTargetCapability {
	if c == nil {
		return DreamTargetCapability{}
	}
	return DreamTargetCapability{Generate: c.GetGenerate(), Decide: c.GetDecide(), UnavailableReason: validText(c.GetUnavailableReason())}
}

// ResolvedModel is the proto-free mirror of mecatlv1.ResolvedModel: the EFFECTIVE
// provider+model THIS session resolved to (server-owned, echoed verbatim), plus its
// context window. The ui shows the effective model in its header from turn zero
// WITHOUT importing proto. The zero value (empty ids) is the safe default for an
// older server that omits the field — the header then shows no model segment. The
// human display NAME is resolved by the ui from its ListModels inventory keyed on
// (ProviderID, ModelID); no display name is carried on the wire.
type ResolvedModel struct {
	ProviderID    string
	ModelID       string
	ContextWindow int64
	// ReasoningEffort is the EFFECTIVE reasoning-effort tier this session resolved
	// to, "" when unset (provider default). The ui shows it in the model
	// footer segment (only when non-empty). Server-owned + echoed verbatim — never
	// recomputed by the client.
	ReasoningEffort string
}

// resolvedModelFrom maps a proto ResolvedModel (nil-safe) to the plain struct. A
// nil message (older server) yields the zero value, which the ui renders as "no
// model segment" — never a guessed default (the server owns the resolution).
func resolvedModelFrom(m *mecatlv1.ResolvedModel) ResolvedModel {
	if m == nil {
		return ResolvedModel{}
	}
	return ResolvedModel{
		ProviderID:      m.GetProviderId(),
		ModelID:         m.GetModelId(),
		ContextWindow:   m.GetContextWindow(),
		ReasoningEffort: m.GetReasoningEffort(),
	}
}
