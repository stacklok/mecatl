package app

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// helmTemplate renders the mecak8s chart with the given extra --set args on top
// of the minimal production-required values (image, redis), skipping the test
// when helm is not on PATH (mirrors deploy/helm/mecak8s/chart_test.go's helm()
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
	}
	args = append(args, extraSet...)
	out, err := exec.Command("helm", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, out)
	}
	return out
}

// TestCallerSeparation_Scenario4_RawDriverIsTenantInaccessible pins AC4.5's
// deployment boundary until ADR 0213 carries caller claims to remote drivers:
// the mecak8s Helm chart, with oidc.enabled=true, admits only the mecak8s agent
// workload to a raw driver and gives a tenant-labelled peer no matching ingress
// rule. The default (oidc disabled) render must carry no such policy at all.
func TestCallerSeparation_Scenario4_RawDriverIsTenantInaccessible(t *testing.T) {
	defaultRendered := helmTemplate(t)
	if bytesContainsNetworkPolicy(defaultRendered) {
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

// bytesContainsNetworkPolicy reports whether any rendered document is a
// NetworkPolicy — used to assert the default (oidc disabled) chart render
// carries none at all (the chart ships no general NetworkPolicy by design).
func bytesContainsNetworkPolicy(rendered []byte) bool {
	for _, doc := range bytes.Split(rendered, []byte("\n---\n")) {
		var probe struct {
			Kind string `json:"kind"`
		}
		if err := yaml.Unmarshal(doc, &probe); err == nil && probe.Kind == "NetworkPolicy" {
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
		if policy.Kind == "NetworkPolicy" {
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
