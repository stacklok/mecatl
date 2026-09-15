package microvm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/worktree"
)

func TestFileSessionPersisterSerializesIndependentWriters(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "placements.json")
	first, err := NewFileSessionPersister(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewFileSessionPersister(path)
	if err != nil {
		t.Fatal(err)
	}

	const entries = 64
	start := make(chan struct{})
	errs := make(chan error, entries)
	var writers sync.WaitGroup
	for i := range entries {
		writers.Add(1)
		go func() {
			defer writers.Done()
			<-start
			persister := first
			if i%2 != 0 {
				persister = second
			}
			errs <- persister.Persist(context.Background(), SessionPlacement{
				SessionID: strconv.Itoa(i), Ref: EnvironmentRef{Kind: Kind, ID: "env-" + strconv.Itoa(i) + "@1"}, Generation: 1,
			})
		}()
	}
	close(start)
	writers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Persist: %v", err)
		}
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var placements map[string]SessionPlacement
	if err := json.Unmarshal(data, &placements); err != nil {
		t.Fatal(err)
	}
	if len(placements) != entries {
		t.Fatalf("persisted entries = %d, want %d", len(placements), entries)
	}
}

func TestGitWorktreesUseHardenedGitInvoker(t *testing.T) {
	root := t.TempDir()
	source := initScenario6Repository(t, root)
	for _, dir := range []string{filepath.Join(root, "worktrees"), filepath.Join(root, "metadata")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := worktree.New().Prepare(context.Background(), worktree.Request{
		Source: source, WorktreePath: filepath.Join(root, "worktrees", "session"),
		MetadataPath: filepath.Join(root, "metadata", "session"), Branch: "mecatl/hardened-git",
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	t.Cleanup(func() { _ = prepared.Cleanup(context.Background()) })

	sentinel := filepath.Join(root, "fsmonitor-ran")
	monitor := filepath.Join(root, "hostile-fsmonitor")
	if err := os.WriteFile(monitor, []byte("#!/bin/sh\nprintf ran >"+strconv.Quote(sentinel)+"\nprintf '\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "config", "core.fsmonitor", monitor)

	worktrees := NewGitWorktrees()
	dirty, err := worktrees.Dirty(context.Background(), EnvironmentRecord{WorktreePath: prepared.WorktreePath})
	if err != nil {
		t.Fatalf("Dirty: %v", err)
	}
	if dirty {
		t.Fatal("clean prepared worktree reported dirty")
	}
	if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository-controlled fsmonitor executed: %v", err)
	}
}
