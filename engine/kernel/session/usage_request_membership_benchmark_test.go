package session

import (
	"strconv"
	"testing"
)

// usageRequestMembershipFixture builds accepted run prefixes outside measurement.
// It selects a request near the end of the prefix, in the penultimate run when
// available, so a historical scan would grow with the prefix rather than stop at
// the first record. That request resolves through final counts once superseded.
func usageRequestMembershipFixture(b *testing.B, runs int) (*Session, ModelRequestID) {
	b.Helper()
	s := interactionFixture(b, "pending")
	latest := interactionRequest(s, 1).ID
	target := latest
	for range runs - 1 {
		target = latest
		batch := beginBatch(b, s)
		mustBatch(b, batch.EndRun(RunEndedInterrupted))
		run := batchRun(b, batch)
		var err error
		latest, err = batch.AddModelRequest(run, captureCommit(b, batch), ModelRequest{Recipe: interactionRequest(s, 1).Recipe})
		mustBatch(b, err)
		submitBatch(b, s, batch)
	}
	return s, target
}

// BenchmarkUsageRequestMembership measures the real AddUsage admission seam.
// The accepted prefix is fixed before timing; the referenced request is near its
// end, while the admitted report stays constant as the prefix grows.
func BenchmarkUsageRequestMembership(b *testing.B) {
	for _, runs := range []int{1, 32, 512} {
		b.Run("accepted-prefix-"+strconv.Itoa(runs), func(b *testing.B) {
			s, target := usageRequestMembershipFixture(b, runs)
			report := UsageReport{State: UsageReportActive}
			b.ReportAllocs()
			for b.Loop() {
				batch := beginBatch(b, s)
				_, err := batch.AddUsage(nil, &target, UsagePurposeMain, report)
				interactionCheck(b, err)
				interactionCheck(b, batch.Discard())
			}
		})
	}
}

// BenchmarkUsageRequestMembershipBatches measures growing report batches at
// the same real AddUsage seam. The historical prefix remains outside timing.
func BenchmarkUsageRequestMembershipBatches(b *testing.B) {
	for _, reports := range []int{1, 16, 128} {
		b.Run("reports-"+strconv.Itoa(reports), func(b *testing.B) {
			s, target := usageRequestMembershipFixture(b, 32)
			report := UsageReport{State: UsageReportActive}
			b.ReportAllocs()
			for b.Loop() {
				batch := beginBatch(b, s)
				for range reports {
					_, err := batch.AddUsage(nil, &target, UsagePurposeMain, report)
					interactionCheck(b, err)
				}
				interactionCheck(b, batch.Discard())
			}
		})
	}
}
