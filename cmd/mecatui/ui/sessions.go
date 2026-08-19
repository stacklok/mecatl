package ui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
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

type sessionDetailsView struct {
	ID         string
	Title      string
	State      string
	Workspace  string
	CreatedAt  int64
	ModifiedAt int64
	ProviderID string
	ModelID    string
}

type sessionIDCopyResultMsg struct {
	id  string
	err error
}

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
	sourceID   string
	newID      string
	snapshot   client.SessionSnapshot
	transcript client.SessionTranscript
	err        error
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

	loadState      sessionsLoadState
	nextCursor     string
	pageCtx        context.Context
	pageCancel     context.CancelFunc
	pageGeneration uint64

	renaming      bool
	renameInput   textinput.Model
	confirmDelete bool
	actionID      string
	actionLoading bool

	adoptionReview    bool
	adoptionSource    client.SessionListItem
	adoptionBindings  client.AdoptionBindings
	adoptionPreflight client.AdoptionPreflight
	adoptionReason    client.CapabilityReason
	adoptionErr       error
	adoptionKey       string

	// Activity-replay fields are retained for the live-delivery catch-up and old
	// schedule replay machinery. /sessions never uses them as conversation truth.
	replayCh       chan tea.Msg
	replayStop     func()
	replayGen      uint64
	transcript     conversation
	receivedMsgs   int
	replayClosed   bool
	replayErr      error
	continueOnLoad bool
}

func (m Model) bindSessionID(id string) Model {
	m.sessionID = id
	m.sessionState = ""
	m.sessionCreatedAt = 0
	m.sessionModifiedAt = 0
	return m
}

func (m Model) sessionDetails() sessionDetailsView {
	return sessionDetailsView{
		ID: m.sessionID, Title: m.sessionTitle, State: m.sessionState,
		Workspace: m.activeWorkspace, CreatedAt: m.sessionCreatedAt,
		ModifiedAt: m.sessionModifiedAt, ProviderID: m.effectiveModel.ProviderID,
		ModelID: m.effectiveModel.ModelID,
	}
}

// ActiveSessionID returns the currently bound opaque session ID. The process
// entry point reads it only after Bubble Tea has restored the terminal.
func (m Model) ActiveSessionID() string { return m.sessionID }

func (m Model) sessionCopyTarget() string {
	if m.sessionID == "" || !utf8.ValidString(m.sessionID) {
		return ""
	}
	return m.sessionID
}

func safeSessionID(id string) string { return strconv.QuoteToASCII(id) }

func (m Model) openSessionDetails() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.sessionID == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no active session")
		return m, nil
	}
	m.sessionDetailsOpen = true
	m.ta.Blur()
	if m.deps.Session != nil {
		return m, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, m.sessionID)
	}
	return m, nil
}

func (m Model) onSessionDetailsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if !m.sessionDetailsOpen {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		m.sessionDetailsOpen = false
		return m, m.ta.Focus(), true
	}
	if msg.String() != "c" {
		return m, nil, true
	}
	id := m.sessionCopyTarget()
	if id == "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("no active session ID to copy")
		return m, nil, true
	}
	if m.deps.Clipboard == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: clipboard unavailable")
		return m, nil, true
	}
	cb, ctx := m.deps.Clipboard, m.deps.Ctx
	return m, func() tea.Msg {
		err := cb.Write(ctx, "text/plain", []byte(id))
		return sessionIDCopyResultMsg{id: id, err: err}
	}, true
}

func (m Model) onSessionIDCopyResult(msg sessionIDCopyResultMsg) Model {
	if msg.id == "" || msg.id != m.sessionID {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: active session changed")
		return m
	}
	if msg.err != nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: " + sanitizeTerminal(msg.err.Error()))
		return m
	}
	m.statusMsg = m.deps.Theme.Style("success").Render("copied session ID")
	return m
}

func formatSessionTimestamp(unixSec int64) string {
	if unixSec <= 0 {
		return "unknown"
	}
	return time.Unix(unixSec, 0).UTC().Format(time.RFC3339)
}

func renderSessionDetails(th theme.Theme, details sessionDetailsView, hk helpKeys, width, height int) string {
	unknown := func(value string) string {
		if value == "" {
			return "unknown"
		}
		return sanitizeTerminal(value)
	}
	var b strings.Builder
	b.WriteString(th.Style("askTitle").Render("Active session") + "\n\n")
	b.WriteString("ID: " + indentWrap(safeSessionID(details.ID), cardTextWidth(width)) + "\n")
	b.WriteString("Title: " + unknown(details.Title) + "\n")
	b.WriteString("State: " + unknown(details.State) + "\n")
	b.WriteString("Workspace: " + unknown(details.Workspace) + "\n")
	b.WriteString("Created: " + formatSessionTimestamp(details.CreatedAt) + "\n")
	b.WriteString("Modified: " + formatSessionTimestamp(details.ModifiedAt) + "\n")
	b.WriteString("Provider: " + unknown(details.ProviderID) + "\n")
	b.WriteString("Model: " + unknown(details.ModelID) + "\n\n")
	b.WriteString(th.Style("muted").Render("c: copy exact ID  " + hk.closeOnly + ": close"))
	return centerCard(th, b.String(), width, height)
}

func newSessionsPanelState() sessionsState {
	ti := textinput.New()
	ti.Placeholder = "search sessions…"
	ti.SetWidth(40)
	ti.Focus()
	return sessionsState{view: sessionsPanel, tab: tabChats, loading: true, loadState: sessionsInitialLoading, filter: ti}
}

func (m Model) beginSessionPagination() Model {
	return m.beginSessionPaginationAt("")
}

