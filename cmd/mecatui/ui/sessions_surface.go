package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/theme"
)

type sessionsTab int

const (
	tabChats sessionsTab = iota
	tabScheduledRuns
	tabChildRuns
	tabOtherRuns
	tabStorageHealth
)

type sessionsView int

const (
	sessionsNone sessionsView = iota
	sessionsPanel
	sessionsTranscript
)

type sessionsLoadState int

const (
	sessionsInitialLoading sessionsLoadState = iota + 1
	sessionsLoadingMore
	sessionsComplete
	sessionsCancelled
	sessionsStaleRestart
	sessionsLaterPageError
	sessionsInitialPageError
)

type inventorySessionIDCopiedMsg struct {
	id  string
	err error
}

type storageHealthLoadedMsg struct {
	health client.StorageHealth
	err    error
}

func loadStorageHealthCmd(ctx context.Context, fetcher client.StorageHealthFetcher) tea.Cmd {
	return func() tea.Msg {
		health, err := fetcher.GetStorageHealth(ctx)
		return storageHealthLoadedMsg{health: health, err: err}
	}
}

type maintenanceView int

const (
	maintenanceNone maintenanceView = iota
	maintenanceOptimizePlan
	maintenanceOptimizeJob
	maintenanceCleanupPlan
	maintenanceCleanupJob
)

const maintenanceBatchSize int32 = 25

type migrationPlanMsg struct {
	plan client.SessionMigrationPlan
	err  error
}
type migrationJobMsg struct {
	job client.SessionMigrationJob
	err error
}
type cleanupPlanMsg struct {
	plan client.CleanupPlan
	err  error
}
type cleanupJobMsg struct {
	job client.CleanupJob
	err error
}

func migrationPlanCmd(ctx context.Context, svc client.SessionMigrator) tea.Cmd {
	return func() tea.Msg {
		plan, err := svc.PlanSessionMigration(ctx)
		return migrationPlanMsg{plan: plan, err: err}
	}
}
func migrationApplyCmd(ctx context.Context, svc client.SessionMigrator, planID string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.ApplySessionMigration(ctx, planID, maintenanceBatchSize)
		return migrationJobMsg{job: job, err: err}
	}
}
func migrationResumeCmd(ctx context.Context, svc client.SessionMigrator, jobID string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.ResumeSessionMigration(ctx, jobID, maintenanceBatchSize)
		return migrationJobMsg{job: job, err: err}
	}
}
func migrationCancelCmd(ctx context.Context, svc client.SessionMigrator, jobID string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.CancelSessionMigration(ctx, jobID)
		return migrationJobMsg{job: job, err: err}
	}
}
func migrationStatusCmd(ctx context.Context, svc client.SessionMigrator, jobID string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.GetSessionMigrationJob(ctx, jobID)
		return migrationJobMsg{job: job, err: err}
	}
}
func cleanupPlanCmd(ctx context.Context, svc client.SessionCleaner) tea.Cmd {
	scope := client.CleanupScope{Kinds: []string{"main", "subagent", "parallel_branch", "team_member", "scheduled"}}
	return func() tea.Msg {
		plan, err := svc.PlanSessionCleanup(ctx, scope)
		return cleanupPlanMsg{plan: plan, err: err}
	}
}
func cleanupApplyCmd(ctx context.Context, svc client.SessionCleaner, token string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.ApplySessionCleanup(ctx, token)
		return cleanupJobMsg{job: job, err: err}
	}
}
func cleanupCancelCmd(ctx context.Context, svc client.SessionCleaner, jobID string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.CancelSessionCleanup(ctx, jobID)
		return cleanupJobMsg{job: job, err: err}
	}
}
func cleanupStatusCmd(ctx context.Context, svc client.SessionCleaner, jobID string) tea.Cmd {
	return func() tea.Msg {
		job, err := svc.GetSessionCleanupJob(ctx, jobID)
		return cleanupJobMsg{job: job, err: err}
	}
}

type sessionForkedMsg struct {
	sourceID           string
	actionRequestToken uint64
	newID              string
	snapshot           client.SessionSnapshot
	transcript         client.SessionTranscript
	err                error
}

type sessionsIntentPhase uint8

const (
	sessionsIntentPhaseIdle sessionsIntentPhase = iota
	sessionsIntentPhaseReplay
)

// Phase intent asks the Model to update the root phase while the surface stays open.
type sessionsPhaseIntent struct {
	phase sessionsIntentPhase
}

func (sessionsPhaseIntent) isSurfaceIntent() {}

// Maintenance intents retain durable job IDs in the Model for later surface instances.
type sessionsMigrationJobIntent struct {
	jobID string
}

func (sessionsMigrationJobIntent) isSurfaceIntent() {}

type sessionsCleanupJobIntent struct {
	jobID string
}

func (sessionsCleanupJobIntent) isSurfaceIntent() {}

// Transcript adoption replaces the Model's active chat with authoritative surface data.
type sessionsTranscriptAdoptionIntent struct {
	row          client.SessionListItem
	transcript   conversation
	capabilities client.Capabilities
	model        client.ResolvedModel
	mode         string
}

func (sessionsTranscriptAdoptionIntent) isSurfaceIntent() {}

// Startup intents ask the Model to quit or create the first chat.
type sessionsStartupQuitIntent struct{}

func (sessionsStartupQuitIntent) isSurfaceIntent() {}

type sessionsStartupNewIntent struct{}

func (sessionsStartupNewIntent) isSurfaceIntent() {}

// Status intents update root-owned active-session metadata or the status line.
type sessionsActiveTitleIntent struct {
	id            string
	title         string
	successNotice string
}

func (sessionsActiveTitleIntent) isSurfaceIntent() {}

type sessionsStatusNoticeStyle uint8

const (
	sessionsStatusNoticeWarning sessionsStatusNoticeStyle = iota
	sessionsStatusNoticeSuccess
)

type sessionsStatusNoticeIntent struct {
	text  string
	style sessionsStatusNoticeStyle
}

func (sessionsStatusNoticeIntent) isSurfaceIntent() {}

type sessionTranscriptLoadedMsg struct {
	sessionID           string
	surfaceRequestToken uint64
	requestToken        uint64
	transcript          client.SessionTranscript
	err                 error
}

func (s *sessionsState) loadTranscriptCmd() tea.Cmd {
	s.transcriptRequestToken++
	requestToken := s.transcriptRequestToken
	surfaceRequestToken := s.transcriptSurfaceRequestToken
	id := s.selected.ID
	loader := s.transcripter
	s.loading, s.loadErr = true, nil
	return func() tea.Msg {
		transcript, err := loader.GetSessionTranscript(s.deps.ctx, id)
		return sessionTranscriptLoadedMsg{sessionID: id, surfaceRequestToken: surfaceRequestToken, requestToken: requestToken, transcript: transcript, err: err}
	}
}

func (s *sessionsState) openTranscript(row client.SessionListItem, inspect bool) tea.Cmd {
	s.selected, s.inspect = row, inspect
	if s.pageCancel != nil {
		s.pageCancel()
		s.pageCancel = nil
	}
	s.loadErr = nil
	s.transcript = conversation{}
	s.transcriptRend = nil
	s.transcriptStuck = true
	s.view = sessionsTranscript
	s.transcriptSurfaceRequestToken++
	s.intent = sessionsPhaseIntent{phase: sessionsIntentPhaseReplay}
	return s.loadTranscriptCmd()
}

