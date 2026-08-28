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

func TestLogoutUsesOneProviderCleanupBudgetOutsideTargetLock(t *testing.T) {
	reg, creds, target, entries := duplicateLogoutState(t)
	var deadlines []time.Time
	result, err := Logout(t.Context(), target, LogoutConfig{
		Registry: reg, Credentials: creds,
		HTTPClient: func(ctx context.Context, _ Connection) (*http.Client, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("HTTP client construction context has no deadline")
			}
			deadlines = append(deadlines, deadline)
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
	if len(deadlines) != len(entries) {
		t.Fatalf("HTTP client constructions = %d, want %d", len(deadlines), len(entries))
	}
	for _, deadline := range deadlines[1:] {
		if !deadline.Equal(deadlines[0]) {
			t.Fatalf("provider cleanup received fresh budgets: %v", deadlines)
		}
	}
	if remaining := time.Until(deadlines[0]); remaining <= 0 || remaining > revocationTimeout {
		t.Fatalf("provider cleanup deadline remaining = %v", remaining)
	}
	if result.RevocationsAttempted != 0 || result.RevocationsFailed != 2*len(entries) {
		t.Fatalf("revocation accounting = %#v", result)
	}
}

func TestLogoutProviderEntriesConsumeOneSharedBudget(t *testing.T) {
	reg, creds, target, entries := duplicateLogoutState(t)
	parent, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	remaining := make([]time.Duration, 0, len(entries))
	calls := 0
	result, err := Logout(parent, target, LogoutConfig{
		Registry: reg, Credentials: creds,
		HTTPClient: func(ctx context.Context, _ Connection) (*http.Client, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Fatal("provider work has no operation deadline")
			}
			remaining = append(remaining, time.Until(deadline))
			calls++
			if calls == 1 {
				timer := time.NewTimer(100 * time.Millisecond)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
				}
			}
			return nil, errors.New("offline")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != len(entries) || len(remaining) != len(entries) {
		t.Fatalf("provider calls = %d, remaining samples = %d", calls, len(remaining))
	}
	if remaining[1] >= remaining[0]-75*time.Millisecond {
		t.Fatalf("later entry received a fresh budget: first=%v later=%v", remaining[0], remaining[1])
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
		HTTPClient: func(ctx context.Context, _ Connection) (*http.Client, error) {
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
	if err := reg.write(entries); err != nil {
		t.Fatal(err)
	}
	for _, conn := range entries {
		if _, err := creds.Upsert(t.Context(), conn.Identity, Token{RefreshToken: "refresh", AccessToken: "access", TokenType: "Bearer"}); err != nil {
			t.Fatal(err)
		}
	}
	return reg, creds, target, entries
}
