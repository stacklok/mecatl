package permconfig

import (
	"bytes"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

func newCapturedResolver(t *testing.T, operatorPath, operatorYAML string, trust bool) (*Resolver, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r := newWithEnv(Options{
		Conventional: true, TrustProject: trust, ExplicitFiles: []string{operatorPath},
		Diagnostics: slogdiag.New(&buf, false, port.LevelDebug),
	}, envWithExplicit(operatorPath, operatorYAML))
	return r, &buf
}
