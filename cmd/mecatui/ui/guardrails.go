package ui

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/internal/terminaltext"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
)

func (m Model) runGuardrails() (tea.Model, tea.Cmd) {
	if m.deps.Guardrails == nil || m.sessionID == "" {
		return m, nil
	}
	m.guardrailStatusRequest++
	return m, client.ListGuardrailCoverageCmd(m.deps.Ctx, m.deps.Guardrails, m.sessionID, m.guardrailStatusRequest, false)
}

type guardrailPresentation struct {
	blockID          scrollback.BlockID
	hookID           scrollback.HookID
	readHook         func(scrollback.HookID) (scrollback.HookSnapshot, bool)
	needsFinalDetail bool
	approvalResolved bool
	requestID        uint64
}

func (r *guardrailPresentation) snapshot() scrollback.HookSnapshot {
	if r == nil || r.readHook == nil {
		return scrollback.HookSnapshot{}
	}
	hook, _ := r.readHook(r.hookID)
	return hook
}

func (r *guardrailPresentation) revise(c *conversation, change func(*scrollback.HookSnapshot)) {
	hook, ok := c.scrollback.Hook(r.hookID)
	if !ok {
		return
	}
	change(&hook)
	c.scrollback.ReviseHook(r.hookID, hook)
}

type guardrailDetailState struct {
	requestID uint64
}

func (d *guardrailDetailState) applyDetail(msg client.GuardrailReviewDetailMsg, sessionID, reviewID, origin, call string) (scrollback.HookLiveDetail, bool) {
	if msg.SessionID != sessionID || msg.ReviewID != reviewID || d.requestID == 0 || d.requestID != msg.RequestID {
		return scrollback.HookLiveDetail{}, false
	}
	d.requestID = 0
	if msg.Err != nil {
		return scrollback.HookLiveDetail{State: scrollback.HookDetailUnavailable}, true
	}
	if msg.Detail.ReviewID != msg.ReviewID || (origin != "" && (msg.Detail.OriginSessionID != origin || msg.Detail.ToolCallID != call)) {
		return scrollback.HookLiveDetail{State: scrollback.HookDetailMismatched}, true
	}
	return scrollback.HookLiveDetail{Concern: terminaltext.Sanitize(msg.Detail.Concern), SourceDisplay: terminaltext.Sanitize(msg.Detail.SourceDisplay)}, true
}

// showBenignGuardrails reports whether known-benign review notices render while
// details are collapsed: by client setting, or always in debug mode.
func (d Deps) showBenignGuardrails() bool { return d.ShowBenignHookNotices || d.Debug }

// routineGuardrail reports the exact known-benign review combination. Missing
// identity or job metadata and every other combination fail visible.
func routineGuardrail(review *client.GuardrailReview) bool {
	return review != nil && review.ReviewID != "" &&
		(review.Job == "action" || review.Job == "inbound") &&
		review.Inspection == "complete" && review.Assessment == "acceptable" &&
		(review.Disposition == "execute" || review.Disposition == "release_result")
}

func (c *conversation) guardrailReview(id string) *guardrailPresentation {
	if id == "" {
		return &guardrailPresentation{needsFinalDetail: true}
	}
	if c.guardrailReviews == nil {
		c.guardrailReviews = make(map[string]*guardrailPresentation)
	}
	if c.guardrailReviews[id] == nil {
		hookID := c.scrollback.EnsureHookReview(id)
		c.guardrailReviews[id] = &guardrailPresentation{hookID: hookID, readHook: c.scrollback.Hook, needsFinalDetail: true}
	}
	return c.guardrailReviews[id]
}

