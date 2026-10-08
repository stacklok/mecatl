package ui

import (
	"fmt"
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
	hook             client.HookMsg
	needsFinalDetail bool
	approvalResolved bool
	guardrailDetailState
}

type guardrailDetailState struct {
	requestID   uint64
	detail      client.GuardrailReviewDetail
	unavailable bool
	// mismatched marks a response for the requested review that names another
	// review; it fails visible without displaying the mismatched text.
	mismatched bool
}

func (d *guardrailDetailState) applyDetail(msg client.GuardrailReviewDetailMsg, sessionID, reviewID string) bool {
	if msg.SessionID != sessionID || msg.ReviewID != reviewID || d.requestID == 0 || d.requestID != msg.RequestID {
		return false
	}
	d.requestID = 0
	d.mismatched = msg.Err == nil && msg.Detail.ReviewID != msg.ReviewID
	d.unavailable = msg.Err != nil || d.mismatched
	if !d.unavailable {
		d.detail = msg.Detail
	}
	return true
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
		c.guardrailReviews[id] = &guardrailPresentation{needsFinalDetail: true}
	}
	return c.guardrailReviews[id]
}

// show retains every review notice; a known-benign review is classified so the
// renderer hides it unless details are expanded or benign notices are shown.
func (r *guardrailPresentation) show(c *conversation, text string) {
	benign := routineGuardrail(r.hook.Guardrail) && !r.mismatched
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

func (r *guardrailPresentation) resolveApproval(c *conversation, detail guardrailDetailState, text string, debug bool) string {
	r.guardrailDetailState = detail
	r.requestID = 0 // The closed ask's token must not become a receipt token.
	r.approvalResolved = true
	// Only missing prompt detail permits one fresh request after a nonroutine final hook.
	r.needsFinalDetail = r.detail.Concern == "" && r.detail.SourceDisplay == ""
	if r.hook.Guardrail != nil {
		text += ". " + guardrailReviewReason(r.hook.Guardrail)
	}
	text += guardrailDetailText(r, debug)
	r.show(c, text)
	return text
}

func (r *guardrailPresentation) beginDetailRequest(requestID uint64) {
	r.requestID = requestID
	r.needsFinalDetail = false
	r.unavailable = false
	r.mismatched = false
}

// Live and replay share the visibility policy. A live approval takes ownership
// of its review's explanation when the permission request arrives.
func (c *conversation) addGuardrailHook(msg client.HookMsg, debug bool) *guardrailPresentation {
	if msg.Guardrail == nil {
		c.addHook(msg.Text, msg.Phase, msg.Tool, string(msg.Decision))
		return nil
	}
	r := c.guardrailReview(msg.Guardrail.ReviewID)
	// A replayed pending-action hook must not undo an answer, and a routine final
	// outcome must not replace or hide the visible approval receipt. Other final
	// outcomes, including checker outages after releasing a result, still update it.
	if r.approvalResolved && (msg.Guardrail.Disposition == "ask_action" || routineGuardrail(msg.Guardrail)) {
		return nil
	}
	r.hook = msg
	r.show(c, guardrailPresentationText(r, debug))
	return r
}

func guardrailPresentationText(r *guardrailPresentation, debug bool) string {
	text := guardrailHookText(r.hook)
	if routineGuardrail(r.hook.Guardrail) {
		text = "Guardrail check passed: " + terminaltext.Sanitize(r.hook.Tool) + "."
	} else {
		text = "⚠ " + text
	}
	return text + guardrailDetailText(r, debug)
}

func guardrailDetailText(r *guardrailPresentation, debug bool) string {
	text := ""
	if r.mismatched {
		text += " Detailed explanation unavailable: response identity mismatch."
	} else if r.unavailable {
		text += " Detailed explanation unavailable or expired."
	} else if r.detail.Concern != "" {
		text += " " + terminaltext.Sanitize(r.detail.Concern)
	}
	if r.detail.SourceDisplay != "" {
		text += " Source: " + terminaltext.Sanitize(r.detail.SourceDisplay)
	}
	if debug && r.hook.Guardrail != nil {
		review := r.hook.Guardrail
		text += terminaltext.Sanitize(fmt.Sprintf(" [%s · %s · %s · %s · %s · checker %s/%s]", r.hook.Phase, review.Job, review.Inspection, review.Assessment, review.Disposition, review.CheckerProviderID, review.CheckerModelID))
	}
	return text
}

func guardrailReviewReason(review *client.GuardrailReview) string {
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

func guardrailHookText(msg client.HookMsg) string {
	review := msg.Guardrail
	if review == nil {
		return msg.Text
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
	return "Guardrail: " + terminaltext.Sanitize(msg.Tool) + ". " + outcome + " " + guardrailReviewReason(review)
}

func (m *Model) applyGuardrailDetail(msg client.GuardrailReviewDetailMsg) {
	r := m.conv.guardrailReviews[msg.ReviewID]
	if r == nil || !r.applyDetail(msg, m.sessionID, msg.ReviewID) {
		return
	}
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
