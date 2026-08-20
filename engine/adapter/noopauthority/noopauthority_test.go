package noopauthority

import (
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/authorityconformance"
	"github.com/stacklok/mecatl/engine/port"
)

func TestEvaluatorConformance(t *testing.T) {
	authorityconformance.Run(t, func(*testing.T) port.AuthorityEvaluator { return New() })
}
