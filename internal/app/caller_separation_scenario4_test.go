package app

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"

	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/adapter/store/jsonlstore"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// helmTemplate renders the mecak8s chart with the given extra --set args on top
// of minimal production values plus the explicit unsafe bypass (this test needs
// an identity-off render), skipping the test when helm is not on PATH (mirrors deploy/helm/mecak8s/chart_test.go's helm()
// helper).
func helmTemplate(t *testing.T, extraSet ...string) []byte {
	t.Helper()
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required for chart render tests")
	}
	args := []string{
		"template", "production", "../../deploy/helm/mecak8s",
		"--set", "image.tag=v0.0.0",
		"--set", "redis.endpoint=redis.example.internal:6380",
		"--set", "redis.credentialsSecret=redis-credentials",
		"--set", "security.allowUnsafeRealProvider=true",
	}
	args = append(args, extraSet...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return out
}

// TestCallerSeparation_Scenario4_RawDriverIngressIsRestrictedToTheAgent pins
// AC4.5's deployment control until ADR 0213 carries caller claims to remote
// drivers: rendered with oidc.enabled=true, the mecak8s Helm chart produces a
// NetworkPolicy admitting only the agent workload to a raw driver, on one port,
// and the default (oidc disabled) render carries no raw-driver policy. The chart's
// general default-deny policy remains enabled in both renders.
//
// This asserts the rendered MANIFEST, not runtime behaviour. It cannot prove a
// peer is blocked — that needs a live cluster with a policy-enforcing CNI — and
// it is not caller enforcement: #368's tenant is an OIDC subject holding a
// token, who is not a cluster peer at all. Caller-level driver enforcement is
// issue #452 / ADR-0213. The test exists so the selector, the single agent-only
// ingress rule, the port, and the chart wiring cannot drift unnoticed.
func TestCallerSeparation_Scenario4_RawDriverIngressIsRestrictedToTheAgent(t *testing.T) {
	defaultRendered := helmTemplate(t)
	if bytesContainsRawDriverNetworkPolicy(defaultRendered) {
		t.Fatal("default (oidc disabled) chart render unexpectedly contains a raw-driver NetworkPolicy")
	}

	rendered := helmTemplate(t, "--set", "oidc.enabled=true", "--set", "oidc.issuer=https://idp.example.com", "--set", "oidc.audience=mecatl")
	policy := decodeRawDriverPolicy(t, rendered)

	wantName := "production-mecak8s-raw-driver"
	if policy.Name != wantName || policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] != "raw-driver" {
		t.Fatalf("raw-driver selector = %#v (name %q), want mecak8s raw-driver pods named %q", policy.Spec.PodSelector, policy.Name, wantName)
	}
	if len(policy.Spec.Ingress) != 1 || len(policy.Spec.Ingress[0].From) != 1 {
		t.Fatalf("raw-driver ingress = %#v, want one agent-only rule", policy.Spec.Ingress)
	}
	peer := policy.Spec.Ingress[0].From[0]
	if peer.NamespaceSelector != nil || peer.PodSelector == nil || peer.PodSelector.MatchLabels["app.kubernetes.io/component"] != "agent" {
		t.Fatalf("raw-driver peer = %#v, want only same-namespace agent pods", peer)
	}
	if len(policy.Spec.Ingress[0].Ports) != 1 || policy.Spec.Ingress[0].Ports[0].Port == nil || policy.Spec.Ingress[0].Ports[0].Port.IntVal != 9090 {
		t.Fatalf("raw-driver ports = %#v, want TCP 9090 only", policy.Spec.Ingress[0].Ports)
	}
}

// bytesContainsRawDriverNetworkPolicy reports whether the rendered manifests
// include the optional raw-driver ingress policy, independent of the chart's
// always-on workload default-deny policy.
func bytesContainsRawDriverNetworkPolicy(rendered []byte) bool {
	for _, doc := range bytes.Split(rendered, []byte("\n---\n")) {
		var policy networkingv1.NetworkPolicy
		if err := yaml.Unmarshal(doc, &policy); err == nil && policy.Kind == "NetworkPolicy" && policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] == "raw-driver" {
			return true
		}
	}
	return false
}

// decodeRawDriverPolicy finds and decodes the raw-driver NetworkPolicy document
// out of a multi-document `helm template` render.
func decodeRawDriverPolicy(t *testing.T, rendered []byte) networkingv1.NetworkPolicy {
	t.Helper()
	for _, doc := range bytes.Split(rendered, []byte("\n---\n")) {
		var policy networkingv1.NetworkPolicy
		if err := yaml.Unmarshal(doc, &policy); err != nil {
			continue
		}
		if policy.Kind == "NetworkPolicy" && policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] == "raw-driver" {
			return policy
		}
	}
	t.Fatal("rendered chart did not contain a NetworkPolicy")
	return networkingv1.NetworkPolicy{}
}

