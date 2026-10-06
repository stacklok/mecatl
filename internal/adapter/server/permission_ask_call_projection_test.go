package server

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/stacklok/mecatl/engine/session"
)

func TestPermissionAskCallIDProjectionUsesDurableCallOnly(t *testing.T) {
	for _, tc := range []struct {
		name string
		call session.ToolCallID
		want string
	}{
		{name: "correlated", call: "call-7", want: "call-7"},
		{name: "unattached", call: "", want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ask := session.PendingAsk{AskID: "ask-call-7", Call: tc.call, Tool: "Write"}
			pb := toProto(session.Event{Type: session.EvPermissionAsk, Ask: &ask})
			if got := pb.GetAsk().GetCallId(); got != tc.want {
				t.Fatalf("call_id = %q, want %q", got, tc.want)
			}
			wire, err := protojson.Marshal(pb)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" && containsJSONKey(wire, "callId") {
				t.Fatalf("empty call ID was projected: %s", wire)
			}
		})
	}
}

func containsJSONKey(wire []byte, key string) bool {
	return string(wire) != "" && bytes.Contains(wire, []byte(`"`+key+`"`))
}
