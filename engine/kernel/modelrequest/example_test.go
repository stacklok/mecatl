package modelrequest_test

import (
	"context"
	"fmt"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
	sessionid "github.com/stacklok/mecatl/engine/kernel/session/id"
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
	sessionID, err := sessionid.ParseSessionID("SES1111111111111111111111")
	if err != nil {
		fmt.Println(err)
		return
	}
	config := session.Config{Policy: policy, InstanceID: 1}
	s, err := session.New(h, sessionID, config)
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
	if err := commit.ReplaceModelBinding(sessionID, session.ModelBinding{Route: "example", API: "example-api", Model: "example-model"}); err != nil {
		fmt.Println(err)
		return
	}
	if err := commit.ReplaceUserInstructions(sessionID, session.UserInstructions{Blocks: []session.InstructionBlock{
		{Key: "operator/persona", Parts: []session.ContentPart{{Text: new("original persona")}}, Source: session.ContentSource{Description: "prepared persona", WorkspaceFile: &session.WorkspaceFileSource{Path: "SOUL.md"}}},
		{Key: "workspace/root-guidance", Parts: []session.ContentPart{{Text: new("retained project guidance")}}, Source: session.ContentSource{Description: "adopted project guidance", WorkspaceFile: &session.WorkspaceFileSource{Path: "AGENTS.md"}}},
	}}); err != nil {
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

	// Finish the original run before resuming. This example records a synthetic
	// response; neither construction nor reconstruction invokes a model.
	finish, err := s.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer finish.Discard()
	if err := finish.AddModelResponse(requestID, session.ModelResponse{Terminal: session.ResponseTerminalNormal}); err != nil {
		fmt.Println(err)
		return
	}
	if err := finish.EndRun(session.RunEndedCompleted); err != nil {
		fmt.Println(err)
		return
	}
	if _, err := s.SubmitCommit(ctx, finish); err != nil {
		fmt.Println(err)
		return
	}

	// Load restores retained facts. It does not reopen SOUL.md or AGENTS.md.
	resumed, err := session.Load(ctx, h, sessionID, config)
	if err != nil {
		fmt.Println(err)
		return
	}
	before := resumed.Snapshot()
	standing, present := before.UserInstructions()
	if !present {
		fmt.Println("missing standing user instructions")
		return
	}
	updated := standing.MutableClone()
	found := false
	for i := range updated.Blocks {
		if updated.Blocks[i].Key == "operator/persona" {
			// Harness policy supplies newly prepared bytes and provenance. The key
			// survives a source change; unrelated contributions retain their order.
			updated.Blocks[i] = session.InstructionBlock{
				Key: "operator/persona", Parts: []session.ContentPart{{Text: new("refreshed persona")}},
				Source: session.ContentSource{Description: "persona prepared by resume policy"},
			}
			found = true
			break
		}
	}
	if !found {
		fmt.Println("missing persona contribution")
		return
	}
	refresh, err := resumed.BeginCommit()
	if err != nil {
		fmt.Println(err)
		return
	}
	defer refresh.Discard()
	if err := refresh.ReplaceUserInstructions(sessionID, updated); err != nil {
		fmt.Println(err)
		return
	}
	if _, err := resumed.SubmitCommit(ctx, refresh); err != nil {
		fmt.Println(err)
		return
	}
	after, _ := resumed.Snapshot().UserInstructions()
	oldPersona, _ := standing.Blocks().At(0).Parts().At(0).Text()
	newPersona, _ := after.Blocks().At(0).Parts().At(0).Text()
	project, _ := after.Blocks().At(1).Parts().At(0).Text()
	fmt.Printf("persona: %s -> %s\n", oldPersona, newPersona)
	fmt.Printf("project: %s\n", project)
	historical, err := modelrequest.Reconstruct(resumed.Snapshot(), requestID, limits)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("historical request unchanged=%t\n", historical.CID() == built.Request().CID())
	// Output:
	// provisional request has run=true
	// accepted request reconstructs=true
	// persona: original persona -> refreshed persona
	// project: retained project guidance
	// historical request unchanged=true
}
