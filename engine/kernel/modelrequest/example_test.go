package modelrequest_test

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func ExampleConstruct_andReconstruct() {
	ctx := context.Background()
	limits := codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 8, MaxNodes: 512, MaxCollectionElements: 128}
	policy := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, MaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, MaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxDecodedWork: 1 << 24}

	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	if err != nil {
		fmt.Println(err)
		return
	}
	historyID, err := historyid.ParseHistoryID("HST1111111111111111111111")
	if err != nil {
		fmt.Println(err)
		return
	}
	s, err := session.New(h, historyID, policy)
	if err != nil {
		fmt.Println(err)
		return
	}
	commit, err := s.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer commit.Discard()

	if err := commit.Create(); err != nil {
		fmt.Println(err)
		return
	}
	run, err := commit.StartRun()
	if err != nil {
		fmt.Println(err)
		return
	}
	if err := commit.ReplaceModelBinding(historyID, session.ModelBinding{Route: "example", API: "example-api", Model: "example-model"}); err != nil {
		fmt.Println(err)
		return
	}
	if err := commit.AddEffectiveInput(run, session.EffectiveInput{Inputs: []session.InputItem{{
		Inline: &session.AttributedInput{
			Actor:   "operator",
			Purpose: session.PromptContext,
			Parts:   []session.ContentPart{{Text: new("Summarize the retained context.")}},
			Source:  session.ContentSource{Description: "prepared example input"},
		},
	}}}); err != nil {
		fmt.Println(err)
		return
	}

	view, err := commit.View()
	if err != nil {
		fmt.Println(err)
		return
	}
	built, err := modelrequest.Construct(view, format.ParametersV1{}, limits)
	if err != nil {
		fmt.Println(err)
		return
	}
	requestID, err := commit.AddModelRequest(run, view, session.ModelRequest{Recipe: built.Recipe()})
	if err != nil {
		fmt.Println(err)
		return
	}

	// requestID is provisional until SubmitCommit reports acceptance. Construction
	// and authoring do not invoke a model; execution requires an accepted commitment.
	fmt.Printf("provisional request has run=%t\n", requestID.RunID() == run)
	if _, err := s.SubmitCommit(ctx, commit); err != nil {
		fmt.Println(err)
		return
	}

	reconstructed, err := modelrequest.Reconstruct(s.Snapshot(), requestID, limits)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("accepted request reconstructs=%t\n", reconstructed.CID() == built.Request().CID())
	// Output:
	// provisional request has run=true
	// accepted request reconstructs=true
}