func TestCallerSeparation_Scenario4_SchedulerActorAndOwnerRemainDistinct(t *testing.T) {
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	actor := session.PrincipalFromContext(syscaller.Context(context.Background(), syscaller.RootScheduler))
	fireOwner := fireSessionOwner(owner)

	if actor == nil || actor.GrantType != session.GrantTypeSystem || actor.Subject != string(syscaller.RootScheduler) {
		t.Fatalf("scheduler actor = %+v, want registered scheduler system principal", actor)
	}
	if fireOwner == nil || !fireOwner.SameIdentity(owner) || fireOwner.GrantType != session.GrantTypeClientCredentials {
		t.Fatalf("fire owner = %+v, want Alice's durable identity with automation grant", fireOwner)
	}
	if fireOwner.SameIdentity(actor) {
		t.Fatalf("scheduler actor replaced durable resource owner: actor=%+v owner=%+v", actor, fireOwner)
	}
}

func TestCallerSeparation_Scenario4_OwnerlessCutoverIsObservableAndSafe(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	seed, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	alice := &session.Principal{Issuer: "https://issuer.example", Subject: "alice", GrantType: session.GrantTypeUser}
	bob := &session.Principal{Issuer: alice.Issuer, Subject: "bob", GrantType: session.GrantTypeUser}
	admin := &session.Principal{Issuer: alice.Issuer, Subject: "storage-admin", GrantType: session.GrantTypeUser}
	created := time.Now().Add(-2 * time.Hour)
	legacy := session.New("legacy-ownerless", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, created)
	owned := session.New("alice-owned", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: workspace, Revision: "in-tree-v1"}, session.Limits{}, created)
	if err := owned.RestoreLabels(alice, session.Authority{}); err != nil {
		t.Fatal(err)
	}
	for _, sess := range []*session.Session{legacy, owned} {
		if err := seed.Save(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}
	scheduleStore := seed.ScheduleStore()
	legacySchedule := port.Schedule{
		Spec:  port.ScheduleSpec{Name: "legacy-schedule", Prompt: "legacy", Trigger: port.TriggerSpec{Cron: "0 0 1 1 *"}},
		State: port.ScheduleState{Enabled: true, NextFireAt: time.Now().Add(-time.Hour)},
	}
	ownedSchedule := legacySchedule
	ownedSchedule.Spec.Name = "alice-schedule"
	ownedSchedule.Spec.Owner = alice.Clone()
	ownedSchedule.Spec.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvKindLocal, ID: localDefaultPlacementID, Revision: localDefaultPlacementRevision}
	ownedSchedule.Spec.PlacementScope = string(defaultPlacementScope)
	for _, schedule := range []port.Schedule{legacySchedule, ownedSchedule} {
		if err := scheduleStore.Save(ctx, schedule); err != nil {
			t.Fatal(err)
		}
	}

	waitForInventory := func() {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			_, pageErr := seed.PageSessionMetadata(ctx, port.SessionMetadataPageRequest{Limit: 256})
			if pageErr == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("session inventory did not become ready: %v", pageErr)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	preflight, err := Build(ctx, Config{
		Workspace: workspace, Model: "mock", StoreDir: storeDir, NoSoul: true,
		MockProvider: mockllm.New(mockllm.TextTurn("done")), LocalStorageManagement: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	waitForInventory()
	preflightHealth, err := preflight.Service.StorageHealth(ctx)
	if err != nil {
		preflight.Close()
		t.Fatalf("pre-cutover StorageHealth: %v", err)
	}
	if preflightHealth.Ownerless.SessionCount != 1 || preflightHealth.Ownerless.ScheduleCount != 1 {
		preflight.Close()
		t.Fatalf("pre-cutover ownerless inventory = %+v", preflightHealth.Ownerless)
	}
	preflight.Close()

	cfg := Config{
		Workspace: workspace, Model: "mock", StoreDir: storeDir, NoSoul: true,
		MockProvider: mockllm.New(mockllm.TextTurn("done")), OwnershipEnforced: true,
		StorageManagementPrincipals: []session.Principal{*admin},
		MainRetention:               time.Nanosecond,
		AcknowledgeMainRetention:    true,
		ChildGCInterval:             100 * time.Millisecond,
		SchedulerEnabled:            true,
		SchedulerTickInterval:       100 * time.Millisecond,
	}
	built, err := Build(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	adminCtx := session.WithPrincipal(ctx, admin)
	waitForInventory()
	health, err := built.Service.StorageHealth(adminCtx)
	if err != nil {
		built.Close()
		t.Fatalf("StorageHealth: %v", err)
	}
	if health.Ownerless.SessionCount != 1 || len(health.Ownerless.SessionIDs) != 1 || health.Ownerless.SessionIDs[0] != "legacy-ownerless" {
		built.Close()
		t.Fatalf("ownerless session preflight = %+v", health.Ownerless)
	}
	if health.Ownerless.ScheduleCount != 1 || len(health.Ownerless.ScheduleNames) != 1 || health.Ownerless.ScheduleNames[0] != "legacy-schedule" {
		built.Close()
		t.Fatalf("ownerless schedule preflight = %+v", health.Ownerless)
	}
	for _, caller := range []*session.Principal{alice, bob} {
		if _, err := built.Service.GetSession(session.WithPrincipal(ctx, caller), legacy.ID); !errors.Is(err, server.ErrNotFound) {
			built.Close()
			t.Fatalf("GetSession(ownerless) for %s = %v, want absence", caller.Subject, err)
		}
	}
	deadline := time.Now().Add(time.Second)
	for {
		claimed, loadErr := scheduleStore.Load(ctx, ownedSchedule.Spec.Name)
		if loadErr != nil {
			built.Close()
			t.Fatalf("load owned schedule while waiting for worker: %v", loadErr)
		}
		if claimed.State.FireCount > 0 {
			break
		}
		if time.Now().After(deadline) {
			built.Close()
			t.Fatal("scheduler worker did not process the eligible owned schedule")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for {
		_, loadErr := seed.Load(ctx, owned.ID)
		if errors.Is(loadErr, port.ErrSessionNotFound) {
			break
		}
		if loadErr != nil {
			built.Close()
			t.Fatalf("load owned session while waiting for retention: %v", loadErr)
		}
		if time.Now().After(deadline) {
			built.Close()
			t.Fatal("retention worker did not process the eligible owned session")
		}
		time.Sleep(5 * time.Millisecond)
	}
	built.Close() // joins the retention and scheduler workers

	after, err := jsonlstore.New(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	legacyAfter, err := after.Load(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("ownerless session was mutated/deleted after cutover: %v", err)
	}
	if legacyAfter.Owner != nil || legacyAfter.State != legacy.State || legacyAfter.EnvironmentRef != legacy.EnvironmentRef {
		t.Fatalf("ownerless session changed after cutover: %+v", legacyAfter)
	}
	if _, err := after.Load(ctx, owned.ID); !errors.Is(err, port.ErrSessionNotFound) {
		t.Fatalf("eligible owned session maintenance result = %v, want deletion", err)
	}
	legacyScheduleAfter, err := after.ScheduleStore().Load(ctx, legacySchedule.Spec.Name)
	if err != nil {
		t.Fatalf("ownerless schedule was mutated/deleted after cutover: %v", err)
	}
	if legacyScheduleAfter.Spec.Owner != nil ||
		!legacyScheduleAfter.State.NextFireAt.Equal(legacySchedule.State.NextFireAt) ||
		legacyScheduleAfter.State.FireCount != legacySchedule.State.FireCount ||
		legacyScheduleAfter.State.Enabled != legacySchedule.State.Enabled {
		t.Fatalf("ownerless schedule changed after cutover: %+v", legacyScheduleAfter)
	}

	legacyBuild, err := Build(ctx, Config{
		Workspace: workspace, Model: "mock", StoreDir: storeDir, NoSoul: true,
		MockProvider: mockllm.New(mockllm.TextTurn("done")),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer legacyBuild.Close()
	loaded, err := legacyBuild.Service.GetSession(ctx, legacy.ID)
	if err != nil || loaded.Owner != nil {
		t.Fatalf("ownerless compatibility after disabling enforcement = (%+v, %v)", loaded, err)
	}
}

func TestCallerSeparation_Scenario4_PostureCannotDisableOwnership(t *testing.T) {
	for _, posture := range []Posture{PostureStrict, PostureTrusted, PostureAuto, PostureYolo} {
		t.Run(posture.String(), func(t *testing.T) {
			cfg := applyPosture(Config{Posture: posture, OwnershipEnforced: true})
			if !cfg.OwnershipEnforced {
				t.Fatalf("%s posture disabled caller ownership enforcement", posture)
			}
		})
	}
}
