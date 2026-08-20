package governance

import (
	"errors"
	"reflect"
	"testing"
)

func TestADR_0233_AuthorityEvaluator_Scenario1_NarrowIsIntersectionOnEveryAxis(t *testing.T) {
	t.Parallel()

	left := CapabilitySet{
		Tools:                    []string{"Read", "Write"},
		RemainingDelegationDepth: 2,
		FileSystem:               true,
		DirectWrite:              true,
	}
	right := CapabilitySet{
		Tools:                    []string{"Grep", "Read"},
		RemainingDelegationDepth: 1,
		FileSystem:               true,
		DirectWrite:              false,
	}

	got := Narrow(left, right)
	want := CapabilitySet{
		Tools:                    []string{"Read"},
		RemainingDelegationDepth: 1,
		FileSystem:               true,
		DirectWrite:              false,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Narrow(%+v, %+v) = %+v, want %+v", left, right, got, want)
	}
	if !left.Contains(got) || !right.Contains(got) {
		t.Fatalf("Narrow result %+v widened an input: left contains=%t right contains=%t", got, left.Contains(got), right.Contains(got))
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario1_OperationsAreMonotone(t *testing.T) {
	t.Parallel()

	left := CapabilitySet{Tools: []string{"Read", "Write"}, RemainingDelegationDepth: 2, FileSystem: true, DirectWrite: true}
	right := CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1, FileSystem: true}
	for _, result := range []CapabilitySet{Narrow(left, right), Narrow(right, left)} {
		if !left.Contains(result) || !right.Contains(result) {
			t.Fatalf("operation result %+v is not contained by both inputs", result)
		}
	}

	descended, err := ConsumeDelegationHop(left)
	if err != nil {
		t.Fatalf("ConsumeDelegationHop(%+v): %v", left, err)
	}
	if !left.Contains(descended) {
		t.Fatalf("ConsumeDelegationHop(%+v) = %+v, which widens authority", left, descended)
	}
}

func FuzzAuthorityOperationsAreMonotone(f *testing.F) {
	f.Add(uint8(0b11), uint8(2), true, true, uint8(0b01), uint8(1), true, false)
	f.Add(uint8(0), uint8(0), false, false, uint8(0b111), uint8(3), true, true)

	f.Fuzz(func(t *testing.T, leftMask uint8, leftDepth uint8, leftFS bool, leftDirect bool, rightMask uint8, rightDepth uint8, rightFS bool, rightDirect bool) {
		left := capabilitySetFromFuzz(leftMask, leftDepth, leftFS, leftDirect)
		right := capabilitySetFromFuzz(rightMask, rightDepth, rightFS, rightDirect)
		narrowed := Narrow(left, right)
		if !left.Contains(narrowed) || !right.Contains(narrowed) {
			t.Fatalf("Narrow widened authority: left=%+v right=%+v got=%+v", left, right, narrowed)
		}
		if left.RemainingDelegationDepth > 0 {
			descended, err := ConsumeDelegationHop(left)
			if err != nil {
				t.Fatalf("ConsumeDelegationHop(%+v): %v", left, err)
			}
			if !left.Contains(descended) {
				t.Fatalf("ConsumeDelegationHop widened authority: before=%+v after=%+v", left, descended)
			}
		}
	})
}

func TestADR_0233_AuthorityEvaluator_Scenario1_DepthExhaustionIsAnError(t *testing.T) {
	t.Parallel()

	_, err := ConsumeDelegationHop(CapabilitySet{RemainingDelegationDepth: 0})
	if !errors.Is(err, ErrDelegationDepthExhausted) {
		t.Fatalf("ConsumeDelegationHop at depth zero = %v, want ErrDelegationDepthExhausted", err)
	}
}

func TestADR_0233_AuthorityEvaluator_Scenario1_SingleRepresentationAndSerializer(t *testing.T) {
	t.Parallel()

	set := CapabilitySet{Tools: []string{"Read"}, RemainingDelegationDepth: 1, FileSystem: true}
	typ := reflect.TypeOf(set)
	if typ.PkgPath() != "github.com/stacklok/mecatl/engine/governance" || typ.Name() != "CapabilitySet" {
		t.Fatalf("capability set representation = %s.%s, want governance.CapabilitySet", typ.PkgPath(), typ.Name())
	}
	if _, ok := typ.MethodByName("MarshalJSON"); ok {
		t.Fatal("CapabilitySet must remain a plain value serialized by the snapshot boundary, not a custom canonical format")
	}
	if _, ok := typ.MethodByName("UnmarshalJSON"); ok {
		t.Fatal("CapabilitySet must not own a parser")
	}
}

func capabilitySetFromFuzz(mask, depth uint8, fileSystem, directWrite bool) CapabilitySet {
	allTools := []string{"Read", "Write", "Grep"}
	tools := make([]string, 0, len(allTools))
	for i, tool := range allTools {
		if mask&(1<<i) != 0 {
			tools = append(tools, tool)
		}
	}
	return CapabilitySet{
		Tools:                    tools,
		RemainingDelegationDepth: int(depth % 4),
		FileSystem:               fileSystem,
		DirectWrite:              directWrite,
	}
}