func (m Model) beginSessionPaginationAt(cursor string) Model {
	if m.sessions.pageCancel != nil {
		m.sessions.pageCancel()
	}
	ctx := m.deps.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.sessionsPageSeq++
	m.sessions.pageCtx, m.sessions.pageCancel = context.WithCancel(ctx)
	m.sessions.pageGeneration = m.sessionsPageSeq
	m.sessions.nextCursor = cursor
	m.sessions.loading = cursor == ""
	if cursor == "" {
		m.sessions.loadState = sessionsInitialLoading
	} else {
		m.sessions.loadState = sessionsLoadingMore
	}
	m.sessions.err = nil
	return m
}

func (m Model) restartStalePagination() Model {
	if m.sessions.pageCancel != nil {
		m.sessions.pageCancel()
	}
	ctx := m.deps.Ctx
	if ctx == nil {
		ctx = context.Background()
	}
	m.sessionsPageSeq++
	m.sessions.pageCtx, m.sessions.pageCancel = context.WithCancel(ctx)
	m.sessions.pageGeneration = m.sessionsPageSeq
	m.sessions.nextCursor = ""
	m.sessions.loading = false
	m.sessions.loadState = sessionsStaleRestart
	return m
}

func (m Model) ensureSessionPagination() Model {
	if m.sessions.pageCtx == nil {
		return m.beginSessionPagination()
	}
	return m
}

func (m Model) sessionPageCmd() tea.Cmd {
	if m.deps.Sessions == nil || m.sessions.pageCtx == nil {
		return nil
	}
	return client.ListSessionsPageCmd(
		m.sessions.pageCtx, m.deps.Sessions, m.sessions.nextCursor, m.sessions.pageGeneration,
	)
}

func (m Model) openSessions() (tea.Model, tea.Cmd) {
	if m.phase != phaseIdle || m.deps.Sessions == nil || m.deps.Transcript == nil {
		return m, nil
	}
	m.ta.Blur()
	m.sessions = newSessionsPanelState()
	m = m.beginSessionPagination()
	cmds := []tea.Cmd{m.sessionPageCmd(), textinput.Blink}
	if m.caps.StorageHealth && m.deps.StorageHealth != nil {
		cmds = append(cmds, loadStorageHealthCmd(m.deps.Ctx, m.deps.StorageHealth))
	}
	if m.maintenanceMigrationJobID != "" && m.caps.StorageMigration && m.deps.Migration != nil {
		cmds = append(cmds, migrationStatusCmd(m.deps.Ctx, m.deps.Migration, m.maintenanceMigrationJobID))
	}
	if m.maintenanceCleanupJobID != "" && m.caps.StorageCleanup && m.deps.Cleanup != nil {
		cmds = append(cmds, cleanupStatusCmd(m.deps.Ctx, m.deps.Cleanup, m.maintenanceCleanupJobID))
	}
	return m, tea.Batch(cmds...)
}

func (m Model) closeSessions() (tea.Model, tea.Cmd) {
	if m.sessions.pageCancel != nil {
		m.sessions.pageCancel()
	}
	m.sessionsPageSeq++
	m.sessions = sessionsState{}
	cmd := m.ta.Focus()
	return m, cmd
}

func (m Model) onSessionsLoadKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "c" && (m.sessions.loadState == sessionsInitialLoading || m.sessions.loadState == sessionsLoadingMore || m.sessions.loadState == sessionsStaleRestart) {
		if m.sessions.pageCancel != nil {
			m.sessions.pageCancel()
		}
		m.sessions.loading = false
		m.sessions.loadState = sessionsCancelled
		return m, nil, true
	}
	if msg.String() == "r" && (m.sessions.loadState == sessionsLaterPageError || m.sessions.loadState == sessionsInitialPageError || m.sessions.loadState == sessionsCancelled) {
		if m.sessions.loadState != sessionsLaterPageError {
			m.sessions.nextCursor = ""
		}
		m = m.beginSessionPaginationAt(m.sessions.nextCursor)
		return m, m.sessionPageCmd(), true
	}
	return m, nil, false
}

func (m Model) onSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.sessions.view == sessionsNone || m.sessions.view == sessionsTranscript {
		return m, nil, false
	}
	if m.sessions.adoptionReview {
		return m.onSessionAdoptionKey(msg)
	}
	if m.sessions.renaming {
		return m.onSessionRenameKey(msg)
	}
	if m.sessions.confirmDelete {
		return m.onSessionDeleteConfirmKey(msg)
	}
	if mm, cmd, handled := m.onMaintenanceKey(msg); handled {
		return mm, cmd, true
	}
	if mm, cmd, handled := m.onSessionsLoadKey(msg); handled {
		return mm, cmd, true
	}
	if m.sessions.actionLoading {
		return m, nil, true
	}
	if mm, cmd, handled := m.onStartupSessionsKey(msg); handled {
		return mm, cmd, true
	}
	if key.Matches(msg, m.keys.NextTab) {
		m = m.switchSessionsTab()
		m = m.invalidateAdoptionPreflight()
		return m, m.adoptionPreflightCmd(), true
	}
	if mm, cmd, handled := m.onSessionsNavigationKey(msg); handled {
		return mm, cmd, true
	}
	if mm, cmd, handled := m.onSessionsActionKey(msg); handled {
		return mm, cmd, true
	}
	selectedBefore := selectedSessionID(m.sessions)
	var cmd tea.Cmd
	m.sessions.filter, cmd = m.sessions.filter.Update(msg)
	m = m.syncSessionsFilter()
	if selectedSessionID(m.sessions) != selectedBefore {
		m = m.invalidateAdoptionPreflight()
		cmd = tea.Batch(cmd, m.adoptionPreflightCmd())
	}
	return m, cmd, true
}