func (s *sessionsState) applyReplayEvent(msg tea.Msg) {
	c := &s.transcript
	switch msg := msg.(type) {
	case client.UserPromptMsg:
		if descs := mediaDescriptors(msg.Parts); len(descs) > 0 {
			c.addUserWithMedia(msg.Text, descs)
		} else {
			c.addUser(msg.Text)
		}
	case client.DeliveryNoteMsg:
		c.addDelivery(msg.ScheduleName, msg.FireID, msg.Text)
	case client.TurnStartMsg:
		c.startAssistant()
	case client.AssistantDeltaMsg:
		c.appendAssistant(msg.Text)
	case client.ReasoningDeltaMsg:
		c.appendReasoning(msg.Text)
	case client.TurnEndMsg:
		c.endReasoningStream()
		if !trivialTurn(msg) {
			c.addTurnStat(turnStatLine(msg))
		}
	case client.ToolCallMsg:
		c.addTool(msg.ID, msg.Name, msg.Args)
	case client.ToolResultMsg:
		if !c.resolveTool(msg.CallID, msg.Content, msg.IsError, msg.Blocks...) {
			c.addNotice("orphan tool result for " + msg.CallID)
		}
	case client.HookMsg:
		c.addHook(msg.Text, msg.Phase, msg.Tool, string(msg.Decision))
	case client.ResultMsg:
		if msg.Stop == stopError && msg.Error != "" {
			c.addError(msg.Error)
		}
	default:
		s.applyReplayEventSecondary(msg)
	}
}

func (s *sessionsState) applyReplayEventSecondary(msg tea.Msg) {
	c := &s.transcript
	switch msg := msg.(type) {
	case client.ApprovalMsg:
		c.addNotice(approvalNotice(msg))
	case client.CompactionArchiveMsg:
		c.addNotice(compactionArchiveNotice(msg))
	case client.ModelRetryMsg, client.CompactionMsg, client.NoProgressMsg:
		c.addNotice(noticeLine(msg))
	case client.ProviderRouteMsg:
		c.addNotice("via " + msg.Text)
	case client.SubagentMsg:
		applySubagentTo(c, msg)
	case client.ParallelMsg:
		applyParallelTo(c, msg)
	case client.TeamMsg:
		applyTeamTo(c, msg)
	}
}

type sessionForker interface {
	ForkSession(context.Context, string, string) (string, error)
	client.SessionGetter
}

type sessionsState struct {
	view      sessionsView
	startup   bool // same /sessions renderer, with launch-only new/quit hints
	tab       sessionsTab
	loading   bool
	err       error
	sessions  []client.SessionListItem
	filtered  []client.SessionListItem
	handles   map[string]string
	filter    textinput.Model
	cursor    int
	selected  client.SessionListItem
	inspect   bool
	loadErr   error
	health    *client.StorageHealth
	healthErr error

	maintenance    maintenanceView
	maintenanceErr bool
	migrationPlan  client.SessionMigrationPlan
	migrationJob   client.SessionMigrationJob
	cleanupPlan    client.CleanupPlan
	cleanupJob     client.CleanupJob
	cleanupConfirm textinput.Model

	loadState        sessionsLoadState
	nextCursor       string
	pageCtx          context.Context
	pageCancel       context.CancelFunc
	pageRequestToken uint64

	renaming      bool
	renameInput   textinput.Model
	confirmDelete bool
	actionID      string
	actionLoading bool

	deps                          surfaceDeps
	activeSessionID               string
	transcript                    conversation
	transcriptVP                  viewport.Model
	transcriptRend                *renderer
	transcriptStuck               bool
	transcriptRequestToken        uint64
	transcriptSurfaceRequestToken uint64
	pager                         client.SessionPager
	transcripter                  client.SessionTranscripter
	healthFetcher                 client.StorageHealthFetcher
	migration                     client.SessionMigrator
	cleanup                       client.SessionCleaner
	forker                        sessionForker
	manager                       client.SessionManager
	clipboard                     client.Clipboard
	actionRequestToken            uint64
	intent                        surfaceIntent
}

func (*sessionsState) modalPlacement() modalPlacement {
	return modalPlacementFill
}

func (s *sessionsState) Render(width, height int) (string, []ClickableRegion) {
	vpContent := ""
	if s.view == sessionsTranscript {
		if s.transcriptRend == nil {
			s.transcriptRend = newRenderer(s.deps.theme, s.deps.marks)
		}
		s.transcriptRend.setWidth(width)
		s.transcriptVP.SetWidth(width)
		s.transcriptVP.SetHeight(height)
		s.transcriptVP.SetContentLines(s.transcriptRend.renderConversationLines(&s.transcript, false))
		if s.transcriptStuck {
			s.transcriptVP.GotoBottom()
		}
		vpContent = s.transcriptVP.View()
	}
	return renderSessionsOverlay(s.deps.theme, *s, s.deps.caps, s.activeSessionID, vpContent, s.deps.marks, width, height), nil
}

func (s *sessionsState) HandleKey(msg tea.KeyPressMsg) (tea.Cmd, bool, bool) {
	if s.view == sessionsTranscript {
		if key.Matches(msg, s.deps.keys.Close) {
			s.closeTranscript()
			s.intent = sessionsPhaseIntent{phase: sessionsIntentPhaseIdle}
			if s.nextCursor != "" {
				return s.beginPage(s.nextCursor), true, false
			}
			return nil, true, false
		}
		if msg.String() == "r" && s.loadErr != nil && s.transcripter != nil {
			return s.loadTranscriptCmd(), true, false
		}
		var cmd tea.Cmd
		switch {
		case key.Matches(msg, s.deps.keys.ScrollTop):
			s.transcriptVP.GotoTop()
		case key.Matches(msg, s.deps.keys.ScrollBottom):
			s.transcriptVP.GotoBottom()
		default:
			s.transcriptVP, cmd = s.transcriptVP.Update(msg)
		}
		s.transcriptStuck = s.transcriptVP.AtBottom()
		return cmd, true, false
	}
	if cmd, handled := s.handleActionKey(msg); handled {
		return cmd, true, false
	}
	if handled, closed := s.handleNavigationKey(msg); handled {
		return nil, true, closed
	}
	if cmd, handled := s.handleLoadKey(msg); handled {
		return cmd, true, false
	}
	if s.actionLoading {
		return nil, true, false
	}
	if s.maintenance != maintenanceNone || s.tab == tabStorageHealth {
		return s.handleMaintenanceKey(msg), true, false
	}
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	s.syncFilter()
	return cmd, true, false
}

func (s *sessionsState) selectedRow() (client.SessionListItem, bool) {
	if s.cursor < 0 || s.cursor >= len(s.filtered) {
		return client.SessionListItem{}, false
	}
	return s.filtered[s.cursor], true
}

func (s *sessionsState) setNotice(reason client.CapabilityReason) {
	s.intent = sessionsStatusNoticeIntent{text: capabilityReasonText(reason)}
}

