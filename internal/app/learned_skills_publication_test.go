package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memskill"
	"github.com/stacklok/mecatl/engine/adapter/skillfs"
	"github.com/stacklok/mecatl/engine/learning"
)

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

func TestADR_0254_DelayedPublicationCannotRevokeNewerGeneration(t *testing.T) {
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