func (m Model) onMaintenanceKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if m.sessions.tab != tabStorageHealth {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) && m.sessions.maintenance != maintenanceNone {
		m.sessions.maintenance = maintenanceNone
		m.sessions.maintenanceErr = false
		return m, nil, true
	}
	if m.sessions.actionLoading {
		return m, nil, true
	}
	var cmd tea.Cmd
	switch m.sessions.maintenance {
	case maintenanceNone:
		m, cmd = m.onMaintenanceMenuKey(msg)
	case maintenanceOptimizePlan, maintenanceOptimizeJob:
		m, cmd = m.onOptimizeStorageKey(msg)
	case maintenanceCleanupPlan:
		m, cmd = m.onCleanupPlanKey(msg)
	case maintenanceCleanupJob:
		m, cmd = m.onCleanupJobKey(msg)
	}
	return m, cmd, true
}

func (m Model) onMaintenanceMenuKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	switch msg.String() {
	case "o":
		if m.caps.StorageMigration && m.deps.Migration != nil {
			m.sessions.actionLoading = true
			m.sessions.maintenanceErr = false
			return m, migrationPlanCmd(m.deps.Ctx, m.deps.Migration)
		}
	case "x":
		if m.caps.StorageCleanup && m.deps.Cleanup != nil {
			m.sessions.actionLoading = true
			m.sessions.maintenanceErr = false
			return m, cleanupPlanCmd(m.deps.Ctx, m.deps.Cleanup)
		}
	}
	return m, nil
}

func (m Model) onOptimizeStorageKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.sessions.maintenance == maintenanceOptimizePlan {
		if key.Matches(msg, m.keys.Choose) && m.sessions.migrationPlan.Available {
			m.sessions.actionLoading = true
			return m, migrationApplyCmd(m.deps.Ctx, m.deps.Migration, m.sessions.migrationPlan.ID)
		}
		return m, nil
	}
	m.sessions.actionLoading = true
	switch msg.String() {
	case "r":
		return m, migrationResumeCmd(m.deps.Ctx, m.deps.Migration, m.sessions.migrationJob.ID)
	case "c":
		return m, migrationCancelCmd(m.deps.Ctx, m.deps.Migration, m.sessions.migrationJob.ID)
	case "s":
		return m, migrationStatusCmd(m.deps.Ctx, m.deps.Migration, m.sessions.migrationJob.ID)
	default:
		m.sessions.actionLoading = false
		return m, nil
	}
}

func (m Model) onCleanupPlanKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if msg.String() == "y" {
		// Single-row delete consent is deliberately inert for bulk cleanup.
		return m, nil
	}
	if key.Matches(msg, m.keys.Choose) {
		if m.sessions.cleanupConfirm.Value() != "CLEAN UP" {
			return m, nil
		}
		m.sessions.actionLoading = true
		return m, cleanupApplyCmd(m.deps.Ctx, m.deps.Cleanup, m.sessions.cleanupPlan.ConfirmationToken)
	}
	var cmd tea.Cmd
	m.sessions.cleanupConfirm, cmd = m.sessions.cleanupConfirm.Update(msg)
	return m, cmd
}

func (m Model) onCleanupJobKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	m.sessions.actionLoading = true
	switch msg.String() {
	case "c":
		return m, cleanupCancelCmd(m.deps.Ctx, m.deps.Cleanup, m.sessions.cleanupJob.ID)
	case "s":
		return m, cleanupStatusCmd(m.deps.Ctx, m.deps.Cleanup, m.sessions.cleanupJob.ID)
	case "r":
		return m, cleanupPlanCmd(m.deps.Ctx, m.deps.Cleanup)
	default:
		m.sessions.actionLoading = false
		return m, nil
	}
}

func (m Model) onStartupSessionsKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if !m.browsingStartupSessions {
		return m, nil, false
	}
	if key.Matches(msg, m.keys.Close) {
		if m.sessions.pageCancel != nil {
			m.sessions.pageCancel()
		}
		m.sessionsPageSeq++
		return m, tea.Quit, true
	}
	if msg.String() != "n" {
		return m, nil, false
	}
	if !m.modelsReconciled {
		m.statusMsg = m.deps.Theme.Style("muted").Render("loading model defaults…")
		return m, nil, true
	}
	if m.sessions.pageCancel != nil {
		m.sessions.pageCancel()
	}
	m.sessionsPageSeq++
	m.sessions = sessionsState{}
	m.phase = phaseConnecting
	return m, m.createSessionCmd(), true
}

const sessionsVisibleRows = 12

func (m Model) onSessionsNavigationKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch {
	case key.Matches(msg, m.keys.Close):
		if m.sessions.filter.Value() != "" {
			m.sessions.filter.SetValue("")
			m = m.syncSessionsFilter()
			return m, nil, true
		}
		mm, cmd := m.closeSessions()
		return mm, cmd, true
	case msg.String() == keyMenuUp:
		if m.sessions.cursor > 0 {
			m.sessions.cursor--
		}
		m = m.invalidateAdoptionPreflight()
		return m, m.adoptionPreflightCmd(), true
	case msg.String() == keyMenuDown:
		if m.sessions.cursor < len(m.sessions.filtered)-1 {
			m.sessions.cursor++
		}
		m = m.invalidateAdoptionPreflight()
		return m, m.adoptionPreflightCmd(), true
	case key.Matches(msg, m.keys.ScrollTop):
		m.sessions.cursor = 0
		m = m.invalidateAdoptionPreflight()
		return m, m.adoptionPreflightCmd(), true
	case key.Matches(msg, m.keys.ScrollBottom):
		m.sessions.cursor = clampModelsCursor(len(m.sessions.filtered)-1, len(m.sessions.filtered))
		m = m.invalidateAdoptionPreflight()
		return m, m.adoptionPreflightCmd(), true
	case key.Matches(msg, m.keys.Choose):
		return m.chooseSession()
	default:
		return m, nil, false
	}
}

func (m Model) onSessionsActionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	switch msg.String() {
	case "a":
		return m.openSessionAdoption()
	case "y":
		return m.copyInventorySessionID()
	case "v":
		return m.viewInventorySession()
	case "f":
		return m.forkInventorySession()
	case "r":
		return m.openSessionRename()
	case "d":
		return m.openSessionDelete()
	default:
		return m, nil, false
	}
}

