package llmresilience

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
)

func TestReplayRemainsTentativeUntilSuccessfulCompletion(t *testing.T) {
	failed := &session.ProviderReplay{Format: "test", Version: 1, Data: []byte(`{"attempt":"failed"}`)}
	success := &session.ProviderReplay{Format: "test", Version: 1, Data: []byte(`{"attempt":"successful"}`)}
	usage := session.Usage{InputTokens: 7, OutputTokens: 3}
	for _, tc := range []struct {
		name    string
		visible bool
		cancel  bool
		idle    bool
	}{
		{name: "precommit retries"},
		{name: "visible failure discards", visible: true},
		{name: "precommit cancellation discards", cancel: true},
		{name: "visible cancellation discards", visible: true, cancel: true},
		{name: "idle-bounded visible failure discards", visible: true, idle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			chunks := []port.Chunk{{Kind: port.ChunkReplay, Replay: failed, ReplayIsOutput: true}}
			if tc.visible {
				chunks = append(chunks, port.Chunk{Kind: port.ChunkText, Text: "partial"}, port.Chunk{Kind: port.ChunkReplay, Replay: failed})
			}
			chunks = append(chunks, port.Chunk{Kind: port.ChunkUsage, Usage: &usage})
			first := step{chunks: chunks, midErr: apiErr(503)}
			if tc.cancel {
				first.midErr = nil
				first.onChunk = func(i int) {
					if i == len(chunks)-1 {
						cancel()
					}
				}
			}
			provider := &fakeProvider{steps: []step{first, {chunks: []port.Chunk{
				{Kind: port.ChunkReplay, Replay: success, ReplayIsOutput: true},
				{Kind: port.ChunkText, Text: "successful"},
				{Kind: port.ChunkDone, Stop: session.StopEndTurn},
			}}}}
			cfg := tinyBackoffCfg(2)
			if tc.idle {
				cfg.StreamIdleTimeout = time.Second
			}
			seq, err := Wrap(provider, cfg).Stream(ctx, port.LLMRequest{})
			var got []port.Chunk
			if err == nil {
				got, err = drain(t, seq)
			}
			wantSuccess := !tc.visible && !tc.cancel
			if tc.cancel {
				if ctx.Err() != context.Canceled || err != nil && !errors.Is(err, context.Canceled) {
					t.Fatalf("error = %v, want caller cancellation", err)
				}
			} else if (err == nil) != wantSuccess {
				t.Fatalf("error = %v, want success=%v", err, wantSuccess)
			}
			var total session.Usage
			var replays []*session.ProviderReplay
			for _, chunk := range got {
				total = total.Add(discardedChunkUsage(chunk))
				if chunk.Kind == port.ChunkReplay {
					replays = append(replays, chunk.Replay)
				}
			}
			if total != usage {
				t.Fatalf("usage = %+v, want %+v", total, usage)
			}
			if wantSuccess {
				if provider.Calls() != 2 || !reflect.DeepEqual(replays, []*session.ProviderReplay{success}) {
					t.Fatalf("attempts=%d replay=%+v, want successful retry only", provider.Calls(), replays)
				}
			} else if provider.Calls() != 1 || len(replays) != 0 {
				t.Fatalf("attempts=%d replay=%+v, rejected attempt leaked or retried", provider.Calls(), replays)
			}
		})
	}
}
