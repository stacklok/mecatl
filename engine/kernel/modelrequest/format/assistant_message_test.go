package format_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
)

func TestAssistantItemUnion(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		v := format.AssistantItem{}
		if mask&1 != 0 {
			v.Output = &format.ContentBlock{}
		}
		if mask&2 != 0 {
			v.Refusal = &format.ContentBlock{}
		}
		if mask&4 != 0 {
			v.Call = &format.ToolCall{ProviderCallID: "id"}
		}
		valid := mask == 1 || mask == 2 || mask == 4
		wire, err := codec.Encode(v, limits())
		if valid {
			if err != nil || v.Validate() != nil {
				t.Fatalf("mask %d: %v", mask, err)
			}
			decoded, err := codec.Decode[format.AssistantItem](wire, limits())
			if err != nil || !decoded.Equal(v) {
				t.Fatalf("mask %d roundtrip: %v", mask, err)
			}
		} else {
			if v.Validate() == nil || wire != nil || !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("mask %d accepted: %v", mask, err)
			}
			if _, err := codec.Decode[format.AssistantItem](v.AppendCBOR(nil), limits()); !errors.Is(err, codec.ErrInvalidData) {
				t.Fatalf("mask %d decoded: %v", mask, err)
			}
		}
	}
}
