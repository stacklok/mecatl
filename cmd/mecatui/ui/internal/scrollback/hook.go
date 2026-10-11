package scrollback

import (
	"fmt"
	"reflect"
	"slices"
)

// HookID identifies a conversation-owned record. New records use the engine's opaque ID.
type HookID string

// HookReview is durable machine review data; no live rationale belongs here.
type HookReview struct {
	ReviewID, Job, Assessment, Inspection, Disposition, ReasonCode string
	RuleID, RuleOrigin, CheckerProviderID, CheckerModelID          string
	ConcernRefs, SourceRefs                                        []string
}

// HookDetailState describes a detail lookup or explicitly correlated receipt.
// Neither is inferred from the durable review projection alone.
type HookDetailState string

const (
	// HookDetailUnavailable means a live detail lookup was unavailable.
	HookDetailUnavailable HookDetailState = "unavailable"
	// HookDetailMismatched means a live detail lookup did not match its review.
	HookDetailMismatched HookDetailState = "mismatched"
	// HookDetailReceipt means Detail contains an approval receipt.
	HookDetailReceipt HookDetailState = "receipt"
)

// HookLiveDetail holds UI-sanitized explanation and receipt text. Explanations
// are live-only; historical receipts require an explicit origin/record/call match.
type HookLiveDetail struct {
	State                  HookDetailState
	Concern, SourceDisplay string
	Receipt                string
}

