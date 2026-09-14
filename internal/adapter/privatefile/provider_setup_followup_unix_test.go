//go:build linux || darwin

package privatefile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func followupTarget(t *testing.T) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "settings.yaml")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, path
}

func followupMutation([]byte) ([]byte, bool, error) { return []byte("new\n"), false, nil }

func TestProviderSetupFollowup_Scenario4_PortablePrivateFileBoundary(t *testing.T) {
	for _, substitution := range []string{"fifo", "symlink", "mode"} {
		t.Run("leaf substitution "+substitution, func(t *testing.T) {
			_, path := followupTarget(t)
			done := make(chan struct{})
			var state CommitState
			var updateErr error
			changed := false
			mutated := false
			testHook = func(stage string) error {
				if stage != "before-target-open" || changed {
					return nil
				}
				changed = true
				if substitution == "mode" {
					return os.Chmod(path, 0o644)
				}
				if err := os.Rename(path, path+".original"); err != nil {
					return err
				}
				if substitution == "fifo" {
					return unix.Mkfifo(path, 0o600)
				}
				return os.Symlink(path+".original", path)
			}
			t.Cleanup(func() { testHook = nil })
			go func() {
				state, updateErr = Update(t.Context(), path, "", 1024, func(data []byte) ([]byte, bool, error) {
					mutated = true
					return followupMutation(data)
				})
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				// Release a regressed blocking FIFO open before failing, without leaking a goroutine.
				fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
				if err != nil {
					t.Fatal(err)
				}
				<-done
				_ = unix.Close(fd)
				t.Error("special-file substitution blocked the writer")
			}
			if mutated {
				t.Error("unsafe leaf reached the document mutator")
			}
			if !changed || state != CommitNotApplied || updateErr == nil {
				t.Fatalf("substituted leaf: state=%s err=%v hook=%v", state, updateErr, changed)
			}
		})
	}

	for _, stage := range []string{"after-temp-sync", "before-compare"} {
		t.Run("canonical parent replaced "+stage, func(t *testing.T) {
			dir, path := followupTarget(t)
			outside := t.TempDir()
			if err := os.Chmod(outside, 0o700); err != nil {
				t.Fatal(err)
			}
			outsideTarget := filepath.Join(outside, filepath.Base(path))
			if err := os.Link(path, outsideTarget); err != nil {
				t.Fatal(err)
			}
			var tempName string
			testHook = func(at string) error {
				if at != stage {
					return nil
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if strings.HasSuffix(entry.Name(), ".tmp") {
						tempName = entry.Name()
					}
				}
				if tempName == "" {
					return errors.New("missing same-directory temporary")
				}
				if err := os.WriteFile(filepath.Join(outside, tempName), []byte("outside sentinel"), 0o600); err != nil {
					return err
				}
				if err := os.Rename(dir, dir+".moved"); err != nil {
					return err
				}
				return os.Symlink(outside, dir)
			}
			t.Cleanup(func() { testHook = nil })
			state, err := Update(t.Context(), path, "", 1024, followupMutation)
			if state != CommitNotApplied || !errors.Is(err, ErrConfigurationChanged) {
				t.Errorf("parent replacement: state=%s err=%v", state, err)
			}
			data, err := os.ReadFile(outsideTarget)
			if err != nil || string(data) != "old\n" {
				t.Error("replacement escaped the validated parent")
			}
			data, err = os.ReadFile(filepath.Join(outside, tempName))
			if err != nil || string(data) != "outside sentinel" {
				t.Error("cleanup escaped the validated parent")
			}
			if _, err := os.Stat(filepath.Join(dir+".moved", tempName)); !errors.Is(err, os.ErrNotExist) {
				t.Error("anchored temporary was not cleaned")
			}
		})
	}

	for _, change := range []string{"identity", "content", "mode", "cancel"} {
		t.Run("final comparison "+change, func(t *testing.T) {
			_, path := followupTarget(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			testHook = func(stage string) error {
				if stage != "before-compare" {
					return nil
				}
				switch change {
				case "identity":
					if err := os.Rename(path, path+".old"); err != nil {
						return err
					}
					return os.WriteFile(path, []byte("old\n"), 0o600)
				case "content":
					return os.WriteFile(path, []byte("peer\n"), 0o600)
				case "mode":
					return os.Chmod(path, 0o644)
				default:
					cancel()
					return nil
				}
			}
			t.Cleanup(func() { testHook = nil })
			state, err := Update(ctx, path, "", 1024, followupMutation)
			wantErr := ErrConfigurationChanged
			if change == "cancel" {
				wantErr = context.Canceled
			}
			if state != CommitNotApplied || !errors.Is(err, wantErr) {
				t.Fatalf("final compare: state=%s err=%v", state, err)
			}
		})
	}

	t.Run("canonical home alias", func(t *testing.T) {
		dir, path := followupTarget(t)
		alias := filepath.Join(t.TempDir(), "home")
		if err := os.Symlink(filepath.Dir(dir), alias); err != nil {
			t.Fatal(err)
		}
		state, err := Update(t.Context(), filepath.Join(alias, "private", filepath.Base(path)), "", 1024, followupMutation)
		if state != CommitDurable || err != nil {
			t.Fatalf("home alias: %s %v", state, err)
		}
	})
}

func TestProviderSetupFollowup_Scenario4_TruthfulWriteOutcomes(t *testing.T) {
	t.Run("no-op is still cancellation and comparison checked", func(t *testing.T) {
		for _, cancelled := range []bool{false, true} {
			_, path := followupTarget(t)
			ctx, cancel := context.WithCancel(t.Context())
			state, err := Update(ctx, path, "", 1024, func(data []byte) ([]byte, bool, error) {
				if cancelled {
					cancel()
				} else if err := os.WriteFile(path, []byte("peer\n"), 0o600); err != nil {
					return nil, false, err
				}
				return data, true, nil
			})
			cancel()
			want := ErrConfigurationChanged
			if cancelled {
				want = context.Canceled
			}
			if state != CommitNotApplied || !errors.Is(err, want) {
				t.Fatalf("no-op state=%s err=%v", state, err)
			}
		}
	})
	t.Run("post-rename cancellation does not undo a durable replacement", func(t *testing.T) {
		_, path := followupTarget(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		testHook = func(stage string) error {
			if stage == "after-rename" {
				cancel()
			}
			return nil
		}
		t.Cleanup(func() { testHook = nil })
		state, err := Update(ctx, path, "", 1024, followupMutation)
		if state != CommitDurable || err != nil {
			t.Fatalf("post-commit cancellation=%s %v", state, err)
		}
	})
	t.Run("new conventional parent is not recursive and preflight never creates", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing", "settings.yaml")
		if err := Preflight(path, path, 1024, func([]byte) error { t.Fatal("missing document validated"); return nil }); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("preflight created a parent")
		}
		if state, err := Update(t.Context(), path, "", 1024, followupMutation); state != CommitNotApplied || err == nil {
			t.Fatal("explicit path created a parent")
		}
		deep := filepath.Join(filepath.Dir(path), "deeper", "settings.yaml")
		if state, err := Update(t.Context(), deep, deep, 1024, followupMutation); state != CommitNotApplied || err == nil {
			t.Fatal("conventional path recursively created parents")
		}
		if state, err := Update(t.Context(), path, path, 1024, followupMutation); state != CommitDurable || err != nil {
			t.Fatalf("conventional create=%s %v", state, err)
		}
	})
	for _, stage := range []string{"after-temp-sync", "before-compare", "after-rename", "before-parent-sync", "before-parent-close", "before-new-parent-sync", "before-new-parent-close"} {
		t.Run(stage, func(t *testing.T) {
			_, path := followupTarget(t)
			conventional := ""
			newParent := strings.Contains(stage, "new-parent")
			if newParent {
				base := t.TempDir()
				alias := filepath.Join(t.TempDir(), "home")
				if err := os.Symlink(base, alias); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(alias, "mecatl", "auth.yaml")
				conventional = path
			}
			seen := false
			testHook = func(at string) error {
				if at == stage {
					seen = true
					return errors.New("secret-fault-sentinel")
				}
				return nil
			}
			t.Cleanup(func() { testHook = nil })
			state, err := Update(t.Context(), path, conventional, 1024, followupMutation)
			post := stage == "after-rename" || stage == "before-parent-sync" || stage == "before-parent-close"
			want := CommitNotApplied
			if post {
				want = CommitReplacementAppliedDurabilityUnknown
			}
			if !seen || state != want || err == nil {
				t.Fatalf("fault: hook=%v state=%s err=%v", seen, state, err)
			}
			if strings.Contains(err.Error(), "secret-fault-sentinel") {
				t.Fatal("error exposed underlying fault material")
			}
			data, readErr := os.ReadFile(path)
			if newParent {
				if !strings.Contains(err.Error(), "directory may remain") {
					t.Error("pre-commit error concealed possible directory residue")
				}
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatal("target committed before new parent's containing directory was synced")
				}
				if info, statErr := os.Stat(filepath.Dir(path)); statErr != nil || !info.IsDir() {
					t.Fatal("new parent residue should remain")
				}
			} else if readErr != nil || (post && string(data) != "new\n") || (!post && string(data) != "old\n") {
				t.Fatal("outcome disagrees with target bytes")
			}
		})
	}
}
