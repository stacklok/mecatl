package scrollback

import (
	"reflect"
	"testing"
)

func TestHookRecordAttachmentAndDetachment(t *testing.T) {
	var c Conversation
	c.Tools().Add(ToolCall{ID: "call", Name: "Read"})
	input := HookSnapshot{
		CallID: "call", RunID: "run", Seq: 42, Phase: "PreToolUse", Tool: "Read", Decision: "blocked", Text: "sanitized",
		Review: &HookReview{ReviewID: "review", Job: "action", ConcernRefs: []string{"c1"}, SourceRefs: []string{"s1"}},
		Detail: HookLiveDetail{State: HookDetailReceipt, Concern: "sanitized concern", SourceDisplay: "source", Receipt: "approved"},
	}
	id := c.RecordHook(input)
	if id == 0 || !c.AttachHook("call", id) || !c.AttachHook("call", id) {
		t.Fatal("record/attach hook")
	}
	input.Review.ConcernRefs[0] = "changed input"
	before := c.SnapshotAt(0)
	card := before.Payload.(ToolCardSnapshot)
	if len(card.Hooks) != 1 || card.Hooks[0].ID != id || card.Hooks[0].Review.ConcernRefs[0] != "c1" {
		t.Fatalf("attachment = %+v", card.Hooks)
	}
	card.Hooks[0].Review.ConcernRefs[0] = "changed snapshot"
	card.Hooks[0].Review.SourceRefs[0] = "changed snapshot"
	standalone, ok := c.Hook(id)
	if !ok || standalone.Review.ConcernRefs[0] != "c1" || standalone.Review.SourceRefs[0] != "s1" {
		t.Fatalf("record = %+v, %t", standalone, ok)
	}
	standalone.Review.ConcernRefs[0] = "changed record snapshot"
	if got := c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks[0].Review.ConcernRefs[0]; got != "c1" {
		t.Fatalf("snapshot leaked: %q", got)
	}
	updated, _ := c.Hook(id)
	updated.Detail.State = HookDetailUnavailable
	updated.Review.SourceRefs[0] = "revised source"
	if !c.ReviseHook(id, updated) || c.SnapshotAt(0).Revision != before.Revision+1 {
		t.Fatal("revision did not invalidate attached card")
	}
	if !c.ReviseHook(id, updated) || c.SnapshotAt(0).Revision != before.Revision+1 {
		t.Fatal("identical revision invalidated card")
	}
	if got := c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks[0].Detail.State; got != HookDetailUnavailable {
		t.Fatalf("attachment not derived from record: %q", got)
	}
	if c.AttachHook("missing", id) || c.AttachHook("call", id+1) || c.ReviseHook(id+1, updated) {
		t.Fatal("accepted unknown reference")
	}
	if _, ok := c.Hook(id + 1); ok {
		t.Fatal("unknown record found")
	}
	updated.Review.SourceRefs[0] = "changed after revision"
	if got := c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks[0].Review.SourceRefs[0]; got != "revised source" {
		t.Fatalf("revision input leaked: %q", got)
	}
	if !reflect.DeepEqual(c.SnapshotAt(0).Payload.(ToolCardSnapshot).Hooks[0].Review.SourceRefs, []string{"revised source"}) {
		t.Fatal("source refs lost")
	}
}

func TestHookAttachmentAcrossToolFamiliesAndResults(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start func(*Conversation) bool
	}{
		{"Read", nil},
		{"Subagent", func(c *Conversation) bool {
			return c.Subagents().Start("call", SubagentStart{Provider: "provider", Routing: RoutingDecision{Confidence: Float64(0.6)}})
		}},
		{"Team", func(c *Conversation) bool {
			return c.Teams().Start("call", TeamStart{TeamID: "team", Lanes: []TeamLane{{Provider: "provider", Routing: RoutingDecision{Confidence: Float64(0.6)}}}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c Conversation
			c.Tools().Add(ToolCall{ID: "call", Name: tc.name})
			outcome := c.Tools().RecordHookEvent(HookSnapshot{CallID: "call", RunID: "run", Seq: 7, Review: &HookReview{ReviewID: "r", ConcernRefs: []string{"original"}}})
			id := outcome.ID
			if outcome.Status != HookRecorded || outcome.Attachment != HookAttached {
				t.Fatal("attach")
			}
			check := func() {
				t.Helper()
				s, ok := c.SnapshotForCall("call")
				if !ok {
					t.Fatal("missing card")
				}
				var hooks []HookSnapshot
				switch p := s.Payload.(type) {
				case ToolCardSnapshot:
					hooks = p.Hooks
				case SubagentCardSnapshot:
					hooks = p.Hooks
					if p.Start.Provider != "provider" || *p.Start.Routing.Confidence != 0.6 {
						t.Fatal("lost subagent routing")
					}
				case TeamCardSnapshot:
					hooks = p.Hooks
					if p.Update.Lanes[0].Provider != "provider" || *p.Update.Lanes[0].Routing.Confidence != 0.6 {
						t.Fatal("lost team routing")
					}
				}
				if len(hooks) != 1 || hooks[0].ID != id || len(hooks[0].Review.ConcernRefs) != 1 || hooks[0].Review.ConcernRefs[0] != "original" {
					t.Fatalf("lost attachment: %+v", hooks)
				}
				hooks[0].Review.ConcernRefs[0] = "mutated"
			}
			check()
			if tc.start != nil && !tc.start(&c) {
				t.Fatal("specialize")
			}
			check()
			if !c.Tools().ResolveAvailable("call", ToolResult{Body: "pending"}) {
				t.Fatal("provisional")
			}
			check()
			if !c.Tools().Resolve("call", ToolResult{Body: "done"}) {
				t.Fatal("canonical")
			}
			check()
			metadata, ok := c.ToolCallMetadataAt(0)
			if !ok || metadata.CallID != "call" || !metadata.ResultReceived {
				t.Fatalf("metadata = %+v, %t", metadata, ok)
			}
		})
	}
}
