package server_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// closingValidator is a PrincipalValidator that ALSO implements io.Closer — the
// shape the real toolhive-core/authn validator has (its Close stops the
// background JWKS refresh). Cancelling the root context does NOT call it, so the
// edge must.
type closingValidator struct {
	mu     sync.Mutex
	closes int
}

func (*closingValidator) Validate(context.Context, string) (*session.Principal, error) {
	return nil, server.ErrInvalidToken
}

func (v *closingValidator) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.closes++
	return nil
}

func (v *closingValidator) count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.closes
}

// TestAuthenticatorCloseTearsDownTheValidatorOnce pins the OPTIONAL teardown
// capability: the edge type-asserts io.Closer on the configured validator and
// closes it on shutdown, exactly once even if shutdown runs twice (a defer plus
// an explicit call). PrincipalValidator itself stays single-method — widening it
// would break every fake.
func TestAuthenticatorCloseTearsDownTheValidatorOnce(t *testing.T) {
	t.Parallel()

	v := &closingValidator{}
	a := server.NewAuthenticator(server.SecurityConfig{Validator: v})

	if got := v.count(); got != 0 {
		t.Fatalf("validator closed %d times before shutdown, want 0", got)
	}
	a.Close()
	if got := v.count(); got != 1 {
		t.Fatalf("validator closed %d times after one shutdown, want 1", got)
	}
	a.Close()
	if got := v.count(); got != 1 {
		t.Fatalf("validator closed %d times after a second shutdown, want 1", got)
	}
}

// TestAuthenticatorCloseWithoutACloserIsANoOp pins that teardown stays OPTIONAL:
// a validator with no Close (the fakes throughout these tests) and the identity-OFF
// zero config both shut down without a panic.
func TestAuthenticatorCloseWithoutACloserIsANoOp(t *testing.T) {
	t.Parallel()

	server.NewAuthenticator(server.SecurityConfig{}).Close()
	server.NewAuthenticator(server.SecurityConfig{Validator: fakeValidator{}}).Close()
}
