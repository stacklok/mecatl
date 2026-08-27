package jsonlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestEventLogAppendRequiresAllSyncCapabilitiesBeforeMutatingSidecar(t *testing.T) {
	for _, capability := range []struct {
		name    string
		disable func(*Store)
	}{
		{
			name:    "file sync",
			disable: func(st *Store) { st.durability.FileSync = false },
		},
		{
			name:    "directory sync",
			disable: func(st *Store) { st.durability.DirectorySync = false },
		},
	} {
		for _, existing := range []bool{false, true} {
			t.Run(capability.name+"/existing="+strconv.FormatBool(existing), func(t *testing.T) {
				st := newInternalStore(t)
				id := session.SessionID("unsupported-sync")
				path := st.resolver.canonicalPath(id, kindEvents)
				want := []byte("existing sidecar bytes\n")
				if existing {
					writeBytes(t, path, want)
				}
				capability.disable(st)
				for seq := int64(1); seq <= 2; seq++ {
					if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: seq}); err == nil {
						t.Fatalf("Append #%d succeeded without %s", seq, capability.name)
					}
				}
				got, err := os.ReadFile(path)
				if !existing {
					if !os.IsNotExist(err) {
						t.Fatalf("sidecar after failed appends = %q, %v; want absent", got, err)
					}
					return
				}
				if err != nil {
					t.Fatalf("ReadFile existing sidecar: %v", err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("existing sidecar changed after failed appends: got %q, want %q", got, want)
				}
			})
		}
	}
}

func TestEventLogAppendDurabilityFailures(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inject    func(*snapshotOps)
		want      error
		committed bool
	}{
		{
			name: "short write",
			inject: func(ops *snapshotOps) {
				ops.write = func(*os.File, []byte) (int, error) { return 1, nil }
			},
			want: io.ErrShortWrite,
		},
		{
			name: "partial error write",
			inject: func(ops *snapshotOps) {
				ops.write = func(f *os.File, data []byte) (int, error) {
					n, _ := f.Write(data[:len(data)/2])
					return n, syscall.EIO
				}
			},
			want: syscall.EIO,
		},
		{
			name: "file sync",
			inject: func(ops *snapshotOps) {
				ops.syncFile = func(*os.File) error { return syscall.EIO }
			},
			want: syscall.EIO, committed: true,
		},
		{
			name: "close",
			inject: func(ops *snapshotOps) {
				ops.closeFile = func(f *os.File) error {
					_ = f.Close()
					return syscall.EIO
				}
			},
			want: syscall.EIO, committed: true,
		},
		{
			name: "directory sync",
			inject: func(ops *snapshotOps) {
				ops.syncDir = func(*os.File) error { return syscall.EIO }
			},
			want: syscall.EIO, committed: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newInternalStore(t)
			original := st.snapshot
			tc.inject(&st.snapshot)
			id := session.SessionID("durability-fault")
			err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 1})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Append error = %v, want %v", err, tc.want)
			}
			got := collectEvents(t, st, id)
			if tc.committed {
				if len(got) != 1 || got[0].Seq != 1 {
					t.Fatalf("committed prefix after %s failure = %+v, want seq 1", tc.name, got)
				}
			} else if len(got) != 0 {
				t.Fatalf("uncommitted prefix after %s failure = %+v, want empty", tc.name, got)
			}

			st.snapshot = original
			if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 2}); err != nil {
				t.Fatalf("retry after %s failure: %v", tc.name, err)
			}
			got = collectEvents(t, st, id)
			wantSeqs := []int64{2}
			if tc.committed {
				wantSeqs = []int64{1, 2}
			}
			if fmt.Sprint(eventSeqs(got)) != fmt.Sprint(wantSeqs) {
				t.Fatalf("reopened events after retry = %v, want %v", eventSeqs(got), wantSeqs)
			}
		})
	}
}

func eventSeqs(events []session.Event) []int64 {
	seqs := make([]int64, len(events))
	for i := range events {
		seqs[i] = events[i].Seq
	}
	return seqs
}

