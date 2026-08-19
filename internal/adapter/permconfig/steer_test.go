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

// TestOperatorSteerFromCLIHonoured: an OPERATOR-TIER (CLI explicit) steer: false is
// read and returned by OperatorSteer() WITH its presence bit (issue #512), mirroring
// the plan-mode-auto-approve operator-tier test. The presence bit matters: the knob is
// an opt-OUT of a default-ON feature, so absent and explicit-false must differ.
func TestOperatorSteerFromCLIHonoured(t *testing.T) {
	const yaml = "steer: false\n"
	env := envWithExplicit("/etc/mecatl/steer.yaml", yaml)
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/steer.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	value, present := r.OperatorSteer()
	if !present {
		t.Fatal("operator-tier steer: false must report present=true")
	}
	if value {
		t.Fatal("operator-tier steer: false must report value=false (the opt-OUT)")
	}
}

// TestOperatorSteerAbsentIsNotPresent: with no steer: key anywhere, OperatorSteer
// reports present=false so composition keeps the DEFAULT-ON (it does not read a bare
// false as a disable).
func TestOperatorSteerAbsentIsNotPresent(t *testing.T) {
	env := envWithExplicit("/etc/mecatl/other.yaml", "posture: auto\n")
	r := newWithEnv(Options{ExplicitFiles: []string{"/etc/mecatl/other.yaml"}}, env)
	if r == nil {
		t.Fatal("resolver should be non-nil with an explicit file")
	}
	if _, present := r.OperatorSteer(); present {
		t.Fatal("no steer: key must report present=false (composition keeps DEFAULT-ON)")
	}
}

// TestProjectSteerIgnoredWithWarn pins the operator-tier-only invariant (issue #512):
// a PROJECT-TIER steer: key must NEVER become the operator value — the harness's
// operator surface is not a project repo's to flip in either direction. The resolver
// WARNs naming why, mirroring the sibling operator-tier scalars
// (guardrails/posture/reasoning-effort/plan-mode-auto-approve).
func TestProjectSteerIgnoredWithWarn(t *testing.T) {
	var buf bytes.Buffer
	diag := slogdiag.New(&buf, false, port.LevelDebug)

	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, "steer: false\n")

	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)

	if _, present := r.OperatorSteer(); present {
		t.Fatal("a PROJECT-tier steer: key must NOT become the operator value (operator-tier only)")
	}
	log := buf.String()
	if !strings.Contains(log, "steer: IGNORING a project-tier steer: key") {
		t.Fatalf("expected an ignore-WARN naming the project tier; got:\n%s", log)
	}
}
