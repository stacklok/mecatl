package history_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestReadRequestValidate(t *testing.T) {
	policy := history.Policy{
		CommitLimits: format.Limits{MaxCommitBytes: 1, MaxRecords: 1, MaxKindBytes: 1, MaxPayloadBytes: 1},
		RetryWindow:  1, MaxPageBytes: 10, MaxPageCommits: 2,
	}
	valid := history.ReadRequest{Through: commitRef(t, 1), MaxCommits: 2}
	for _, after := range []history.CommitRef{{}, valid.Through} {
		r := valid
		r.After = after
		if err := r.Validate(policy); err != nil {
			t.Fatal(err)
		}
	}
	other := byteCommit(t, dto(history.NewHistoryID(), history.CommitRef{}, "other")).Ref
	for name, request := range map[string]history.ReadRequest{
		"negative after":             {After: history.CommitRef{Sequence: -1, CID: valid.Through.CID}, Through: valid.Through, MaxCommits: 1},
		"after through":              {After: commitRef(t, 2), Through: valid.Through, MaxCommits: 1},
		"after missing CID":          {After: history.CommitRef{Sequence: 1}, Through: valid.Through, MaxCommits: 1},
		"after missing sequence":     {After: history.CommitRef{CID: valid.Through.CID}, Through: valid.Through, MaxCommits: 1},
		"same ordinal different CID": {After: history.CommitRef{Sequence: 1, CID: other.CID}, Through: valid.Through, MaxCommits: 1},
		"zero commits":               {Through: valid.Through},
		"negative commits":           {Through: valid.Through, MaxCommits: -1},
		"too many commits":           {Through: valid.Through, MaxCommits: 3},
		"invalid through":            {MaxCommits: 1},
	} {
		t.Run(name, func(t *testing.T) {
			if err := request.Validate(policy); !errors.Is(err, history.ErrInvalid) {
				t.Fatalf("Validate() error = %v; want ErrInvalid", err)
			}
		})
	}
}
