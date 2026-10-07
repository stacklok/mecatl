package mcpbrokerserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type sessionProductionRedis struct {
	redis.UniversalClient
	closed  atomic.Int32
	unready atomic.Bool
}

func (r *sessionProductionRedis) Options() *redis.Options {
	return r.UniversalClient.(*redis.Client).Options()
}

func (r *sessionProductionRedis) Close() error {
	r.closed.Add(1)
	return r.UniversalClient.Close()
}
func (r *sessionProductionRedis) Ping(ctx context.Context) *redis.StatusCmd {
	if r.unready.Load() {
		cmd := redis.NewStatusCmd(ctx)
		cmd.SetErr(errors.New("fixture unavailable"))
		return cmd
	}
	return r.UniversalClient.Ping(ctx)
}

func TestSessionAPIProductionAuthenticationRegistrationAndLifecycle(t *testing.T) {
	issuer := newIdentityFixture(t)
	store := miniredis.RunT(t)
	key := filepath.Join(t.TempDir(), "kek")
	if err := os.WriteFile(key, bytes.Repeat([]byte{0x42}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("offline-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	var clients []*sessionProductionRedis
	jwt := productionOIDC(issuer, time.Minute)
	jwt.AllowedSubjects = append(jwt.AllowedSubjects, "other-allowed-workload")
	cfg := ProductionConfig{
		PublicAddress: "127.0.0.1:0", AdminAddress: "127.0.0.1:0",
		TLSConfig:   &tls.Config{Certificates: issuer.server.TLS.Certificates, MinVersion: tls.VersionTLS12},
		WorkloadJWT: jwt, DrainTimeout: time.Second,
		SessionAPI: &SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-a"},
		ToolHive: mcpbroker.ToolHiveConfig{CallbackURL: "https://broker.example/callback", Profiles: []mcpbroker.ToolHiveProfile{{
			Name: "fixture", URL: "https://upstream.example/mcp", Auth: "oauth",
			OAuth:  &mcpbroker.ToolHiveOAuth{AuthorizationEndpoint: "https://identity.example/authorize", TokenEndpoint: "https://identity.example/token", ClientID: "fixture", ClientSecretFile: secret},
			Static: []mcpbroker.StaticTool{{Name: "read", Schema: json.RawMessage(`{"type":"object"}`), ReadOnly: true}},
		}}, ProtectedStorage: &mcpbroker.ProtectedStorageConfig{
			Redis: mcpbroker.ProtectedRedisConfig{Client: func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
				r := &sessionProductionRedis{UniversalClient: redis.NewClient(&redis.Options{Addr: store.Addr()})}
				clients = append(clients, r)
				return r, nil
			}, ClientConfig: mcpbroker.ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second},
			Encryption: mcpbroker.ProtectedEncryptionConfig{ActiveID: "active", Keys: []mcpbroker.ProtectedEncryptionKey{{ID: "active", File: key}}},
		}},
	}
	start := func() (*Lifecycle, p.SessionServiceClient, *grpc.ClientConn) {
		t.Helper()
		l, err := NewProduction(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		l.Start()
		t.Cleanup(func() {
			if err := l.Close(context.Background()); err != nil {
				t.Error(err)
			}
		})
		roots := x509.NewCertPool()
		roots.AppendCertsFromPEM(issuer.caPEM())
		conn, err := grpc.NewClient(l.PublicAddress(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12})))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return l, p.NewSessionServiceClient(conn), conn
	}
	valid := authContext(issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil))
	l, rpc, donor := start()
	if !l.Ready(t.Context()) || len(clients) != 1 {
		t.Fatal("not ready or more than one Redis client")
	}
	for name, tc := range map[string]struct {
		ctx  context.Context
		code codes.Code
	}{
		"missing":        {t.Context(), codes.Unauthenticated},
		"wrong-subject":  {authContext(issuer.tokenWithSubject(t, issuer.server.URL, testAudience, "not-allowed", time.Now().Add(time.Minute), nil)), codes.PermissionDenied},
		"wrong-audience": {authContext(issuer.token(t, issuer.server.URL, "wrong", time.Now().Add(time.Minute), nil)), codes.Unauthenticated},
		"wrong-issuer":   {authContext(issuer.token(t, "https://wrong.example", testAudience, time.Now().Add(time.Minute), nil)), codes.Unauthenticated},
		"forged-owner":   {metadata.AppendToOutgoingContext(valid, "x-mecatl-owner-assertion", "forged"), codes.InvalidArgument},
		"empty-owner":    {metadata.AppendToOutgoingContext(valid, "owner", ""), codes.InvalidArgument},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := rpc.OpenSession(tc.ctx, &p.OpenSessionRequest{}); status.Code(err) != tc.code {
				t.Fatalf("got %v want %v", err, tc.code)
			}
		})
	}
	opened, err := rpc.OpenSession(valid, &p.OpenSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := l.broker.verifier.Validate(t.Context(), issuer.token(t, issuer.server.URL, testAudience, time.Now().Add(time.Minute), nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.broker.sessionRPC.OpenSession(session.WithPrincipal(t.Context(), verified), &p.OpenSessionRequest{}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("missing independent verified-workload context: %v", err)
	}
	var record struct{ Owner, Workload [32]byte }
	stored, err := clients[0].Get(t.Context(), "mecatl:poc:broker-session:v1:"+opened.Ref).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(stored, &record); err != nil {
		t.Fatal(err)
	}
	verifiedCtx := context.WithValue(t.Context(), verifiedSessionWorkloadKey{}, verified)
	wantOwner, err := ownerlessPartition("offline-a")(verifiedCtx)
	if err != nil {
		t.Fatal(err)
	}
	wantWorkload, err := contract.ContinuityPrincipalPartition(contract.ContinuityPartitionWorkload, verified)
	if err != nil {
		t.Fatal(err)
	}
	if record.Owner != wantOwner || record.Workload != wantWorkload || record.Owner == record.Workload {
		t.Fatal("actual RPC saved incorrect owner/workload partitions")
	}
	inspect := &p.InspectConnectorsRequest{SessionRef: opened.Ref, CatalogueRef: opened.Catalogue.Ref}
	inventory, err := rpc.InspectConnectors(valid, inspect)
	if err != nil || inventory.Availability != "unavailable" || inventory.EnrollmentState != "unknown" || inventory.TotalConnectors != 1 {
		t.Fatalf("TLS status: %v %v", inventory, err)
	}
	if _, err := rpc.InspectConnectors(t.Context(), inspect); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous status: %v", err)
	}
	if _, err := l.broker.sessionRPC.InspectConnectors(session.WithPrincipal(t.Context(), verified), inspect); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("unverified workload status: %v", err)
	}
	reopen := &p.OpenSessionRequest{SavedRef: &opened.Ref}
	if _, err := rpc.OpenSession(valid, reopen); err != nil {
		t.Fatal(err)
	}
	other := authContext(issuer.tokenWithSubject(t, issuer.server.URL, testAudience, "other-allowed-workload", time.Now().Add(time.Minute), nil))
	if _, err := rpc.OpenSession(other, reopen); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign workload: %v", err)
	}
	if _, err := rpc.InspectConnectors(other, inspect); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign workload status: %v", err)
	}
	// The legacy surface must not provide a second route to the same native IDs.
	for _, ctx := range []context.Context{valid, other} {
		for _, method := range []string{"Attach", "Execute", "Delete"} {
			if err := donor.Invoke(ctx, "/mecatl.broker.v1.BrokerService/"+method, &p.OpenSessionRequest{}, &p.SessionSnapshot{}); status.Code(err) != codes.Unimplemented {
				t.Fatalf("legacy %s: %v", method, err)
			}
		}
	}
	clients[0].unready.Store(true)
	if l.Ready(t.Context()) {
		t.Fatal("Redis failure left listener ready")
	}
	clients[0].unready.Store(false)
	l.BeginDrain()
	if l.Ready(t.Context()) {
		t.Fatal("draining listener ready")
	}
	if _, err := rpc.OpenSession(valid, reopen); status.Code(err) != codes.Unavailable {
		t.Fatalf("drain admission: %v", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if clients[0].closed.Load() != 1 {
		t.Fatal("native Redis client not closed exactly once")
	}
	if _, err := l.broker.sessionRPC.OpenSession(session.WithPrincipal(t.Context(), &session.Principal{Subject: "forged"}), reopen); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("closed facade: %v", err)
	}
	// A declared different deployment cannot adopt an otherwise valid saved ref.
	cfg.SessionAPI = &SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-b"}
	l2, rpc2, _ := start()
	if _, err := rpc2.OpenSession(valid, reopen); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign deployment: %v", err)
	}
	if _, err := rpc2.InspectConnectors(valid, inspect); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("foreign deployment status: %v", err)
	}
	if err := l2.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.SessionAPI = &SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-a"}
	l3, rpc3, _ := start()
	cold, err := rpc3.InspectConnectors(valid, inspect)
	if err != nil || cold.Availability != "unavailable" || cold.EnrollmentState != "unknown" || cold.Connectors[0].CatalogueState != "unknown" || cold.Connectors[0].ToolCount != 0 {
		t.Fatalf("TLS cold status: %v %v", cold, err)
	}
	if _, err := rpc3.OpenSession(valid, reopen); err != nil {
		t.Fatalf("same deployment replacement: %v", err)
	}
	if err := l3.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	cfg.SessionAPI = nil
	if fallback, err := NewProduction(t.Context(), cfg); err == nil {
		_ = fallback.Close(context.Background())
		t.Fatal("missing session configuration selected a legacy fallback")
	}
}