func (m Model) syncSessionsFilter() Model {
	tabbed := filterSessionsByTab(m.sessions.sessions, m.sessions.tab)
	m.sessions.handles = sessionDisplayHandles(tabbed)
	m.sessions.filtered = filterSessions(tabbed, m.sessions.handles, m.sessions.filter.Value())
	if m.sessions.cursor >= len(m.sessions.filtered) {
		m.sessions.cursor = 0
	}
	return m
}

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
			s.Title, s.ID, handles[s.ID], s.ModelID, s.Workspace,
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

func sessionDigest(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:])
}

// sessionDisplayHandles derives terminal-safe lowercase-hex handles and expands
// only colliding prefixes. The map is display-only; callers retain the full ID.
func sessionDisplayHandles(rows []client.SessionListItem) map[string]string {
	const minimum = 8
	digests := make(map[string]string, len(rows))
	lengths := make(map[string]int, len(rows))
	for _, row := range rows {
		digests[row.ID] = sessionDigest(row.ID)
		lengths[row.ID] = minimum
	}
	for {
		groups := make(map[string][]string, len(rows))
		for id, digest := range digests {
			groups[digest[:lengths[id]]] = append(groups[digest[:lengths[id]]], id)
		}
		changed := false
		for _, ids := range groups {
			if len(ids) < 2 {
				continue
			}
			for _, id := range ids {
				if lengths[id] < len(digests[id]) {
					lengths[id]++
					changed = true
				}
			}
		}
		if !changed {
			break
		}
	}
	out := make(map[string]string, len(rows))
	for _, row := range rows {
		out[row.ID] = digests[row.ID][:lengths[row.ID]]
	}
	return out
}

func (m Model) selectedInventorySession() (client.SessionListItem, bool) {
	if m.sessions.cursor < 0 || m.sessions.cursor >= len(m.sessions.filtered) {
		return client.SessionListItem{}, false
	}
	return m.sessions.filtered[m.sessions.cursor], true
}

func (m Model) adoptionBindings() client.AdoptionBindings {
	provider, model := m.effectiveModel.ProviderID, m.effectiveModel.ModelID
	if provider == "" && model == "" {
		provider, model = m.deps.InitialModel.ProviderID, m.deps.InitialModel.ModelID
	}
	return client.AdoptionBindings{
		Workspace: m.activeWorkspace, EnvironmentKind: "local", EnvironmentID: m.activeWorkspace,
		ProviderID: provider, ModelID: model,
	}
}

func completeAdoptionBindings(binding client.AdoptionBindings) bool {
	return binding.Workspace != "" && binding.EnvironmentKind != "" && binding.EnvironmentID != "" &&
		binding.ProviderID != "" && binding.ModelID != ""
}

func (m Model) invalidateAdoptionPreflight() Model {
	m.sessions.adoptionPreflight = client.AdoptionPreflight{}
	m.sessions.adoptionReason = ""
	m.sessions.adoptionBindings = client.AdoptionBindings{}
	return m
}

func (m Model) adoptionPreflightCmd() tea.Cmd {
	row, ok := m.selectedInventorySession()
	if !ok || row.Kind != client.SessionKindUnknown || m.deps.Adoption == nil {
		return nil
	}
	bindings := m.adoptionBindings()
	if !completeAdoptionBindings(bindings) {
		return nil
	}
	return client.PreflightSessionAdoptionCmd(m.deps.Ctx, m.deps.Adoption, row.ID, bindings)
}

func (m Model) openSessionAdoption() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !m.sessions.adoptionPreflight.Eligible || m.sessions.actionID != row.ID {
		return m, nil, true
	}
	m.sessions.adoptionReview = true
	m.sessions.adoptionSource = row
	m.sessions.adoptionBindings = m.sessions.adoptionPreflight.Bindings
	m.sessions.adoptionErr = nil
	m.sessions.adoptionKey = ""
	return m, nil, true
}

func newAdoptionKey() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("create adoption request: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

func (m Model) onSessionAdoptionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) {
		m.sessions.adoptionReview = false
		m.sessions.adoptionErr = nil
		return m, nil, true
	}
	if !key.Matches(msg, m.keys.Choose) || m.sessions.actionLoading {
		return m, nil, true
	}
	if !completeAdoptionBindings(m.sessions.adoptionBindings) {
		m.sessions.adoptionErr = errors.New("select an explicit workspace, environment, provider, and model")
		return m, nil, true
	}
	if m.sessions.adoptionKey == "" {
		requestKey, err := newAdoptionKey()
		if err != nil {
			m.sessions.adoptionErr = err
			return m, nil, true
		}
		m.sessions.adoptionKey = requestKey
	}
	m.sessions.actionLoading = true
	source, binding, requestKey := m.sessions.adoptionSource, m.sessions.adoptionBindings, m.sessions.adoptionKey
	adopter, getter, transcript, ctx := m.deps.Adoption, m.deps.Session, m.deps.Transcript, m.deps.Ctx
	return m, func() tea.Msg {
		result, err := adopter.AdoptSession(ctx, source.ID, requestKey, binding)
		if err != nil {
			return client.SessionAdoptedMsg{SourceID: source.ID, Err: err}
		}
		if result.SourceSessionID != source.ID || result.SessionID == "" {
			return client.SessionAdoptedMsg{SourceID: source.ID, Err: errors.New("adoption returned an invalid target")}
		}
		if getter == nil || transcript == nil {
			return client.SessionAdoptedMsg{SourceID: source.ID, Err: errors.New("authoritative target refetch is unavailable")}
		}
		snapshot, err := getter.GetSession(ctx, result.SessionID)
		if err != nil {
			return client.SessionAdoptedMsg{SourceID: source.ID, Result: result, Err: err}
		}
		loaded, err := transcript.GetSessionTranscript(ctx, result.SessionID)
		if err != nil || !loaded.Complete || loaded.SessionID != result.SessionID {
			if err == nil {
				err = errIncompleteTranscript
			}
			return client.SessionAdoptedMsg{SourceID: source.ID, Result: result, Snapshot: snapshot, Err: err}
		}
		return client.SessionAdoptedMsg{SourceID: source.ID, Result: result, Snapshot: snapshot, Transcript: loaded}
	}, true
}

