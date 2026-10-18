package session_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestHistoricalSnapshotResultProjection(t *testing.T) {
	m := session.Message{Text: "visible", Legacy: &session.LegacyMessageIdentity{Incarnation: session.LegacyIncarnationID("s", 0, nil), Namespace: "sessnap/legacy-v1", Ordinal: 4, Unsupported: []byte(`{"role":"tool","tool_result":{"CallID":"private-provider","Content":"visible","Parts":[{"BlockKind":"image","Kind":"image","MIMEType":"image/png","Data":"AQI="}]},"reasoning":"opaque-private"}`)}}
	h := session.HistoricalMessage(m)
	if h == nil || h.Text != "visible" || len(h.Parts) != 1 || h.RecordID == "" || !h.AttachmentUnavailable {
		t.Fatal(h)
	}
	raw, err := json.Marshal(h)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("private-provider")) || bytes.Contains(raw, []byte("opaque-private")) {
		t.Fatal("private data projected")
	}
	h.Parts[0].Data[0] = 9
	if session.HistoricalMessage(m).Parts[0].Data[0] != 1 {
		t.Fatal("projection aliased carrier bytes")
	}
}
