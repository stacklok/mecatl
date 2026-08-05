package anthropic

import "testing"

// FuzzUnpackReasoning asserts unpackReasoning never panics and always fails soft
// over arbitrary input (a corrupted/forged Message.Reasoning blob from a stored
// session must never crash the adapter).
func FuzzUnpackReasoning(f *testing.F) {
	f.Add("")
	f.Add("{}")
	f.Add(`{"v":1,"blocks":[{"t":"thinking","x":"a","s":"b"}]}`)
	f.Add(`{"v":1,"blocks":[{"t":"redacted","d":"x"}]}`)
	f.Add("not json at all")
	f.Add(`{"v":1,"blocks":null}`)
	f.Fuzz(func(t *testing.T, blob string) {
		blocks := unpackReasoning(blob)
		// Re-packing what we unpacked must round-trip (idempotence of the valid subset).
		if len(blocks) > 0 {
			if got := unpackReasoning(packReasoning(blocks)); len(got) != len(blocks) {
				t.Fatalf("re-pack/unpack changed block count: %d -> %d", len(blocks), len(got))
			}
		}
	})
}