func (m Model) copyInventorySessionID() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.CopyID {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.CopyID))
		return m, nil, true
	}
	if row.ID == "" || !utf8.ValidString(row.ID) {
		m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: invalid ID")
		return m, nil, true
	}
	id, cb, ctx := row.ID, m.deps.Clipboard, m.deps.Ctx
	return m, func() tea.Msg {
		if cb != nil {
			return inventorySessionIDCopiedMsg{id: id, err: cb.Write(ctx, "text/plain", []byte(id))}
		}
		return inventorySessionIDCopiedMsg{id: id}
	}, true
}

func (m Model) viewInventorySession() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.ViewTranscript {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.ViewTranscript))
		return m, nil, true
	}
	return m.loadSessionTranscript(row, true)
}

func (m Model) forkInventorySession() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.Fork || m.deps.Session == nil || m.deps.Transcript == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.Fork))
		return m, nil, true
	}
	m.sessions.actionLoading = true
	m.sessions.actionID = row.ID
	session, transcript, ctx := m.deps.Session, m.deps.Transcript, m.deps.Ctx
	return m, func() tea.Msg {
		newID, err := session.ForkSession(ctx, row.ID, "")
		if err != nil {
			return sessionForkedMsg{sourceID: row.ID, err: err}
		}
		snapshot, err := session.GetSession(ctx, newID)
		if err != nil {
			return sessionForkedMsg{sourceID: row.ID, newID: newID, err: err}
		}
		loaded, err := transcript.GetSessionTranscript(ctx, newID)
		if err != nil || !loaded.Complete || loaded.SessionID != newID {
			if err == nil {
				err = errIncompleteTranscript
			}
			return sessionForkedMsg{sourceID: row.ID, newID: newID, err: err}
		}
		return sessionForkedMsg{sourceID: row.ID, newID: newID, snapshot: snapshot, transcript: loaded}
	}, true
}

func (m Model) openSessionRename() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.Rename || m.deps.SessionManagement == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.Rename))
		return m, nil, true
	}
	input := textinput.New()
	input.SetWidth(50)
	input.SetValue(row.Title)
	input.Focus()
	m.sessions.renaming = true
	m.sessions.renameInput = input
	m.sessions.actionID = row.ID
	return m, textinput.Blink, true
}

func (m Model) onSessionRenameKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) {
		m.sessions.renaming = false
		m.sessions.actionID = ""
		return m, nil, true
	}
	if key.Matches(msg, m.keys.Choose) {
		id, title := m.sessions.actionID, m.sessions.renameInput.Value()
		m.sessions.renaming = false
		m.sessions.actionLoading = true
		return m, client.RenameSessionCmd(m.deps.Ctx, m.deps.SessionManagement, id, title), true
	}
	var cmd tea.Cmd
	m.sessions.renameInput, cmd = m.sessions.renameInput.Update(msg)
	return m, cmd, true
}

func (m Model) openSessionDelete() (tea.Model, tea.Cmd, bool) {
	row, ok := m.selectedInventorySession()
	if !ok || !row.Capabilities.Delete || m.deps.SessionManagement == nil {
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(row.Reasons.Delete))
		return m, nil, true
	}
	if row.ID == m.sessionID && m.sessionID != "" {
		m.statusMsg = m.deps.Theme.Style("warning").Render("cannot delete the current chat — switch to another chat first")
		return m, nil, true
	}
	m.sessions.confirmDelete = true
	m.sessions.actionID = row.ID
	return m, nil, true
}

func (m Model) onSessionDeleteConfirmKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd, bool) {
	if key.Matches(msg, m.keys.Close) || msg.String() == "n" {
		m.sessions.confirmDelete = false
		m.sessions.actionID = ""
		return m, nil, true
	}
	if msg.String() != "y" && !key.Matches(msg, m.keys.Choose) {
		return m, nil, true
	}
	id := m.sessions.actionID
	m.sessions.confirmDelete = false
	m.sessions.actionLoading = true
	return m, client.DeleteSessionCmd(m.deps.Ctx, m.deps.SessionManagement, id), true
}

