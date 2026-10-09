package scrollback

import (
	"reflect"
	"slices"
)

// HookID identifies a conversation-owned hook record, independently of card IDs.
type HookID uint64

// HookReview is durable machine review data; no live rationale belongs here.
type HookReview struct {
	ReviewID, Job, Assessment, Inspection, Disposition, ReasonCode string
	RuleID, RuleOrigin, CheckerProviderID, CheckerModelID          string
	ConcernRefs, SourceRefs                                        []string
}

// HookDetailState describes a live-only detail lookup or approval receipt. It
// must not be inferred from the durable review projection on replay.
type HookDetailState string

const (
	// HookDetailUnavailable means a live detail lookup was unavailable.
	HookDetailUnavailable HookDetailState = "unavailable"
	// HookDetailMismatched means a live detail lookup did not match its review.
	HookDetailMismatched HookDetailState = "mismatched"
	// HookDetailReceipt means Detail contains an approval receipt.
	HookDetailReceipt HookDetailState = "receipt"
)

// HookLiveDetail holds UI-sanitized live explanation and receipt text. None of
// these fields are durable review data; an empty State means no lookup outcome.
type HookLiveDetail struct {
	State                  HookDetailState
	Concern, SourceDisplay string
	Receipt                string
}

// HookSnapshot is a detached presentation-neutral hook record. Text and all
// live detail strings must be sanitized by the UI before recording them. CallID
// and RunID/Seq are exact source identities, not display labels. ID is allocated
// by the conversation and stable across revisions.
type HookSnapshot struct {
	ID       HookID
	CallID   string
	RunID    string
	Seq      int64
	Phase    string
	Tool     string
	Decision string
	Text     string
	Review   *HookReview
	Detail   HookLiveDetail
}

func cloneHook(in HookSnapshot) HookSnapshot {
	if in.Review != nil {
		review := *in.Review
		review.ConcernRefs = slices.Clone(review.ConcernRefs)
		review.SourceRefs = slices.Clone(review.SourceRefs)
		in.Review = &review
	}
	return in
}

func cloneHooks(in []HookSnapshot) []HookSnapshot {
	out := slices.Clone(in)
	for i := range out {
		out[i] = cloneHook(out[i])
	}
	return out
}

// EnsureHookReview allocates a canonical live record for an ask that precedes its
// hook event. It is not attached until source evidence supplies a call identity.
func (c *Conversation) EnsureHookReview(reviewID string) HookID {
	if reviewID == "" {
		return 0
	}
	if id := c.hookReviews[reviewID]; id != 0 {
		return id
	}
	id := c.RecordHook(HookSnapshot{Review: &HookReview{ReviewID: reviewID}})
	if c.hookReviews == nil {
		c.hookReviews = make(map[string]HookID)
	}
	c.hookReviews[reviewID] = id
	return id
}

// RecordHook adds a detached authoritative record. Use ToolCards.RecordHookEvent
// for source deduplication, review upserts, and safe attachment.
func (c *Conversation) RecordHook(hook HookSnapshot) HookID {
	c.nextHookID++
	id := c.nextHookID
	hook.ID = id
	if c.hooks == nil {
		c.hooks = make(map[HookID]HookSnapshot)
	}
	c.hooks[id] = cloneHook(hook)
	return id
}

// Hook returns a detached record for use by a notice or other presentation.
func (c *Conversation) Hook(id HookID) (HookSnapshot, bool) {
	hook, ok := c.hooks[id]
	return cloneHook(hook), ok
}

// ReviseHook replaces a record and invalidates every attached card's snapshot.
// Source call/event/review identity cannot change through a live revision; source
// review updates must go through RecordHookEvent.
func (c *Conversation) ReviseHook(id HookID, hook HookSnapshot) bool {
	old, ok := c.hooks[id]
	if !ok || old.CallID != hook.CallID || old.RunID != hook.RunID || old.Seq != hook.Seq || reviewID(old) != reviewID(hook) {
		return false
	}
	return c.replaceHook(id, hook)
}

