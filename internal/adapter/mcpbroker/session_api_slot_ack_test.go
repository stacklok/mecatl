package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
	"sync/atomic"
	"testing"
	"time"
)

type slotAckRedis struct {
	redis.UniversalClient
	phase        string
	hideReadback bool
}

func (r *slotAckRedis) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	result := r.UniversalClient.Set(ctx, key, value, ttl)
	var record apiRecord
	b, ok := value.([]byte)
	if result.Err() == nil && ok && json.Unmarshal(b, &record) == nil && record.Slots[0].Phase == r.phase {
		r.phase = ""
		r.hideReadback = true
		result = redis.NewStatusCmd(ctx)
		result.SetErr(errors.New("lost slot acknowledgement"))
	}
	return result
}
func (r *slotAckRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	if r.hideReadback {
		r.hideReadback = false
		cmd := redis.NewStringCmd(ctx)
		cmd.SetErr(errors.New("lost slot readback"))
		return cmd
	}
	return r.UniversalClient.Get(ctx, key)
}
func TestSessionAPISlots_LostDispatchAndTerminalAcknowledgement(t *testing.T) {
	for _, phase := range []string{"dispatched", "terminal"} {
		t.Run(phase, func(t *testing.T) {
			var calls atomic.Int32
			api, ctx, opened, cat := slotAPI(t, func(context.Context, *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
				calls.Add(1)
				return slotResult(), nil
			})
			api.redis = &slotAckRedis{UniversalClient: api.redis, phase: phase}
			a := c.BrokerAttempt{Sequence: 1}
			call := c.Call{ID: "same", Name: "mcp__slots__echo", Arguments: []byte(`{}`)}
			out, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, a)
			want := int32(0)
			if phase == "terminal" {
				want = 1
				if err != nil || out.Kind != c.InvocationOutcomeUnknown {
					t.Fatalf("terminal ambiguity: %#v %v", out, err)
				}
			} else if err == nil {
				t.Fatal("unverified dispatch ack accepted")
			}
			status, err := api.InspectAttempt(ctx, opened.Ref, a)
			if err != nil || !status.Valid() || status.Disposition != session.BrokerAttemptUnknown || calls.Load() != want {
				t.Fatalf("reconciled uncertain fence: %#v %v calls=%d", status, err, calls.Load())
			}
			if _, err := api.InvokeTool(ctx, opened.Ref, cat.Ref(), call, a); err != nil || calls.Load() != want {
				t.Fatalf("ambiguous replay: %v", err)
			}
		})
	}
}