//nolint:gocyclo // action keys are one user-facing route; helpers own each form.
func (s *sessionsState) handleActionKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if s.renaming {
		return s.handleRenameKey(msg), true
	}
	if s.confirmDelete {
		return s.handleDeleteKey(msg), true
	}
	if s.startup && key.Matches(msg, s.deps.keys.Close) {
		s.intent = sessionsStartupQuitIntent{}
		return nil, true
	}
	if s.startup && msg.String() == "n" {
		s.intent = sessionsStartupNewIntent{}
		return nil, true
	}
	if s.actionLoading || s.maintenance != maintenanceNone || s.tab == tabStorageHealth {
		return nil, false
	}
	if msg.String() == "r" && (s.loadState == sessionsLaterPageError || s.loadState == sessionsInitialPageError || s.loadState == sessionsCancelled) {
		return nil, false
	}
	row, ok := s.selectedRow()
	switch msg.String() {
	case "y":
		if !ok || !row.Capabilities.CopyID || row.ID == "" || !utf8.ValidString(row.ID) {
			if ok && row.ID != "" && !utf8.ValidString(row.ID) {
				s.intent = sessionsStatusNoticeIntent{text: "could not copy session ID: invalid ID"}
			} else if ok {
				s.setNotice(row.Reasons.CopyID)
			}
			return nil, true
		}
		id, clipboard, ctx := row.ID, s.clipboard, s.deps.ctx
		return func() tea.Msg {
			if clipboard != nil {
				return inventorySessionIDCopiedMsg{id: id, err: clipboard.Write(ctx, "text/plain", []byte(id))}
			}
			return inventorySessionIDCopiedMsg{id: id}
		}, true
	case "v":
		if !ok || !row.Capabilities.ViewTranscript {
			if ok {
				s.setNotice(row.Reasons.ViewTranscript)
			}
			return nil, true
		}
		return s.openTranscript(row, true), true
	case "f":
		if !ok || !row.Capabilities.Fork {
			if ok {
				s.setNotice(row.Reasons.Fork)
			}
			return nil, true
		}
		s.actionLoading, s.actionID = true, row.ID
		s.actionRequestToken++
		return s.forkCmd(row, s.actionRequestToken), true
	case "r":
		if !ok || !row.Capabilities.Rename {
			if ok {
				s.setNotice(row.Reasons.Rename)
			}
			return nil, true
		}
		input := textinput.New()
		input.SetWidth(50)
		input.SetValue(row.Title)
		input.Focus()
		s.renaming, s.renameInput, s.actionID = true, input, row.ID
		return textinput.Blink, true
	case "d":
		if !ok || !row.Capabilities.Delete {
			if ok {
				s.setNotice(row.Reasons.Delete)
			}
			return nil, true
		}
		if row.ID == s.activeSessionID && row.ID != "" {
			s.intent = sessionsStatusNoticeIntent{text: "cannot delete the current chat — switch to another chat first"}
			return nil, true
		}
		s.confirmDelete, s.actionID = true, row.ID
		return nil, true
	}
	if key.Matches(msg, s.deps.keys.Choose) {
		if !ok {
			return nil, true
		}
		if row.ID == s.activeSessionID {
			s.intent = sessionsStatusNoticeIntent{text: "this is the current chat"}
			return nil, true
		}
		if !row.Capabilities.PublicChat && !row.Capabilities.Inspect {
			reason := row.Reasons.PublicChat
			if reason == "" {
				reason = row.Reasons.Inspect
			}
			if reason == "" {
				reason = row.ReasonCode
			}
			s.setNotice(reason)
			return nil, true
		}
		return s.openTranscript(row, !row.Capabilities.PublicChat && row.Capabilities.Inspect), true
	}
	return nil, false
}

func (s *sessionsState) forkCmd(row client.SessionListItem, token uint64) tea.Cmd {
	forker, transcript, ctx := s.forker, s.transcripter, s.deps.ctx
	return func() tea.Msg {
		newID, err := forker.ForkSession(ctx, row.ID, "")
		if err != nil {
			return sessionForkedMsg{sourceID: row.ID, actionRequestToken: token, err: err}
		}
		snapshot, err := forker.GetSession(ctx, newID)
		if err != nil {
			return sessionForkedMsg{sourceID: row.ID, actionRequestToken: token, newID: newID, err: err}
		}
		loaded, err := transcript.GetSessionTranscript(ctx, newID)
		if err != nil || !loaded.Complete || loaded.SessionID != newID {
			if err == nil {
				err = errIncompleteTranscript
			}
			return sessionForkedMsg{sourceID: row.ID, actionRequestToken: token, newID: newID, err: err}
		}
		return sessionForkedMsg{sourceID: row.ID, actionRequestToken: token, newID: newID, snapshot: snapshot, transcript: loaded}
	}
}

func (s *sessionsState) handleRenameKey(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, s.deps.keys.Close) {
		s.renaming, s.actionID = false, ""
		return nil
	}
	if key.Matches(msg, s.deps.keys.Choose) {
		s.renaming, s.actionLoading = false, true
		return client.RenameSessionCmd(s.deps.ctx, s.manager, s.actionID, s.renameInput.Value())
	}
	var cmd tea.Cmd
	s.renameInput, cmd = s.renameInput.Update(msg)
	return cmd
}

//nolint:unparam // matches the form-key handler shape; a future binding may schedule a command.
func (s *sessionsState) handleDeleteKey(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, s.deps.keys.Close) || msg.String() == "n" {
		s.confirmDelete, s.actionID = false, ""
		return nil
	}
	if msg.String() != "y" && !key.Matches(msg, s.deps.keys.Choose) {
		return nil
	}
	s.confirmDelete, s.actionLoading = false, true
	return client.DeleteSessionCmd(s.deps.ctx, s.manager, s.actionID)
}

func (s *sessionsState) handleLoadKey(msg tea.KeyPressMsg) (tea.Cmd, bool) {
	if msg.String() == "c" && (s.loadState == sessionsInitialLoading || s.loadState == sessionsLoadingMore || s.loadState == sessionsStaleRestart) {
		if s.pageCancel != nil {
			s.pageCancel()
		}
		s.loading, s.loadState = false, sessionsCancelled
		return nil, true
	}
	if msg.String() == "r" && (s.loadState == sessionsLaterPageError || s.loadState == sessionsInitialPageError || s.loadState == sessionsCancelled) {
		if s.loadState != sessionsLaterPageError {
			s.nextCursor = ""
		}
		return s.beginPage(s.nextCursor), true
	}
	return nil, false
}

func (s *sessionsState) handleNavigationKey(msg tea.KeyPressMsg) (handled bool, closed bool) {
	if s.tab == tabStorageHealth && s.maintenance != maintenanceNone && key.Matches(msg, s.deps.keys.Close) {
		s.maintenance, s.maintenanceErr = maintenanceNone, false
		return true, false
	}
	if key.Matches(msg, s.deps.keys.Close) {
		if s.filter.Value() != "" {
			s.filter.SetValue("")
			s.syncFilter()
			return true, false
		}
		return true, true
	}
	if key.Matches(msg, s.deps.keys.NextTab) {
		s.nextTab()
		s.cursor = 0
		s.syncFilter()
		return true, false
	}
	switch msg.String() {
	case keyMenuUp:
		if s.cursor > 0 {
			s.cursor--
		}
		return true, false
	case keyMenuDown:
		if s.cursor < len(s.filtered)-1 {
			s.cursor++
		}
		return true, false
	case "home":
		if key.Matches(msg, s.deps.keys.ScrollTop) {
			s.cursor = 0
			return true, false
		}
	case "end":
		if key.Matches(msg, s.deps.keys.ScrollBottom) {
			s.cursor = clampModelsCursor(len(s.filtered)-1, len(s.filtered))
			return true, false
		}
	}
	return false, false
}

