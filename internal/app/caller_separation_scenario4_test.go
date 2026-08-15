package app

import (
	"context"
	"os"
	"strings"
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	"sigs.k8s.io/yaml"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/syscaller"
)

// TestCallerSeparation_Scenario4_RawDriverIsTenantInaccessible pins AC4.5's
// deployment boundary until ADR 0213 carries caller claims to remote drivers:
// the OIDC overlay admits only the mecak8s agent workload to a raw driver and
// gives a tenant-labelled peer no matching ingress rule.
func TestCallerSeparation_Scenario4_RawDriverIsTenantInaccessible(t *testing.T) {
	policyBytes, err := os.ReadFile("../../deploy/mecak8s-oidc/raw-driver-networkpolicy.yaml")
	if err != nil {
		t.Fatalf("read raw-driver NetworkPolicy: %v", err)
	}
	var policy networkingv1.NetworkPolicy
	if err := yaml.Unmarshal(policyBytes, &policy); err != nil {
		t.Fatalf("decode raw-driver NetworkPolicy: %v", err)
	}
	if policy.Name != "mecak8s-raw-driver" || policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/component"] != "raw-driver" {
		t.Fatalf("raw-driver selector = %#v, want mecak8s raw-driver pods", policy.Spec.PodSelector)
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

	overlay, err := os.ReadFile("../../deploy/mecak8s-oidc/kustomization.yaml")
	if err != nil {
		t.Fatalf("read OIDC kustomization: %v", err)
	}
	if !strings.Contains(string(overlay), "- raw-driver-networkpolicy.yaml") {
		t.Fatal("OIDC overlay does not install the raw-driver NetworkPolicy")
	}
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
