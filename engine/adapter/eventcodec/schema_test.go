package eventcodec_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/eventcodec"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSchemaDispatch(t *testing.T) {
	// Even a canonical-looking reference has no invocation authority in schema 1.
	const body = `"Type":"tool.call","RunID":"run","Seq":7,"ToolCall":{"ID":"call_current","Name":"Read","Args":{"z":1,"a":2}}`
	for _, version := range []struct {
		name, format, raw string
		legacy            bool
	}{
		{"legacy", "eventlog-json/1", `{` + body + `}`, true},
		{"current", eventcodec.Format, `{"identity_schema":2,` + body + `}`, false},
	} {
		t.Run(version.name, func(t *testing.T) {
			for _, entry := range []struct {
				name   string
				decode func([]byte) (session.Event, error)
			}{
				{"local", eventcodec.Unmarshal},
				{"format", func(data []byte) (session.Event, error) {
					return eventcodec.UnmarshalFormat(data, version.format)
				}},
			} {
				t.Run(entry.name, func(t *testing.T) {
					e, err := entry.decode([]byte(version.raw))
					if version.legacy {
						if !errors.Is(err, eventcodec.ErrSkipLegacyRecord) || !reflect.DeepEqual(e, session.Event{}) {
							t.Fatalf("legacy reference escaped: %+v, %v", e, err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if e.RunID != "run" || e.Seq != 7 {
						t.Fatal("source coordinates changed")
					}
					want := session.NewToolCall("call_current", "Read", json.RawMessage(`{"z":1,"a":2}`))
					if e.Type != session.EvToolCall || !reflect.DeepEqual(e.ToolCall, &want) {
						t.Fatalf("current call changed: %+v", e)
					}
					stored, err := eventcodec.Marshal(e)
					if err != nil {
						t.Fatal(err)
					}
					reloaded, err := eventcodec.UnmarshalFormat(stored, eventcodec.Format)
					if err != nil || !reflect.DeepEqual(e, reloaded) {
						t.Fatalf("current reload changed event: %v", err)
					}
				})
			}
		})
	}
}

func TestCurrentDecodingDoesNotUseLegacyDTOs(t *testing.T) {
	// item_id is a typed legacy field, but an unknown field in the current DTO.
	const body = `"Type":"tool.call","ToolCall":{"ID":"call_current","Name":"Read","item_id":17}`
	if _, err := eventcodec.UnmarshalFormat([]byte(`{`+body+`}`), "eventlog-json/1"); err == nil {
		t.Fatal("legacy decoder ignored malformed legacy field")
	}
	for _, decode := range []func([]byte) (session.Event, error){
		eventcodec.Unmarshal,
		func(data []byte) (session.Event, error) { return eventcodec.UnmarshalFormat(data, eventcodec.Format) },
	} {
		e, err := decode([]byte(`{"identity_schema":2,` + body + `}`))
		if err != nil || e.ToolCall == nil || e.ToolCall.ID != "call_current" {
			t.Fatalf("current decoder interpreted legacy field: %+v, %v", e, err)
		}
		if _, err := decode([]byte(`{"identity_schema":2,"ToolCall":{"ID":17}}`)); err == nil {
			t.Fatal("current decoder accepted malformed current field")
		}
	}
	_, err := eventcodec.UnmarshalFormat([]byte(`{"identity_schema":2,"ToolCall":{"ID":17}}`), "eventlog-json/1")
	if err == nil || !strings.Contains(err.Error(), "does not match identity schema") {
		t.Fatalf("outer/local mismatch did not reject before payload decoding: %v", err)
	}
}