func TestEventLogAppendRetryAfterPartialWrite(t *testing.T) {
	st := newInternalStore(t)
	write := st.snapshot.write
	st.snapshot.write = func(f *os.File, data []byte) (int, error) {
		n, _ := f.Write(data[:len(data)/2])
		return n, syscall.EIO
	}
	ev := session.Event{Type: session.EvResult, Seq: 7}
	if err := st.Append(context.Background(), "retry-torn-event", ev); !errors.Is(err, syscall.EIO) {
		t.Fatalf("partial Append error = %v, want EIO", err)
	}
	st.snapshot.write = write
	if err := st.Append(context.Background(), "retry-torn-event", ev); err != nil {
		t.Fatalf("retry Append: %v", err)
	}
	got := collectEvents(t, st, "retry-torn-event")
	if len(got) != 1 || got[0].Seq != ev.Seq {
		t.Fatalf("Read after retry = %+v, want exactly the retried event", got)
	}
}

func TestEventLogAppendRepairsTornTailAndCreatesPrivateSidecar(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("repair-torn-events")
	path := st.resolver.canonicalPath(id, kindEvents)
	first := eventRecordLine(t, session.Event{Type: session.EvMessageDelta, Seq: 1})
	writeBytes(t, path, append(append(first, '\n'), []byte("TORN-FRAGMENT")...))

	if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 2}); err != nil {
		t.Fatalf("Append after torn tail: %v", err)
	}
	got := collectEvents(t, st, id)
	if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("Read after repair = %+v, want seq 1 and 2", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), "TORN-FRAGMENT") {
		t.Fatalf("torn fragment was retained: %q", raw)
	}

	privateID := session.SessionID("private-sidecar")
	if err := st.Append(context.Background(), privateID, session.Event{Type: session.EvResult}); err != nil {
		t.Fatalf("Append private sidecar: %v", err)
	}
	info, err := os.Stat(st.resolver.canonicalPath(privateID, kindEvents))
	if err != nil {
		t.Fatalf("Stat sidecar: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o600 {
		t.Fatalf("sidecar mode = %04o, want 0600", gotMode)
	}
}

