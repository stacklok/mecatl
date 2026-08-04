package openaichat

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecodeSSE feeds arbitrary bytes through the SDK SSE decoder + translate,
// the path that parses untrusted provider stream bytes. It must never panic and
// must be deterministic: the same bytes twice yield the same chunk count and the
// same error presence. Seeded from the recorded fixtures plus hostile edge cases,
// including OpenCode Go's non-standard cost frame and a frame after [DONE].
func FuzzDecodeSSE(f *testing.F) {
	matches, err := filepath.Glob(filepath.Join("testdata", "*.sse"))
	if err != nil {
		f.Fatalf("glob fixtures: %v", err)
	}
	for _, m := range matches {
		data, rerr := os.ReadFile(m)
		if rerr != nil {
			f.Fatalf("read fixture %s: %v", m, rerr)
		}
		f.Add(data)
	}
	f.Add([]byte(""))
	f.Add([]byte("data:"))
	f.Add([]byte("data: [DONE]"))
	f.Add([]byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`))
	f.Add([]byte(`data: {"choices":[],"x-opencode-type":"inference-cost","cost":"0"}`))
	f.Add([]byte(`data: {"choices":[{"finish_reason":"tool_calls","delta":{"tool_calls":[{"index":0}]}}]}`))
	f.Add([]byte("data: {not json"))
	f.Add([]byte("data: \x00\x00\x00"))
	f.Add(bytes.Repeat([]byte("data: {}\n"), 1000))

	f.Fuzz(func(t *testing.T, data []byte) {
		chunks, derr := decodeSSE(bytes.NewReader(data))
		chunks2, derr2 := decodeSSE(bytes.NewReader(data))
		if (derr == nil) != (derr2 == nil) {
			t.Fatalf("decodeSSE non-deterministic error presence: %v vs %v (input %q)", derr, derr2, data)
		}
		if len(chunks) != len(chunks2) {
			t.Fatalf("decodeSSE non-deterministic chunk count: %d vs %d (input %q)", len(chunks), len(chunks2), data)
		}
	})
}