func (c *conversation) guardrailReviewForAsk(id, parentID, origin, call string) *guardrailPresentation {
	if id != "" {
		if existing, ok := c.scrollback.Hook(scrollback.HookID(id)); ok && existing.SourceID == id && (origin != existing.OriginSessionID || call != existing.CallID) {
			return &guardrailPresentation{needsFinalDetail: true}
		}
	}
	if rootAsk(origin, parentID) && id != "" {
		if prior := c.guardrailReviews[id]; prior != nil {
			hook := prior.snapshot()
			if hook.SourceID == "" && hook.CallID == call {
				return prior
			}
		}
	}
	if origin != "" && call != "" && id != "" {
		return c.identifiedReviewForAsk(id, parentID, origin, call)
	}
	if rootAsk(origin, parentID) || id == "" {
		return c.guardrailReview(id)
	}
	if origin == "" {
		hookID := c.scrollback.RecordHook(scrollback.HookSnapshot{Review: &scrollback.HookReview{ReviewID: id}})
		return &guardrailPresentation{hookID: hookID, readHook: c.scrollback.Hook, needsFinalDetail: true}
	}
	key := origin + "\x00" + id
	if c.guardrailReviews == nil {
		c.guardrailReviews = make(map[string]*guardrailPresentation)
	}
	if c.guardrailReviews[key] == nil {
		hookID := c.scrollback.RecordHook(scrollback.HookSnapshot{Review: &scrollback.HookReview{ReviewID: id}})
		c.guardrailReviews[key] = &guardrailPresentation{hookID: hookID, readHook: c.scrollback.Hook, needsFinalDetail: true}
	}
	return c.guardrailReviews[key]
}

func (c *conversation) identifiedReviewForAsk(id, parentID, origin, call string) *guardrailPresentation {
	if c.guardrailReviews == nil {
		c.guardrailReviews = make(map[string]*guardrailPresentation)
	}
	hookID, ok := c.scrollback.EnsureHookRecord(id, origin, call)
	if !ok {
		return &guardrailPresentation{needsFinalDetail: true}
	}
	key := id
	if origin != parentID {
		key = origin + "\x00" + id
	}
	if current := c.guardrailReviews[key]; current == nil || current.hookID != hookID {
		c.guardrailReviews[key] = &guardrailPresentation{hookID: hookID, readHook: c.scrollback.Hook, needsFinalDetail: true}
	}
	return c.guardrailReviews[key]
}

// show retains every review notice; a known-benign review is classified so the
// renderer hides it unless details are expanded or benign notices are shown.
func (r *guardrailPresentation) show(c *conversation, text string) {
	benign := routineHookReview(r.snapshot().Review) && r.snapshot().Detail.State != scrollback.HookDetailMismatched
	if r.blockID == 0 {
		r.blockID = c.scrollback.Notices().AddGuardrailNotice(text, benign)
	} else {
		c.scrollback.Notices().UpdateGuardrailNotice(r.blockID, text, benign)
	}
}

func (r *guardrailPresentation) handoffToApproval(c *conversation) {
	c.scrollback.Notices().RemoveNotice(r.blockID)
	r.blockID = 0
	r.requestID = 0
	r.approvalResolved = false
}

func (r *guardrailPresentation) resolveApproval(c *conversation, text string, debug bool) string {
	r.requestID = 0 // The closed ask's token must not become a receipt token.
	r.approvalResolved = true
	hook := r.snapshot()
	r.needsFinalDetail = hook.Detail.Concern == "" && hook.Detail.SourceDisplay == ""
	if hook.Review != nil && hook.Review.Inspection != "" {
		text += ". " + hookReviewReason(hook.Review)
	}
	detailText := guardrailDetailText(r, debug)
	r.revise(c, func(h *scrollback.HookSnapshot) {
		h.Detail.Receipt = terminaltext.Sanitize(text)
		if h.Detail.State == "" {
			h.Detail.State = scrollback.HookDetailReceipt
		}
	})
	text += detailText
	r.show(c, text)
	return text
}

func (r *guardrailPresentation) beginDetailRequest(c *conversation, requestID uint64) {
	r.requestID = requestID
	r.needsFinalDetail = false
	r.revise(c, func(h *scrollback.HookSnapshot) {
		h.Detail.State = ""
		if h.Detail.Receipt != "" {
			h.Detail.State = scrollback.HookDetailReceipt
		}
	})
}

