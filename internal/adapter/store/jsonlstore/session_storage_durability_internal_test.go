package jsonlstore

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario1_AtomicCrashRecovery(t *testing.T) {
	t.Parallel()

	t.Run("temporary replacement shares destination directory", func(t *testing.T) {
		dir := t.TempDir()
		expected := filepath.Join(dir, canonicalDirName)
		ops := defaultSnapshotOps()
		createTemp := ops.createTemp
		ops.createTemp = func(gotDir, pattern string) (*os.File, error) {
			if gotDir != expected {
				return nil, errors.New("temporary replacement is not in the snapshot directory")
			}
			return createTemp(gotDir, pattern)
		}
		st, err := newStoreWithSnapshotOps(dir, ops)
		if err != nil {
			t.Fatalf("newStoreWithSnapshotOps: %v", err)
		}
		if err := st.Save(context.Background(), newSnapshotSession("same-dir", "saved")); err != nil {
			t.Fatalf("Save with same-directory temporary: %v", err)
		}
	})

	for _, tc := range []struct {
		name    string
		inject  func(*snapshotOps)
		wantNew bool
	}{
		{
			name: "short write before file sync",
			inject: func(ops *snapshotOps) {
				ops.write = func(_ *os.File, data []byte) (int, error) {
					return len(data) / 2, nil
				}
			},
		},
		{
			name: "partial write before file sync",
			inject: func(ops *snapshotOps) {
				ops.write = func(f *os.File, data []byte) (int, error) {
					n, _ := f.Write(data[:len(data)/2])
					return n, syscall.EIO
				}
			},
		},
		{
			name: "file sync failure",
			inject: func(ops *snapshotOps) {
				syncFile := ops.syncFile
				ops.syncFile = func(f *os.File) error {
					if strings.Contains(filepath.Base(f.Name()), ".snapshot-sync-probe-") {
						return syncFile(f)
					}
					return syscall.EIO
				}
			},
		},
		{
			name: "rename failure",
			inject: func(ops *snapshotOps) {
				rename := ops.rename
				ops.rename = func(source, target string) error {
					if strings.Contains(filepath.Base(source), ".snapshot-rename-probe-") {
						return rename(source, target)
					}
					return syscall.EIO
				}
			},
		},
		{
			name: "directory open failure after rename",
			inject: func(ops *snapshotOps) {
				openDir := ops.openDir
				probed := false
				ops.openDir = func(path string) (*os.File, error) {
					if !probed {
						probed = true
						return openDir(path)
					}
					return nil, syscall.EIO
				}
			},
			wantNew: true,
		},
		{
			name: "directory sync failure after rename",
			inject: func(ops *snapshotOps) {
				syncDir := ops.syncDir
				probed := false
				ops.syncDir = func(dir *os.File) error {
					if !probed {
						probed = true
						return syncDir(dir)
					}
					return syscall.EIO
				}
			},
			wantNew: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			prior := newSnapshotSession("atomic-crash", "prior")
			st, err := New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := st.Save(context.Background(), prior); err != nil {
				t.Fatalf("seed Save: %v", err)
			}
			// A stale v1 record must never regain authority after v2 committed.
			v1Path := st.resolver.canonicalPath(prior.ID, kindSnapshot)
			if err := os.WriteFile(v1Path, []byte(`{"id":"atomic-crash","state":"idle","mode":"default","limits":{},"counters":{},"workspace":"/stale","created_at":"2023-11-14T22:13:20Z","messages":[],"title":"stale-v1"}`+"\n"), 0o600); err != nil {
				t.Fatalf("write stale v1: %v", err)
			}

			ops := defaultSnapshotOps()
			tc.inject(&ops)
			failing, err := newStoreWithSnapshotOps(dir, ops)
			if err != nil {
				t.Fatalf("newStoreWithSnapshotOps: %v", err)
			}
			if err := failing.Save(context.Background(), newSnapshotSession(prior.ID, "new")); err == nil {
				t.Fatal("Save with injected failure = nil, want loud error")
			}

			reopened, err := New(dir)
			if err != nil {
				t.Fatalf("reopen New: %v", err)
			}
			got, err := reopened.Load(context.Background(), prior.ID)
			if err != nil {
				t.Fatalf("Load after injected failure: %v", err)
			}
			wantTitle := "prior"
			if tc.wantNew {
				wantTitle = "new"
			}
			if got.Title != wantTitle {
				t.Fatalf("reopened title = %q, want committed %q (never torn, absent, or stale v1)", got.Title, wantTitle)
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario1_DurabilityCapabilityTruth(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	ops := defaultSnapshotOps()
	ops.atomicRename = true
	ops.fileSync = true
	ops.syncDir = func(*os.File) error { return syscall.ENOTSUP }
	st, err := newStoreWithSnapshotOps(dir, ops)
	if err != nil {
		t.Fatalf("New with unsupported directory sync: %v", err)
	}
	capability := st.SnapshotDurability()
	if !capability.AtomicReplace || !capability.FileSync || capability.DirectorySync || capability.HostCrashSafe() {
		t.Fatalf("unsupported directory sync overclaimed: %+v", capability)
	}
	if err := st.Save(context.Background(), newSnapshotSession("weak-durability", "saved")); err != nil {
		t.Fatalf("Save under explicit weaker durability: %v", err)
	}

	noFileSyncOps := defaultSnapshotOps()
	noFileSyncOps.syncFile = func(*os.File) error { return syscall.ENOTSUP }
	noFileSync, err := newStoreWithSnapshotOps(filepath.Join(dir, "no-file-sync"), noFileSyncOps)
	if err != nil {
		t.Fatalf("New with unsupported file sync: %v", err)
	}
	if got := noFileSync.SnapshotDurability(); got.FileSync || got.HostCrashSafe() {
		t.Fatalf("unsupported file sync overclaimed: %+v", got)
	}
	if err := noFileSync.Save(context.Background(), newSnapshotSession("weak-file-sync", "saved")); err != nil {
		t.Fatalf("Save under weaker file-sync durability: %v", err)
	}

	noRenameOps := defaultSnapshotOps()
	noRenameOps.rename = func(string, string) error { return syscall.ENOTSUP }
	noRename, err := newStoreWithSnapshotOps(filepath.Join(dir, "no-rename"), noRenameOps)
	if err != nil {
		t.Fatalf("New with unsupported atomic replacement: %v", err)
	}
	if got := noRename.SnapshotDurability(); got.AtomicReplace || got.HostCrashSafe() {
		t.Fatalf("unsupported atomic replacement overclaimed: %+v", got)
	}

	supportedOps := defaultSnapshotOps()
	supportedOps.atomicRename = true
	supportedOps.fileSync = true
	supportedOps.syncDir = func(*os.File) error { return nil }
	fullyDurable, err := newStoreWithSnapshotOps(filepath.Join(dir, "supported"), supportedOps)
	if err != nil {
		t.Fatalf("New with directory sync: %v", err)
	}
	if got := fullyDurable.SnapshotDurability(); !got.HostCrashSafe() {
		t.Fatalf("supported durability underreported: %+v", got)
	}
}

func TestSessionStorageContinuity_Scenario1_DurabilityProbeFaultsFailConstruction(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		inject func(*snapshotOps)
	}{
		{name: "rename", inject: func(ops *snapshotOps) {
			ops.rename = func(string, string) error { return syscall.EIO }
		}},
		{name: "file sync", inject: func(ops *snapshotOps) {
			ops.syncFile = func(*os.File) error { return syscall.EIO }
		}},
		{name: "directory sync", inject: func(ops *snapshotOps) {
			ops.syncDir = func(*os.File) error { return syscall.EIO }
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := defaultSnapshotOps()
			tc.inject(&ops)
			st, err := newStoreWithSnapshotOps(t.TempDir(), ops)
			if err == nil || !errors.Is(err, syscall.EIO) {
				t.Fatalf("newStoreWithSnapshotOps = (%v, %v), want nil Store and EIO", st, err)
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario1_DiskFullPreservesCommittedSnapshot(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		inject func(*snapshotOps)
	}{
		{
			name: "write",
			inject: func(ops *snapshotOps) {
				ops.write = func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }
			},
		},
		{
			name: "file sync",
			inject: func(ops *snapshotOps) {
				syncFile := ops.syncFile
				ops.syncFile = func(f *os.File) error {
					if strings.Contains(filepath.Base(f.Name()), ".snapshot-sync-probe-") {
						return syncFile(f)
					}
					return syscall.ENOSPC
				}
			},
		},
		{
			name: "rename",
			inject: func(ops *snapshotOps) {
				rename := ops.rename
				ops.rename = func(source, target string) error {
					if strings.Contains(filepath.Base(source), ".snapshot-rename-probe-") {
						return rename(source, target)
					}
					return syscall.ENOSPC
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			id := session.SessionID("disk-full")
			st, err := New(dir)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if err := st.Save(context.Background(), newSnapshotSession(id, "prior")); err != nil {
				t.Fatalf("seed Save: %v", err)
			}
			toolPath := st.resolver.canonicalPath(id, kindTools)
			toolData := []byte("committed sidecar\n")
			if err := os.WriteFile(toolPath, toolData, 0o600); err != nil {
				t.Fatalf("write sidecar: %v", err)
			}

			ops := defaultSnapshotOps()
			tc.inject(&ops)
			failing, err := newStoreWithSnapshotOps(dir, ops)
			if err != nil {
				t.Fatalf("newStoreWithSnapshotOps: %v", err)
			}
			err = failing.Save(context.Background(), newSnapshotSession(id, "new"))
			if err == nil || !errors.Is(err, syscall.ENOSPC) {
				t.Fatalf("Save error = %v, want loud ENOSPC", err)
			}
			got, err := failing.Load(context.Background(), id)
			if err != nil || got.Title != "prior" {
				t.Fatalf("committed snapshot after failure = title %q, err %v; want prior", got.Title, err)
			}
			gotSidecar, err := os.ReadFile(toolPath)
			if err != nil || string(gotSidecar) != string(toolData) {
				t.Fatalf("committed sidecar changed: %q, %v", gotSidecar, err)
			}
		})
	}
}

func TestSessionStorageContinuity_Scenario1_OrphanTemporaryRecovery(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	id := session.SessionID("orphan-recovery")
	if err := st.Save(context.Background(), newSnapshotSession(id, "committed")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	path := st.resolver.currentSnapshotPath(id)
	for generation := uint64(1); generation <= 4; generation++ {
		name := snapshotTempPattern(path, "0123456789abcdef0123456789abcdef", generation)
		f, createErr := os.CreateTemp(filepath.Dir(path), name)
		if createErr != nil {
			t.Fatalf("create orphan generation %d: %v", generation, createErr)
		}
		if closeErr := f.Close(); closeErr != nil {
			t.Fatalf("close orphan generation %d: %v", generation, closeErr)
		}
	}
	lookalike := path + ".tmp-unowned"
	if err := os.WriteFile(lookalike, []byte("not owned by the temp protocol"), 0o600); err != nil {
		t.Fatalf("write lookalike: %v", err)
	}

	// Reopening is startup recovery. It must reap only protocol-owned inactive
	// generations while preserving both the committed snapshot and unrelated names.
	reopened, err := New(dir)
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	assertNoSnapshotTemps(t, path)
	if _, err := os.Stat(lookalike); err != nil {
		t.Fatalf("unowned lookalike was reaped: %v", err)
	}
	got, err := reopened.Load(context.Background(), id)
	if err != nil || got.Title != "committed" {
		t.Fatalf("committed snapshot after startup recovery = title %q, err %v", got.Title, err)
	}

	// Repeated crash generations converge again on the next successful Save.
	for generation := uint64(5); generation <= 8; generation++ {
		f, createErr := os.CreateTemp(filepath.Dir(path), snapshotTempPattern(path, "fedcba9876543210fedcba9876543210", generation))
		if createErr != nil {
			t.Fatalf("create repeated orphan generation %d: %v", generation, createErr)
		}
		_ = f.Close()
	}
	if err := reopened.Save(context.Background(), newSnapshotSession(id, "new")); err != nil {
		t.Fatalf("Save after repeated crashes: %v", err)
	}
	assertNoSnapshotTemps(t, path)
}

func TestSessionStorageContinuity_Scenario1_ActiveTemporaryNotReaped(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeEntered := make(chan string, 1)
	releaseWrite := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseWrite) }) }
	defer release()
	ops := defaultSnapshotOps()
	write := ops.write
	ops.write = func(f *os.File, data []byte) (int, error) {
		writeEntered <- f.Name()
		<-releaseWrite
		return write(f, data)
	}
	first, err := newStoreWithSnapshotOps(dir, ops)
	if err != nil {
		t.Fatalf("new first Store: %v", err)
	}
	id := session.SessionID("active-temp")
	firstErr := make(chan error, 1)
	go func() {
		firstErr <- first.Save(context.Background(), newSnapshotSession(id, "first"))
	}()
	activePath := <-writeEntered
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("active temp missing before competing Store: %v", err)
	}

	// A second Store performs startup recovery against the same physical family.
	// It must skip the flocked active generation rather than deleting it.
	second, err := New(dir)
	if err != nil {
		t.Fatalf("new competing Store: %v", err)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("competing startup reaped active temp: %v", err)
	}

	secondErr := make(chan error, 1)
	go func() {
		secondErr <- second.Save(context.Background(), newSnapshotSession(id, "second"))
	}()
	release()
	if err := <-firstErr; err != nil {
		t.Fatalf("first Save after competing recovery: %v", err)
	}
	if err := <-secondErr; err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, err := second.Load(context.Background(), id)
	if err != nil || got.Title != "second" {
		t.Fatalf("serialized committed snapshot = title %q, err %v", got.Title, err)
	}
	assertNoSnapshotTemps(t, first.resolver.currentSnapshotPath(id))
}

func TestSessionStorageContinuity_Scenario1_ActiveTemporaryCrossProcess(t *testing.T) {
	dir := t.TempDir()
	id := session.SessionID("cross-process-active-temp")
	seed, err := New(dir)
	if err != nil {
		t.Fatalf("New seed Store: %v", err)
	}
	if err := seed.Save(context.Background(), newSnapshotSession(id, "committed-before-child")); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	snapshotPath := seed.resolver.currentSnapshotPath(id)
	before, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read seed snapshot: %v", err)
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create ready pipe: %v", err)
	}
	defer func() { _ = readyR.Close() }()
	releaseR, releaseW, err := os.Pipe()
	if err != nil {
		t.Fatalf("create release pipe: %v", err)
	}
	defer func() { _ = releaseW.Close() }()

	var childOutput bytes.Buffer
	cmd := exec.Command(os.Args[0], "-test.run=^TestSessionStorageContinuity_HelperProcessActiveSave$")
	cmd.Env = append(os.Environ(), "MECATL_JSONLSTORE_HELPER=1", "MECATL_JSONLSTORE_DIR="+dir)
	cmd.ExtraFiles = []*os.File{readyW, releaseR}
	cmd.Stdout = &childOutput
	cmd.Stderr = &childOutput
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper process: %v", err)
	}
	_ = readyW.Close()
	_ = releaseR.Close()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()

	if err := readyR.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set helper readiness deadline: %v", err)
	}
	scanner := bufio.NewScanner(readyR)
	if !scanner.Scan() {
		t.Fatalf("helper did not report active temp: %v", scanner.Err())
	}
	activePath := scanner.Text()
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("helper active temp missing: %v", err)
	}

	peer, err := New(dir)
	if err != nil {
		t.Fatalf("New peer Store: %v", err)
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("peer startup reaped helper active temp: %v", err)
	}
	reachedLock := make(chan struct{}, 1)
	peer.snapshotFamilyLockBlocked = func() {
		select {
		case reachedLock <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	peerDone := make(chan error, 1)
	go func() {
		peerDone <- peer.Save(ctx, newSnapshotSession(id, "peer-must-not-commit"))
	}()
	awaitSignal(t, reachedLock, "peer Save did not reach the helper-held family lock")
	cancel()
	if err := awaitError(t, peerDone, "cancelled peer Save did not return"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled peer Save error = %v, want context.Canceled", err)
	}
	gotBeforeRelease, err := os.ReadFile(snapshotPath)
	if err != nil {
		t.Fatalf("read snapshot while helper is paused: %v", err)
	}
	if !bytes.Equal(gotBeforeRelease, before) {
		t.Fatal("cancelled peer mutated the committed snapshot")
	}
	if _, err := os.Stat(activePath); err != nil {
		t.Fatalf("cancelled peer removed helper active temp: %v", err)
	}

	if _, err := releaseW.Write([]byte{1}); err != nil {
		t.Fatalf("release helper Save: %v", err)
	}
	_ = releaseW.Close()
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	if err := awaitError(t, childDone, "helper Save did not finish"); err != nil {
		t.Fatalf("helper process: %v; output: %s", err, childOutput.String())
	}
	got, err := peer.Load(context.Background(), id)
	if err != nil {
		t.Fatalf("load committed authority after helper completion: %v", err)
	}
	if got.Title != "child-committed" {
		t.Fatalf("committed authority after helper completion = title %q", got.Title)
	}
	assertNoSnapshotTemps(t, snapshotPath)
}

func TestSessionStorageContinuity_HelperProcessActiveSave(t *testing.T) {
	if os.Getenv("MECATL_JSONLSTORE_HELPER") != "1" {
		return
	}
	dir := os.Getenv("MECATL_JSONLSTORE_DIR")
	ready := os.NewFile(3, "jsonlstore-helper-ready")
	release := os.NewFile(4, "jsonlstore-helper-release")
	if ready == nil || release == nil {
		t.Fatal("helper control pipes are unavailable")
	}
	defer func() { _ = ready.Close() }()
	defer func() { _ = release.Close() }()

	ops := defaultSnapshotOps()
	write := ops.write
	ops.write = func(f *os.File, data []byte) (int, error) {
		if _, err := ready.Write([]byte(f.Name() + "\n")); err != nil {
			return 0, err
		}
		var signal [1]byte
		if _, err := release.Read(signal[:]); err != nil {
			return 0, err
		}
		return write(f, data)
	}
	st, err := newStoreWithSnapshotOps(dir, ops)
	if err != nil {
		t.Fatalf("new helper Store: %v", err)
	}
	if err := st.Save(context.Background(), newSnapshotSession("cross-process-active-temp", "child-committed")); err != nil {
		t.Fatalf("helper Save: %v", err)
	}
}

func assertNoSnapshotTemps(t *testing.T, snapshotPath string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(snapshotPath))
	if err != nil {
		t.Fatalf("ReadDir snapshot directory: %v", err)
	}
	prefix := filepath.Base(snapshotPath) + snapshotTempMarker
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			t.Errorf("orphan snapshot temp remains: %s", entry.Name())
		}
	}
}

func newSnapshotSession(id session.SessionID, title string) *session.Session {
	s := session.New(id, session.ModeDefault, "/workspace", session.Limits{}, time.Unix(1700000000, 0).UTC())
	s.SetTitle(title)
	return s
}