func (s *sessionsState) nextTab() {
	tabCount := tabStorageHealth
	if (s.deps.caps.StorageHealth && s.healthFetcher != nil) ||
		(s.deps.caps.StorageMigration && s.migration != nil) ||
		(s.deps.caps.StorageCleanup && s.cleanup != nil) {
		tabCount++
	}
	s.tab = (s.tab + 1) % tabCount
}

func (s *sessionsState) handleRenamed(msg client.SessionRenamedMsg) (tea.Cmd, bool, bool) {
	if msg.SessionID != s.actionID {
		return nil, true, false
	}
	s.actionLoading = false
	if msg.Err != nil {
		s.intent = sessionsStatusNoticeIntent{text: "could not rename session: " + sanitizeTerminal(msg.Err.Error())}
		return nil, true, false
	}
	for i := range s.sessions {
		if s.sessions[i].ID == msg.SessionID {
			s.sessions[i].Title = msg.Title
			s.sessions[i].TitleProvenance = msg.TitleProvenance
		}
	}
	s.syncFilter()
	s.intent = sessionsActiveTitleIntent{id: msg.SessionID, title: msg.Title, successNotice: "renamed session"}
	if s.pager != nil {
		return s.beginPage(""), true, false
	}
	return nil, true, false
}

func (s *sessionsState) handleDeleted(msg client.SessionDeletedMsg) (tea.Cmd, bool, bool) {
	if msg.SessionID != s.actionID {
		return nil, true, false
	}
	s.actionLoading = false
	if msg.Err != nil {
		s.intent = sessionsStatusNoticeIntent{text: "could not delete session: " + sanitizeTerminal(msg.Err.Error())}
		return nil, true, false
	}
	kept := s.sessions[:0]
	for _, row := range s.sessions {
		if row.ID != msg.SessionID {
			kept = append(kept, row)
		}
	}
	s.sessions, s.actionID = kept, ""
	s.syncFilter()
	s.intent = sessionsStatusNoticeIntent{text: "deleted session", style: sessionsStatusNoticeSuccess}
	return nil, true, false
}

func (s *sessionsState) handleForked(msg sessionForkedMsg) {
	if msg.sourceID != s.actionID || msg.actionRequestToken != s.actionRequestToken {
		return
	}
	s.actionLoading = false
	if msg.err != nil {
		s.intent = sessionsStatusNoticeIntent{text: "could not fork session: " + sanitizeTerminal(msg.err.Error())}
		return
	}
	s.selected = client.SessionListItem{
		ID: msg.newID, Title: msg.snapshot.Title, TitleProvenance: msg.snapshot.TitleProvenance,
		State: msg.snapshot.State, Placement: msg.snapshot.Placement, CreatedAt: msg.snapshot.CreatedAt,
		Kind: msg.transcript.Kind, Relationship: msg.transcript.Relationship,
	}
	s.transcript = conversationFromTranscript(msg.transcript.Messages)
	s.transcriptRend = nil
	s.inspect = false
	s.intent = sessionsTranscriptAdoptionIntent{row: s.selected, transcript: s.transcript}
}

func (s *sessionsState) HandleMsg(msg tea.Msg) (tea.Cmd, bool, bool) {
	switch msg := msg.(type) {
	case client.SessionRenamedMsg:
		return s.handleRenamed(msg)
	case client.SessionDeletedMsg:
		return s.handleDeleted(msg)
	case sessionForkedMsg:
		s.handleForked(msg)
	case sessionTranscriptLoadedMsg:
		s.handleTranscriptLoaded(msg)
	case client.SessionInventoryPageMsg:
		return s.applyPage(msg), true, false
	case client.SessionsListedMsg:
		s.handleSessionsListed(msg)
	case storageHealthLoadedMsg:
		s.handleStorageHealthLoaded(msg)
	case migrationPlanMsg:
		s.handleMigrationPlan(msg)
	case migrationJobMsg:
		s.handleMigrationJob(msg)
	case cleanupPlanMsg:
		s.handleCleanupPlan(msg)
	case cleanupJobMsg:
		s.handleCleanupJob(msg)
	default:
		return nil, false, false
	}
	return nil, true, false
}

func (s *sessionsState) handleTranscriptLoaded(msg sessionTranscriptLoadedMsg) {
	if s.view != sessionsTranscript || msg.sessionID != s.selected.ID || msg.surfaceRequestToken != s.transcriptSurfaceRequestToken || msg.requestToken != s.transcriptRequestToken {
		return
	}
	s.loading = false
	if msg.err != nil || !msg.transcript.Complete || msg.transcript.SessionID != msg.sessionID {
		s.loadErr = msg.err
		if s.loadErr == nil {
			s.loadErr = errIncompleteTranscript
		}
		return
	}
	s.transcript = conversationFromTranscript(msg.transcript.Messages)
	s.transcriptRend = nil
	s.transcriptStuck = true
	if !s.inspect {
		s.intent = sessionsTranscriptAdoptionIntent{row: s.selected, transcript: s.transcript}
	}
}

func (s *sessionsState) handleSessionsListed(msg client.SessionsListedMsg) {
	s.loading, s.loadState = false, sessionsComplete
	selectedID := selectedSessionID(*s)
	if msg.Err != nil {
		s.err, s.loadState, s.sessions, s.filtered = msg.Err, sessionsInitialPageError, nil, nil
		return
	}
	s.err, s.sessions = nil, msg.Sessions
	if s.actionID != "" {
		selectedID = s.actionID
	}
	s.syncFilter()
	for i := range s.filtered {
		if s.filtered[i].ID == selectedID {
			s.cursor = i
			break
		}
	}
	s.actionID = ""
}

func (s *sessionsState) handleStorageHealthLoaded(msg storageHealthLoadedMsg) {
	s.healthErr = msg.err
	if msg.err == nil {
		s.health = &msg.health
	}
}

func (s *sessionsState) handleMigrationPlan(msg migrationPlanMsg) {
	s.actionLoading, s.maintenanceErr = false, msg.err != nil
	if msg.err == nil {
		s.migrationPlan, s.maintenance = msg.plan, maintenanceOptimizePlan
	}
}

func (s *sessionsState) handleMigrationJob(msg migrationJobMsg) {
	s.actionLoading, s.maintenanceErr = false, msg.err != nil
	if msg.err == nil {
		s.migrationJob, s.maintenance = msg.job, maintenanceOptimizeJob
		s.intent = sessionsMigrationJobIntent{jobID: msg.job.ID}
	}
}

func (s *sessionsState) handleCleanupPlan(msg cleanupPlanMsg) {
	s.actionLoading, s.maintenanceErr = false, msg.err != nil
	if msg.err != nil {
		return
	}
	s.cleanupPlan, s.maintenance = msg.plan, maintenanceCleanupPlan
	s.cleanupConfirm = textinput.New()
	s.cleanupConfirm.Placeholder = "type CLEAN UP"
	s.cleanupConfirm.SetWidth(24)
	s.cleanupConfirm.Focus()
}

func (s *sessionsState) handleCleanupJob(msg cleanupJobMsg) {
	s.actionLoading, s.maintenanceErr = false, msg.err != nil
	if msg.err == nil {
		s.cleanupJob, s.maintenance = msg.job, maintenanceCleanupJob
		s.intent = sessionsCleanupJobIntent{jobID: msg.job.ID}
	}
}

