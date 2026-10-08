package executionenv

import "testing"

func TestExecutorStdinDecodeStrictRejectsUnknownTrailingAndOversize(t *testing.T) {
	var req ExecutorRequest
	for _, body := range []string{`{"operation":"file.read","extra":true}`, `{"operation":"file.read"}{}`, `{"path":"` + string(make([]byte, MaxJSONBody)) + `"}`} {
		if err := DecodeStrict([]byte(body), &req); err == nil {
			t.Fatalf("accepted invalid body")
		}
	}
}