func (m Model) chooseSession() (tea.Model, tea.Cmd, bool) {
	if m.sessions.cursor < 0 || m.sessions.cursor >= len(m.sessions.filtered) {
		return m, nil, false
	}
	chosen := m.sessions.filtered[m.sessions.cursor]
	if chosen.ID == m.sessionID {
		m.statusMsg = m.deps.Theme.Style("muted").Render("this is the current chat")
		return m, nil, true
	}
	inspect := !chosen.Capabilities.PublicChat && chosen.Capabilities.Inspect
	if !chosen.Capabilities.PublicChat && !chosen.Capabilities.Inspect {
		reason := chosen.Reasons.PublicChat
		if reason == "" {
			reason = chosen.Reasons.Inspect
		}
		if reason == "" {
			reason = chosen.ReasonCode // compatibility with older servers/tests.
		}
		m.statusMsg = m.deps.Theme.Style("warning").Render(capabilityReasonText(reason))
		return m, nil, true
	}
	return m.loadSessionTranscript(chosen, inspect)
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

func (m Model) loadSessionTranscript(row client.SessionListItem, inspect bool) (tea.Model, tea.Cmd, bool) {
	if m.deps.Transcript == nil {
		return m, nil, false
	}
	m.sessions.selected = row
	m.sessions.inspect = inspect
	if m.sessions.pageCancel != nil {
		m.sessions.pageCancel()
		m.sessions.pageCancel = nil
	}
	m.sessionsPageSeq++
	m.sessions.loadErr = nil
	m.sessions.loading = true
	m.sessions.transcript = conversation{}
	m.sessions.view = sessionsTranscript
	m.phase = phaseReplay
	m.ta.Blur()
	return m, client.GetSessionTranscriptCmd(m.deps.Ctx, m.deps.Transcript, row.ID), true
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

func (m Model) applySessionPage(msg client.SessionInventoryPageMsg) (tea.Model, tea.Cmd, bool) {
	if m.sessions.view != sessionsPanel || (msg.Generation != 0 && msg.Generation != m.sessions.pageGeneration) {
		return m, nil, true
	}
	selectedID := selectedSessionID(m.sessions)
	if m.sessions.actionID != "" {
		selectedID = m.sessions.actionID
	}
	if msg.Err != nil {
		m.sessions.loading = false
		m.sessions.err = msg.Err
		m.sessions.nextCursor = msg.Cursor
		switch {
		case errors.Is(msg.Err, client.ErrSessionInventoryRestart):
			m = m.restartStalePagination()
			return m, m.sessionPageCmd(), true
		case errors.Is(msg.Err, context.Canceled):
			m.sessions.loadState = sessionsCancelled
		case len(m.sessions.sessions) > 0:
			m.sessions.loadState = sessionsLaterPageError
		default:
			m.sessions.loadState = sessionsInitialPageError
		}
		return m, nil, true
	}

	replace := msg.Cursor == "" && (m.sessions.loadState == sessionsInitialLoading || m.sessions.loadState == sessionsStaleRestart)
	m.sessions.sessions = mergeSessionPages(m.sessions.sessions, msg.Page.Sessions, replace)
	m.sessions.err = nil
	m.sessions.loading = false
	m = m.syncSessionsFilter()
	if selectedID != "" {
		for i := range m.sessions.filtered {
			if m.sessions.filtered[i].ID == selectedID {
				m.sessions.cursor = i
				break
			}
		}
	}
	m.sessions.nextCursor = msg.Page.NextCursor
	m.sessions.actionID = ""
	if msg.Page.NextCursor == "" {
		m.sessions.loadState = sessionsComplete
		if m.sessions.pageCancel != nil {
			m.sessions.pageCancel()
			m.sessions.pageCancel = nil
		}
		return m, m.adoptionPreflightCmd(), true
	}
	m.sessions.loadState = sessionsLoadingMore
	return m, tea.Batch(m.sessionPageCmd(), m.adoptionPreflightCmd()), true
}

func (m Model) updateMaintenanceMsg(msg tea.Msg) (Model, bool) {
	switch sm := msg.(type) {
	case migrationPlanMsg:
		m.sessions.actionLoading = false
		m.sessions.maintenanceErr = sm.err != nil
		if sm.err == nil {
			m.sessions.migrationPlan = sm.plan
			m.sessions.maintenance = maintenanceOptimizePlan
		}
		return m, true
	case migrationJobMsg:
		m.sessions.actionLoading = false
		m.sessions.maintenanceErr = sm.err != nil
		if sm.err == nil {
			m.sessions.migrationJob = sm.job
			m.sessions.maintenance = maintenanceOptimizeJob
			if sm.job.ID != "" {
				m.maintenanceMigrationJobID = sm.job.ID
			}
		}
		return m, true
	case cleanupPlanMsg:
		m.sessions.actionLoading = false
		m.sessions.maintenanceErr = sm.err != nil
		if sm.err == nil {
			m.sessions.cleanupPlan = sm.plan
			m.sessions.maintenance = maintenanceCleanupPlan
			confirm := textinput.New()
			confirm.Placeholder = "type CLEAN UP"
			confirm.SetWidth(24)
			confirm.Focus()
			m.sessions.cleanupConfirm = confirm
		}
		return m, true
	case cleanupJobMsg:
		m.sessions.actionLoading = false
		m.sessions.maintenanceErr = sm.err != nil
		if sm.err == nil {
			m.sessions.cleanupJob = sm.job
			m.sessions.maintenance = maintenanceCleanupJob
			if sm.job.ID != "" {
				m.maintenanceCleanupJobID = sm.job.ID
			}
		}
		return m, true
	default:
		return m, false
	}
}

func (m Model) updateSessionsMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if mm, cmd, handled := m.updateSessionActionMsg(msg); handled {
		return mm, cmd, true
	}
	switch sm := msg.(type) {
	case client.SessionInventoryPageMsg:
		return m.applySessionPage(sm)
	case storageHealthLoadedMsg:
		m.sessions.healthErr = sm.err
		if sm.err == nil {
			m.sessions.health = &sm.health
		}
		return m, nil, true
	case client.SessionsListedMsg:
		m.sessions.loading = false
		m.sessions.loadState = sessionsComplete
		selectedID := ""
		if m.sessions.cursor >= 0 && m.sessions.cursor < len(m.sessions.filtered) {
			selectedID = m.sessions.filtered[m.sessions.cursor].ID
		}
		if sm.Err != nil {
			m.sessions.loadState = sessionsInitialPageError
			m.sessions.err = sm.Err
			m.sessions.sessions = nil
			m.sessions.filtered = nil
			return m, nil, true
		}
		m.sessions.err = nil
		m.sessions.sessions = sm.Sessions
		if m.sessions.actionID != "" {
			selectedID = m.sessions.actionID
		}
		m = m.syncSessionsFilter()
		for i := range m.sessions.filtered {
			if m.sessions.filtered[i].ID == selectedID {
				m.sessions.cursor = i
				break
			}
		}
		m.sessions.actionID = ""
		return m, m.adoptionPreflightCmd(), true
	case client.SessionTranscriptMsg:
		if m.sessions.view != sessionsTranscript || sm.SessionID != m.sessions.selected.ID {
			return m, nil, true
		}
		m.sessions.loading = false
		if sm.Err != nil || !sm.Transcript.Complete || sm.Transcript.SessionID != sm.SessionID {
			m.sessions.loadErr = sm.Err
			if m.sessions.loadErr == nil {
				m.sessions.loadErr = errIncompleteTranscript
			}
			m.refreshView()
			return m, nil, true
		}
		m.sessions.transcript = conversationFromTranscript(sm.Transcript.Messages)
		if m.sessions.inspect {
			m.refreshView()
			return m, nil, true
		}
		return m.adoptAuthoritativeTranscript()
	default:
		return m, nil, false
	}
}

func (m Model) updateAdoptionMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch sm := msg.(type) {
	case client.SessionAdoptionPreflightMsg:
		row, ok := m.selectedInventorySession()
		if !ok || row.ID != sm.SourceID || row.Kind != client.SessionKindUnknown {
			return m, nil, true
		}
		m.sessions.actionID = sm.SourceID
		m.sessions.adoptionErr = sm.Err
		if sm.Err != nil {
			m.sessions.adoptionPreflight = client.AdoptionPreflight{}
			m.sessions.adoptionReason = client.CapabilityReasonUnknown
			return m, nil, true
		}
		m.sessions.adoptionPreflight = sm.Preflight
		m.sessions.adoptionReason = sm.Preflight.Reason
		if sm.Preflight.Eligible && completeAdoptionBindings(sm.Preflight.Bindings) {
			m.sessions.adoptionBindings = sm.Preflight.Bindings
		}
		return m, nil, true
	case client.SessionAdoptedMsg:
		return m.onSessionAdopted(sm)
	default:
		return m, nil, false
	}
}