func TestEventLogReadCommitMarker(t *testing.T) {
	good := func(t *testing.T, seq int64) []byte {
		t.Helper()
		return append(eventRecordLine(t, session.Event{Type: session.EvMessageDelta, Seq: seq}), '\n')
	}
	for _, tc := range []struct {
		name      string
		contents  func(*testing.T) []byte
		wantSeqs  []int64
		wantError bool
	}{
		{
			name: "unterminated malformed final record is ignored",
			contents: func(t *testing.T) []byte {
				return append(good(t, 1), []byte(`{"v":"eventlog-json/1","ev":{`)...)
			},
			wantSeqs: []int64{1},
		},
		{
			name: "fully torn first record is ignored",
			contents: func(*testing.T) []byte {
				return []byte(`{"v":"eventlog-json/1"`)
			},
		},
		{
			name: "newline terminated malformed final record fails",
			contents: func(t *testing.T) []byte {
				return append(good(t, 1), []byte("{not-json\n")...)
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "blank final record fails",
			contents: func(t *testing.T) []byte {
				return append(good(t, 1), '\n')
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "whitespace final record fails",
			contents: func(t *testing.T) []byte {
				return append(good(t, 1), []byte(" \t \n")...)
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "whitespace middle record fails before later valid record",
			contents: func(t *testing.T) []byte {
				out := append(good(t, 1), []byte(" \t\n")...)
				return append(out, good(t, 2)...)
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "malformed middle record fails before later valid record",
			contents: func(t *testing.T) []byte {
				out := append(good(t, 1), []byte("{not-json\n")...)
				return append(out, good(t, 2)...)
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "complete unknown format fails",
			contents: func(t *testing.T) []byte {
				return append(good(t, 1), []byte("{\"v\":\"eventlog-json/999\",\"ev\":{}}\n")...)
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "complete malformed event payload fails",
			contents: func(t *testing.T) []byte {
				return append(good(t, 1), []byte("{\"v\":\"eventlog-json/1\",\"ev\":\"bad\"}\n")...)
			},
			wantSeqs:  []int64{1},
			wantError: true,
		},
		{
			name: "scanner fault fails",
			contents: func(_ *testing.T) []byte {
				return append(make([]byte, maxScannerTokenSize+1), '\n')
			},
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newInternalStore(t)
			id := session.SessionID("commit-marker")
			writeBytes(t, st.resolver.canonicalPath(id, kindEvents), tc.contents(t))
			var seqs []int64
			var sawErr bool
			for ev, err := range st.Read(context.Background(), id) {
				if err != nil {
					sawErr = true
					break
				}
				seqs = append(seqs, ev.Seq)
			}
			if fmt.Sprint(seqs) != fmt.Sprint(tc.wantSeqs) || sawErr != tc.wantError {
				t.Fatalf("Read = seqs %v, error %v; want seqs %v, error %v", seqs, sawErr, tc.wantSeqs, tc.wantError)
			}
		})
	}
}

func TestEventLogReadCapturesCoherentPrefixBeforeYield(t *testing.T) {
	st := newInternalStore(t)
	id := session.SessionID("coherent-read-prefix")
	for seq := int64(1); seq <= 2; seq++ {
		if err := st.Append(context.Background(), id, session.Event{Type: session.EvMessageDelta, Seq: seq}); err != nil {
			t.Fatalf("Append %d: %v", seq, err)
		}
	}
	path := st.resolver.canonicalPath(id, kindEvents)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("open torn-tail seed: %v", err)
	}
	if _, err := f.WriteString("TORN"); err != nil {
		t.Fatalf("write torn-tail seed: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close torn-tail seed: %v", err)
	}

	var got []int64
	for ev, err := range st.Read(context.Background(), id) {
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		got = append(got, ev.Seq)
		if len(got) == 1 {
			done := make(chan error, 1)
			go func() {
				done <- st.Append(context.Background(), id, session.Event{Type: session.EvMessageDelta, Seq: 3})
			}()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("concurrent Append from yield: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("concurrent Append blocked behind Read yield")
			}
		}
	}
	if fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("captured prefix = %v, want [1 2]", got)
	}
	all := collectEvents(t, st, id)
	if len(all) != 3 || all[0].Seq != 1 || all[1].Seq != 2 || all[2].Seq != 3 {
		t.Fatalf("events after append/tail repair = %+v, want seq 1,2,3", all)
	}
}

func TestEventLogRejectsSymlinkAndSpecialSidecars(t *testing.T) {
	t.Run("outside symlink", func(t *testing.T) {
		st := newInternalStore(t)
		id := session.SessionID("symlink-event")
		outside := filepath.Join(t.TempDir(), "outside")
		writeBytes(t, outside, []byte("outside-safe"))
		path := st.resolver.canonicalPath(id, kindEvents)
		if err := os.Symlink(outside, path); err != nil {
			t.Fatalf("Symlink: %v", err)
		}
		if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 1}); err == nil {
			t.Fatal("Append through outside symlink succeeded")
		}
		assertBytes(t, outside, []byte("outside-safe"))
		errs := readEventErrors(st, id)
		if len(errs) == 0 {
			t.Fatal("Read through outside symlink did not fail")
		}
		if errs[0] == nil {
			t.Fatal("Read through outside symlink yielded an event")
		}
	})

	t.Run("fifo", func(t *testing.T) {
		st := newInternalStore(t)
		id := session.SessionID("fifo-event")
		path := st.resolver.canonicalPath(id, kindEvents)
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Skipf("mkfifo unavailable: %v", err)
		}
		assertPromptFailure(t, "Append FIFO", func() error {
			return st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 1})
		})
		assertPromptFailure(t, "Read FIFO", func() error {
			errs := readEventErrors(st, id)
			if len(errs) == 0 {
				return errors.New("Read FIFO returned no error")
			}
			return errs[0]
		})
		if err := os.Remove(path); err != nil {
			t.Fatalf("remove FIFO: %v", err)
		}
		if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 2}); err != nil {
			t.Fatalf("family lock remained wedged after FIFO rejection: %v", err)
		}
	})
}

func readEventErrors(st *Store, id session.SessionID) []error {
	var errs []error
	for _, err := range st.Read(context.Background(), id) {
		errs = append(errs, err)
	}
	return errs
}

