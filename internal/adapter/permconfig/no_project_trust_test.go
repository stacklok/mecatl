package permconfig

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/slogdiag"
)

// TestOperatorNoProjectTrustFromCLIHonoured: an OPERATOR-TIER (CLI explicit)
// no-project-trust: true is read and returned by OperatorNoProjectTrust() (issue
// #359), mirroring the plan-mode-auto-approve operator-tier test.
func TestOperatorNoProjectTrustFromCLIHonoured(t *testing.T) {
	const yaml = "no-project-trust: true\n"
	env := envWithExplicit("/etc/mecatl/npt.yaml", yaml)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/npt.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	if !r.OperatorNoProjectTrust() {
		t.Fatal("operator-tier no-project-trust must be honoured from CLI/explicit")
	}
}

// TestOperatorNoProjectTrustNilResolver pins the nil-safe accessor (a typed-nil
// resolver returns false not a panic), matching OperatorPlanModeAutoApprove.
func TestOperatorNoProjectTrustNilResolver(t *testing.T) {
	var r *Resolver
	if r.OperatorNoProjectTrust() {
		t.Fatal("nil resolver OperatorNoProjectTrust = true, want false")
	}
}

// TestProjectNoProjectTrustIgnoredWithWarn pins the feature's highest-risk invariant
// (issue #359): a PROJECT-TIER no-project-trust: key must NEVER become the operator
// value — suppressing the repo's own project-tier ingestion is an operator deployment
// decision, never the repo's call. The resolver WARNs naming why, mirroring the
// sibling operator-tier scalars (guardrails/posture/plan-mode-auto-approve).
func TestProjectNoProjectTrustIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "no-project-trust: true\n")

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if r.OperatorNoProjectTrust() {
		t.Fatal("a PROJECT-tier no-project-trust must NOT become the operator value (ingestion suppression must not be settable from a repo)")
	}
	log := buf.String()
	if !strings.Contains(log, "no-project-trust: IGNORING a project-tier no-project-trust: key") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}
