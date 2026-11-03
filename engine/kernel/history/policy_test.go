package history_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
)

func TestPolicyValidate(t *testing.T) {
	valid := history.Policy{
		CommitLimits:   codec.Limits{MaxBytes: 1, MaxArrayElements: 1, MaxStringBytes: 1, MaxByteStringBytes: 1, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 1, MaxCollectionElements: 1},
		RetryWindow:    1,
		MaxPageBytes:   1,
		MaxPageCommits: 1,
	}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, policy := range map[string]history.Policy{
		"zero limits":         {RetryWindow: 1, MaxPageBytes: 1, MaxPageCommits: 1},
		"negative limits":     {CommitLimits: codec.Limits{MaxBytes: -1, MaxArrayElements: 1, MaxStringBytes: 1, MaxByteStringBytes: 1, MaxMapPairs: 1, MaxDepth: 1, MaxNodes: 1, MaxCollectionElements: 1}, RetryWindow: 1, MaxPageBytes: 1, MaxPageCommits: 1},
		"zero window":         {CommitLimits: valid.CommitLimits, MaxPageBytes: 1, MaxPageCommits: 1},
		"negative page bytes": {CommitLimits: valid.CommitLimits, RetryWindow: 1, MaxPageBytes: -1, MaxPageCommits: 1},
		"zero page commits":   {CommitLimits: valid.CommitLimits, RetryWindow: 1, MaxPageBytes: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := policy.Validate(); !errors.Is(err, history.ErrInvalid) {
				t.Fatalf("Validate() error = %v; want ErrInvalid", err)
			}
		})
	}
}