func (m Model) onSessionAdopted(sm client.SessionAdoptedMsg) (tea.Model, tea.Cmd, bool) {
	if !m.sessions.adoptionReview || sm.SourceID != m.sessions.adoptionSource.ID {
		return m, nil, true
	}
	m.sessions.actionLoading = false
	if sm.Err != nil {
		m.sessions.adoptionErr = sm.Err
		return m, nil, true
	}
	if sm.Result.SourceSessionID != sm.SourceID || sm.Result.SessionID == "" || !sm.Transcript.Complete || sm.Transcript.SessionID != sm.Result.SessionID || sm.Transcript.Kind != client.SessionKindMain {
		m.sessions.adoptionErr = errors.New("server returned an incomplete adopted chat")
		return m, nil, true
	}
	m.caps = sm.Result.Capabilities
	m.effectiveModel = sm.Snapshot.ResolvedModel
	m.activeMode = sm.Snapshot.Mode
	m.sessions.selected = client.SessionListItem{
		ID: sm.Result.SessionID, Title: sm.Snapshot.Title, TitleProvenance: sm.Snapshot.TitleProvenance,
		State: sm.Snapshot.State, Workspace: sm.Snapshot.Workspace, CreatedAt: sm.Snapshot.CreatedAt,
		Kind: client.SessionKindMain, Capabilities: client.SessionInventoryCapabilities{PublicChat: true, Inspect: true},
	}
	m.sessions.transcript = conversationFromTranscript(sm.Transcript.Messages)
	m.sessions.inspect = false
	return m.adoptAuthoritativeTranscript()
}

func (m Model) updateSessionActionMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	if mm, handled := m.updateMaintenanceMsg(msg); handled {
		return mm, nil, true
	}
	if mm, cmd, handled := m.updateAdoptionMsg(msg); handled {
		return mm, cmd, true
	}
	switch sm := msg.(type) {
	case inventorySessionIDCopiedMsg:
		if sm.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not copy session ID: " + sanitizeTerminal(sm.err.Error()))
			return m, nil, true
		}
		m.statusMsg = m.deps.Theme.Style("success").Render("copied exact session ID " + safeSessionID(sm.id))
		return m, tea.SetClipboard(sm.id), true
	case client.SessionRenamedMsg:
		m.sessions.actionLoading = false
		if sm.Err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not rename session: " + sanitizeTerminal(sm.Err.Error()))
			return m, nil, true
		}
		for i := range m.sessions.sessions {
			if m.sessions.sessions[i].ID == sm.SessionID {
				m.sessions.sessions[i].Title = sm.Title
				m.sessions.sessions[i].TitleProvenance = sm.TitleProvenance
			}
		}
		if sm.SessionID == m.sessionID {
			m.sessionTitle = sm.Title
		}
		m = m.syncSessionsFilter()
		m.statusMsg = m.deps.Theme.Style("success").Render("renamed session")
		if m.deps.Sessions != nil {
			m = m.beginSessionPagination()
			return m, m.sessionPageCmd(), true
		}
		return m, nil, true
	case client.SessionDeletedMsg:
		m.sessions.actionLoading = false
		if sm.Err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not delete session: " + sanitizeTerminal(sm.Err.Error()))
			return m, nil, true
		}
		kept := m.sessions.sessions[:0]
		for _, row := range m.sessions.sessions {
			if row.ID != sm.SessionID {
				kept = append(kept, row)
			}
		}
		m.sessions.sessions = kept
		m.sessions.actionID = ""
		m = m.syncSessionsFilter()
		m.statusMsg = m.deps.Theme.Style("success").Render("deleted session")
		return m, nil, true
	case sessionForkedMsg:
		m.sessions.actionLoading = false
		if sm.sourceID != m.sessions.actionID {
			return m, nil, true
		}
		if sm.err != nil {
			m.statusMsg = m.deps.Theme.Style("warning").Render("could not fork session: " + sanitizeTerminal(sm.err.Error()))
			return m, nil, true
		}
		m.sessions.selected = client.SessionListItem{
			ID: sm.newID, Title: sm.snapshot.Title, TitleProvenance: sm.snapshot.TitleProvenance,
			State: sm.snapshot.State, Workspace: sm.snapshot.Workspace, CreatedAt: sm.snapshot.CreatedAt,
			Kind: sm.transcript.Kind, Relationship: sm.transcript.Relationship,
		}
		m.sessions.transcript = conversationFromTranscript(sm.transcript.Messages)
		m.sessions.inspect = false
		return m.adoptAuthoritativeTranscript()
	default:
		return m, nil, false
	}
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

