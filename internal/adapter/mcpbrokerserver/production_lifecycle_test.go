package mcpbrokerserver

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	p "github.com/stacklok/mecatl/contracts/gen/go/mecatl/broker/v1"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	contract "github.com/stacklok/mecatl/internal/mcpbroker"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func anonymousProductionConfig(t *testing.T, issuer *identityFixture) ProductionConfig {
	t.Helper()
	store := miniredis.RunT(t)
	return ProductionConfig{
		PublicAddress: "127.0.0.1:0", AdminAddress: "127.0.0.1:0",
		TLSConfig:   &tls.Config{Certificates: issuer.server.TLS.Certificates, MinVersion: tls.VersionTLS12},
		WorkloadJWT: productionOIDC(issuer, time.Minute),
		SessionAPI:  &SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-lifecycle"},
		ToolHive:    mcpbroker.ToolHiveConfig{Profiles: []mcpbroker.ToolHiveProfile{{Name: "public", URL: "https://offline.example/mcp", Auth: "none"}}},
		SessionMetadataRedis: &mcpbroker.ProtectedRedisConfig{
			Client: func(mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
				return redis.NewClient(&redis.Options{Addr: store.Addr()}), nil
			},
			ClientConfig: mcpbroker.ProtectedRedisClientConfig{TLS: true}, HealthTimeout: time.Second,
		},
		PropagationWait: time.Millisecond, DrainTimeout: time.Second,
	}
}

func TestSessionAPIProductionExplicitEmptyProfiles(t *testing.T) {
	issuer := newIdentityFixture(t)
	cfg := anonymousProductionConfig(t, issuer)
	cfg.ToolHive.Profiles = []mcpbroker.ToolHiveProfile{}
	missingRedis := cfg
	missingRedis.SessionMetadataRedis = nil
	if l, err := NewProduction(t.Context(), missingRedis); err == nil {
		_ = l.Close(context.Background())
		t.Fatal("empty profiles bypassed metadata Redis requirement")
	}
	l, err := NewProduction(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := l.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	l.Start()
	if !l.Ready(t.Context()) {
		t.Fatal("empty production runtime not ready")
	}
	ctx := context.WithValue(t.Context(), verifiedSessionWorkloadKey{}, &session.Principal{Issuer: "test", Subject: "workload"})
	opened, err := l.broker.sessionRPC.OpenSession(ctx, &p.OpenSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Catalogue.Tools) != 0 {
		t.Fatal("empty selection exposed tools")
	}
	reopened, err := l.broker.sessionRPC.OpenSession(ctx, &p.OpenSessionRequest{SavedRef: &opened.Ref})
	if err != nil || reopened.Ref != opened.Ref || reopened.Catalogue.Ref != opened.Catalogue.Ref {
		t.Fatalf("unstable empty open: %v", err)
	}
	out, err := l.broker.sessionRPC.BeginEnrollment(ctx, &p.BeginEnrollmentRequest{SessionRef: opened.Ref})
	if status.Code(err) != codes.FailedPrecondition || out != nil {
		t.Fatalf("empty enrollment must remain unavailable without a browser: %v, %v", out, err)
	}
	reopened, err = l.broker.sessionRPC.OpenSession(ctx, &p.OpenSessionRequest{SavedRef: &opened.Ref})
	if err != nil || reopened.Catalogue.Ref != opened.Catalogue.Ref || len(reopened.Catalogue.Tools) != 0 {
		t.Fatalf("enrollment changed idle catalogue: %v", err)
	}
	if err := l.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	replacement, err := NewProduction(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	l = replacement
	l.Start()
	reopened, err = l.broker.sessionRPC.OpenSession(ctx, &p.OpenSessionRequest{SavedRef: &opened.Ref})
	if err != nil || reopened.Ref != opened.Ref || reopened.Catalogue.Ref == "" || len(reopened.Catalogue.Tools) != 0 {
		t.Fatalf("restart lost stable session or exposed tools: %v", err)
	}
	foreign := context.WithValue(t.Context(), verifiedSessionWorkloadKey{}, &session.Principal{Issuer: "test", Subject: "foreign"})
	if _, err := l.broker.sessionRPC.OpenSession(foreign, &p.OpenSessionRequest{SavedRef: &opened.Ref}); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("empty session disclosed to another workload: %v", err)
	}
}

func TestProductionLifecycleIsReadyWithAnIdleToolHiveRuntime(t *testing.T) {
	issuer := newIdentityFixture(t)
	lifecycle, err := NewProduction(t.Context(), anonymousProductionConfig(t, issuer))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Start()
	if !lifecycle.Ready(t.Context()) {
		t.Fatal("idle production lifecycle never became ready")
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := lifecycle.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

func TestProductionLifecycleKeepsCredentialContinuityInternal(t *testing.T) {
	issuer := newIdentityFixture(t)
	lifecycle, err := NewProduction(t.Context(), anonymousProductionConfig(t, issuer))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := lifecycle.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if _, ok := lifecycle.broker.service.(contract.CredentialContinuityService); !ok {
		t.Fatal("native process lost credential continuity")
	}
	services := lifecycle.broker.grpcServer.GetServiceInfo()
	if len(services) != 1 || services["mecatl.broker.v1.SessionService"].Methods == nil {
		t.Fatalf("unexpected public services: %v", services)
	}
}

func TestSingletonBrokerRemediation_Scenario5_ProductionLifecycleUsesSharedFactory(t *testing.T) {
	issuer := newIdentityFixture(t)
	cfg := anonymousProductionConfig(t, issuer)
	cfg.RuntimeLimits = mcpbroker.Limits{MaxLogicalSessions: 1, LogicalRetention: time.Hour, SweepInterval: time.Minute, MaxPendingStates: 1}
	lifecycle, err := NewProduction(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle.Start()
	if !lifecycle.Ready(t.Context()) {
		t.Fatal("shared production lifecycle never became ready")
	}
	// Capacity remains a native runtime concern, not a public attachment RPC.
	principalCtx := session.WithPrincipal(t.Context(), &session.Principal{Subject: "runtime-limit-test"})
	if _, _, err := lifecycle.broker.service.AttachSession(principalCtx, "runtime-one"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := lifecycle.broker.service.AttachSession(principalCtx, "runtime-two"); !errors.Is(err, contract.ErrCapacity) {
		t.Fatalf("native runtime capacity: %v", err)
	}
	closeCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := lifecycle.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}
