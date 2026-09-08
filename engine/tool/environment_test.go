package tool

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
)

func TestPersistentReadLedgers_EnvironmentSeparatesWorkspaceAndLedger(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo"}
	ws := stubWorkspace{}
	ledger := &stubReadLedger{}
	env, err := NewEnvironment(ref, ws, ledger, nil)
	if err != nil {
		t.Fatalf("NewEnvironment: %v", err)
	}
	if env.Workspace() != ws {
		t.Fatal("Environment did not preserve the supplied Workspace")
	}
	if env.ReadLedger() != ledger {
		t.Fatal("Environment did not preserve the independently supplied ReadLedger")
	}
	workspaceType := reflect.TypeOf((*Workspace)(nil)).Elem()
	for _, obsolete := range []string{"RecordRead", "RecordedVersion"} {
		if _, ok := workspaceType.MethodByName(obsolete); ok {
			t.Fatalf("Workspace still exposes obsolete ledger method %s", obsolete)
		}
	}
}

func TestNewEnvironmentRejectsNilReadLedger(t *testing.T) {
	_, err := NewEnvironment(session.EnvironmentRef{}, stubWorkspace{}, nil, nil)
	if !errors.Is(err, ErrEnvironmentNoReadLedger) {
		t.Fatalf("NewEnvironment(nil ledger) err = %v, want ErrEnvironmentNoReadLedger", err)
	}
}

// TestNewEnvironmentRejectsNilWorkspace pins the one mandatory capability: a
// nil Workspace is always rejected. Not every tool consumes the Workspace —
// FS tools do, remote/self-contained tools may ignore the environment's
// capabilities — but the Environment itself must always carry a non-nil
// Workspace for a coherent execution context.
func TestNewEnvironmentRejectsNilWorkspace(t *testing.T) {
	_, err := NewEnvironment(session.EnvironmentRef{Kind: session.EnvKindMem, ID: "x"}, nil, &stubReadLedger{}, nil)
	if !errors.Is(err, ErrEnvironmentNoWorkspace) {
		t.Fatalf("NewEnvironment(nil ws) err = %v, want ErrEnvironmentNoWorkspace", err)
	}
}

// TestMustEnvironmentPanicsOnNilWorkspace pins the constructor variant for
// construction sites where a nil workspace is a programmer error.
func TestMustEnvironmentPanicsOnNilWorkspace(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustEnvironment(nil ws) must panic")
		}
	}()
	_ = MustEnvironment(session.EnvironmentRef{}, nil, &stubReadLedger{}, nil)
}

// TestZeroEnvironmentHasNilWorkspaceAndIsInvalid pins the doc contract: the
// zero-value Environment{} has a nil Workspace (and a nil CommandRunner) and
// is therefore invalid for execution. NewEnvironment/MustEnvironment are the
// ONLY constructors that enforce a non-nil Workspace; a caller must never
// assume a zero value is usable.
func TestZeroEnvironmentHasNilWorkspaceAndIsInvalid(t *testing.T) {
	var env Environment
	if env.Workspace() != nil {
		t.Errorf("zero Environment Workspace() = %v, want nil", env.Workspace())
	}
	if env.CommandRunner() != nil {
		t.Errorf("zero Environment CommandRunner() = %v, want nil", env.CommandRunner())
	}
	// The zero value must NOT satisfy the non-nil invariant the constructors
	// enforce — it is the reason NewEnvironment exists at trust boundaries.
	if env.Workspace() != nil {
		t.Fatal("a zero Environment must not be treated as execution-ready")
	}
}

// TestEnvironmentAccessors pins the immutable accessors: Ref, Workspace, and
// CommandRunner report the values NewEnvironment was given.
func TestEnvironmentAccessors(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/repo"}
	ws := stubWorkspace{}
	env := MustEnvironment(ref, ws, &stubReadLedger{}, nil)
	if env.Ref() != ref {
		t.Errorf("Ref() = %+v, want %+v", env.Ref(), ref)
	}
	if env.Workspace() == nil {
		t.Fatal("Workspace() is nil")
	}
	if env.Workspace().Root() != "/" {
		t.Errorf("Workspace().Root() = %q, want %q", env.Workspace().Root(), "/")
	}
	if env.CommandRunner() != nil {
		t.Fatal("shell-less Environment must have a nil CommandRunner")
	}
}