func assertPromptFailure(t *testing.T, label string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("%s succeeded", label)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s blocked", label)
	}
}

func TestEventLogInterruptedProcessRepairsTornTailAfterOSLockRelease(t *testing.T) {
	if os.Getenv("MECATL_JSONLSTORE_TORN_HELPER") == "1" {
		dir := os.Getenv("MECATL_JSONLSTORE_DIR")
		st, err := New(dir)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		id := session.SessionID("interrupted-events")
		err = st.withSnapshotFamilyLock(context.Background(), st.resolver.currentSnapshotPath(id), func() error {
			path := st.resolver.canonicalPath(id, kindEvents)
			f, openErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // helper-owned path
			if openErr != nil {
				return openErr
			}
			first := eventRecordLine(t, session.Event{Type: session.EvMessageDelta, Seq: 1})
			if _, writeErr := f.Write(append(first, '\n')); writeErr != nil {
				return writeErr
			}
			if _, writeErr := f.Write([]byte(`{"v":"eventlog-json/1","ev":`)); writeErr != nil {
				return writeErr
			}
			if syncErr := f.Sync(); syncErr != nil {
				return syncErr
			}
			os.Exit(0) // deliberately skips Close and flock cleanup
			return nil
		})
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}

	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestEventLogInterruptedProcessRepairsTornTailAfterOSLockRelease$")
	cmd.Env = append(os.Environ(), "MECATL_JSONLSTORE_TORN_HELPER=1", "MECATL_JSONLSTORE_DIR="+dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("interrupted helper: %v: %s", err, out)
	}
	st, err := New(dir)
	if err != nil {
		t.Fatalf("reopen Store: %v", err)
	}
	if got := eventSeqs(collectEvents(t, st, "interrupted-events")); fmt.Sprint(got) != "[1]" {
		t.Fatalf("prefix after interrupted process = %v, want [1]", got)
	}
	if err := st.Append(context.Background(), "interrupted-events", session.Event{Type: session.EvResult, Seq: 2}); err != nil {
		t.Fatalf("Append after interrupted process: %v", err)
	}
	if got := eventSeqs(collectEvents(t, st, "interrupted-events")); fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("events after repair = %v, want [1 2]", got)
	}
}

func TestEventLogAppendCrossProcess(t *testing.T) {
	if os.Getenv("MECATL_JSONLSTORE_APPEND_HELPER") == "1" {
		runEventLogAppendHelper(t)
		return
	}

	dir := t.TempDir()
	const processes, perProcess = 4, 10
	commands := make([]*exec.Cmd, 0, processes)
	outputs := make([]bytes.Buffer, processes)
	readyReaders := make([]*os.File, 0, processes)
	startWriters := make([]*os.File, 0, processes)
	for process := 0; process < processes; process++ {
		readyR, readyW, err := os.Pipe()
		if err != nil {
			t.Fatalf("ready pipe %d: %v", process, err)
		}
		startR, startW, err := os.Pipe()
		if err != nil {
			t.Fatalf("start pipe %d: %v", process, err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestEventLogAppendCrossProcess$")
		cmd.Env = append(os.Environ(),
			"MECATL_JSONLSTORE_APPEND_HELPER=1",
			"MECATL_JSONLSTORE_DIR="+dir,
			"MECATL_JSONLSTORE_PROCESS="+strconv.Itoa(process),
			"MECATL_JSONLSTORE_COUNT="+strconv.Itoa(perProcess),
		)
		cmd.ExtraFiles = []*os.File{readyW, startR}
		cmd.Stdout = &outputs[process]
		cmd.Stderr = &outputs[process]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start append helper %d: %v", process, err)
		}
		_ = readyW.Close()
		_ = startR.Close()
		commands = append(commands, cmd)
		readyReaders = append(readyReaders, readyR)
		startWriters = append(startWriters, startW)
	}
	for process, ready := range readyReaders {
		var signal [1]byte
		if _, err := io.ReadFull(ready, signal[:]); err != nil {
			t.Fatalf("append helper %d readiness: %v: %s", process, err, outputs[process].String())
		}
		_ = ready.Close()
	}
	for _, start := range startWriters {
		_ = start.Close()
	}
	for process, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("append helper %d: %v: %s", process, err, outputs[process].String())
		}
	}

	st, err := New(dir)
	if err != nil {
		t.Fatalf("New reader: %v", err)
	}
	seen := make(map[int64]bool, processes*perProcess)
	for ev, err := range st.Read(context.Background(), "cross-process-events") {
		if err != nil {
			t.Fatalf("Read cross-process log: %v", err)
		}
		if ev.Seq < 0 || ev.Seq >= processes*perProcess {
			t.Fatalf("unexpected sequence %d", ev.Seq)
		}
		if seen[ev.Seq] {
			t.Fatalf("duplicate sequence %d", ev.Seq)
		}
		seen[ev.Seq] = true
		process := int(ev.Seq) / perProcess
		wantText := fmt.Sprintf("event-%d:", ev.Seq) + strings.Repeat(strconv.Itoa(process), 128*1024)
		if ev.Text != wantText {
			t.Fatalf("event %d payload is malformed or interleaved", ev.Seq)
		}
	}
	if len(seen) != processes*perProcess {
		t.Fatalf("Read %d unique events, want %d", len(seen), processes*perProcess)
	}
}