func (c *Conversation) replaceHook(id HookID, hook HookSnapshot) bool {
	old := c.hooks[id]
	hook.ID = id
	hook = cloneHook(hook)
	if reflect.DeepEqual(old, hook) {
		return true
	}
	c.hooks[id] = hook
	for i := range c.cards {
		if slices.Contains(c.cards[i].hookRefs, id) {
			c.cards[i].revision++
		}
	}
	return true
}

// AttachHook links an existing record to exactly one retained root tool-family
// card. It never rebinds a record, even if a later card reuses the call ID.
func (c *Conversation) AttachHook(callID string, id HookID) bool {
	hook, ok := c.hooks[id]
	if !ok || id == 0 || callID == "" || hook.CallID != callID {
		return false
	}
	index := -1
	for i := range c.cards {
		metadata, tool := toolCallMetadata(c.cards[i].id, c.cards[i].revision, c.cards[i].payload)
		if tool && metadata.CallID == callID {
			if index != -1 {
				return false
			}
			index = i
		}
	}
	if index == -1 {
		return false
	}
	if bound := c.hookBindings[id]; bound != 0 && bound != c.cards[index].id {
		return false
	}
	if slices.Contains(c.cards[index].hookRefs, id) {
		return true
	}
	if c.hookBindings == nil {
		c.hookBindings = make(map[HookID]BlockID)
	}
	c.hookBindings[id] = c.cards[index].id
	c.cards[index].hookRefs = append(c.cards[index].hookRefs, id)
	c.cards[index].revision++
	return true
}

func reviewID(hook HookSnapshot) string {
	if hook.Review != nil {
		return hook.Review.ReviewID
	}
	return ""
}

type hookEventKey struct {
	run string
	seq int64
}

type hookEvent struct {
	id      HookID
	payload HookSnapshot // original source, unaffected by later review or live-detail revisions
}

// HookRecordStatus distinguishes a new record, review update, identical source
// replay, and contradictory evidence. Conflicts have their own standalone ID.
type HookRecordStatus uint8

const (
	// HookRecorded means the source evidence created a record.
	HookRecorded HookRecordStatus = iota
	// HookUpdated means the source evidence updated its canonical review.
	HookUpdated
	// HookDuplicate means the exact source evidence was already recorded.
	HookDuplicate
	// HookConflict means contradictory evidence was retained separately.
	HookConflict
)

// HookAttachment reports whether a record was attached, awaits a root call, or
// cannot be attached. A pending ID may be retried with AttachHook after a call.
type HookAttachment uint8

const (
	// HookPending means the hook awaits a unique root call.
	HookPending HookAttachment = iota
	// HookAttached means the hook is attached to a unique root call.
	HookAttached
	// HookRejected means no safe root attachment exists.
	HookRejected
)

// HookRecordOutcome keeps the canonical (or standalone conflict) ID available to
// the UI for notices, even when no root card can be safely associated.
type HookRecordOutcome struct {
	ID         HookID
	Status     HookRecordStatus
	Attachment HookAttachment
}

