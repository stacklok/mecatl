package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/memproposal"
	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
	"github.com/stacklok/mecatl/engine/tool"
)

// blockingWriteSkillRepository pauses the FIRST mutating call — CreateDraft,
// which skillmaterialize.Materialize issues to create the draft version,
// BEFORE skilllifecycle.Pipeline.Process ever runs — until release is
// closed, signalling entered exactly once. It lets a test observe whatever
// synchronization buildProcedureProcessor holds across its FULL write
// sequence (Materialize's CreateDraft through Pipeline.Process), without
// depending on the exact internal ordering beyond "CreateDraft goes first".
type blockingWriteSkillRepository struct {
	learning.SkillRepository
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingWriteSkillRepository) CreateDraft(ctx context.Context, partition learning.SkillPartition, owner string, bundle learning.SkillBundle, prov learning.SkillProvenance) (learning.SkillVersion, error) {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.SkillRepository.CreateDraft(ctx, partition, owner, bundle, prov)
}

// TestBuildProcedureProcessorHoldsPublicationLockForNonPublishablePartition
// pins the fix for the flaky
// TestUsableAutoSkillsStockBuildPolicyMatrix/untrusted_project_ignored: the
// per-partition assets.skillPublication lock must be held for the WHOLE
// buildProcedureProcessor write, even when the partition is NOT publishable
// (untrusted workspace, demoted to Review) — not only on the publishable
// branch. Before the fix, a non-publishable write acquired no lock at all, so
// a concurrent reader (ListLearnedSkills's own publishPartition, guarded by
// the SAME assets.skillPublication instance) could observe the partition's
// generation change mid-read and fail with errLearnedSkillGenerationChanged.
// This test proves the lock, deterministically: it forces the processor's
// write to pause mid-flight and asserts a concurrent lock() attempt on the
// SAME partition blocks until the write finishes, rather than relying on the
// probabilistic e2e reproduction (which only surfaced ~15-20% of the time
// under `-race -count=30`).
func TestBuildProcedureProcessorHoldsPublicationLockForNonPublishablePartition(t *testing.T) {
	proposals := memproposal.New()
	blocking := &blockingWriteSkillRepository{SkillRepository: memskill.New(), entered: make(chan struct{}), release: make(chan struct{})}
	catalog := skillfs.NewAtomicCatalog(nil, nil, nil)
	pub := &learnedSkillPublication{}
	input, outcome, digest := reflectionOutcomeFixture(t, learning.CandidateProcedure)
	assets := catalogAssets{reflectionRepository: proposals, learnedSkills: blocking, liveSkills: catalog, skillOwner: "reflection", skillPublication: pub}
	// TrustProject:false + Workspace==the candidate's project makes this
	// partition NON-publishable (the exact "untrusted_project_ignored" shape):
	// publishable := partition.Project=="" || (partition.Project==cfg.Workspace && projectIngestionAdmitted(cfg))
	// both disjuncts are false here.
	cfg := Config{LearningMode: learning.Auto, Workspace: input.Trajectory.Workspace, TrustProject: false}
	processor := buildProcedureProcessor(cfg, assets)
	if processor == nil {
		t.Fatal("buildProcedureProcessor returned nil")
	}

	done := make(chan error, 1)
	go func() {
		_, err := processReflectionOutcome(context.Background(), proposals, nil, nil, "principal", input, digest, outcome,
			learning.DetectSignals(input), learning.Auto, false, input.Trajectory.Workspace, processor)
		done <- err
	}()

	select {
	case <-blocking.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("buildProcedureProcessor never reached its first repository write")
	}

	partition := learning.SkillPartition{Principal: "principal", Project: input.Trajectory.Workspace}
	acquired := make(chan func(), 1)
	go func() { acquired <- pub.lock(partition) }()

	select {
	case unlock := <-acquired:
		unlock()
		t.Fatal("publication lock was acquired while a non-publishable buildProcedureProcessor write was still in flight — the lock must guard every partition-mutating write, not only the publishable branch")
	case <-time.After(100 * time.Millisecond):
		// Still blocked, as required: the lock is held across the non-publishable write.
	}

	close(blocking.release)
	if err := <-done; err != nil {
		t.Fatalf("processReflectionOutcome: %v", err)
	}

	select {
	case unlock := <-acquired:
		unlock()
	case <-time.After(2 * time.Second):
		t.Fatal("publication lock was never released after buildProcedureProcessor finished")
	}
}

type failingListSkillRepository struct {
	learning.SkillRepository
	fail learning.SkillPartition
	err  error
}