func (c *conversation) addHistoricalApproval(msg client.ApprovalMsg) {
	text := approvalNotice(msg)
	if msg.OriginSessionID != "" && msg.CallID != "" && msg.ReviewID != "" {
		if hook, ok := c.scrollback.Hook(scrollback.HookID(msg.ReviewID)); !ok {
			if c.pendingReceipts == nil {
				c.pendingReceipts = make(map[string][]client.ApprovalMsg)
			}
			c.pendingReceipts[msg.ReviewID] = append(c.pendingReceipts[msg.ReviewID], msg)
		} else {
			c.attachHistoricalReceipt(hook, msg)
		}
	}
	c.addNotice(text)
}

func (c *conversation) attachHistoricalReceipt(hook scrollback.HookSnapshot, msg client.ApprovalMsg) {
	if hook.SourceID != msg.ReviewID || hook.OriginSessionID != msg.OriginSessionID || hook.CallID != msg.CallID || hook.Review == nil || hook.Review.ReviewID != msg.ReviewID {
		return
	}
	hook.Detail.Receipt = terminaltext.Sanitize(approvalNotice(msg))
	if hook.Detail.State == "" {
		hook.Detail.State = scrollback.HookDetailReceipt
	}
	c.scrollback.ReviseHook(hook.ID, hook)
}

// Live and replay share the visibility policy. A live approval takes ownership
// of its review's explanation when the permission request arrives.
func (c *conversation) addGuardrailHook(msg client.HookMsg, debug bool) (*guardrailPresentation, scrollback.HookRecordOutcome) {
	hook := hookSnapshot(msg)
	outcome := c.scrollback.Tools().RecordHookEvent(hook)
	if outcome.Attachment == scrollback.HookPending {
		if !slices.Contains(c.pendingHookOrder, outcome.ID) {
			c.pendingHookOrder = append(c.pendingHookOrder, outcome.ID)
		}
	} else {
		c.pendingHookOrder = slices.DeleteFunc(c.pendingHookOrder, func(id scrollback.HookID) bool { return id == outcome.ID })
	}
	if hook.SourceID != "" && outcome.Status != scrollback.HookConflict && hook.Review != nil && hook.Review.ReviewID == hook.SourceID {
		for _, receipt := range c.pendingReceipts[hook.SourceID] {
			if recorded, ok := c.scrollback.Hook(outcome.ID); ok {
				c.attachHistoricalReceipt(recorded, receipt)
			}
		}
		delete(c.pendingReceipts, hook.SourceID)
	}
	if outcome.Status == scrollback.HookDuplicate {
		return nil, outcome
	}
	if outcome.Status == scrollback.HookConflict {
		r := &guardrailPresentation{hookID: outcome.ID, readHook: c.scrollback.Hook}
		text := "⚠ Conflicting guardrail hook: " + guardrailPresentationText(r, debug)
		if recorded, ok := c.scrollback.Hook(outcome.ID); ok && recorded.Text != "" {
			text += " " + recorded.Text
		}
		c.scrollback.Notices().AddGuardrailNotice(text, false)
		return nil, outcome
	}
	if msg.Guardrail == nil {
		recorded, _ := c.scrollback.Hook(outcome.ID)
		c.addHook(recorded.Text, recorded.Phase, recorded.Tool, recorded.Decision)
		return nil, outcome
	}
	r := c.presentationForHook(msg, outcome.ID)
	// A replayed pending-action hook must not undo an answer, and a routine final
	// outcome must not replace or hide the visible approval receipt.
	if r.approvalResolved && (msg.Guardrail.Disposition == "ask_action" || routineGuardrail(msg.Guardrail)) {
		return nil, outcome
	}
	r.show(c, guardrailPresentationText(r, debug))
	return r, outcome
}

func (c *conversation) presentationForHook(msg client.HookMsg, id scrollback.HookID) *guardrailPresentation {
	if msg.HookID == "" {
		r := c.guardrailReview(msg.Guardrail.ReviewID)
		if r.hookID == "" {
			r.hookID, r.readHook = id, c.scrollback.Hook
		}
		return r
	}
	if c.guardrailReviews == nil {
		c.guardrailReviews = make(map[string]*guardrailPresentation)
	}
	key := msg.HookID
	if msg.OriginSessionID != "" && msg.OriginSessionID != c.scrollback.OriginSession() {
		key = msg.OriginSessionID + "\x00" + msg.HookID
	}
	if r := c.guardrailReviews[key]; r != nil && r.hookID == id {
		return r
	}
	r := &guardrailPresentation{hookID: id, readHook: c.scrollback.Hook, needsFinalDetail: true}
	c.guardrailReviews[key] = r
	return r
}

