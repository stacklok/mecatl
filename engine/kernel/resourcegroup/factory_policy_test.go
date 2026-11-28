package resourcegroup_test

import (
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/resourcegroup"
)

func validPolicy() resourcegroup.FactoryPolicy {
	return resourcegroup.FactoryPolicy{
		History: history.Policy{
			CommitLimits: codec.Limits{
				MaxBytes: 1, MaxArrayElements: 1, MaxStringBytes: 1,
				MaxByteStringBytes: 1, MaxMapPairs: 1, MaxDepth: 1,
				MaxNodes: 1, MaxCollectionElements: 1,
			},
			RetryWindow: 1, MaxPageBytes: 1, MaxPageCommits: 1,
		},
		MaxInstances: 1,
	}
}

func TestFactoryPolicyValidate(t *testing.T) {
	if err := validPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*resourcegroup.FactoryPolicy){
		"zero instances":     func(p *resourcegroup.FactoryPolicy) { p.MaxInstances = 0 },
		"negative instances": func(p *resourcegroup.FactoryPolicy) { p.MaxInstances = -1 },
		"zero history":       func(p *resourcegroup.FactoryPolicy) { p.History = history.Policy{} },
		"invalid limits":     func(p *resourcegroup.FactoryPolicy) { p.History.CommitLimits.MaxBytes = -1 },
		"invalid retry":      func(p *resourcegroup.FactoryPolicy) { p.History.RetryWindow = 0 },
		"invalid page bytes": func(p *resourcegroup.FactoryPolicy) { p.History.MaxPageBytes = 0 },
		"invalid page count": func(p *resourcegroup.FactoryPolicy) { p.History.MaxPageCommits = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			p := validPolicy()
			mutate(&p)
			if err := p.Validate(); err != resourcegroup.ErrInvalidPolicy {
				t.Fatalf("Validate() = %v, want ErrInvalidPolicy", err)
			}
		})
	}
	if err := (resourcegroup.FactoryPolicy{}).Validate(); err != resourcegroup.ErrInvalidPolicy {
		t.Fatalf("zero Validate() = %v", err)
	}
}