func (r *failingListSkillRepository) List(ctx context.Context, partition learning.SkillPartition, query learning.SkillList) (learning.SkillPage, error) {
	if r.err != nil && partition == r.fail {
		return learning.SkillPage{}, r.err
	}
	return r.SkillRepository.List(ctx, partition, query)
}

type barrierSkillRepository struct {
	learning.SkillRepository
	mu       sync.Mutex
	calls    int
	entered  [2]chan struct{}
	releases [2]chan struct{}
}

func (r *barrierSkillRepository) List(ctx context.Context, partition learning.SkillPartition, query learning.SkillList) (learning.SkillPage, error) {
	page, err := r.SkillRepository.List(ctx, partition, query)
	r.mu.Lock()
	index := r.calls
	r.calls++
	r.mu.Unlock()
	if index < len(r.entered) {
		close(r.entered[index])
		<-r.releases[index]
	}
	return page, err
}

type partitionFailureSkillRepository struct {
	learning.SkillRepository
	partition learning.SkillPartition
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (r *partitionFailureSkillRepository) List(ctx context.Context, partition learning.SkillPartition, query learning.SkillList) (learning.SkillPage, error) {
	if partition != r.partition {
		return r.SkillRepository.List(ctx, partition, query)
	}
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
		return learning.SkillPage{}, errors.New("injected partition read failure")
	case <-ctx.Done():
		return learning.SkillPage{}, ctx.Err()
	}
}