func runEventLogAppendHelper(t *testing.T) {
	process, err := strconv.Atoi(os.Getenv("MECATL_JSONLSTORE_PROCESS"))
	if err != nil {
		t.Fatalf("parse helper process: %v", err)
	}
	count, err := strconv.Atoi(os.Getenv("MECATL_JSONLSTORE_COUNT"))
	if err != nil {
		t.Fatalf("parse helper count: %v", err)
	}
	st, err := New(os.Getenv("MECATL_JSONLSTORE_DIR"))
	if err != nil {
		t.Fatalf("New helper Store: %v", err)
	}
	ready := os.NewFile(3, "jsonlstore-append-ready")
	start := os.NewFile(4, "jsonlstore-append-start")
	if ready == nil || start == nil {
		t.Fatal("append helper barrier pipes unavailable")
	}
	if _, err := ready.Write([]byte{1}); err != nil {
		t.Fatalf("signal helper readiness: %v", err)
	}
	_ = ready.Close()
	if _, err := io.ReadAll(start); err != nil {
		t.Fatalf("await append start: %v", err)
	}
	_ = start.Close()
	for i := 0; i < count; i++ {
		seq := int64(process*count + i)
		text := fmt.Sprintf("event-%d:", seq) + strings.Repeat(strconv.Itoa(process), 128*1024)
		if err := st.Append(context.Background(), "cross-process-events", session.Event{Type: session.EvMessageDelta, Seq: seq, Text: text}); err != nil {
			t.Fatalf("Append helper event %d: %v", seq, err)
		}
	}
}

func TestEventLogAppendRequiresDirectorySyncOnEveryAttempt(t *testing.T) {
	t.Run("unsupported first publication never succeeds on retry", func(t *testing.T) {
		st := newInternalStore(t)
		id := session.SessionID("new-sidecar")
		st.durability.DirectorySync = false
		for attempt := 1; attempt <= 2; attempt++ {
			if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: int64(attempt)}); err == nil {
				t.Fatalf("Append attempt %d without directory sync succeeded", attempt)
			}
		}
		assertMissing(t, st.resolver.canonicalPath(id, kindEvents))
	})

	t.Run("existing sidecar", func(t *testing.T) {
		st := newInternalStore(t)
		id := session.SessionID("existing-sidecar")
		if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: 1}); err != nil {
			t.Fatalf("seed Append: %v", err)
		}
		st.durability.DirectorySync = false
		for attempt := 1; attempt <= 2; attempt++ {
			if err := st.Append(context.Background(), id, session.Event{Type: session.EvResult, Seq: int64(attempt + 1)}); err == nil {
				t.Fatalf("Append attempt %d without directory sync succeeded", attempt)
			}
		}
		got := collectEvents(t, st, id)
		if fmt.Sprint(eventSeqs(got)) != "[1]" {
			t.Fatalf("unsupported append changed committed prefix: %v", eventSeqs(got))
		}
	})
}