func (s *sessionsState) takeSurfaceIntent() surfaceIntent {
	intent := s.intent
	s.intent = nil
	return intent
}

func (s *sessionsState) HandleWheel(msg tea.MouseWheelMsg) (tea.Cmd, bool) {
	if s.view != sessionsTranscript {
		return nil, true
	}
	var cmd tea.Cmd
	s.transcriptVP, cmd = s.transcriptVP.Update(msg)
	s.transcriptStuck = s.transcriptVP.AtBottom()
	return cmd, true
}

func (s *sessionsState) closeTranscript() {
	s.transcriptRequestToken++
	s.selected = client.SessionListItem{}
	s.inspect, s.loading = false, false
	s.loadErr = nil
	s.transcript = conversation{}
	s.transcriptRend = nil
	s.view = sessionsPanel
}

func (s *sessionsState) Close() {
	if s.pageCancel != nil {
		s.pageCancel()
	}
	s.transcriptRequestToken++
	s.transcriptRend = nil
}

func (s *sessionsState) syncFilter() {
	tabbed := filterSessionsByTab(s.sessions, s.tab)
	s.handles = sessionDisplayHandles(tabbed)
	s.filtered = filterSessions(tabbed, s.handles, s.filter.Value())
	if s.cursor >= len(s.filtered) {
		s.cursor = 0
	}
}

func (s *sessionsState) beginPage(cursor string) tea.Cmd {
	if s.pageCancel != nil {
		s.pageCancel()
	}
	ctx := s.deps.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	s.pageRequestToken++
	s.pageCtx, s.pageCancel = context.WithCancel(ctx)
	s.nextCursor, s.loading, s.err = cursor, cursor == "", nil
	if cursor == "" {
		s.loadState = sessionsInitialLoading
	} else {
		s.loadState = sessionsLoadingMore
	}
	if s.pager == nil {
		return nil
	}
	return client.ListSessionsPageCmd(s.pageCtx, s.pager, s.nextCursor, s.pageRequestToken)
}

func (s *sessionsState) applyPage(msg client.SessionInventoryPageMsg) tea.Cmd {
	if s.view != sessionsPanel || (msg.RequestToken != 0 && msg.RequestToken != s.pageRequestToken) {
		return nil
	}
	if msg.Err != nil {
		s.loading, s.err, s.nextCursor = false, msg.Err, msg.Cursor
		if errors.Is(msg.Err, client.ErrSessionInventoryRestart) {
			cmd := s.beginPage("")
			s.loading, s.loadState = false, sessionsStaleRestart
			return cmd
		}
		if errors.Is(msg.Err, context.Canceled) {
			s.loadState = sessionsCancelled
		} else if len(s.sessions) > 0 {
			s.loadState = sessionsLaterPageError
		} else {
			s.loadState = sessionsInitialPageError
		}
		return nil
	}
	selectedID := selectedSessionID(*s)
	if s.actionID != "" {
		selectedID = s.actionID
	}
	replace := msg.Cursor == "" && (s.loadState == sessionsInitialLoading || s.loadState == sessionsStaleRestart)
	s.sessions, s.err, s.loading = mergeSessionPages(s.sessions, msg.Page.Sessions, replace), nil, false
	s.syncFilter()
	for i := range s.filtered {
		if s.filtered[i].ID == selectedID {
			s.cursor = i
			break
		}
	}
	s.nextCursor, s.actionID = msg.Page.NextCursor, ""
	if s.nextCursor == "" {
		s.loadState = sessionsComplete
		if s.pageCancel != nil {
			s.pageCancel()
			s.pageCancel = nil
		}
		return nil
	}
	s.loadState = sessionsLoadingMore
	if s.pager == nil {
		return nil
	}
	return client.ListSessionsPageCmd(s.pageCtx, s.pager, s.nextCursor, s.pageRequestToken)
}

func (s *sessionsState) handleMaintenanceKey(msg tea.KeyPressMsg) tea.Cmd {
	if key.Matches(msg, s.deps.keys.Close) && s.maintenance != maintenanceNone {
		s.maintenance, s.maintenanceErr = maintenanceNone, false
		return nil
	}
	if s.actionLoading {
		return nil
	}
	switch s.maintenance {
	case maintenanceNone:
		return s.handleMaintenanceMenuKey(msg)
	case maintenanceOptimizePlan, maintenanceOptimizeJob:
		return s.handleMigrationKey(msg)
	case maintenanceCleanupPlan:
		return s.handleCleanupPlanKey(msg)
	case maintenanceCleanupJob:
		return s.handleCleanupJobKey(msg)
	}
	return nil
}

func (s *sessionsState) handleMaintenanceMenuKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "o" && s.deps.caps.StorageMigration && s.migration != nil {
		s.actionLoading = true
		s.maintenanceErr = false
		return migrationPlanCmd(s.deps.ctx, s.migration)
	}
	if msg.String() == "x" && s.deps.caps.StorageCleanup && s.cleanup != nil {
		s.actionLoading = true
		s.maintenanceErr = false
		return cleanupPlanCmd(s.deps.ctx, s.cleanup)
	}
	return nil
}

func (s *sessionsState) handleMigrationKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.maintenance == maintenanceOptimizePlan {
		if key.Matches(msg, s.deps.keys.Choose) && s.migrationPlan.Available && s.migration != nil {
			s.actionLoading = true
			return migrationApplyCmd(s.deps.ctx, s.migration, s.migrationPlan.ID)
		}
		return nil
	}
	if s.migration == nil {
		return nil
	}
	s.actionLoading = true
	switch msg.String() {
	case "r":
		return migrationResumeCmd(s.deps.ctx, s.migration, s.migrationJob.ID)
	case "c":
		return migrationCancelCmd(s.deps.ctx, s.migration, s.migrationJob.ID)
	case "s":
		return migrationStatusCmd(s.deps.ctx, s.migration, s.migrationJob.ID)
	default:
		s.actionLoading = false
		return nil
	}
}

func (s *sessionsState) handleCleanupPlanKey(msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "y" {
		return nil
	}
	if key.Matches(msg, s.deps.keys.Choose) && s.cleanupConfirm.Value() == "CLEAN UP" && s.cleanup != nil {
		s.actionLoading = true
		return cleanupApplyCmd(s.deps.ctx, s.cleanup, s.cleanupPlan.ConfirmationToken)
	}
	var cmd tea.Cmd
	s.cleanupConfirm, cmd = s.cleanupConfirm.Update(msg)
	return cmd
}

func (s *sessionsState) handleCleanupJobKey(msg tea.KeyPressMsg) tea.Cmd {
	if s.cleanup == nil {
		return nil
	}
	s.actionLoading = true
	switch msg.String() {
	case "c":
		return cleanupCancelCmd(s.deps.ctx, s.cleanup, s.cleanupJob.ID)
	case "s":
		return cleanupStatusCmd(s.deps.ctx, s.cleanup, s.cleanupJob.ID)
	case "r":
		return cleanupPlanCmd(s.deps.ctx, s.cleanup)
	default:
		s.actionLoading = false
		return nil
	}
}
func (s *sessionsState) pageCmd() tea.Cmd {
	if s.pager == nil || s.pageCtx == nil {
		return nil
	}
	return client.ListSessionsPageCmd(s.pageCtx, s.pager, s.nextCursor, s.pageRequestToken)
}

