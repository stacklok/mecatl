package ui

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
	"github.com/stacklok/mecatl/cmd/mecatui/ui/internal/scrollback"
	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
)

type historicalTranscriptFixture struct {
	mecatlv1.UnimplementedHarnessServiceServer
	messages []*mecatlv1.ConversationMessage
}

func (f *historicalTranscriptFixture) GetSessionTranscript(context.Context, *mecatlv1.GetSessionTranscriptRequest) (*mecatlv1.GetSessionTranscriptResponse, error) {
	return &mecatlv1.GetSessionTranscriptResponse{SessionId: "history", Complete: true, Messages: f.messages}, nil
}

func TestHistoricalTranscriptToScrollback(t *testing.T) {
	h := &mecatlv1.HistoricalRecord{RecordId: "snapshot:4", Kind: "tool.result", Text: "orphan legacy output\nadditional readable text", AttachmentUnavailable: true,
		Parts: []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte("retained-media")}, {Kind: mecatlv1.Content_KIND_AUDIO, MimeType: "audio/wav", Url: "https://example.invalid/audio"}}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(server, &historicalTranscriptFixture{messages: []*mecatlv1.ConversationMessage{
		{Role: "assistant", ToolCalls: []*mecatlv1.ToolCall{{Id: "canonical", Name: "Read", Args: `{}`}}},
		{Role: "tool", Historical: h},
	}})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { server.Stop(); _ = listener.Close() })
	cl, err := client.Dial(client.DialConfig{Server: listener.Addr().String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	transcript, err := cl.GetSessionTranscript(t.Context(), "history")
	if err != nil {
		t.Fatal(err)
	}
	c := conversationFromTranscript(transcript.Messages)
	var notice scrollback.NoticeCardSnapshot
	tools := 0
	for _, card := range c.testBlocks() {
		switch p := card.Payload.(type) {
		case scrollback.ToolCardSnapshot:
			tools++
			if p.Call.ID != "canonical" || p.Resolved {
				t.Fatalf("history attached to a tool: %#v", p)
			}
		case scrollback.NoticeCardSnapshot:
			notice = p
		}
	}
	if tools != 1 || len(notice.Artifacts) != 2 || string(notice.Artifacts[0].Data) != "retained-media" || notice.Artifacts[1].URL != h.Parts[1].Url {
		t.Fatalf("standalone media lost: %#v", notice)
	}
	out := stripANSIstr(newTestRenderer().renderConversation(&c, false))
	for _, want := range []string{"orphan legacy output", "additional readable text", "[image: image/png]", "[audio: audio/wav]", "invocation association unavailable"} {
		if !strings.Contains(out, want) {
			t.Errorf("render missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "retained-media") || strings.Contains(out, "media unavailable") {
		t.Fatalf("untruthful/binary media display: %q", out)
	}
}

func TestHistoricalLiveAndReplayRemainStandalone(t *testing.T) {
	for _, kind := range []string{"tool.result", "hook", "approval", "subagent.tool", "parallel.branch", "team.member"} {
		t.Run(kind, func(t *testing.T) {
			m := newToolcallsInspectorModel(t)
			m.phase, m.liveGen = phaseRunning, 2
			m.conv.addTool("canonical", "Read", `{}`)
			var replay sessionsState
			replay.transcript.addTool("canonical", "Read", `{}`)
			ev := &mecatlv1.Event{Type: "historical", Historical: &mecatlv1.HistoricalRecord{RecordId: "log:7", Kind: kind, Text: "unattached safe content", AttachmentUnavailable: true,
				Parts: []*mecatlv1.Content{{Kind: mecatlv1.Content_KIND_IMAGE, MimeType: "image/png", Data: []byte{1, 2}}}}}
			msg := client.EventToMsg(ev)
			updated, _ := m.Update(liveMsg{gen: 2, msg: msg})
			m = updated.(Model)
			replay.applyReplayEvent(msg)
			if m.modal != nil || len(m.conv.subagentFleet) != 0 || m.conv.scrollback.Len() != 2 {
				t.Fatal("history created authority, a lane, or an ordinary tool card")
			}
			if !reflect.DeepEqual(m.conv.scrollback.SnapshotAt(1).Payload, replay.transcript.scrollback.SnapshotAt(1).Payload) {
				t.Fatal("live/replay historical projection differs")
			}
			if m.conv.scrollback.SnapshotAt(0).Payload.(scrollback.ToolCardSnapshot).Resolved {
				t.Fatal("history falsely resolved a current invocation")
			}
		})
	}
	var replay sessionsState
	h := &client.HistoricalRecord{Kind: "approval", Text: "old receipt", AttachmentUnavailable: false}
	replay.applyReplayEvent(client.CompactionArchiveMsg{Replaced: []client.ConversationMessage{{Historical: h}}})
	notice := replay.transcript.scrollback.SnapshotAt(1).Payload.(scrollback.NoticeCardSnapshot)
	if !strings.Contains(notice.Text, "old receipt") || strings.Contains(notice.Text, "unavailable") {
		t.Fatalf("archive history dropped or association status invented: %#v", notice)
	}
}
