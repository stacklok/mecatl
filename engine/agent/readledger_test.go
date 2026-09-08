package agent

import (
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/tool"
)

func init() { //nolint:gochecknoinits // test-only default for constructor-focused fixtures
	testReadLedgerFactory = func() tool.ReadLedger { return memledger.New() }
}
