package eventcodec

import "testing"

func TestFormatIdentityConsistency(t *testing.T) {
	for _, tc := range []struct {
		format, data string
		valid        bool
	}{
		{"eventlog-json/1", `{"Type":"message.delta","Text":"old"}`, true},
		{Format, `{"identity_schema":2,"Type":"message.delta","Text":"new"}`, true},
		{"eventlog-json/1", `{"identity_schema":2}`, false},
		{Format, `{"Type":"message.delta"}`, false},
		{"eventlog-json/99", `{"identity_schema":2}`, false},
	} {
		t.Run(tc.format+tc.data, func(t *testing.T) {
			_, err := UnmarshalFormat([]byte(tc.data), tc.format)
			if (err == nil) != tc.valid {
				t.Fatalf("decode: %v", err)
			}
		})
	}
	for _, raw := range []string{`null`, `[]`, `{"identity_schema":null}`, `{"identity_schema":0}`, `{"identity_schema":1}`, `{"identity_schema":99}`, `{"identity_schema":"2"}`, `{"identity_schema":2,"IDENTITY_SCHEMA":2}`, `{"identity_schema":2,"identity_schema":2}`, `{"identity_schema":99,"identity_schema":2}`, `{"identity_schema":2} {}`} {
		if _, err := Unmarshal([]byte(raw)); err == nil {
			t.Errorf("accepted invalid local schema %s", raw)
		}
	}
}
