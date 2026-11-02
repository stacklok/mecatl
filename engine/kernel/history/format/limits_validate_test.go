package format_test

import (
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestLimitsValidate(t *testing.T) {
	valid := format.Limits{MaxCommitBytes: 1, MaxRecords: 1, MaxKindBytes: 1, MaxPayloadBytes: 1}
	before := valid
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(valid, before) {
		t.Fatal("Validate() mutated valid Limits")
	}
	maximum := format.Limits{MaxCommitBytes: int(^uint(0) >> 1), MaxRecords: int(^uint(0) >> 1), MaxKindBytes: int(^uint(0) >> 1), MaxPayloadBytes: int(^uint(0) >> 1)}
	before = maximum
	if err := maximum.Validate(); err != nil {
		t.Fatalf("maximum limits: %v", err)
	}
	if !reflect.DeepEqual(maximum, before) {
		t.Fatal("Validate() mutated maximum Limits")
	}
	for name, limits := range map[string]format.Limits{
		"zero commit":     {MaxRecords: 1, MaxKindBytes: 1, MaxPayloadBytes: 1},
		"negative commit": {MaxCommitBytes: -1, MaxRecords: 1, MaxKindBytes: 1, MaxPayloadBytes: 1},
		"zero records":    {MaxCommitBytes: 1, MaxKindBytes: 1, MaxPayloadBytes: 1},
		"negative kind":   {MaxCommitBytes: 1, MaxRecords: 1, MaxKindBytes: -1, MaxPayloadBytes: 1},
		"zero payload":    {MaxCommitBytes: 1, MaxRecords: 1, MaxKindBytes: 1},
	} {
		t.Run(name, func(t *testing.T) {
			before := limits
			if err := limits.Validate(); err == nil {
				t.Fatal("Validate() succeeded")
			}
			if !reflect.DeepEqual(limits, before) {
				t.Fatal("Validate() mutated Limits")
			}
		})
	}
}
