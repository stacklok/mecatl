package modelrequest

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	historyid "github.com/stacklok/mecatl/engine/kernel/history/id"
	"github.com/stacklok/mecatl/engine/kernel/modelrequest/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func Example_usageAccounting() {
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			panic(err)
		}
	}
	limits := codec.Limits{MaxBytes: 4096, MaxStringBytes: 128, MaxByteStringBytes: 1024, MaxArrayElements: 16, MaxMapPairs: 16, MaxDepth: 16, MaxNodes: 512, MaxCollectionElements: 128}
	policy := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, MaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, MaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxDecodedWork: 1 << 24}
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	must(err)
	historyID := historyid.NewHistoryID()
	s, err := session.New(h, historyID, policy)
	must(err)
	begin := func() *session.SessionCommit {
		b, err := s.BeginCommit()
		must(err)
		return b
	}
	submit := func(b *session.SessionCommit) {
		_, err := s.SubmitCommit(ctx, b)
		must(err)
	}
	show := func(label string) {
		// Rebuild from storage, not a producer-maintained counter or cached sum.
		loaded, err := session.Load(ctx, h, historyID, policy)
		must(err)
		tokens, incomplete, err := knownUsageTokens(loaded.Snapshot().Usages())
		must(err)
		fmt.Printf("%s: known tokens=%d; incomplete reports=%d\n", label, tokens, incomplete)
	}

	b := begin()
	must(b.Create())
	runA, err := b.StartRun()
	must(err)
	must(b.ReplaceModelBinding(historyID, session.ModelBinding{Route: "primary", API: "example", Model: "chat"}))
	input, err := b.AddUserInput(runA, session.UserInput{Parts: []session.ContentPart{{Text: new("Hello")}}})
	must(err)
	must(b.AddEffectiveInput(runA, session.EffectiveInput{Inputs: []session.InputItem{{UserInputID: new(input.RunRelative())}}}))
	capture, err := b.View()
	must(err)
	built, err := Construct(capture, format.ParametersV1{}, limits)
	must(err)
	request, err := b.AddModelRequest(runA, capture, session.ModelRequest{Recipe: built.Recipe()})
	must(err)
	conversation, err := b.AddUsage(nil, &request, session.UsagePurposeMain, session.UsageReport{State: session.UsageReportActive})
	must(err) // Retained work with no measurements is not reported zero.
	submit(b)
	show("declared")

	b = begin()
	must(b.AddModelResponse(request, session.ModelResponse{Terminal: session.ResponseTerminalNormal, Items: []session.ResponseItem{{Content: &session.ResponseContent{Kind: session.ResponseContentOutput, Parts: []session.ContentPart{{Text: new("Hello back")}}}}}}))
	measured := session.UsageReport{State: session.UsageReportActive, Buckets: []session.UsageBucket{
		{SelectedRoute: new("primary"), SelectedModel: new("chat"), Counters: session.UsageCounters{Input: new(int64(100)), Output: new(int64(20)), CacheRead: new(int64(60)), Reasoning: new(int64(5))}},
		{SelectedRoute: new("fallback"), SelectedModel: new("chat-small"), Counters: session.UsageCounters{Input: new(int64(10)), Output: new(int64(2))}},
	}}
	must(b.ReplaceUsage(conversation, measured)) // Disjoint consumption, not total plus breakdown.
	auxiliary, err := b.AddUsage(&runA, nil, "example/title", session.UsageReport{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Input: new(int64(8))}}}})
	must(err) // Producer-defined purpose; missing output is not inferred.
	must(b.EndRun(session.RunEndedCompleted))
	runB, err := b.StartRun()
	must(err)
	submit(b)
	show("measured")

	// A decreasing correction arrives for ended A while B remains active.
	b = begin()
	*measured.Buckets[0].Counters.Input = 90
	must(b.ReplaceUsage(conversation, measured))
	submit(b)
	show("corrected")
	b = begin()
	must(b.ReplaceUsage(auxiliary, session.UsageReport{State: session.UsageReportWithdrawn}))
	submit(b)
	show("withdrawn")
	b = begin()
	must(b.ReplaceUsage(auxiliary, session.UsageReport{State: session.UsageReportActive, Buckets: []session.UsageBucket{{Counters: session.UsageCounters{Input: new(int64(8)), Output: new(int64(1))}}}}))
	submit(b)
	show("restored")

	loaded, err := session.Load(ctx, h, historyID, policy)
	must(err)
	entry, ok := loaded.Snapshot().Usages().ByID(conversation)
	owner, _ := entry.Definition().RunID()
	reconstructed, err := Reconstruct(loaded.Snapshot(), request, limits)
	must(err)
	fmt.Printf("late report still belongs to A=%t; B active=%t; request unchanged=%t\n", ok && owner == runA, loaded.Snapshot().ActiveRun() == runB, reconstructed.CID() == built.Request().CID())
	fmt.Println("Subtotal covers retained active reports only, not unobserved work or spend.")
	// Output:
	// declared: known tokens=0; incomplete reports=1
	// measured: known tokens=140; incomplete reports=1
	// corrected: known tokens=130; incomplete reports=1
	// withdrawn: known tokens=122; incomplete reports=0
	// restored: known tokens=131; incomplete reports=0
	// late report still belongs to A=true; B active=true; request unchanged=true
	// Subtotal covers retained active reports only, not unobserved work or spend.
}

// knownUsageTokens is example-only showback, not a production rollup contract.
// Input/output are inclusive: cache and reasoning breakdowns are never added.
// Incomplete counts active reports with no buckets or any missing input/output.
// Even zero incomplete reports says nothing about work absent from history.
// Overflow returns no partial subtotal or coverage result.
func knownUsageTokens(entries session.UsageEntriesView) (total int64, incomplete int, err error) {
	for _, entry := range entries.All() {
		report := entry.Report()
		if report.State() == session.UsageReportWithdrawn {
			continue
		}
		missing := report.Buckets().Len() == 0
		for _, bucket := range report.Buckets().All() {
			input, hasInput := bucket.Counters().Input()
			output, hasOutput := bucket.Counters().Output()
			missing = missing || !hasInput || !hasOutput
			for _, count := range [...]int64{input, output} {
				if count > math.MaxInt64-total {
					return 0, 0, errors.New("known token subtotal overflows int64")
				}
				total += count
			}
		}
		if missing {
			incomplete++
		}
	}
	return total, incomplete, nil
}
