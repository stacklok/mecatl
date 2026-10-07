package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/session"
	c "github.com/stacklok/mecatl/internal/mcpbroker"
)

func sessionAPIProofAccount(t *testing.T, f *continuityProofFixture) [32]byte {
	t.Helper()
	provider := f.process.providers[0]
	row, err := f.process.authStorage.GetUpstreamTokens(t.Context(), "verified-tsid", provider)
	if err != nil {
		t.Fatal(err)
	}
	row.UserID, row.UpstreamSubject, row.ClientID = "native-user", "native-subject", "upstream-client"
	if err := f.process.authStorage.StoreUpstreamTokens(t.Context(), "verified-tsid", provider, row); err != nil {
		t.Fatal(err)
	}
	account, err := f.process.nativeAccount(t.Context(), "verified-tsid")
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func seedCleanupConnection(t *testing.T) (*SessionAPI, *continuityProofFixture, context.Context, apiRecord) {
	t.Helper()
	api, f, ctx := reviewAPI(t)
	account := sessionAPIProofAccount(t, f)
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
	st.record.Account, st.record.Connected, st.record.Connection = account, true, apiRef()
	st.record.Slots[0] = apiSlotRecord{Sequence: 7, Digest: [32]byte{1}, Catalogue: st.record.Catalogue, Phase: "reserved"}
	st.record.Slots[1] = apiSlotRecord{Sequence: 9, Digest: [32]byte{2}, Catalogue: st.record.Catalogue, Phase: "terminal", Disposition: session.BrokerAttemptUnknown}
	if err := saveAPIRecord(t, api, ctx, st); err != nil {
		t.Fatal(err)
	}
	record := st.record
	delete(api.states, opened.Ref)
	return api, f, ctx, record
}

type cleanupAckRedis struct {
	redis.UniversalClient
	final, landed, armed, blocked bool
}

func (r *cleanupAckRedis) Set(ctx context.Context, key string, value any, ttl time.Duration) *redis.StatusCmd {
	var record apiRecord
	b, ok := value.([]byte)
	if r.armed && ok && json.Unmarshal(b, &record) == nil && !record.Connected && record.Withdrawing != r.final {
		r.armed, r.blocked = false, true
		if r.landed {
			if cmd := r.UniversalClient.Set(ctx, key, value, ttl); cmd.Err() != nil {
				return cmd
			}
		}
		cmd := redis.NewStatusCmd(ctx)
		cmd.SetErr(errors.New("cleanup acknowledgement lost"))
		return cmd
	}
	return r.UniversalClient.Set(ctx, key, value, ttl)
}
func (r *cleanupAckRedis) Get(ctx context.Context, key string) *redis.StringCmd {
	if r.blocked {
		cmd := redis.NewStringCmd(ctx)
		cmd.SetErr(errors.New("cleanup readback unavailable"))
		return cmd
	}
	return r.UniversalClient.Get(ctx, key)
}

func TestSessionAPIConnectionCleanupColdRetryAndAmbiguity(t *testing.T) {
	for _, final := range []bool{false, true} {
		for _, landed := range []bool{false, true} {
			t.Run(map[bool]string{false: "withdrawal", true: "completion"}[final]+map[bool]string{false: "-not-landed", true: "-lost-ack"}[landed], func(t *testing.T) {
				api, f, ctx, old := seedCleanupConnection(t)
				fault := &cleanupAckRedis{UniversalClient: api.redis, final: final, landed: landed, armed: true}
				api.redis = fault
				issued, calls := f.issued.Load(), f.upstreamCalls.Load()
				connection := c.ConnectionRef(old.Connection)
				if _, err := api.DisconnectTools(ctx, old.Ref, connection); err == nil {
					t.Fatal("ambiguous cleanup succeeded")
				}
				st := api.states[old.Ref]
				if st.pendingWrite == nil || st.attachment != nil || st.record.Connection != old.Connection {
					t.Fatal("lost cleanup fence or recovered attachment")
				}
				if !final && (st.record.Custody == nil || st.record.Binding != old.Binding) {
					t.Fatal("lost retry custody/binding")
				}
				if _, err := api.DisconnectTools(ctx, old.Ref, connection); err == nil {
					t.Fatal("retry bypassed unresolved write")
				}
				if _, err := api.BeginEnrollment(ctx, old.Ref); err == nil {
					t.Fatal("enrollment bypassed cleanup ambiguity")
				}
				if f.issued.Load() != issued || f.upstreamCalls.Load() != calls {
					t.Fatal("cleanup recovered/issued/discovered native state")
				}
				fault.blocked = false
				if !landed {
					// Only exact acknowledgement of the outstanding candidate resolves it.
					candidate, _ := json.Marshal(st.pendingWrite)
					if err := fault.UniversalClient.Set(ctx, sessionAPIPrefix+string(old.Ref), candidate, time.Hour).Err(); err != nil {
						t.Fatal(err)
					}
				}
				out, err := api.DisconnectTools(ctx, old.Ref, connection)
				if err != nil || (out != c.Disconnected && out != c.AlreadyDisconnected) {
					t.Fatalf("retry cleanup: %v %v", out, err)
				}
				out, err = api.DisconnectTools(ctx, old.Ref, connection)
				if err != nil || out != c.AlreadyDisconnected {
					t.Fatalf("idempotent cleanup: %v %v", out, err)
				}
				if f.issued.Load() != issued || f.upstreamCalls.Load() != calls || len(f.process.Runtime.sessions) != 0 {
					t.Fatal("cold cleanup activated native state")
				}
				got := api.states[old.Ref].record
				if got.Connected || got.Withdrawing || got.Custody != nil || got.Connection != old.Connection || got.Slots[0].Sequence != 7 || got.Slots[0].Disposition != session.BrokerAttemptNotDispatched || got.Slots[1] != old.Slots[1] {
					t.Fatalf("cleanup changed durable fences: %+v", got)
				}
			})
		}
	}
}

func TestSessionAPIConnectionCleanupNativeFailureRetry(t *testing.T) {
	api, f, ctx, old := seedCleanupConnection(t)
	fault := &cleanupAckRedis{UniversalClient: f.process.custody.client, blocked: true}
	f.process.custody.client = fault
	issued, calls := f.issued.Load(), f.upstreamCalls.Load()
	for range 2 {
		if _, err := api.DisconnectTools(ctx, old.Ref, c.ConnectionRef(old.Connection)); err == nil {
			t.Fatal("native cleanup failure succeeded")
		}
		st := api.states[old.Ref]
		if st.record.Connected || !st.record.Withdrawing || st.record.Custody == nil || st.record.Connection != old.Connection || st.attachment != nil {
			t.Fatal("native cleanup failure lost withdrawal/retry custody")
		}
		if f.issued.Load() != issued || f.upstreamCalls.Load() != calls {
			t.Fatal("failed cleanup recovered native credentials")
		}
	}
	fault.blocked = false
	out, err := api.DisconnectTools(ctx, old.Ref, c.ConnectionRef(old.Connection))
	if err != nil || out != c.Disconnected {
		t.Fatalf("native cleanup retry: %v %v", out, err)
	}
	if f.issued.Load() != issued || f.upstreamCalls.Load() != calls {
		t.Fatal("cleanup retry issued/discovered native credentials")
	}
}

func TestSessionAPIConnectionCleanupChangedRecoveredAccount(t *testing.T) {
	api, f, ctx, old := seedCleanupConnection(t)
	provider := f.process.providers[0]
	row, err := f.process.authStorage.GetUpstreamTokens(ctx, "verified-tsid", provider)
	if err != nil {
		t.Fatal(err)
	}
	row.UpstreamSubject = "changed-subject"
	if err := f.process.authStorage.StoreUpstreamTokens(ctx, "verified-tsid", provider, row); err != nil {
		t.Fatal(err)
	}
	if _, err := api.OpenSession(ctx, &old.Ref); err == nil {
		t.Fatal("changed account recovered as original connection")
	}
	st := api.states[old.Ref]
	if st.attachment != nil || st.record.Connection != old.Connection || st.record.Catalogue != old.Catalogue || st.record.Account != old.Account {
		t.Fatal("changed recovery published authority")
	}
	out, err := api.DisconnectTools(ctx, old.Ref, c.ConnectionRef(old.Connection))
	if err != nil || out != c.Disconnected {
		t.Fatalf("original cleanup after refused recovery: %v %v", out, err)
	}
	if !reflect.DeepEqual(st.record.Slots[1], old.Slots[1]) {
		t.Fatal("cleanup released unknown slot")
	}
}
