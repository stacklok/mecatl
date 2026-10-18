package anthropic

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func FuzzUnpackReplay(f *testing.F) {
	f.Add("")
	f.Add("{}")
	f.Add(`{"v":1,"blocks":[{"t":"thinking","x":"a","s":"b"}]}`)
	f.Add(`{"v":1,"blocks":[{"t":"redacted","d":"x"}]}`)
	f.Add("not json at all")
	f.Add(`{"v":1,"blocks":null}`)
	f.Fuzz(func(t *testing.T, blob string) {
		blocks, err := unpackReplay(&session.ProviderReplay{Format: anthropicReplayFormat, Version: anthropicReplayVersion, Data: []byte(blob)})
		if err != nil && len(blocks) != 0 {
			t.Fatal("invalid replay returned partial blocks")
		}
		if len(blocks) > 0 {
			packed := packReplay(blocks)
			repacked := packReplay(replayBlocks(t, packed))
			if !bytes.Equal(packed.Data, repacked.Data) {
				t.Fatal("re-pack/unpack changed replay bytes")
			}
		}
	})
}
