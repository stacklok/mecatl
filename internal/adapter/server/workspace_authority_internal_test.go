package server

import (
	"errors"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
)

func TestValidateEnvironmentOverrideRequiresExactPlacementIdentity(t *testing.T) {
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "opaque", Revision: "r1"}
	sess := session.New("s", session.ModeDefault, ref, session.Limits{}, time.Unix(0, 0))
	svc := &Service{}

	matching := tool.MustEnvironment(ref, memfs.NewWorkspace("/private/root"), memledger.New(), nil)
	if err := svc.validateEnvironmentOverride(sess, matching, matching); err != nil {
		t.Fatalf("matching ref: %v", err)
	}

	changed := ref
	changed.Revision = "r2"
	mismatch := tool.MustEnvironment(changed, memfs.NewWorkspace("/private/root"), memledger.New(), nil)
	if err := svc.validateEnvironmentOverride(sess, mismatch, matching); !errors.Is(err, ErrFailedPrecondition) {
		t.Fatalf("mismatched revision = %v, want ErrFailedPrecondition", err)
	}
}