func (m Model) adoptAuthoritativeTranscript() (tea.Model, tea.Cmd, bool) {
	row := m.sessions.selected
	loaded := m.sessions.transcript
	m = m.endRun("")
	m = m.resetSession()
	m = m.bindSessionID(row.ID)
	m.sessionTitle = row.Title
	m.sessionState = row.State
	m.sessionCreatedAt = row.CreatedAt
	m.sessionModifiedAt = row.ModifiedAt
	m.activeWorkspace = row.Workspace
	m.conv = loaded
	m.restartedThisRun = true
	m.sessions = sessionsState{}
	m.browsingStartupSessions = false
	m.phase = phaseIdle
	m.stuck = true
	m.statusMsg = "continuing chat " + sanitizeTerminal(row.Title) + " — type to add a turn"
	cmd := m.ta.Focus()
	m.refreshView()
	if m.deps.Session != nil {
		cmd = tea.Batch(cmd, client.RefreshResolvedModelCmd(m.deps.Ctx, m.deps.Session, row.ID))
	}
	cmd = tea.Batch(cmd, (&m).armLiveFeed())
	return m, cmd, true
}

func (m Model) closeSessionsTranscript() (tea.Model, tea.Cmd) {
	if m.sessions.replayStop != nil {
		m.sessions.replayStop()
	}
	m.sessions.selected = client.SessionListItem{}
	m.sessions.inspect = false
	m.sessions.loading = false
	m.sessions.loadErr = nil
	m.sessions.transcript = conversation{}
	m.sessions.view = sessionsPanel
	m.phase = phaseIdle
	m.statusMsg = ""
	m.stuck = true
	var cmd tea.Cmd
	if m.sessions.nextCursor != "" && m.deps.Sessions != nil {
		m = m.beginSessionPaginationAt(m.sessions.nextCursor)
		cmd = m.sessionPageCmd()
	}
	m.refreshView()
	return m, cmd
}

func (m Model) switchSessionsTab() Model {
	tabCount := sessionsTab(4)
	if (m.caps.StorageHealth && m.deps.StorageHealth != nil) || (m.caps.StorageMigration && m.deps.Migration != nil) || (m.caps.StorageCleanup && m.deps.Cleanup != nil) {
		tabCount = 5
	}
	m.sessions.tab = (m.sessions.tab + 1) % tabCount
	return m.syncSessionsFilter()
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

func renderSessionAdoptionReview(th theme.Theme, st sessionsState, hk helpKeys) string {
	source, binding := st.adoptionSource, st.adoptionBindings
	title := source.Title
	if title == "" {
		title = "untitled"
	}
	lines := []string{
		th.Style("askTitle").Render("Adopt as new chat"), "",
		"Source ID: " + safeSessionID(source.ID),
		"Source title: " + sanitizeTerminal(title),
		"This creates a new main chat; the source remains inspect-only.",
		"Target workspace: " + sanitizeTerminal(binding.Workspace),
		"Target environment: " + sanitizeTerminal(binding.EnvironmentKind) + " / " + sanitizeTerminal(binding.EnvironmentID),
		"Provider/model: " + sanitizeTerminal(binding.ProviderID) + " / " + sanitizeTerminal(binding.ModelID),
		"Future tool writes affect the target workspace and do not modify the legacy source.",
	}
	if st.adoptionErr != nil {
		lines = append(lines, "", th.Style("errorText").Render("Adoption failed: "+sanitizeTerminal(st.adoptionErr.Error())))
	}
	if st.actionLoading {
		lines = append(lines, "", th.Style("muted").Render("adopting and refetching authoritative chat…"))
	} else {
		lines = append(lines, "", th.Style("muted").Render(hk.choose+": create new chat  "+hk.closeOnly+": cancel"))
	}
	return strings.Join(lines, "\n")
}

func renderSessionsPanelState(th theme.Theme, st sessionsState, hk helpKeys) (string, bool) {
	switch {
	case st.adoptionReview:
		return renderSessionAdoptionReview(th, st, hk), true
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
			line += "  #" + handle
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

func adoptionReasonText(reason client.CapabilityReason) string {
	switch reason {
	case client.CapabilityReasonProtectedProvenance:
		return "reserved legacy provenance cannot be adopted"
	case client.CapabilityReasonInvalidTranscript:
		return "the authoritative transcript cannot be adopted"
	case client.CapabilityReasonAdoptionActive:
		return "the source is active"
	case client.CapabilityReasonAwaitingApproval:
		return "the source is awaiting approval"
	case client.CapabilityReasonAdoptionLeased:
		return "the source is leased by another process"
	case client.CapabilityReasonBindingUnresolved:
		return "the selected target binding cannot be resolved"
	case client.CapabilityReasonNotLegacy:
		return "the source is not a legacy session"
	default:
		return "the server did not advertise adoption eligibility"
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
	if selected.Kind == client.SessionKindUnknown {
		switch {
		case st.actionID == selected.ID && st.adoptionPreflight.Eligible:
			actions = append(actions, "a: adopt as chat")
		case st.actionID == selected.ID && st.adoptionReason != "":
			actions = append(actions, "adoption disabled: "+adoptionReasonText(st.adoptionReason))
		case st.adoptionErr != nil:
			actions = append(actions, "adoption unavailable: "+sanitizeTerminal(st.adoptionErr.Error()))
		default:
			actions = append(actions, "adoption unavailable: select an explicit workspace, environment, provider, and model")
		}
	}
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
