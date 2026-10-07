package mcpbroker

import (
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func TestSessionAPISlotDigest_ExactOuterCallCompatibility(t *testing.T) {
	for _, call := range []c.Call{
		{ID: "same", Name: "remote", Arguments: []byte(`{"x":1}`)},
		{ID: "same", Name: "CallMcpWithQuery", Arguments: []byte(`{"tool":"remote","arguments":{"x":1},"query":".x"}`)},
		{ID: "same", Name: "CallMcpWithQuery", Arguments: []byte(` {"tool":"remote","arguments":{"x":1},"query":".x"}`)},
	} {
		encoded, err := json.Marshal(call)
		if err != nil {
			t.Fatal(err)
		}
		want := sha256.Sum256(encoded)
		got := session.BrokerCallDigest(session.ToolCall{ID: call.ID, Name: call.Name, Args: call.Arguments})
		if got != want || callDigest(call) != want {
			t.Fatalf("outer digest changed for %s", call.Name)
		}
	}
}
