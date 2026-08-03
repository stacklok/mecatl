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

// TestOperatorPlanModeAutoApproveFromCLIHonoured: an OPERATOR-TIER (CLI explicit)
// plan-mode-auto-approve: true is read and returned by OperatorPlanModeAutoApprove()
// (issue #206 Wave 6a), mirroring the reasoning-effort operator-tier test.
func TestOperatorPlanModeAutoApproveFromCLIHonoured(t *testing.T) {
	const yaml = "plan-mode-auto-approve: true\n"
	env := envWithExplicit("/etc/mecatl/plan.yaml", yaml)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/plan.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	if !r.OperatorPlanModeAutoApprove() {
		t.Fatal("operator-tier plan-mode-auto-approve must be honoured from CLI/explicit")
	}
}

// TestProjectPlanModeAutoApproveIgnoredWithWarn pins the feature's highest-risk
// invariant (issue #206 Wave 6a): a PROJECT-TIER plan-mode-auto-approve: key must
// NEVER become the operator value — a project repo (attacker-influenceable) must not
// be able to enable autonomous plan approval, which bypasses the human plan-review
// gate (a security DOWNGRADE). The resolver WARNs naming why, mirroring the sibling
// operator-tier scalars (guardrails/posture/reasoning-effort).
func TestProjectPlanModeAutoApproveIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "plan-mode-auto-approve: true\n")

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if r.OperatorPlanModeAutoApprove() {
		t.Fatal("a PROJECT-tier plan-mode-auto-approve must NOT become the operator value (autonomous plan approval must not be enableable from a repo)")
	}
	log := buf.String()
	if !strings.Contains(log, "plan-mode-auto-approve: IGNORING a project-tier plan-mode-auto-approve: key") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}
