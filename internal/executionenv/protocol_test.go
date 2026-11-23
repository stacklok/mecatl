package executionenv

import "testing"

func TestDecodeStrictRejectsUnknownTrailingAndOversize(t *testing.T) {
	var req struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
	}
	for _, body := range []string{
		`{"operation":"attach","extra":true}`,
		`{"operation":"attach"}{}`,
		`{"path":"` + string(make([]byte, MaxJSONBody)) + `"}`,
	} {
		if err := DecodeStrict([]byte(body), &req); err == nil {
			t.Fatalf("accepted invalid body")
		}
	}
}
