package format_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
)

func TestConversationItemUnion(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		v := format.ConversationItem{}
		if mask&1 != 0 {
			v.User = &format.UserMessage{}
		}
		if mask&2 != 0 {
			v.Assistant = &format.AssistantMessage{}
		}
		if mask&4 != 0 {
			v.ToolResult = result("id").ToolResult
		}
		valid := mask == 1 || mask == 2 || mask == 4
		wire, err := codec.Encode(v, limits())
		if valid {
			if err != nil || v.Validate() != nil {
				t.Fatalf("mask %d: %v", mask, err)
			}
			decoded, err := codec.Decode[format.ConversationItem](wire, limits())
			if err != nil || !decoded.Equal(v) {
				t.Fatalf("mask %d roundtrip: %v", mask, err)
			}
		} else {
			if v.Validate() == nil || wire != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("mask %d accepted: %v", mask, err)
			}
			if _, err := codec.Decode[format.ConversationItem](v.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("mask %d decoded: %v", mask, err)
			}
		}
	}
}
