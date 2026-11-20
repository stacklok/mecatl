package id_test

import (
	"strings"
	"testing"

	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
)

func TestSessionID(t *testing.T) {
	v := sessionid.NewSessionID()
	parsed, err := sessionid.ParseSessionID(v.String())
	if err != nil || parsed != v {
		t.Fatalf("round trip: got %v, %v; want %v", parsed, err, v)
	}
	if got := map[sessionid.SessionID]bool{v: true}; !got[parsed] {
		t.Fatal("equal Session IDs must be interchangeable map keys")
	}
}

func TestParseSessionID(t *testing.T) {
	text := "SES" + strings.Repeat("z", 22)
	v, err := sessionid.ParseSessionID(text)
	if err != nil || v.String() != text {
		t.Fatalf("ParseSessionID(%q) = %v, %v", text, v, err)
	}
	for _, text := range []string{
		"",
		"RUN" + strings.Repeat("1", 22),
		"HST" + strings.Repeat("1", 22),
		"FOO" + strings.Repeat("1", 22),
		"SES" + strings.Repeat("0", 22),
		"SES" + strings.Repeat("1", 21),
		"SES" + strings.Repeat("1", 23),
		" " + text,
		text + "\n",
		strings.ToLower(text),
	} {
		v, err := sessionid.ParseSessionID(text)
		if err == nil || v != (sessionid.SessionID{}) {
			t.Errorf("ParseSessionID(%q) = %v, %v; want zero ID and error", text, v, err)
		}
	}
	var zero sessionid.SessionID
	if zero.String() != "" {
		t.Fatalf("zero SessionID = %q", zero.String())
	}
}
