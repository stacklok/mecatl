//go:build !linux && !darwin

package privatefile

import (
	"context"
	"testing"
)

func TestUnsupportedUpdateFailsBeforeMutation(t *testing.T) {
	called := false
	state, err := Update(context.Background(), "settings.yaml", "", 1024, func([]byte) ([]byte, bool, error) {
		called = true
		return nil, false, nil
	})
	if state != CommitNotApplied || err == nil || called {
		t.Fatalf("Update = (%v, %v), mutation called=%v", state, err, called)
	}
}
