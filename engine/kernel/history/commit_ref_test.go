package history_test

import (
	"errors"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

const testCID = "bafyreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"

func commitRef(t *testing.T, sequence int64) history.CommitRef {
	t.Helper()
	cid, err := format.ParseCID(testCID)
	if err != nil {
		t.Fatal(err)
	}
	return history.CommitRef{Sequence: sequence, CID: cid}
}

func TestCommitRefValidate(t *testing.T) {
	for name, ref := range map[string]history.CommitRef{
		"valid":    commitRef(t, 1),
		"zero":     {},
		"sequence": {CID: commitRef(t, 1).CID},
		"CID":      {Sequence: 1},
		"negative": {Sequence: -1, CID: commitRef(t, 1).CID},
	} {
		t.Run(name, func(t *testing.T) {
			err := ref.Validate()
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, history.ErrInvalid) {
				t.Fatalf("Validate() error = %v; want ErrInvalid", err)
			}
		})
	}
}
