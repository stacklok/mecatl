package learning

// ActivityKind is the closed, content-free learning telemetry vocabulary.
type ActivityKind string

// Activity kinds cover admission, reflection outcomes, transitions, and reservation counts.
const (
	ActivityAdmitted                ActivityKind = "admitted"
	ActivitySkipped                 ActivityKind = "skipped"
	ActivityRateLimited             ActivityKind = "rate_limited"
	ActivityDuplicate               ActivityKind = "duplicate"
	ActivityQueueFull               ActivityKind = "queue_full"
	ActivityClosed                  ActivityKind = "closed"
	ActivityAbstained               ActivityKind = "abstained"
	ActivityStaged                  ActivityKind = "staged"
	ActivityPromoted                ActivityKind = "promoted"
	ActivityConflicted              ActivityKind = "conflicted"
	ActivityFailed                  ActivityKind = "failed"
	ActivityTimedOut                ActivityKind = "timed_out"
	ActivityReservedTokens          ActivityKind = "reserved_tokens"
	ActivitySkillActivatedValidated ActivityKind = "skill_activated_validated"
	ActivitySkillActivatedEvaluated ActivityKind = "skill_activated_evaluated"
	ActivitySkillStaged             ActivityKind = "skill_staged"
	ActivitySkillRejected           ActivityKind = "skill_rejected"
)

// Valid reports whether k belongs to the closed activity vocabulary.
func (k ActivityKind) Valid() bool {
	switch k {
	case ActivityAdmitted, ActivitySkipped, ActivityRateLimited, ActivityDuplicate,
		ActivityQueueFull, ActivityClosed, ActivityAbstained, ActivityStaged,
		ActivityPromoted, ActivityConflicted, ActivityFailed, ActivityTimedOut,
		ActivityReservedTokens, ActivitySkillActivatedValidated,
		ActivitySkillActivatedEvaluated, ActivitySkillStaged, ActivitySkillRejected:
		return true
	default:
		return false
	}
}

// Activity carries only closed labels and a count. It deliberately has no
// identity, path, digest, project, principal, session, or model-authored text.
type Activity struct {
	Kind        ActivityKind
	Reason      AdmissionReason
	Sensitivity Sensitivity
	Count       int64
}
