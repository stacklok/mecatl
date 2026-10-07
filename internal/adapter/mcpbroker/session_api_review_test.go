package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func reviewAPI(t *testing.T) (*SessionAPI, *continuityProofFixture, context.Context) {
	t.Helper()
	f := newContinuityProofFixture(t, time.Now().Add(time.Minute))
	client := redis.NewClient(&redis.Options{MaxRetries: -1, Addr: f.mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	api, err := NewSessionAPI(f.process, client, func(context.Context) *session.Principal {
		return &session.Principal{Issuer: "https://workload.test", Subject: "client"}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = api.Close() })
	return api, f, session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "owner"})
}

func TestSessionAPIReviewPendingDisconnect(t *testing.T) {
	api, f, ctx := reviewAPI(t)
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := api.BeginEnrollment(ctx, opened.Ref)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := api.BeginEnrollment(ctx, opened.Ref)
	if err != nil || repeated.Started == nil || repeated.Started.Ref != pending.Started.Ref || repeated.Started.Prompt != pending.Started.Prompt {
		t.Fatalf("pending enrollment was not reused: %#v %v", repeated, err)
	}
	uncompleted, err := api.ObserveEnrollment(ctx, opened.Ref, pending.Started.Ref)
	if err != nil || uncompleted.Kind != c.FlowPending || api.states[opened.Ref].record.Connected {
		t.Fatalf("browser prompt was mistaken for publication: %#v %v", uncompleted, err)
	}
	u, _ := url.Parse(pending.Started.Prompt.URL)
	a := api.states[opened.Ref].attachment
	if _, err := api.DisconnectTools(ctx, opened.Ref, ""); err == nil {
		t.Fatal("never-published enrollment accepted empty cleanup authority")
	}
	if api.states[opened.Ref].attachment != a {
		t.Fatal("invalid cleanup touched provisional attachment")
	}
	result, err := api.CancelEnrollment(ctx, opened.Ref, pending.Started.Ref)
	if err != nil || result != c.Cancelled {
		t.Fatalf("cancel pending: %v %v", result, err)
	}
	if code := callback(t, f.process.Runtime, "old-code", u.Query().Get("state")).Code; code == http.StatusOK {
		t.Fatal("old callback accepted")
	}
	observed, err := api.ObserveEnrollment(ctx, opened.Ref, pending.Started.Ref)
	if err != nil || observed.Kind == c.FlowCompleted || observed.Kind == c.FlowPending {
		t.Fatalf("old observation: %#v %v", observed, err)
	}
	fresh, err := api.BeginEnrollment(ctx, opened.Ref)
	if err != nil || fresh.Started == nil || fresh.Started.Ref == pending.Started.Ref || fresh.Started.Prompt.URL == pending.Started.Prompt.URL {
		t.Fatalf("fresh enrollment: %#v %v", fresh, err)
	}
	if api.states[opened.Ref].attachment != a {
		t.Fatal("enrollment cancellation unexpectedly replaced attachment")
	}
}

func TestSessionAPIReviewExpiredCapacity(t *testing.T) {
	api, f, ctx := reviewAPI(t)
	var first c.SessionSnapshot
	var attachments []*Attachment
	for i := 0; i < 32; i++ {
		opened, err := api.OpenSession(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = opened
		}
		if _, err = api.BeginEnrollment(ctx, opened.Ref); err != nil {
			t.Fatal(err)
		}
		attachments = append(attachments, api.states[opened.Ref].attachment)
	}
	f.mini.FastForward(31 * 24 * time.Hour)
	later := first.ExpiresAt.Add(time.Second)
	api.now = func() time.Time { return later }
	if _, err := api.OpenSession(ctx, nil); err != nil {
		t.Fatalf("expired cache consumed capacity: %v", err)
	}
	for _, a := range attachments {
		a.mu.RLock()
		closed := a.closed
		a.mu.RUnlock()
		if !closed {
			t.Fatal("expired native attachment left open")
		}
	}
	if len(api.states) != 1 {
		t.Fatalf("cached states: %d", len(api.states))
	}
	f.process.Runtime.mu.RLock()
	nativeCount := len(f.process.Runtime.sessions)
	f.process.Runtime.mu.RUnlock()
	if nativeCount != 0 {
		t.Fatalf("expired native logical resources retained: %d", nativeCount)
	}
}

