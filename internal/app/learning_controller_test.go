package app

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/learning"
)

func TestAutomaticAdmissionControllerHardBypassesCooldownButNotBudgets(t *testing.T) {
	now := time.Unix(100, 0)
	cfg := defaultLearningAutomaticConfig()
	cfg.MaxReflectionsPerPrincipal = 2
	c := newAutomaticAdmissionController(cfg, nil)
	c.now = func() time.Time { return now }
	if !c.reserve("p", "one", 100, learning.AdmissionWeighted, learning.Balanced) {
		t.Fatal("first weighted reservation rejected")
	}
	if c.reserve("p", "two", 100, learning.AdmissionWeighted, learning.Balanced) {
		t.Fatal("weighted cooldown did not bind")
	}
	if !c.reserve("p", "two", 100, learning.AdmissionHard, learning.Balanced) {
		t.Fatal("hard trigger did not bypass cooldown")
	}
	if c.reserve("p", "three", 100, learning.AdmissionHard, learning.Balanced) {
		t.Fatal("hard trigger bypassed principal count budget")
	}
}

func TestAutomaticAdmissionControllerCooldownMapBoundedAndPruned(t *testing.T) {
	now := time.Unix(100, 0)
	cfg := defaultLearningAutomaticConfig()
	cfg.Window, cfg.Cooldown = time.Minute, 24*time.Hour
	cfg.MaxReflections, cfg.MaxTokens = 5000, 1_000_000
	cfg.MaxReflectionsPerPrincipal, cfg.MaxTokensPerPrincipal = 2, 1_000_000
	c := newAutomaticAdmissionController(cfg, nil)
	c.now = func() time.Time { return now }
	for i := 0; i < learningCooldownMax+100; i++ {
		principal := fmt.Sprintf("principal-%04d", i)
		if !c.reserve(principal, fmt.Sprintf("digest-%04d", i), 1, learning.AdmissionWeighted, learning.Balanced) {
			t.Fatalf("reservation %d rejected", i)
		}
		now = now.Add(cfg.Window + time.Second)
	}
	if got := len(c.cooldowns); got != learningCooldownMax {
		t.Fatalf("cooldowns = %d, want bound %d", got, learningCooldownMax)
	}
	now = now.Add(cfg.Cooldown)
	if !c.reserve("fresh", "fresh", 1, learning.AdmissionWeighted, learning.Balanced) {
		t.Fatal("fresh reservation rejected after expiry")
	}
	if got := len(c.cooldowns); got != 1 {
		t.Fatalf("expired cooldowns retained: %d", got)
	}
}

func TestReflectionQueueFullDoesNotConsumeAutomaticReservation(t *testing.T) {
	block := make(chan struct{})
	reflector := &testReflector{release: block}
	coordinator := newReflectionCoordinator(context.Background(), reflectionCoordinatorConfig{Workers: 1, Capacity: 1, PrincipalCapacity: 1})
	t.Cleanup(func() { close(block); coordinator.Close() })
	first := testJob("p", "one", reflector)
	if receipt, err := coordinator.Enqueue(first); err != nil || receipt.Disposition != reflectionQueued {
		t.Fatalf("first = %+v, %v", receipt, err)
	}
	called := 0
	second := testJob("p", "two", reflector)
	second.reserve = func() bool { called++; return true }
	if receipt, err := coordinator.Enqueue(second); err != nil || receipt.Disposition != reflectionQueueFull {
		t.Fatalf("second = %+v, %v", receipt, err)
	}
	if called != 0 {
		t.Fatalf("queue-full called reservation %d times", called)
	}
}

func TestAutomaticAdmissionControllerCompletedDigestAndRestartReset(t *testing.T) {
	cfg := defaultLearningAutomaticConfig()
	cfg.Cooldown = 0
	first := newAutomaticAdmissionController(cfg, nil)
	if !first.reserve("p", "digest", 10, learning.AdmissionWeighted, learning.Balanced) {
		t.Fatal("initial reservation rejected")
	}
	first.complete("digest")
	if first.reserve("p", "digest", 10, learning.AdmissionWeighted, learning.Balanced) {
		t.Fatal("completed digest admitted twice")
	}
	restarted := newAutomaticAdmissionController(cfg, nil)
	if !restarted.reserve("p", "digest", 10, learning.AdmissionWeighted, learning.Balanced) {
		t.Fatal("process-local controller did not reset on restart")
	}
}
