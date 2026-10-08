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

func TestOperatorExecutionStrictAndCaptured(t *testing.T) {
	r := newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, envWithExplicit("/operator/settings.yaml", `
execution:
  default_placement: microvm-local
  microvm:
    guest_egress:
      mode: allowlist
      allow: [api.example.com:443/tcp]
`))
	got, err := r.OperatorExecution()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.DefaultPlacement != "microvm-local" || got.MicroVM == nil || got.MicroVM.GuestEgress == nil || len(got.MicroVM.GuestEgress.Allow) != 1 {
		t.Fatalf("operator execution = %#v", got)
	}
}

func TestOperatorExecutionMalformedFailsClosed(t *testing.T) {
	r := newWithEnv(Options{ExplicitFiles: []string{"/operator/settings.yaml"}}, envWithExplicit("/operator/settings.yaml", `execution: {default_placement: container}`))
	if _, err := r.OperatorExecution(); err == nil {
		t.Fatal("malformed operator execution configuration was not retained as a startup error")
	}
}

func TestProjectExecutionIgnoredWithValueFreeWarning(t *testing.T) {
	var log bytes.Buffer
	diag := slogdiag.New(&log, false, port.LevelDebug)
	ws := &countingWS{Workspace: memfs.NewWorkspace("/repo")}
	ws.seed(t, projectFileMecatl, `execution: {default_placement: microvm-local}`)
	r := newWithEnv(Options{Conventional: true, TrustProject: true, Diagnostics: diag}, fakeEnv())
	_ = r.Resolve(context.Background(), ws)
	if got, err := r.OperatorExecution(); got != nil || err != nil {
		t.Fatalf("project execution escaped operator tier: got=%#v err=%v", got, err)
	}
	line := log.String()
	if !strings.Contains(line, "IGNORING project-tier execution block") {
		t.Fatalf("missing ignore warning: %s", line)
	}
	if strings.Contains(line, "microvm-local") {
		t.Fatalf("warning leaked ignored value: %s", line)
	}
}