func hookSnapshot(msg client.HookMsg) scrollback.HookSnapshot {
	hook := scrollback.HookSnapshot{SourceID: msg.HookID, Revision: msg.Revision, OriginSessionID: msg.OriginSessionID, CallID: msg.CallID, RunID: msg.RunID, Seq: msg.Seq, Phase: terminaltext.Sanitize(msg.Phase), Tool: terminaltext.Sanitize(msg.Tool), Decision: string(msg.Decision), Text: terminaltext.Sanitize(msg.Text)}
	if v := msg.Guardrail; v != nil {
		hook.Review = &scrollback.HookReview{
			ReviewID: v.ReviewID, Job: v.Job, Assessment: v.Assessment, Inspection: v.Inspection, Disposition: v.Disposition, ReasonCode: v.ReasonCode,
			RuleID: v.RuleID, RuleOrigin: v.RuleOrigin, CheckerProviderID: terminaltext.Sanitize(v.CheckerProviderID), CheckerModelID: terminaltext.Sanitize(v.CheckerModelID),
			ConcernRefs: append([]string(nil), v.ConcernRefs...), SourceRefs: append([]string(nil), v.SourceRefs...),
		}
	}
	return hook
}

func routineHookReview(review *scrollback.HookReview) bool {
	return review != nil && review.ReviewID != "" && (review.Job == "action" || review.Job == "inbound") && review.Inspection == "complete" && review.Assessment == "acceptable" && (review.Disposition == "execute" || review.Disposition == "release_result")
}

func guardrailPresentationText(r *guardrailPresentation, debug bool) string {
	hook := r.snapshot()
	text := hookText(hook)
	if routineHookReview(hook.Review) {
		text = "Guardrail check passed: " + hook.Tool + "."
	} else {
		text = "⚠ " + text
	}
	return text + guardrailDetailText(r, debug)
}

func guardrailDetailText(r *guardrailPresentation, debug bool) string {
	hook := r.snapshot()
	detail := hook.Detail
	text := ""
	if detail.State == scrollback.HookDetailMismatched {
		text += " Detailed explanation unavailable: response identity mismatch."
	} else if detail.State == scrollback.HookDetailUnavailable {
		text += " Detailed explanation unavailable or expired."
	} else if detail.Concern != "" {
		text += " " + detail.Concern
	}
	if detail.SourceDisplay != "" {
		text += " Source: " + detail.SourceDisplay
	}
	if debug && hook.Review != nil {
		review := hook.Review
		text += terminaltext.Sanitize(fmt.Sprintf(" [%s · %s · %s · %s · %s · checker %s/%s]", hook.Phase, review.Job, review.Inspection, review.Assessment, review.Disposition, review.CheckerProviderID, review.CheckerModelID))
	}
	return text
}

func hookReviewReason(review *scrollback.HookReview) string {
	why := "The review status is unknown; do not assume the check passed."
	switch {
	case review.Inspection == "operational_failure":
		why = "The safety check could not finish because of a checker outage; this is not a security finding."
	case review.Inspection != "complete":
	case review.Assessment == "prohibited":
		why = "The safety check reported a security finding."
	case review.Assessment == "unresolved":
		why = "The safety check could not determine whether this is safe; no specific security finding was established."
	case review.Assessment == "acceptable":
		why = "The safety check found no concern."
	}
	return why
}

func guardrailHookText(msg client.HookMsg) string { return hookText(hookSnapshot(msg)) }

func hookText(hook scrollback.HookSnapshot) string {
	review := hook.Review
	if review == nil {
		return hook.Text
	}
	outcome := "The outcome is unknown. Check /guardrails before continuing."
	switch review.Disposition {
	case "ask_action":
		outcome = "Action paused for your decision."
	case "withhold_result", "deny":
		outcome = "Result withheld from the model. The tool has already run; withholding its result does not undo its side effects."
		if review.Disposition == "deny" && review.Job != "inbound" {
			outcome = "Action stopped. Review the explanation before retrying."
		}
	case "execute":
		outcome = "Action allowed to continue. Review the warning before relying on its result."
	case "release_result":
		outcome = "Result released to the model. Review the warning before relying on it."
	case "pass_advisory", "continue_warning":
		outcome = "Work continued with a warning. Review the explanation before relying on the result."
	}
	return "Guardrail: " + hook.Tool + ". " + outcome + " " + hookReviewReason(review)
}

