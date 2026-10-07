package agent

import (
	"reflect"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/governance"
	"github.com/stacklok/mecatl/engine/session"
)

func TestBrokerAuthorityDelegationAttenuatesDiscoveryScope(t *testing.T) {
	for _, finite := range []bool{false, true} {
		parent := authorityParent()
		if finite {
			scope := []string{"Read"}
			parent.BrokerToolScope = &scope
		}
		derived, err := deriveDelegatedAuthority(parent, governance.CapabilitySet{Tools: []string{"Read", "mcp__github__issues"}, RemainingDelegationDepth: 2, FileSystem: true}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"Read", "mcp__github__issues"}
		if finite {
			want = []string{"Read"}
		}
		if derived.BrokerToolScope == nil || !reflect.DeepEqual(*derived.BrokerToolScope, want) {
			t.Fatalf("derived scope: %+v want %v", derived, want)
		}
		child := session.New("child", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "child", Revision: "v1"}, session.Limits{}, time.Unix(1, 0))
		if err := stampDelegatedLabels(child, nil, derived); err != nil {
			t.Fatal(err)
		}
		if _, ok := child.BrokerAccess(); ok {
			t.Fatal("child received parent broker state")
		}
		(*derived.BrokerToolScope)[0] = "mutated"
		bound, _ := child.BoundAuthority()
		if (*bound.BrokerToolScope)[0] != "Read" {
			t.Fatal("child scope alias")
		}
	}
	parent := authorityParent()
	child, err := deriveDelegatedAuthority(parent, governance.CapabilitySet{RemainingDelegationDepth: 2}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if child.BrokerToolScope == nil || *child.BrokerToolScope == nil || len(*child.BrokerToolScope) != 0 {
		t.Fatal("empty delegation became open discovery")
	}
}
