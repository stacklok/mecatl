package app

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/learning"
)

func TestRecoveredLegacyAttemptIsUnavailable(t *testing.T) {
	source, err := sessnap.Unmarshal([]byte(`{"id":"legacy-attempt-source","state":"completed","mode":"default","environment_ref":{"kind":"local","id":"/fixture","revision":"v1"},"messages":[{"role":"user","text":"remember this workflow"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	store := memstore.New()
	if err := store.Save(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	provenance, err := learning.NewAdmissionProvenance(learning.AdmissionHostRequested, learning.AttemptSource{SessionID: source.ID, RunID: "run_aaaaaaaaaaaaaaaaaaaaaaaaaa", CanonicalDigest: learning.CanonicalDigest(strings.Repeat("a", 64))}, learning.CurrentPromptBinding{Ordinal: 0, Digest: learning.CanonicalDigest(strings.Repeat("b", 64)), Origin: learning.PromptOriginCurrentPrincipal})
	if err != nil {
		t.Fatal(err)
	}
	partition, err := learning.DeriveAttemptPartition(reflectionPrincipal(nil))
	if err != nil {
		t.Fatal(err)
	}
	loader := newLearningEvidenceLoader(store, memstore.NewEventLog())
	_, projection, failure, err := loader.loadForExecution(t.Context(), partition, learning.AttemptRecord{Provenance: provenance}, "")
	if err != nil || failure != learning.FailureEvidenceUnavailable || len(projection.Messages) != 0 {
		t.Fatalf("legacy recovery: failure=%s err=%v", failure, err)
	}
}
