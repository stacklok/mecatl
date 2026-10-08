package app

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/agent"
)

func TestContextualReviewEvidenceCalibrationBeforeAllocation(t *testing.T) {
	if maxReviewEvidenceHandles != 16 || maxReviewEvidenceBytes != 400_000 || maxReviewEvidenceRead != 25_000 || maxReviewEvidenceLines != 2_000 {
		t.Fatalf("evidence capacities changed without calibration: handles=%d bytes=%d read=%d lines=%d", maxReviewEvidenceHandles, maxReviewEvidenceBytes, maxReviewEvidenceRead, maxReviewEvidenceLines)
	}
	req := completeReviewRequest()
	binding := completeEvidenceBinding(req)
	backend := &finiteEvidenceBackend{size: maxReviewEvidenceRead, content: strings.Repeat("x", int(maxReviewEvidenceRead))}
	candidates := make([]reviewEvidenceCandidate, maxReviewEvidenceHandles+1)
	for i := range candidates {
		candidates[i] = reviewEvidenceCandidate{Kind: "text_file", Version: fmt.Sprintf("v%d", i), Complete: true, Authorized: true, Binding: binding, Backend: backend}
	}
	source, metas, complete := newFiniteReviewEvidenceSource(context.Background(), binding, candidates, agent.ReviewCapacity{MaxEvidenceHandles: maxReviewEvidenceHandles, MaxEvidenceBytes: maxReviewEvidenceBytes})
	if complete || len(metas) != maxReviewEvidenceHandles || backend.reads != 0 || len(source.entries) != maxReviewEvidenceHandles {
		t.Fatalf("pre-allocation evidence exhaustion: complete=%v metas=%d reads=%d entries=%d", complete, len(metas), backend.reads, len(source.entries))
	}
	req.EvidenceComplete = complete
	if err := validateReviewAssessmentShape(agent.ToolReviewResult{Assessment: agent.ReviewAcceptable}, req); err == nil {
		t.Fatal("incomplete evidence capacity produced an acceptable assessment")
	}
}

func BenchmarkContextualReviewEvidenceCapacity(b *testing.B) {
	req := completeReviewRequest()
	binding := completeEvidenceBinding(req)
	backend := &finiteEvidenceBackend{size: maxReviewEvidenceRead, content: strings.Repeat("x", int(maxReviewEvidenceRead))}
	candidates := make([]reviewEvidenceCandidate, maxReviewEvidenceHandles+1)
	for i := range candidates {
		candidates[i] = reviewEvidenceCandidate{Kind: "text_file", Version: fmt.Sprintf("v%d", i), Complete: true, Authorized: true, Binding: binding, Backend: backend}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		source, metas, complete := newFiniteReviewEvidenceSource(context.Background(), binding, candidates, agent.ReviewCapacity{MaxEvidenceHandles: maxReviewEvidenceHandles, MaxEvidenceBytes: maxReviewEvidenceBytes})
		if complete || len(metas) != maxReviewEvidenceHandles || len(source.entries) != maxReviewEvidenceHandles {
			b.Fatal("capacity workload did not exhaust deterministically")
		}
	}
	if backend.reads != 0 {
		b.Fatalf("capacity benchmark read full evidence bodies: %d", backend.reads)
	}
}