// HookSnapshot is a detached view of a conversation-owned hook record. Text and
// live detail strings must be sanitized by the UI before recording them. SourceID
// and Revision are engine-owned; RunID/Seq remain legacy source identities.
type HookSnapshot struct {
	ID              HookID
	SourceID        string
	Revision        uint64
	OriginSessionID string
	CallID          string
	RunID           string
	Seq             int64
	Phase           string
	Tool            string
	Decision        string
	Text            string
	Review          *HookReview
	Detail          HookLiveDetail
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

// SetOriginSession scopes new-format root-card attachment to this conversation.
func (c *Conversation) SetOriginSession(id string) { c.originSessionID = id }

// OriginSession returns the root session whose tool cards this document holds.
func (c *Conversation) OriginSession() string { return c.originSessionID }

// EnsureHookRecord makes a pre-hook review's engine identity available to live asks.
// A conflicting existing record is never repurposed for another origin or call.
func (c *Conversation) EnsureHookRecord(id, origin, call string) (HookID, bool) {
	if id == "" || origin == "" || call == "" {
		return "", false
	}
	key := HookID(id)
	if prior, ok := c.hooks[key]; ok {
		return key, prior.SourceID == id && prior.OriginSessionID == origin && prior.CallID == call && (reviewID(prior) == "" || reviewID(prior) == id)
	}
	if c.hooks == nil {
		c.hooks = make(map[HookID]HookSnapshot)
	}
	c.hooks[key] = HookSnapshot{ID: key, SourceID: id, OriginSessionID: origin, CallID: call, Review: &HookReview{ReviewID: id}}
	return key, true
}

// EnsureHookReview allocates a legacy live record for an ask that precedes its
// hook event. It is not attached until source evidence supplies a call identity.
func (c *Conversation) EnsureHookReview(reviewID string) HookID {
	if reviewID == "" {
		return ""
	}
	if id := c.hookReviews[reviewID]; id != "" {
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
	id := HookID(fmt.Sprintf("legacy_hook_%d", c.nextHookID))
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
	if !ok || old.SourceID != hook.SourceID || old.Revision != hook.Revision || old.OriginSessionID != hook.OriginSessionID || old.CallID != hook.CallID || old.RunID != hook.RunID || old.Seq != hook.Seq || reviewID(old) != reviewID(hook) {
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
	if !ok || id == "" || callID == "" || hook.CallID != callID || (hook.SourceID != "" && (hook.OriginSessionID == "" || hook.OriginSessionID != c.originSessionID)) {
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

// RecordHookEvent records engine-identified updates directly; only legacy events
// use source sequence and review lookup to find a record.
func (t ToolCards) RecordHookEvent(hook HookSnapshot) HookRecordOutcome {
	c := t.conversation
	hook.ID = ""
	hook = cloneHook(hook)
	if hook.SourceID != "" || hook.Revision != 0 {
		return c.recordIdentifiedHook(hook)
	}
	if rid := reviewID(hook); rid != "" && !c.acceptLegacyReviewSource(rid, hook) {
		return c.hookConflict(hook)
	}

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
		if id := c.hookReviews[rid]; id != "" {
			previous := c.hooks[id]
			if c.legacyReviewConflicts(rid, hook, previous, usable) {
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

func (c *Conversation) legacyReviewConflicts(rid string, hook, previous HookSnapshot, usable bool) bool {
	return c.hasNewerHookSource(rid, hook.RunID, hook.Seq) ||
		(previous.RunID != "" && previous.Seq > 0 && !usable) ||
		(previous.CallID != "" && previous.CallID != hook.CallID) ||
		(hook.CallID != "" && c.hookCallCount(hook.CallID) > 1)
}

func (c *Conversation) acceptLegacyReviewSource(rid string, hook HookSnapshot) bool {
	if id := c.hookReviews[rid]; id != "" && c.hooks[id].SourceID != "" {
		return false
	}
	if id := HookID(rid); c.hooks[id].SourceID != "" {
		prior := c.hooks[id]
		if prior.Revision != 0 || prior.OriginSessionID == "" || prior.OriginSessionID != c.originSessionID || prior.CallID == "" || prior.CallID != hook.CallID {
			return false
		}
		if c.hookReviews == nil {
			c.hookReviews = make(map[string]HookID)
		}
		c.hookReviews[rid] = id // legacy event proves the same root call, not a new-format machine update
		prior.SourceID = ""
		c.replaceHook(id, prior)
	}
	return true
}

func (c *Conversation) recordIdentifiedHook(hook HookSnapshot) HookRecordOutcome {
	id := HookID(hook.SourceID)
	key := fmt.Sprintf("%s\x00%d", hook.SourceID, hook.Revision)
	valid := hook.SourceID != "" && hook.Revision > 0 && hook.OriginSessionID != "" && (reviewID(hook) == "" || reviewID(hook) == hook.SourceID)
	if !valid {
		return c.identifiedHookConflict(key, hook)
	}
	if previous, ok := c.hooks[id]; ok {
		if previous.SourceID != hook.SourceID || previous.OriginSessionID != hook.OriginSessionID || previous.CallID != hook.CallID || reviewID(previous) != reviewID(hook) || previous.Revision > hook.Revision {
			return c.identifiedHookConflict(key, hook)
		}
		if previous.Revision == hook.Revision {
			machine := cloneHook(previous)
			machine.Detail = HookLiveDetail{}
			if reflect.DeepEqual(machine, identifiedPayload(id, hook)) {
				return c.hookOutcome(id, HookDuplicate)
			}
			return c.identifiedHookConflict(key, hook)
		}
		updated := identifiedPayload(id, hook)
		updated.Detail = previous.Detail
		c.replaceHook(id, updated)
		return c.hookOutcome(id, HookUpdated)
	}
	if c.hooks == nil {
		c.hooks = make(map[HookID]HookSnapshot)
	}
	c.hooks[id] = identifiedPayload(id, hook)
	return c.hookOutcome(id, HookRecorded)
}

func identifiedPayload(id HookID, hook HookSnapshot) HookSnapshot {
	hook.ID = id
	hook.Detail = HookLiveDetail{}
	return hook
}

func (c *Conversation) identifiedHookConflict(key string, hook HookSnapshot) HookRecordOutcome {
	for _, prior := range c.newHookConflicts[key] {
		if reflect.DeepEqual(prior.payload, hook) {
			return HookRecordOutcome{ID: prior.id, Status: HookDuplicate, Attachment: HookRejected}
		}
	}
	id := c.RecordHook(hook)
	if c.newHookConflicts == nil {
		c.newHookConflicts = make(map[string][]hookEvent)
	}
	c.newHookConflicts[key] = append(c.newHookConflicts[key], hookEvent{id: id, payload: cloneHook(hook)})
	return HookRecordOutcome{ID: id, Status: HookConflict, Attachment: HookRejected}
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
	if hook.CallID == "" || (hook.SourceID != "" && (hook.OriginSessionID == "" || hook.OriginSessionID != c.originSessionID)) {
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