const sessionsVisibleRows = 12

func filterSessionsByTab(sessions []client.SessionListItem, tab sessionsTab) []client.SessionListItem {
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		keep := false
		switch tab {
		case tabChats:
			keep = s.Kind == client.SessionKindMain
		case tabScheduledRuns:
			keep = s.Kind == client.SessionKindScheduled
		case tabChildRuns:
			keep = s.Kind == client.SessionKindSubagent || s.Kind == client.SessionKindParallelBranch || s.Kind == client.SessionKindTeamMember
		case tabOtherRuns:
			keep = s.Kind != client.SessionKindMain && s.Kind != client.SessionKindScheduled &&
				s.Kind != client.SessionKindSubagent && s.Kind != client.SessionKindParallelBranch && s.Kind != client.SessionKindTeamMember
		}
		if keep {
			out = append(out, s)
		}
	}
	return out
}

func filterSessions(sessions []client.SessionListItem, handles map[string]string, q string) []client.SessionListItem {
	if q == "" {
		return sessions
	}
	needle := strings.ToLower(q)
	out := make([]client.SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		fields := []string{
			s.Title, s.ID, handles[s.ID], s.ModelID, s.Placement.Label, s.Placement.Branch, s.Placement.Revision,
			s.Relationship.ParentSessionID, s.Relationship.CallID,
			s.Relationship.ScheduleName, s.Relationship.OriginSessionID,
			s.Relationship.TeamID, s.Relationship.MemberName,
		}
		for _, field := range fields {
			if strings.Contains(strings.ToLower(field), needle) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// sessionDisplayHandles projects every row independently. Collisions deliberately
// remain identical: inventory contents, ordering, and pagination never alter a handle.
func sessionDisplayHandles(rows []client.SessionListItem) map[string]string {
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = client.SessionHandle(row.ID)
	}
	return out
}
func capabilityReasonText(reason client.CapabilityReason) string {
	switch reason {
	case client.CapabilityReasonInspectOnlyKind:
		return "this run is available for inspection only"
	case client.CapabilityReasonAwaitingApproval:
		return "this chat is awaiting approval"
	case client.CapabilityReasonActiveElsewhere:
		return "this chat is active elsewhere"
	case client.CapabilityReasonTranscriptUnavailable:
		return "the authoritative transcript is unavailable"
	case client.CapabilityReasonEnvironmentUnavailable:
		return "the chat environment is unavailable"
	case client.CapabilityReasonStorageUnsupported:
		return "this action is unsupported by session storage"
	default:
		return "this action is unavailable"
	}
}
func selectedSessionID(st sessionsState) string {
	if st.cursor >= 0 && st.cursor < len(st.filtered) {
		return st.filtered[st.cursor].ID
	}
	return ""
}

func mergeSessionPages(existing, incoming []client.SessionListItem, replace bool) []client.SessionListItem {
	out := existing
	if replace {
		out = nil
	}
	seen := make(map[string]struct{}, len(out)+len(incoming))
	for _, row := range out {
		seen[row.ID] = struct{}{}
	}
	for _, row := range incoming {
		if _, ok := seen[row.ID]; ok {
			continue
		}
		seen[row.ID] = struct{}{}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ModifiedAt != out[j].ModifiedAt {
			return out[i].ModifiedAt > out[j].ModifiedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

var errIncompleteTranscript = &sessionTranscriptError{"authoritative transcript incomplete"}

type sessionTranscriptError struct{ text string }

func (e *sessionTranscriptError) Error() string { return e.text }

func conversationFromTranscript(messages []client.ConversationMessage) conversation {
	var out conversation
	for _, message := range messages {
		switch message.Role {
		case "user":
			if media := mediaDescriptors(message.Parts); len(media) > 0 {
				out.addUserWithMedia(message.Text, media)
			} else {
				out.addUser(message.Text)
			}
		case "assistant":
			out.appendAssistant(message.Text)
			for _, call := range message.ToolCalls {
				out.addTool(call.ID, call.Name, call.Args)
			}
		case "tool":
			if message.ToolResult != nil {
				out.resolveTool(message.ToolResult.CallID, message.ToolResult.Content, message.ToolResult.IsError, message.ToolResult.Blocks...)
			}
		}
	}
	return out
}
func stateBadge(state string) string {
	switch state {
	case "running":
		return "▶"
	case "completed":
		return "✓"
	case teamStopReasonCancelled, "failed":
		return "✗"
	case "awaiting":
		return "⏸"
	default:
		return "·"
	}
}

func relativeTime(unixSec int64) string {
	if unixSec <= 0 {
		return "—"
	}
	d := time.Since(time.Unix(unixSec, 0))
	switch {
	case d >= 24*time.Hour:
		return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
	case d >= time.Hour:
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	default:
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	}
}

func renderSessionsOverlay(th theme.Theme, st sessionsState, caps client.Capabilities, sessionID, vpContent string, hk helpKeys, width, height int) string {
	if st.view == sessionsTranscript {
		return renderSessionsTranscript(th, st, sessionID, vpContent, hk, width, height)
	}
	return renderSessionsPanel(th, st, caps, hk, width, height, sessionID)
}

func sessionsTabBar(th theme.Theme, tab sessionsTab, storageHealth bool) string {
	labels := []string{"Chats", "Scheduled runs", "Child runs", "Other"}
	if storageHealth {
		labels = append(labels, "Maintenance")
	}
	var parts []string
	for i, label := range labels {
		prefix := "  "
		style := th.Style("muted")
		if int(tab) == i {
			prefix = "▸ "
			style = th.Style("askTitle")
		}
		parts = append(parts, style.Render(prefix+label))
	}
	return strings.Join(parts, th.Style("muted").Render("  "))
}

func renderSessionsPanel(th theme.Theme, st sessionsState, caps client.Capabilities, hk helpKeys, _, _ int, currentID ...string) string {
	current := ""
	if len(currentID) > 0 {
		current = currentID[0]
	}
	var b strings.Builder
	maintenance := caps.StorageHealth || caps.StorageMigration || caps.StorageCleanup
	b.WriteString(sessionsTabBar(th, st.tab, maintenance) + "\n\n")
	if st.tab == tabStorageHealth {
		b.WriteString(renderStorageHealth(th, st, caps, hk))
		return b.String()
	}
	if rendered, ok := renderSessionsPanelState(th, st, hk); ok {
		b.WriteString(rendered)
		return b.String()
	}
	renderSessionRows(&b, th, st, current)
	if status := sessionsPaginationStatus(st); status != "" {
		b.WriteString(th.Style("muted").Render(status) + "\n")
	}
	b.WriteString("\n" + th.Style("muted").Render(sessionActionsHint(st, hk)))
	return b.String()
}

const unavailableText = "unavailable"

func renderStorageHealth(th theme.Theme, st sessionsState, caps client.Capabilities, hk helpKeys) string {
	if st.maintenanceErr {
		return th.Style("errorText").Render("maintenance request failed; retry or inspect server diagnostics") + "\n" + th.Style("muted").Render(hk.closeOnly+": back")
	}
	if st.actionLoading {
		return th.Style("muted").Render("loading maintenance status…")
	}
	switch st.maintenance {
	case maintenanceOptimizePlan:
		return renderMigrationPlan(th, st.migrationPlan, hk)
	case maintenanceOptimizeJob:
		return renderMigrationJob(th, st.migrationJob, hk)
	case maintenanceCleanupPlan:
		return renderCleanupPlan(th, st, hk)
	case maintenanceCleanupJob:
		return renderCleanupJob(th, st.cleanupJob, hk)
	}

	var lines []string
	if caps.StorageHealth {
		switch {
		case st.healthErr != nil:
			lines = append(lines, th.Style("errorText").Render("storage health unavailable"))
		case st.health == nil:
			lines = append(lines, th.Style("muted").Render("loading storage health…"))
		case !st.health.Available:
			lines = append(lines, th.Style("muted").Render("storage health unavailable"))
		default:
			h := *st.health
			bytesText, reclaimable := unavailableText, unavailableText
			if h.CurrentBytesAvailable {
				bytesText = humanizeBytes(h.CurrentBytes)
			}
			if h.ReclaimableBytesAvailable {
				reclaimable = humanizeBytes(h.ReclaimableBytes)
			}
			last, next := unavailableText, unavailableText
			if h.LastSweepAvailable {
				last = h.LastSweep.UTC().Format(time.RFC3339)
			}
			if h.NextSweepAvailable {
				next = h.NextSweep.UTC().Format(time.RFC3339)
			}
			job := "none"
			if h.ActiveJob != "" {
				job = sanitizeTerminal(h.ActiveJob)
			}
			failure := "none"
			if h.LastFailure != "" {
				failure = sanitizeTerminal(h.LastFailure)
			}
			lines = append(lines,
				"Current: "+bytesText+"  Reclaimable: "+reclaimable,
				fmt.Sprintf("Sessions: %d  Files: %d  v1: %d  v2: %d", h.SessionCount, h.FileCount, h.V1Count, h.V2Count),
				fmt.Sprintf("Main: %d  Child: %d  Scheduled: %d  Unknown: %d  Corrupt: %d", h.MainCount, h.ChildCount, h.ScheduledCount, h.UnknownCount, h.CorruptCount),
				fmt.Sprintf("Policy: main %s/%d  child %s/%d  scheduled %s/%d  cadence %s", h.Policy.MainMaxAge, h.Policy.MainMaxCount, h.Policy.ChildMaxAge, h.Policy.ChildMaxCount, h.Policy.ScheduledMaxAge, h.Policy.ScheduledMaxCount, h.Policy.SweepCadence),
				"Last sweep: "+last+"  Next sweep: "+next,
				"Active job: "+job+"  Last failure: "+failure)
		}
	}
	var actions []string
	if caps.StorageMigration {
		actions = append(actions, "o: Optimize storage (sessions preserved)")
	}
	if caps.StorageCleanup {
		actions = append(actions, "x: Clean up sessions (destructive)")
	}
	if len(actions) == 0 {
		actions = append(actions, "Status only — maintenance actions unavailable on this server")
	}
	lines = append(lines, "", th.Style("muted").Render(strings.Join(actions, "  ")+"  "+hk.nextTab+": switch  "+hk.closeOnly+": close"))
	return strings.Join(lines, "\n")
}

func renderMigrationPlan(th theme.Theme, plan client.SessionMigrationPlan, hk helpKeys) string {
	if !plan.Available {
		return th.Style("askTitle").Render("Optimize storage") + "\n\n" + th.Style("muted").Render("Optimization is unavailable on this backend.  "+hk.closeOnly+": back")
	}
	return strings.Join([]string{
		th.Style("askTitle").Render("Optimize storage — dry run"), "",
		"Sessions are preserved; this changes only their physical storage format.",
		fmt.Sprintf("v1: %d  v2: %d  Invalid: %d  Skipped: %d", plan.V1Families, plan.V2Families, plan.InvalidFamilies, plan.SkippedFamilies),
		"Current: " + humanizeBytes(plan.CurrentBytes) + "  Reclaimable: " + humanizeBytes(plan.ReclaimableBytes),
		"Temporary space required: " + humanizeBytes(plan.TemporaryBytes), "",
		th.Style("muted").Render(hk.choose + ": start resumable optimization  " + hk.closeOnly + ": back"),
	}, "\n")
}

func renderMigrationJob(th theme.Theme, job client.SessionMigrationJob, hk helpKeys) string {
	lines := []string{th.Style("askTitle").Render("Optimize storage"), "", "Job: " + sanitizeTerminal(job.ID) + "  State: " + sanitizeTerminal(job.State),
		fmt.Sprintf("Processed: %d/%d  Migrated: %d  Skipped: %d  Failed: %d", job.Processed, job.V1Families, job.Migrated, job.SkippedFamilies, job.Failed),
		"Sessions are preserved; completed items stay committed."}
	lines = append(lines, renderMigrationErrors(job.Errors)...)
	if job.State == teamStopReasonCancelled {
		lines = append(lines, "Cancellation stops future items; completed items stay committed.")
	}
	lines = append(lines, "", th.Style("muted").Render("r: resume  s: refresh status  c: cancel  "+hk.closeOnly+": back"))
	return strings.Join(lines, "\n")
}

func renderMigrationErrors(items []client.SessionMigrationItemError) []string {
	if len(items) == 0 {
		return nil
	}
	lines := []string{"Item failures:"}
	for i, item := range items {
		if i == 5 {
			lines = append(lines, fmt.Sprintf("… and %d more", len(items)-i))
			break
		}
		lines = append(lines, "- "+sanitizeTerminal(item.ItemHandle)+" ["+sanitizeTerminal(item.ReasonCode)+"] "+sanitizeTerminal(item.Message))
	}
	return lines
}

func cleanupKindCount(counts client.CleanupCounts, keys ...string) int {
	total := 0
	for _, kind := range keys {
		total += counts.ByKind[kind]
	}
	return total
}

func renderCleanupPlan(th theme.Theme, st sessionsState, hk helpKeys) string {
	plan := st.cleanupPlan
	if !plan.Available {
		return th.Style("askTitle").Render("Clean up sessions") + "\n\n" + th.Style("muted").Render("Cleanup is unavailable on this backend.  "+hk.closeOnly+": back")
	}
	eligible, protected := plan.EligibleCounts, plan.Protected
	return strings.Join([]string{
		th.Style("errorText").Render("Clean up sessions — DESTRUCTIVE dry run"), "",
		fmt.Sprintf("Eligible: %d  Main: %d  Child: %d  Scheduled: %d", eligible.Total, cleanupKindCount(eligible, "main"), cleanupKindCount(eligible, "subagent", "parallel_branch", "team_member"), cleanupKindCount(eligible, "scheduled")),
		fmt.Sprintf("Protected: %d  Unknown: %d protected  Live: %d  Awaiting: %d", protected.Total, cleanupKindCount(protected, "unknown"), protected.ByReason["live"], protected.ByState["awaiting"]),
		"Estimated deletion: " + humanizeBytes(plan.EstimatedBytes),
		"Unknown sessions are protected by default. Active, live, and awaiting sessions are not selected.", "",
		"To confirm this bulk operation, type CLEAN UP (single-row delete consent is not accepted):", st.cleanupConfirm.View(), "",
		th.Style("muted").Render(hk.choose + ": apply exact dry-run  " + hk.closeOnly + ": back"),
	}, "\n")
}

func renderCleanupJob(th theme.Theme, job client.CleanupJob, hk helpKeys) string {
	lines := []string{th.Style("errorText").Render("Clean up sessions"), "", "Job: " + sanitizeTerminal(job.ID) + "  State: " + sanitizeTerminal(job.State),
		fmt.Sprintf("Processed: %d  Deleted: %d  Skipped: %d  Stale: %d  Failed: %d", job.Processed, job.Deleted, job.Skipped, job.Stale, job.Failed),
		"Apply-time changes are skipped; partial completion is safe to inspect and retry."}
	for i, item := range job.Errors {
		if i == 5 {
			lines = append(lines, fmt.Sprintf("… and %d more", len(job.Errors)-i))
			break
		}
		lines = append(lines, "- "+sanitizeTerminal(item.ItemHandle)+" ["+sanitizeTerminal(item.ReasonCode)+"] "+sanitizeTerminal(item.Message))
	}
	if job.State == teamStopReasonCancelled {
		lines = append(lines, "Cancellation stops future items; completed deletions stay committed.")
	}
	lines = append(lines, "", th.Style("muted").Render("r: new dry run  s: refresh status  c: cancel  "+hk.closeOnly+": back"))
	return strings.Join(lines, "\n")
}

func renderSessionsPanelState(th theme.Theme, st sessionsState, hk helpKeys) (string, bool) {
	switch {
	case st.renaming:
		return th.Style("askTitle").Render("Rename session") + "\n\n" +
			st.renameInput.View() + "\n\n" +
			th.Style("muted").Render(hk.choose+": save  "+hk.closeOnly+": cancel"), true
	case st.confirmDelete:
		return th.Style("errorText").Render("Permanently delete session "+safeSessionID(st.actionID)+"?") + "\n\n" +
			th.Style("muted").Render("y/"+hk.choose+": delete  "+hk.closeOnly+": cancel"), true
	case st.actionLoading:
		return th.Style("muted").Render("loading…"), true
	case st.loadState == sessionsInitialLoading || (st.loadState == 0 && st.loading):
		return th.Style("muted").Render("loading sessions…  c: cancel"), true
	case st.loadState == sessionsInitialPageError:
		hint := "r: retry  " + hk.closeOnly + ": close"
		if st.startup {
			hint = "r: retry  " + sessionsPanelHint(hk, true)
		}
		return th.Style("errorText").Render("could not list sessions") + "\n" + th.Style("muted").Render(hint), true
	case st.err != nil && len(st.sessions) == 0:
		hint := hk.closeOnly + ": close"
		if st.startup {
			hint = sessionsPanelHint(hk, true)
		}
		return th.Style("errorText").Render("could not list sessions") + "\n" + th.Style("muted").Render(hint), true
	case len(st.filtered) == 0:
		message := "no " + []string{"chats", "scheduled runs", "child runs", "other sessions"}[st.tab] + " found"
		if st.filter.Value() != "" {
			message = "no matches — clear search to see all"
		}
		return th.Style("muted").Render(message) + "\n" + th.Style("muted").Render(sessionsPanelHint(hk, st.startup)), true
	default:
		return "", false
	}
}

func sessionsPaginationStatus(st sessionsState) string {
	switch st.loadState {
	case sessionsLoadingMore:
		return "loading more sessions…  c: cancel"
	case sessionsCancelled:
		return "loading cancelled — r: restart"
	case sessionsStaleRestart:
		return "session inventory changed — restarting from page one…"
	case sessionsLaterPageError:
		return "could not load more sessions — showing partial results · r: retry"
	default:
		return ""
	}
}

func renderSessionRows(b *strings.Builder, th theme.Theme, st sessionsState, current string) {
	start, end := scrollWindow(st.cursor, len(st.filtered), sessionsVisibleRows)
	for i := start; i < end; i++ {
		s := st.filtered[i]
		marker := "  "
		if i == st.cursor {
			marker = "▶ "
		}
		label := s.Title
		if label == "" {
			label = "untitled"
		}
		line := marker + stateBadge(s.State) + " " + relativeTime(s.ModifiedAt) + " " + strconv.Itoa(int(s.Turns)) + "t " + sanitizeTerminal(label)
		if handle := st.handles[s.ID]; handle != "" {
			line += "  " + handle
		}
		if s.ModelID != "" {
			line += "  (" + sanitizeTerminal(s.ModelID) + ")"
		}
		if s.Kind == client.SessionKindUnknown {
			line += "  [Legacy session — inspect only]"
		}
		if s.ID == current {
			line += "  [current]"
		}
		if s.Kind == client.SessionKindTeamMember && s.Relationship.MemberName != "" {
			line += "  [member " + sanitizeTerminal(s.Relationship.MemberName) + "]"
		}
		if i == st.cursor {
			line = th.Style("accent").Render(line)
		}
		b.WriteString(line + "\n")
	}
}

func sessionActionsHint(st sessionsState, hk helpKeys) string {
	selected := client.SessionListItem{}
	if st.cursor >= 0 && st.cursor < len(st.filtered) {
		selected = st.filtered[st.cursor]
	}
	action := "unavailable"
	if selected.Capabilities.PublicChat {
		action = "continue"
	} else if selected.Capabilities.Inspect {
		action = "inspect"
	}
	actions := []string{hk.choose + ": " + action}
	for _, action := range []struct {
		enabled bool
		label   string
	}{
		{selected.Capabilities.CopyID, "y: copy ID"},
		{selected.Capabilities.ViewTranscript, "v: view"},
		{selected.Capabilities.Fork, "f: fork"},
		{selected.Capabilities.Rename, "r: rename"},
		{selected.Capabilities.Delete, "d: delete"},
	} {
		if action.enabled {
			actions = append(actions, action.label)
		}
	}
	return strings.Join(actions, "  ") + "  " + sessionsPanelHint(hk, st.startup)
}

func sessionsPanelHint(hk helpKeys, startup bool) string {
	if startup {
		return hk.nextTab + ": switch  n: new chat  " + hk.closeOnly + ": quit"
	}
	return hk.nextTab + ": switch  " + hk.closeOnly + ": close"
}

func renderSessionsTranscript(th theme.Theme, st sessionsState, _ string, vpContent string, hk helpKeys, _, _ int) string {
	var b strings.Builder
	label := st.selected.Title
	if label == "" {
		label = "session"
	}
	if st.loading {
		b.WriteString(th.Style("title").Render("loading authoritative transcript") + "\n\n")
		b.WriteString(th.Style("muted").Render("Loading " + sanitizeTerminal(label) + "…"))
		b.WriteString("\n" + th.Style("muted").Render(hk.closeOnly+": Back"))
		return b.String()
	}
	if st.loadErr != nil {
		b.WriteString(th.Style("title").Render("transcript unavailable") + "\n\n")
		b.WriteString(th.Style("errorText").Render("The authoritative transcript could not be loaded. Continuation remains disabled."))
		b.WriteString("\n" + th.Style("muted").Render("r: Retry  "+hk.closeOnly+": Back"))
		return b.String()
	}
	b.WriteString(th.Style("muted").Render("Inspecting "+sanitizeTerminal(label)+" · read-only") + "\n")
	if st.transcript.isEmpty() {
		b.WriteString(th.Style("muted").Render("(empty authoritative transcript)"))
	} else {
		b.WriteString(vpContent)
	}
	b.WriteString(th.Style("muted").Render(hk.closeOnly + ": Back"))
	return b.String()
}