func TestSessionAPIReviewAbsentDeleteReleasesOwnedCache(t *testing.T) {
	api, f, ctx := reviewAPI(t)
	opened, err := api.OpenSession(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.BeginEnrollment(ctx, opened.Ref); err != nil {
		t.Fatal(err)
	}
	a := api.states[opened.Ref].attachment
	foreign := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://owner.test", Subject: "foreign"})
	if _, err := api.DeleteSession(foreign, opened.Ref); err == nil {
		t.Fatal("foreign ref authorized native deletion")
	}
	a.mu.RLock()
	prematurelyClosed := a.closed
	a.mu.RUnlock()
	if prematurelyClosed || api.states[opened.Ref] == nil {
		t.Fatal("foreign input released live owned resources")
	}
	f.mini.Del(sessionAPIPrefix + string(opened.Ref))
	if result, err := api.DeleteSession(ctx, opened.Ref); err != nil || result != c.AlreadyAbsent {
		t.Fatalf("absent delete: %v %v", result, err)
	}
	if api.states[opened.Ref] != nil {
		t.Fatal("absent cache retained")
	}
	a.mu.RLock()
	closed := a.closed
	a.mu.RUnlock()
	if !closed {
		t.Fatal("absent native attachment open")
	}
}

type reviewSaveRedis struct {
	redis.UniversalClient
	mode   string
	sets   int
	cancel context.CancelFunc
}

func (r *reviewSaveRedis) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) *redis.StatusCmd {
	r.sets++
	if !strings.HasPrefix(key, sessionAPIPrefix) || r.mode == "" {
		return r.UniversalClient.Set(ctx, key, value, ttl)
	}
	mode := r.mode
	r.mode = ""
	if mode != "before" {
		result := r.UniversalClient.Set(ctx, key, value, ttl)
		if result.Err() != nil {
			return result
		}
	}
	if r.cancel != nil {
		r.cancel()
	}
	cmd := redis.NewStatusCmd(ctx)
	cmd.SetErr(errors.New("injected lost save acknowledgment"))
	return cmd
}

func TestSessionAPIReviewRecoveryRetry(t *testing.T) {
	for _, mode := range []string{"before", "landed", "commit-cancel", "commit-lost"} {
		t.Run(mode, func(t *testing.T) {
			api, f, ctx := reviewAPI(t)
			opened, err := api.OpenSession(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			st := api.states[opened.Ref]
			guard := c.ContinuityGuard{SessionID: session.SessionID(opened.Ref), SessionIncarnation: st.record.Incarnation, OwnerPartition: st.record.Owner, WorkloadPartition: st.record.Workload, ProfileDigest: f.process.profileDigest, Providers: f.process.providers}
			staged, err := f.process.custody.Stage(ctx, custodyRequest{Guard: custodyGuardFromContract(guard), AttemptDeadline: time.Now().Add(time.Minute)}, "verified-tsid")
			if err != nil {
				t.Fatal(err)
			}
			st.record.Custody = &c.StagedCredentialCustody{RecoveryReference: string(staged.Recovery), ExpiresAt: staged.ExpiresAt, ProfileDigest: guard.ProfileDigest, Providers: guard.Providers}
			st.record.Account = sessionAPIProofAccount(t, f)
			st.record.Connected = true
			st.record.Connection = apiRef()
			if err = saveAPIRecord(t, api, ctx, st); err != nil {
				t.Fatal(err)
			}
			delete(api.states, opened.Ref)
			faulty := &reviewSaveRedis{UniversalClient: api.redis, mode: mode}
			api.redis = faulty
			if mode == "commit-lost" {
				faulty.mode = ""
			}
			attemptCtx := ctx
			if mode == "commit-cancel" {
				var cancel context.CancelFunc
				attemptCtx, cancel = context.WithCancel(ctx)
				defer cancel()
				faulty.cancel = cancel
			}
			first, err := api.OpenSession(attemptCtx, &opened.Ref)
			if (mode == "landed" || mode == "commit-lost") && (err != nil || len(first.Catalogue.Tools()) != 2 || find(first.Catalogue, "CallMcpWithQuery") == nil) {
				t.Fatalf("landed adoption: %#v %v", first, err)
			}
			if mode != "landed" && mode != "commit-lost" && err == nil {
				t.Fatal("expected injected failure")
			}
			// In commit-lost, discard the successful reply and retry as a caller
			// that never received the acknowledgement of native adoption.
			issued := f.issued.Load()
			var original *Attachment
			if cached := api.states[opened.Ref]; cached != nil {
				original = cached.attachment
			}
			if mode == "before" {
				if _, err := api.OpenSession(ctx, &opened.Ref); err == nil {
					t.Fatal("unresolved recovery write reopened authority")
				}
				candidate, err := json.Marshal(api.states[opened.Ref].pendingWrite)
				if err != nil {
					t.Fatal(err)
				}
				if err := faulty.UniversalClient.Set(ctx, sessionAPIPrefix+string(opened.Ref), candidate, time.Hour).Err(); err != nil {
					t.Fatal(err)
				}
			}
			sets := faulty.sets
			retry, err := api.OpenSession(ctx, &opened.Ref)
			if faulty.sets != sets {
				t.Fatal("recovery retry issued a redundant identical SET")
			}
			if err != nil || len(retry.Catalogue.Tools()) != 2 || find(retry.Catalogue, "CallMcpWithQuery") == nil {
				t.Fatalf("exact recovery retry: %#v %v", retry, err)
			}
			if f.issued.Load() != issued {
				t.Fatal("retry minted a new native credential")
			}
			if original != nil && api.states[opened.Ref].attachment != original {
				t.Fatal("retry replaced native handle")
			}
			a := api.states[opened.Ref].attachment
			a.mu.RLock()
			settled, closed := a.settled, a.closed
			a.mu.RUnlock()
			if !settled || closed {
				t.Fatal("recovered handle not committed")
			}
			f.process.Runtime.mu.RLock()
			count := len(f.process.Runtime.sessions)
			f.process.Runtime.mu.RUnlock()
			if count != 1 {
				t.Fatalf("native logical count: %d", count)
			}
		})
	}
}
