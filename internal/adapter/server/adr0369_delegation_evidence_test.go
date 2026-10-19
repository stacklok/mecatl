package server

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/session"
)

func TestDelegationEvidenceProtoContract(t *testing.T) {
	for _, tc := range []struct {
		name     string
		message  interface{ ProtoReflect() protoreflect.Message }
		provider protoreflect.FieldNumber
		explicit protoreflect.FieldNumber
		child    protoreflect.FieldNumber
	}{
		{name: "Subagent", message: &mecatlv1.Subagent{}, provider: 21, explicit: 22, child: 20},
		{name: "TeamMemberSpec", message: &mecatlv1.TeamMemberSpec{}, provider: 10, explicit: 11},
		{name: "Parallel", message: &mecatlv1.Parallel{}, provider: 28, explicit: 29, child: 27},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := tc.message.ProtoReflect().Descriptor().Fields()
			if tc.child != 0 {
				child := fields.ByName("child_tool_call_id")
				if child == nil || child.Number() != tc.child || child.JSONName() != "childToolCallId" {
					t.Fatalf("child_tool_call_id descriptor = %v, want published field %d", child, tc.child)
				}
			}
			provider := fields.ByName("provider")
			if provider == nil || provider.Number() != tc.provider || provider.JSONName() != "provider" {
				t.Fatalf("provider descriptor = %v, want string field %d", provider, tc.provider)
			}
			explicit := fields.ByName("explicit_router_category")
			if explicit == nil || explicit.Number() != tc.explicit || explicit.JSONName() != "explicitRouterCategory" {
				t.Fatalf("explicit_router_category descriptor = %v, want string field %d", explicit, tc.explicit)
			}
		})
	}
}

func TestDelegationEvidenceMapperExplicitSelection(t *testing.T) {
	const (
		provider        = "anthropic"
		model           = "claude-opus-4-1"
		category        = "deep"
		forbiddenAlias  = "operator-secret-alias"
		forbiddenConfig = "credential-shaped-config-secret"
	)

	subagent := toProtoSubagent(session.SubagentPayload{
		Provider: provider, Model: model, ExplicitRouterCategory: category,
		ChildIncarnation: session.IncarnationID(forbiddenAlias + forbiddenConfig),
	})
	parallel := toProtoParallel(session.ParallelPayload{
		Provider: provider, Model: model, ExplicitRouterCategory: category,
		ChildIncarnation: session.IncarnationID(forbiddenAlias + forbiddenConfig),
	})
	team := toProtoTeam(session.TeamPayload{Roster: []session.TeamMemberSpec{{
		Name: "reviewer", Provider: provider, Model: model, ExplicitRouterCategory: category,
		MemberIncarnation: session.IncarnationID(forbiddenAlias + forbiddenConfig),
	}}}).GetRoster()[0]

	for name, got := range map[string]interface {
		proto.Message
		GetProvider() string
		GetModel() string
		GetExplicitRouterCategory() string
		GetRoutedCategory() string
		GetRoutedModel() string
		GetRoutingReason() string
		GetRoutingDecision() *mecatlv1.RoutingDecision
	}{"subagent": subagent, "parallel": parallel, "team": team} {
		t.Run(name, func(t *testing.T) {
			if got.GetProvider() != provider || got.GetModel() != model || got.GetExplicitRouterCategory() != category {
				t.Fatalf("actual selection = %q/%q explicit %q", got.GetProvider(), got.GetModel(), got.GetExplicitRouterCategory())
			}
			if got.GetRoutedCategory() != "" || got.GetRoutedModel() != "" || got.GetRoutingReason() != "" || got.GetRoutingDecision() != nil {
				t.Fatalf("explicit selection leaked classifier evidence: %#v", got)
			}
			wire, err := protojson.Marshal(got)
			if err != nil {
				t.Fatalf("protojson: %v", err)
			}
			json := string(wire)
			if !strings.Contains(json, `"provider":"`+provider+`"`) || !strings.Contains(json, `"explicitRouterCategory":"`+category+`"`) {
				t.Fatalf("JSON names or values missing: %s", json)
			}
			for _, forbidden := range []string{forbiddenAlias, forbiddenConfig, "routedCategory", "routedModel", "routingReason", "routingDecision"} {
				if strings.Contains(json, forbidden) {
					t.Fatalf("JSON leaked absent/private %q: %s", forbidden, json)
				}
			}
		})
	}
}
