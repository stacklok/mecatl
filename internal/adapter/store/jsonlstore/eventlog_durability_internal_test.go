package jsonlstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/session"
)

func TestEventLogAppendDurabilityFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		inject func(*snapshotOps)
		want   error
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
			want: syscall.EIO,
		},
		{
			name: "directory sync on first creation",
			inject: func(ops *snapshotOps) {
				ops.syncDir = func(*os.File) error { return syscall.EIO }
			},
			want: syscall.EIO,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newInternalStore(t)
			tc.inject(&st.snapshot)
			err := st.Append(context.Background(), "durability-fault", session.Event{Type: session.EvResult, Seq: 1})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Append error = %v, want %v", err, tc.want)
			}
		})
	}
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

func TestEventLogAppendCrossProcess(t *testing.T) {
	if os.Getenv("MECATL_JSONLSTORE_APPEND_HELPER") == "1" {
		runEventLogAppendHelper(t)
		return
	}

	dir := t.TempDir()
	const processes, perProcess = 4, 10
	commands := make([]*exec.Cmd, 0, processes)
	outputs := make([]bytes.Buffer, processes)
	for process := 0; process < processes; process++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestEventLogAppendCrossProcess$")
		cmd.Env = append(os.Environ(),
			"MECATL_JSONLSTORE_APPEND_HELPER=1",
			"MECATL_JSONLSTORE_DIR="+dir,
			"MECATL_JSONLSTORE_PROCESS="+strconv.Itoa(process),
			"MECATL_JSONLSTORE_COUNT="+strconv.Itoa(perProcess),
		)
		cmd.Stdout = &outputs[process]
		cmd.Stderr = &outputs[process]
		if err := cmd.Start(); err != nil {
			t.Fatalf("start append helper %d: %v", process, err)
		}
		commands = append(commands, cmd)
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
	for i := 0; i < count; i++ {
		seq := int64(process*count + i)
		text := fmt.Sprintf("event-%d:", seq) + strings.Repeat(strconv.Itoa(process), 128*1024)
		if err := st.Append(context.Background(), "cross-process-events", session.Event{Type: session.EvMessageDelta, Seq: seq, Text: text}); err != nil {
			t.Fatalf("Append helper event %d: %v", seq, err)
		}
	}
}

func TestEventLogDirectorySyncOnlyOnCreation(t *testing.T) {
	st := newInternalStore(t)
	if err := st.Append(context.Background(), "existing-sidecar", session.Event{Type: session.EvResult, Seq: 1}); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	st.snapshot.syncDir = func(*os.File) error { return syscall.EIO }
	if err := st.Append(context.Background(), "existing-sidecar", session.Event{Type: session.EvResult, Seq: 2}); err != nil {
		t.Fatalf("Append to existing sidecar unexpectedly synced directory: %v", err)
	}
}