func TestSessionAPIOwnerlessResolverDoesNotUseDonorPrincipal(t *testing.T) {
	principal := &session.Principal{Subject: "workload", Issuer: "https://issuer.example"}
	ctx := session.WithPrincipal(t.Context(), principal)
	if _, err := ownerlessPartition("a")(ctx); err == nil {
		t.Fatal("unverified donor principal accepted")
	}
	ctx = context.WithValue(ctx, verifiedSessionWorkloadKey{}, principal)
	owner, err := ownerlessPartition("a")(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workload, err := contract.ContinuityPrincipalPartition(contract.ContinuityPartitionWorkload, principal)
	if err != nil {
		t.Fatal(err)
	}
	donorOwner, err := contract.ContinuityPrincipalPartition(contract.ContinuityPartitionOwner, principal)
	if err != nil {
		t.Fatal(err)
	}
	if owner == workload || owner == donorOwner {
		t.Fatal("identity domains conflated")
	}
}

func TestSessionAPIMetadataRedisStartupRollback(t *testing.T) {
	issuer := newIdentityFixture(t)
	store := miniredis.RunT(t)
	client := &sessionProductionRedis{UniversalClient: redis.NewClient(&redis.Options{Addr: store.Addr()})}
	client.unready.Store(true)
	cfg := ProductionConfig{
		PublicAddress: "127.0.0.1:0", AdminAddress: "127.0.0.1:0", DrainTimeout: time.Second,
		TLSConfig:   &tls.Config{Certificates: issuer.server.TLS.Certificates, MinVersion: tls.VersionTLS12},
		WorkloadJWT: productionOIDC(issuer, time.Minute),
		SessionAPI:  &SessionAPIConfig{Mode: "OWNERLESS", Deployment: "rollback"},
		ToolHive:    mcpbroker.ToolHiveConfig{Profiles: []mcpbroker.ToolHiveProfile{{Name: "public", URL: "https://offline.example/mcp", Auth: "none"}}},
		SessionMetadataRedis: &mcpbroker.ProtectedRedisConfig{
			Client:       func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) { return client, nil },
			ClientConfig: mcpbroker.ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second,
		},
	}
	if lifecycle, err := NewProduction(t.Context(), cfg); err == nil {
		_ = lifecycle.Close(context.Background())
		t.Fatal("unhealthy metadata Redis admitted")
	}
	if client.closed.Load() != 1 {
		t.Fatal("startup rollback did not close metadata client exactly once")
	}
}