func (m *Model) applyGuardrailDetail(msg client.GuardrailReviewDetailMsg) {
	r := m.conv.guardrailReviews[msg.ReviewID]
	if r == nil {
		return
	}
	state := guardrailDetailState{requestID: r.requestID}
	detail, ok := state.applyDetail(msg, m.sessionID, msg.ReviewID, r.snapshot().OriginSessionID, r.snapshot().CallID)
	if !ok {
		return
	}
	r.requestID = 0
	r.revise(&m.conv, func(h *scrollback.HookSnapshot) {
		detail.Receipt = h.Detail.Receipt
		if detail.Receipt != "" && detail.State == "" {
			detail.State = scrollback.HookDetailReceipt
		}
		h.Detail = detail
	})
	r.show(&m.conv, guardrailPresentationText(r, m.deps.Debug))
}

func guardrailCoverageCurrent(msg client.GuardrailCoverageMsg, sessionID string, requestID uint64) bool {
	return msg.SessionID == sessionID && msg.RequestID == requestID
}

func guardrailPostureSummary(coverage client.GuardrailCoverage) string {
	if !coverage.Enabled {
		return "checker off (permission posture is independent); configure models.slots.guardrail or --guardrails-model to enable contextual inspection"
	}
	if len(coverage.Entries) == 0 {
		return fmt.Sprintf("checker configured but no effective rule coverage; use /guardrails and verify rule matches against the session tool catalog · %s/%s", terminaltext.Sanitize(coverage.CheckerProviderID), terminaltext.Sanitize(coverage.CheckerModelID))
	}
	blocking, advisory := 0, 0
	for _, entry := range coverage.Entries {
		if entry.Mode == "block" {
			blocking++
		} else {
			advisory++
		}
	}
	var mode string
	switch {
	case blocking > 0 && advisory > 0:
		mode = fmt.Sprintf("mixed: %d enforcing, %d advisory", blocking, advisory)
	case blocking > 0:
		mode = fmt.Sprintf("enforcing: %d", blocking)
	default:
		mode = fmt.Sprintf("advisory: %d", advisory)
	}
	return fmt.Sprintf("checker on (%s) · %s/%s", mode, terminaltext.Sanitize(coverage.CheckerProviderID), terminaltext.Sanitize(coverage.CheckerModelID))
}

func guardrailCoverageNotice(coverage client.GuardrailCoverage) string {
	if !coverage.Enabled {
		return "Guardrails: off for this session. No contextual action or inbound inspection is configured."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Guardrails: on · checker %s/%s\n", terminaltext.Sanitize(coverage.CheckerProviderID), terminaltext.Sanitize(coverage.CheckerModelID))
	if len(coverage.Entries) == 0 {
		b.WriteString("No effective rules apply to this session's assembled tool catalog. Check /guardrails and verify configured rule matches against the tool names and phases shown for this session.")
		return b.String()
	}
	for _, entry := range coverage.Entries {
		status := entry.Inspection
		if status == "operational_failure" {
			status = "checker outage (not an unsafe finding)"
		}
		if status == "" || status == "unknown" {
			status = "not yet checked"
		}
		fmt.Fprintf(&b, "• %s %s (%s): %s · %s · rule %s/%s", terminaltext.Sanitize(entry.Tool), terminaltext.Sanitize(entry.Phase), terminaltext.Sanitize(entry.Job), terminaltext.Sanitize(entry.Mode), terminaltext.Sanitize(status), terminaltext.Sanitize(entry.RuleOrigin), terminaltext.Sanitize(entry.RuleID))
		if entry.Reason != "" {
			fmt.Fprintf(&b, " — %s", terminaltext.Sanitize(entry.Reason))
		}
		b.WriteByte('\n')
	}
	return strings.TrimSpace(b.String())
}
