package eventsource_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/adapter/eventsource"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestLegacyMalformedSourceRejected(t *testing.T) {
	call := decode(t, oldCall).WithLegacySource("durable", "call")
	ask := decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"provider-secret"}}`)
	other := decode(t, `{"Type":"tool.call","ToolCall":{"ID":"other","Name":"Read"}}`).WithLegacySource("durable", "other")
	for name, events := range map[string][]session.Event{
		"duplicate outstanding":   {call, call.WithLegacySource("durable", "duplicate")},
		"missing call key":        {decode(t, `{"Type":"tool.call","ToolCall":{"Name":"Read"}}`).WithLegacySource("durable", "call")},
		"missing call payload":    {decode(t, `{"Type":"tool.call"}`)},
		"missing result key":      {call, decode(t, `{"Type":"tool.result","ToolResult":{"Content":"orphan"}}`)},
		"missing result payload":  {call, decode(t, `{"Type":"tool.result"}`)},
		"orphan result":           {decode(t, oldResult)},
		"different namespace":     {call, decode(t, oldResult).WithLegacySource("other-source", "result")},
		"different incarnation":   {call, decode(t, oldResult).WithLegacyIdentity(session.LegacyEventIdentity{Payload: []byte(oldResult), Incarnation: session.NewIncarnationID(), Namespace: "durable", Coordinate: "result", Ordinal: -1})},
		"unanswered":              {call},
		"ask crosses sources":     {call, ask},
		"verdict crosses sources": {call, ask.WithLegacySource("durable", "ask"), decode(t, `{"Type":"approval","Approval":{"AskID":"ask","Call":"provider-secret","Tool":"Read","Verdict":"allow_once"}}`).WithLegacySource("other", "approval")},
		"unproven ask":            {call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read","Call":"wrong"}}`).WithLegacySource("durable", "ask")},
		"empty ask reference":     {call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Read"}}`).WithLegacySource("durable", "ask")},
		"empty ask id":            {call, decode(t, `{"Type":"permission.ask","Ask":{"Tool":"Read","Call":"provider-secret"}}`).WithLegacySource("durable", "ask")},
		"wrong ask tool":          {call, decode(t, `{"Type":"permission.ask","Ask":{"AskID":"ask","Tool":"Write","Call":"provider-secret"}}`).WithLegacySource("durable", "ask")},
		"missing ask":             {call, decode(t, `{"Type":"permission.ask","Text":"unavailable"}`)},
		"pending siblings":        {call, other, ask.WithLegacySource("durable", "ask")},
		"duplicate asks":          {call, ask.WithLegacySource("durable", "ask"), ask.WithLegacySource("durable", "another-ask")},
		"completed ask reused":    {call, decode(t, oldResult).WithLegacySource("durable", "result"), ask.WithLegacySource("durable", "ask")},
		"authorization required":  {call, decode(t, `{"Type":"authorization.required","Authorization":{"AuthorizationID":"old","Call":"provider-secret","Status":"pending"}}`)},
		"authorization resolved":  {decode(t, `{"Type":"authorization.resolved","Authorization":{"AuthorizationID":"old","Call":"provider-secret","Status":"granted"}}`)},
		"missing authorization":   {decode(t, `{"Type":"authorization.required","Text":"unavailable"}`)},
		"unanchored archive":      {decode(t, `{"Type":"compaction.archive","CompactionArchive":{"Replaced":[{"Role":"assistant","ToolCalls":[{"ID":"old","Name":"Read"}]},{"Role":"tool","ToolResult":{"CallID":"old","Content":"done"}}]}}`)},
	} {
		t.Run(name, func(t *testing.T) {
			requireFoldError(t, events, eventsource.ErrReconstruct)
			p, err := eventsource.Project(events, session.LegacyIncarnationID("test", 0, nil))
			if !errors.Is(err, eventsource.ErrReconstruct) || p.Events != nil {
				t.Fatal("malformed source projected", p, err)
			}
		})
	}
}

func TestLegacyProjectedEvidencePreservesAuthorityBoundary(t *testing.T) {
	inc := session.LegacyIncarnationID("test", 0, nil)
	call := decode(t, oldCall).WithLegacySource("durable", "call")
	identity := call.LegacyIdentity()
	identity.Ordinal = 0
	call = call.WithLegacyIdentity(*identity)
	old := learning.Input{Trajectory: learning.Trajectory{SessionID: "test"}, Events: []learning.EvidenceEventData{{Type: session.EvToolCall, Seq: 90, ToolCall: &learning.ToolProjection{ID: "provider-secret", Name: "Read"}}}}
	ref, err := learning.EventEvidenceRef(old, 0, "provider-secret")
	if err != nil {
		t.Fatal(err)
	}
	p := project(t, []session.Event{call, decode(t, oldResult).WithLegacySource("durable", "result")})
	if _, err := fold(p.Events); err != nil {
		t.Fatal(err)
	}
	citation, err := learning.VerifyEvidenceSource(old.Trajectory, p.Events, inc, ref)
	if err != nil || citation.ToolCallID != p.Events[0].ToolCall.ID {
		t.Fatal("proven original lost attachment", err)
	}
	sparse, err := eventcodec.ProjectRecord(p.Events[0], inc, -1)
	if err != nil {
		t.Fatal(err)
	}
	citation, err = learning.VerifyEvidenceSource(old.Trajectory, []session.Event{sparse}, inc, ref)
	if err != nil || citation.ToolCallID != "" {
		t.Fatal("sparse evidence acquired attachment", err)
	}
	data, err := json.Marshal(p.Events[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("provider-secret")) {
		t.Fatal("provider reference leaked")
	}
	var display session.Event
	if err := json.Unmarshal(data, &display); err != nil {
		t.Fatal(err)
	}
	for _, unavailable := range [][]session.Event{nil, p.Events[1:], {display}} {
		if _, err := learning.VerifyEvidenceSource(old.Trajectory, unavailable, inc, ref); err == nil {
			t.Fatal("missing/pruned/display-only source verified")
		}
	}
}
