package learning_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/session"
)

func TestMode(t *testing.T) {
	if got := learning.Mode(0); got != learning.Off || got.String() != "off" {
		t.Fatalf("zero mode = %v (%q), want off", got, got.String())
	}
	for _, tc := range []struct {
		text string
		want learning.Mode
	}{{"off", learning.Off}, {"review", learning.Review}, {"auto", learning.Auto}} {
		got, err := learning.ParseMode(tc.text)
		if err != nil || got != tc.want || got.String() != tc.text {
			t.Errorf("ParseMode(%q) = %v, %v; String=%q", tc.text, got, err, got.String())
		}
	}
	for _, invalid := range []string{"", "OFF", " auto", "automatic"} {
		if _, err := learning.ParseMode(invalid); err == nil {
			t.Errorf("ParseMode(%q) succeeded", invalid)
		}
	}
	if learning.Off.Next() != learning.Review || learning.Review.Next() != learning.Auto || learning.Auto.Next() != learning.Off {
		t.Fatal("mode selection cycle is not off → review → auto → off")
	}
}

func TestNewTrajectoryDeeplyOwnsMessages(t *testing.T) {
	source := []session.Message{
		session.NewAssistantMessage("assistant", "reasoning", []session.ToolCall{
			session.NewToolCall("call", "Tool", json.RawMessage(`{"key":"value"}`)),
		}),
		session.NewUserMessageWithParts("user", []session.Content{{Data: []byte("user-data"), Audience: []string{"user-audience"}}}),
		session.NewToolMessage(session.NewToolResultWithParts("call", "result", []session.Content{{Data: []byte("result-data"), Audience: []string{"result-audience"}}})),
	}
	expected := learning.NewTrajectory("expected", "/ws", session.StopEndTurn, session.Usage{}, source).Messages
	trajectory := learning.NewTrajectory("s", "/ws", session.StopEndTurn, session.Usage{}, source)

	trajectory.Messages[0].ToolCalls[0].Args[2] = 'X'
	trajectory.Messages[0].ToolCalls = append(trajectory.Messages[0].ToolCalls, session.ToolCall{})
	trajectory.Messages[1].Parts[0].Data[0] = 'X'
	trajectory.Messages[1].Parts[0].Audience[0] = "changed"
	trajectory.Messages[1].Parts = append(trajectory.Messages[1].Parts, session.Content{})
	trajectory.Messages[2].ToolResult.Content = "changed"
	trajectory.Messages[2].ToolResult.Parts[0].Data[0] = 'X'
	trajectory.Messages[2].ToolResult.Parts[0].Audience[0] = "changed"
	trajectory.Messages[2].ToolResult.Parts = append(trajectory.Messages[2].ToolResult.Parts, session.Content{})

	if !reflect.DeepEqual(source, expected) {
		t.Fatalf("source messages mutated through trajectory:\n got: %#v\nwant: %#v", source, expected)
	}
}

// compileObserver is deliberately engine-only: it proves an external consumer can
// implement and invoke the public seam without importing the host module.
type compileObserver struct{ called bool }

func (o *compileObserver) Observe(_ context.Context, tr learning.Trajectory) error {
	o.called = tr.SessionID == "s"
	return nil
}

func TestExternalConsumerCanInvokeObserver(t *testing.T) {
	var observer learning.Observer = &compileObserver{}
	if err := observer.Observe(context.Background(), learning.Trajectory{SessionID: session.SessionID("s")}); err != nil {
		t.Fatal(err)
	}
	if !observer.(*compileObserver).called {
		t.Fatal("observer was not invoked")
	}
}
