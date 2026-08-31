package clientauth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// TestLogoutUsesOneProviderCleanupBudgetOutsideTargetLock pins that the HTTP
// client is built exactly ONCE per Logout call -- covering every retained
// connection needing revocation, not once per connection -- and outside the
// target transaction lock, with a bounded deadline.
func TestLogoutUsesOneProviderCleanupBudgetOutsideTargetLock(t *testing.T) {
	reg, creds, target, entries := duplicateLogoutState(t)
	builds := 0
	var deadline time.Time
	result, err := Logout(t.Context(), target, LogoutConfig{
		Registry: reg, Credentials: creds,
		HTTPClient: func(ctx context.Context, conns []Connection) (*http.Client, error) {
			builds++
			if len(conns) != len(entries) {
				t.Fatalf("client requested for %d connections, want %d", len(conns), len(entries))
			}
			var ok bool
			deadline, ok = ctx.Deadline()
			if !ok {
				t.Fatal("HTTP client construction context has no deadline")
			}
			unlock, lockErr := reg.lockTarget(ctx, target)
			if lockErr != nil {
				t.Fatalf("provider cleanup ran under target lock: %v", lockErr)
			}
			unlock()
			return nil, errors.New("offline")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("HTTP client constructions = %d, want 1 (one shared client for the whole operation)", builds)
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > revocationTimeout {
		t.Fatalf("provider cleanup deadline remaining = %v", remaining)
	}
	if result.RevocationsAttempted != 0 || result.RevocationsFailed != 2*len(entries) {
		t.Fatalf("revocation accounting = %#v", result)
	}
}

// TestLogoutSharedClientServesEveryEntryUnderOneBudget pins that the single
// built client (not a fresh one per entry) is reused for every retained
// connection's revocation requests, and none of those requests observe a
// renewed deadline.
func TestLogoutSharedClientServesEveryEntryUnderOneBudget(t *testing.T) {
	reg, creds, target, entries := duplicateLogoutState(t)
	builds := 0
	var deadlines []time.Time
	result, err := Logout(t.Context(), target, LogoutConfig{
		Registry: reg, Credentials: creds,
		HTTPClient: func(_ context.Context, conns []Connection) (*http.Client, error) {
			builds++
			if len(conns) != len(entries) {
				t.Fatalf("client requested for %d connections, want %d", len(conns), len(entries))
			}
			return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				deadline, ok := req.Context().Deadline()
				if !ok {
					t.Fatal("revocation request has no deadline")
				}
				deadlines = append(deadlines, deadline)
				return nil, errors.New("offline")
			})}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if builds != 1 {
		t.Fatalf("HTTP client constructions = %d, want 1 (one shared client for every entry)", builds)
	}
	if len(deadlines) < len(entries) {
		t.Fatalf("revocation requests observed = %d, want at least one per entry (%d)", len(deadlines), len(entries))
	}
	for _, d := range deadlines[1:] {
		if !d.Equal(deadlines[0]) {
			t.Fatalf("a later entry's request observed a renewed deadline: %v vs %v", d, deadlines[0])
		}
	}
	if result.RevocationsFailed != 2*len(entries) {
		t.Fatalf("revocation accounting = %#v", result)
	}
}

func TestLogoutShorterCallerDeadlineStopsRemoteWorkWithoutRestoringState(t *testing.T) {
	reg, creds, target, entries := duplicateLogoutState(t)
	parent, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	parentDeadline, _ := parent.Deadline()
	calls := 0
	result, err := Logout(parent, target, LogoutConfig{
		Registry: reg, Credentials: creds,
		HTTPClient: func(ctx context.Context, _ []Connection) (*http.Client, error) {
			calls++
			deadline, ok := ctx.Deadline()
			if !ok || !deadline.Equal(parentDeadline) {
				t.Fatalf("HTTP client deadline = %v, %v; want caller deadline %v", deadline, ok, parentDeadline)
			}
			return &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				<-req.Context().Done()
				return nil, req.Context().Err()
			})}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("HTTP client constructions after budget expiry = %d, want 1", calls)
	}
	if result.RevocationsAttempted != 0 || result.RevocationsFailed != 2*len(entries) {
		t.Fatalf("revocation accounting = %#v", result)
	}
	if _, err := reg.FindTarget(target); !IsNotEnrolled(err) {
		t.Fatalf("registry state was restored after timeout: %v", err)
	}
	for _, conn := range entries {
		if _, err := creds.Load(t.Context(), conn.Identity); !IsNotEnrolled(err) {
			t.Fatalf("credential state was restored after timeout: %v", err)
		}
	}
}

func duplicateLogoutState(t *testing.T) (*Registry, *Credentials, string, []Connection) {
	t.Helper()
	reg, err := OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials(t)
	const target = "duplicate.example:443"
	first := identity(target)
	first.Issuer = "https://first.example"
	second := identity(target)
	second.Issuer = "https://second.example"
	entries := []Connection{{Identity: first}, {Identity: second}}
	if err := reg.writeRows([]registryRow{
		{connection: entries[0], valid: true},
		{connection: entries[1], valid: true},
	}); err != nil {
		t.Fatal(err)
	}
	for _, conn := range entries {
		if _, err := creds.Upsert(t.Context(), conn.Identity, Token{RefreshToken: "refresh", AccessToken: "access", TokenType: "Bearer"}); err != nil {
			t.Fatal(err)
		}
	}
	return reg, creds, target, entries
}
