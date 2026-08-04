package openai

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecodeSSE feeds arbitrary bytes to the SSE decoder, which parses untrusted
// provider stream bytes. The decoder must never panic and must always terminate
// cleanly, returning either a slice of chunks or an error — never hang or
// allocate unbounded on a single bounded input.
//
// The corpus is seeded with the recorded golden fixtures in testdata/*.sse so
// the fuzzer starts from well-formed multi-event streams and mutates outward.
func FuzzDecodeSSE(f *testing.F) {
	// Seed from every recorded fixture.
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
	// A few hand-crafted hostile seeds: truncated JSON, no terminating blank
	// line, lone data prefix, NUL bytes, an oversized-ish single line.
	f.Add([]byte(""))
	f.Add([]byte("data:"))
	f.Add([]byte("data: [DONE]"))
	f.Add([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}"))
	f.Add([]byte("data: {not json"))
	f.Add([]byte("data: {\"type\":"))
	f.Add([]byte("event: foo\nevent: bar\n\n"))
	f.Add([]byte("data: \x00\x00\x00"))
	f.Add(bytes.Repeat([]byte("data: {}\n"), 1000))

	f.Fuzz(func(t *testing.T, data []byte) {
		// Primary contract: no panic and a clean return. decodeSSE returns
		// (chunks, err); either outcome is acceptable — if it panicked or hung the
		// test framework would catch it.
		chunks, derr := decodeSSE(bytes.NewReader(data))

		// Determinism invariant: decoding the same bytes a second time must
		// produce the same outcome (same chunk count and the same error
		// presence). A non-deterministic parser on untrusted input is a bug; this
		// also keeps a meaningful assertion on the test handle.
		chunks2, derr2 := decodeSSE(bytes.NewReader(data))
		if (derr == nil) != (derr2 == nil) {
			t.Fatalf("decodeSSE non-deterministic error presence: %v vs %v (input %q)", derr, derr2, data)
		}
		if len(chunks) != len(chunks2) {
			t.Fatalf("decodeSSE non-deterministic chunk count: %d vs %d (input %q)", len(chunks), len(chunks2), data)
		}
	})
}
