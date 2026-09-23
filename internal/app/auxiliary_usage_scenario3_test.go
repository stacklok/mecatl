package app

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/session"
)

type auxiliaryBranchJudge struct {
	usage session.AuxiliaryUsage
}

func (j auxiliaryBranchJudge) Judge(context.Context, []agent.BranchSummary, string) (int, string, session.AuxiliaryUsage, error) {
	return 0, "best", j.usage, nil
}

func TestAuxiliaryTokenUsage_Scenario3_ParallelJudgeRecordsInheritedModel(t *testing.T) {
	usage := session.Usage{InputTokens: 8, OutputTokens: 2}
	producer := session.AuxiliaryUsage{Buckets: map[session.UsageKind]session.TokenUsage{
		session.UsageKindParallelJudge: {Total: usage, Models: map[string]session.Usage{"unknown": usage}},
	}}
	judge := attributedBranchJudge{
		inner:    auxiliaryBranchJudge{usage: producer},
		identity: session.ProviderModelID{ProviderID: "provider-p", ModelID: "inherited-model"},
	}

	winner, _, got, err := judge.Judge(t.Context(), []agent.BranchSummary{{Label: "one"}, {Label: "two"}}, "best")
	if err != nil || winner != 0 {
		t.Fatalf("Judge() winner=%d err=%v", winner, err)
	}
	bucket := got.Buckets[session.UsageKindParallelJudge]
	if modelUsage := bucket.Models["provider-p/inherited-model"]; modelUsage != usage {
		t.Fatalf("parallel judge attribution = %+v, want inherited model usage %+v", bucket, usage)
	}
	if len(bucket.Models) != 1 {
		t.Fatalf("parallel judge retained non-inherited attribution: %+v", bucket.Models)
	}
}