// RecordHookEvent records source evidence, upserts reviews by nonempty ReviewID,
// and attaches only to a unique retained root call. Source event identities are
// usable only with a nonempty RunID and positive Seq; identity-less generic events
// are separate records. Source payloads are retained to reject stale replays.
func (t ToolCards) RecordHookEvent(hook HookSnapshot) HookRecordOutcome {
	c := t.conversation
	hook.ID = 0
	hook = cloneHook(hook)
	key := hookEventKey{hook.RunID, hook.Seq}
	usable := hook.RunID != "" && hook.Seq > 0
	if usable {
		if prior, ok := c.hookEvents[key]; ok {
			if reflect.DeepEqual(prior.payload, hook) {
				return c.hookOutcome(prior.id, HookDuplicate)
			}
			return c.hookConflict(hook)
		}
	}
	if rid := reviewID(hook); rid != "" {
		if id := c.hookReviews[rid]; id != 0 {
			previous := c.hooks[id]
			if c.hasNewerHookSource(rid, hook.RunID, hook.Seq) ||
				(previous.RunID != "" && previous.Seq > 0 && !usable) ||
				(previous.CallID != "" && previous.CallID != hook.CallID) ||
				(hook.CallID != "" && c.hookCallCount(hook.CallID) > 1) {
				return c.hookConflict(hook)
			}
			updated := hook
			updated.Detail = previous.Detail
			c.replaceHook(id, updated)
			c.acceptHookEvent(key, usable, id, hook)
			return c.hookOutcome(id, HookUpdated)
		}
	}
	id := c.RecordHook(hook)
	if rid := reviewID(hook); rid != "" {
		if c.hookReviews == nil {
			c.hookReviews = make(map[string]HookID)
		}
		c.hookReviews[rid] = id
	}
	c.acceptHookEvent(key, usable, id, hook)
	return c.hookOutcome(id, HookRecorded)
}

func (c *Conversation) hasNewerHookSource(rid, runID string, seq int64) bool {
	if runID == "" || seq <= 0 {
		return false
	}
	for key, event := range c.hookEvents {
		if key.run == runID && key.seq > seq && rid == reviewID(event.payload) {
			return true
		}
	}
	return false
}

func (c *Conversation) acceptHookEvent(key hookEventKey, usable bool, id HookID, hook HookSnapshot) {
	if usable {
		if c.hookEvents == nil {
			c.hookEvents = make(map[hookEventKey]hookEvent)
		}
		c.hookEvents[key] = hookEvent{id, cloneHook(hook)}
	}
}

func (c *Conversation) hookConflict(hook HookSnapshot) HookRecordOutcome {
	if hook.RunID != "" && hook.Seq > 0 {
		key := hookEventKey{hook.RunID, hook.Seq}
		for _, prior := range c.hookConflicts[key] {
			if reflect.DeepEqual(prior.payload, hook) {
				return HookRecordOutcome{ID: prior.id, Status: HookDuplicate, Attachment: HookRejected}
			}
		}
		id := c.RecordHook(hook)
		if c.hookConflicts == nil {
			c.hookConflicts = make(map[hookEventKey][]hookEvent)
		}
		c.hookConflicts[key] = append(c.hookConflicts[key], hookEvent{id, cloneHook(hook)})
		return HookRecordOutcome{ID: id, Status: HookConflict, Attachment: HookRejected}
	}
	return HookRecordOutcome{ID: c.RecordHook(hook), Status: HookConflict, Attachment: HookRejected}
}

func (c *Conversation) hookOutcome(id HookID, status HookRecordStatus) HookRecordOutcome {
	hook := c.hooks[id]
	out := HookRecordOutcome{ID: id, Status: status, Attachment: HookRejected}
	if hook.CallID == "" {
		return out
	}
	if c.AttachHook(hook.CallID, id) {
		out.Attachment = HookAttached
	} else if c.hookBindings[id] == 0 && c.hookCallCount(hook.CallID) == 0 {
		out.Attachment = HookPending
	}
	return out
}

func (c *Conversation) hookCallCount(callID string) int {
	count := 0
	for i := range c.cards {
		metadata, tool := toolCallMetadata(c.cards[i].id, c.cards[i].revision, c.cards[i].payload)
		if tool && metadata.CallID == callID {
			count++
		}
	}
	return count
}

func (c *Conversation) attachedHooks(refs []HookID) []HookSnapshot {
	if len(refs) == 0 {
		return nil
	}
	hooks := make([]HookSnapshot, len(refs))
	for i, id := range refs {
		hooks[i] = cloneHook(c.hooks[id])
	}
	return hooks
}