func TestADR_0259_LearnedSkillPartitionPublicationIsolation(t *testing.T) {
	ctx := context.Background()
	alice := learning.SkillPartition{Principal: "alice"}
	bob := learning.SkillPartition{Principal: "bob"}
	repository := memskill.New()
	aliceV1 := activateCompositionSkill(t, repository, alice, "alice-skill", "Alice version one.")
	bobVersion := activateCompositionSkill(t, repository, bob, "bob-skill", "Bob procedure.")
	catalog := skillfs.NewAtomicCatalog(
		[]tool.SkillMeta{{Name: "external", Description: "deployment skill"}},
		nil,
		nil,
	)
	aliceGeneration, err := repository.Generation(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	bobGeneration, err := repository.Generation(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.RefreshPartitionsAtGeneration(map[learning.SkillPartition]learning.SkillGeneration{
		alice: aliceGeneration,
		bob:   bobGeneration,
	}, []learning.SkillVersion{aliceV1, bobVersion}) {
		t.Fatal("initial publication failed")
	}

	failing := &partitionFailureSkillRepository{
		SkillRepository: repository,
		partition:       alice,
		entered:         make(chan struct{}),
		release:         make(chan struct{}),
	}
	gate := &learnedSkillPublication{}
	alicePublisher := learnedSkillPublisher{repository: failing, partitions: []learning.SkillPartition{alice}, catalog: catalog, serial: gate}
	bobPublisher := learnedSkillPublisher{repository: failing, partitions: []learning.SkillPartition{bob}, catalog: catalog, serial: gate}
	aliceDone := make(chan error, 1)
	go func() { aliceDone <- alicePublisher.Publish(ctx) }()
	<-failing.entered

	aliceV2 := activateCompositionSkill(t, repository, alice, "alice-skill", "Alice version two.")
	bobDone := make(chan error, 1)
	go func() { bobDone <- bobPublisher.Publish(ctx) }()
	select {
	case err := <-bobDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		close(failing.release)
		<-aliceDone
		<-bobDone
		t.Fatal("Alice's failed hydration blocked Bob's independent partition")
	}

	beforeFailure := catalog.View(alice, bob)
	if !hasSkillMeta(beforeFailure.Metas, "external") || !hasSkillMeta(beforeFailure.Metas, "alice-skill") || !hasSkillMeta(beforeFailure.Metas, "bob-skill") {
		t.Fatalf("unrelated or external skills disappeared while Alice was uncertain: %+v", beforeFailure.Metas)
	}
	close(failing.release)
	if err := <-aliceDone; err == nil {
		t.Fatal("injected Alice hydration failure was not reported")
	}
	aliceView := catalog.View(alice)
	if !hasSkillMeta(aliceView.Metas, "external") || !hasSkillMeta(aliceView.Metas, "alice-skill") {
		t.Fatalf("older failed hydration revoked Alice's newer durable generation: %+v", aliceView.Metas)
	}

	if err := (learnedSkillPublisher{repository: repository, partitions: []learning.SkillPartition{alice}, catalog: catalog, serial: gate}).Publish(ctx); err != nil {
		t.Fatal(err)
	}
	view := catalog.View(alice, bob)
	if !hasSkillMeta(view.Metas, "external") || !hasSkillMeta(view.Metas, "bob-skill") || len(view.Metas) != 3 {
		t.Fatalf("reconciliation crossed a partition or lost external precedence: %+v", view.Metas)
	}
	for _, meta := range view.Metas {
		if meta.Name == "alice-skill" && meta.Metadata["mecatl.active_version"] != string(aliceV2.Version) {
			t.Fatalf("Alice did not converge to the newer active version: %+v", meta)
		}
	}
}

func hasSkillMeta(metas []tool.SkillMeta, name string) bool {
	for _, meta := range metas {
		if meta.Name == name {
			return true
		}
	}
	return false
}

func TestADR_0259_DelayedPublicationCannotRevokeNewerGeneration(t *testing.T) {
	ctx := context.Background()
	partition := learning.SkillPartition{Principal: "alice"}
	repository := memskill.New()
	first := activateCompositionSkill(t, repository, partition, "serialized", "First publication.")
	catalog := skillfs.NewAtomicCatalog(nil, nil, nil)
	barrier := &barrierSkillRepository{
		SkillRepository: repository,
		entered:         [2]chan struct{}{make(chan struct{}), make(chan struct{})},
		releases:        [2]chan struct{}{make(chan struct{}), make(chan struct{})},
	}
	publisher := learnedSkillPublisher{repository: barrier, partitions: []learning.SkillPartition{partition}, catalog: catalog}
	oldDone := make(chan error, 1)
	go func() { oldDone <- publisher.Publish(ctx) }()
	<-barrier.entered[0]

	second := activateCompositionSkill(t, repository, partition, "serialized", "Second publication.")
	newDone := make(chan error, 1)
	go func() { newDone <- publisher.Publish(ctx) }()
	<-barrier.entered[1]
	close(barrier.releases[1])
	if err := <-newDone; err != nil {
		t.Fatal(err)
	}

	close(barrier.releases[0])
	if err := <-oldDone; !errors.Is(err, errLearnedSkillGenerationChanged) {
		t.Fatalf("stale publisher error=%v", err)
	}
	view := catalog.View(partition)
	if len(view.Metas) != 1 || view.Metas[0].Metadata["mecatl.active_version"] != string(second.Version) || second.Version == first.Version {
		t.Fatalf("stale publication or invalidation won: first=%s second=%s generation=%d metas=%+v", first.Version, second.Version, view.Generation, view.Metas)
	}
}

func TestLearnedSkillPublicationFailureQuarantinesOnlyOwnedPartitionsAndReconciles(t *testing.T) {
	ctx := context.Background()
	repository := memskill.New()
	alice := learning.SkillPartition{Principal: "alice"}
	aliceProject := learning.SkillPartition{Principal: "alice", Project: "/project"}
	bob := learning.SkillPartition{Principal: "bob"}
	aliceSkill := activateCompositionSkill(t, repository, alice, "alice-global", "Alice global procedure.")
	projectSkill := activateCompositionSkill(t, repository, aliceProject, "alice-project", "Alice project procedure.")
	bobSkill := activateCompositionSkill(t, repository, bob, "bob-global", "Bob global procedure.")
	catalog := skillfs.NewAtomicCatalog(nil, nil, []learning.SkillVersion{aliceSkill, projectSkill, bobSkill})
	wrapped := &failingListSkillRepository{SkillRepository: repository, fail: aliceProject, err: errors.New("publication read failed")}
	publisher := learnedSkillPublisher{
		repository: wrapped, partitions: []learning.SkillPartition{alice, aliceProject}, catalog: catalog, serial: &learnedSkillPublication{},
	}

	if err := publisher.Publish(ctx); !errors.Is(err, wrapped.err) {
		t.Fatalf("publish error=%v", err)
	}
	if got := catalog.View(alice, aliceProject).Metas; len(got) != 1 || got[0].Name != "alice-global" {
		t.Fatalf("publication uncertainty changed a healthy caller partition: %+v", got)
	}
	if got := catalog.View(bob).Metas; len(got) != 1 || got[0].Name != "bob-global" {
		t.Fatalf("unrelated caller partition changed during quarantine: %+v", got)
	}

	wrapped.err = nil
	if err := publisher.Publish(ctx); err != nil {
		t.Fatal(err)
	}
	got := catalog.View(alice, aliceProject).Metas
	if len(got) != 2 || got[0].Name != "alice-global" || got[1].Name != "alice-project" {
		t.Fatalf("authoritative reconcile did not restore caller partitions: %+v", got)
	}
	if other := catalog.View(bob).Metas; len(other) != 1 || other[0].Name != "bob-global" {
		t.Fatalf("reconcile replaced unrelated caller partition: %+v", other)
	}
}
