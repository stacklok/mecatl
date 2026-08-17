package jsonlstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

type virtualTailReader struct {
	size      int64
	suffix    []byte
	bytesRead int64
	minOffset int64
}

func (r *virtualTailReader) ReadAt(p []byte, off int64) (int, error) {
	if r.minOffset == 0 || off < r.minOffset {
		r.minOffset = off
	}
	r.bytesRead += int64(len(p))
	for i := range p {
		p[i] = 'x'
	}
	suffixStart := r.size - int64(len(r.suffix))
	for i := range p {
		pos := off + int64(i)
		if pos >= suffixStart {
			p[i] = r.suffix[pos-suffixStart]
		}
	}
	return len(p), nil
}

func TestReadLastLineAtIsBoundedByLatestRecord(t *testing.T) {
	latest := []byte(strings.Repeat("z", 1024*1024))
	r := &virtualTailReader{
		size:   8 << 30,
		suffix: append(append([]byte{'\n'}, latest...), '\n'),
	}
	got, err := readLastLineAt(r, r.size)
	if err != nil {
		t.Fatalf("readLastLineAt: %v", err)
	}
	if !bytes.Equal(got, latest) {
		t.Fatalf("latest record length = %d, want %d", len(got), len(latest))
	}
	if r.bytesRead > int64(4*len(latest)+lastLineSeekWindow) {
		t.Fatalf("read %d bytes for a %d-byte latest record over an 8 GiB history", r.bytesRead, len(latest))
	}
	if r.minOffset < r.size-int64(2*len(latest)+lastLineSeekWindow) {
		t.Fatalf("read reached offset %d, too far into the 8 GiB history", r.minOffset)
	}
}

func TestStoreLoadAndMetaListSkipOversizedHistory(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	sess := session.New("large-tail", session.ModeDefault, "/ws", session.Limits{}, time.Unix(1700000000, 0).UTC())
	if err := sess.SeedHistory([]session.Message{session.NewUserMessage(strings.Repeat("z", 128*1024))}); err != nil {
		t.Fatalf("SeedHistory: %v", err)
	}
	latest, err := sessnap.Marshal(sess)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if len(latest) <= lastLineSeekWindow {
		t.Fatalf("fixture latest record = %d bytes, want > %d", len(latest), lastLineSeekWindow)
	}
	path := st.resolver.canonicalPath(sess.ID, kindSnapshot)
	f, err := os.Create(path) //nolint:gosec // test-owned temporary store
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := f.Seek(int64(maxScannerTokenSize+1), 0); err != nil {
		t.Fatalf("Seek sparse history: %v", err)
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		t.Fatalf("Write history delimiter: %v", err)
	}
	if _, err := f.Write(append(latest, '\n')); err != nil {
		t.Fatalf("Write latest: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	loaded, err := st.Load(context.Background(), sess.ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != sess.ID || len(loaded.Conversation.Messages) != 1 {
		t.Fatalf("Load = id %q messages %d, want %q/1", loaded.ID, len(loaded.Conversation.Messages), sess.ID)
	}
	rows, err := st.MetaList(context.Background())
	if err != nil {
		t.Fatalf("MetaList: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != sess.ID {
		t.Fatalf("MetaList = %#v, want one %q row", rows, sess.ID)
	}
}

func TestLastNonBlankRecord(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"final newline", "old\nlatest\n", "latest"},
		{"missing final newline", "old\nlatest", "latest"},
		{"trailing blank lines", "old\nlatest\n \t\n\r\n", "latest"},
		{"crlf", "old\r\nlatest\r\n", "latest"},
		{"single record", "latest", "latest"},
		{"empty", "", ""},
		{"blank", " \t\r\n", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, complete, err := lastNonBlankRecord([]byte(tc.input), true)
			if err != nil {
				t.Fatalf("lastNonBlankRecord: %v", err)
			}
			if !complete {
				t.Fatal("complete = false, want true for a whole-file window")
			}
			if string(got) != tc.want {
				t.Fatalf("record = %q, want %q", got, tc.want)
			}
		})
	}
}

type failingReaderAt struct{ err error }

func (r failingReaderAt) ReadAt([]byte, int64) (int, error) { return 0, r.err }

func TestReadLastLineAtPropagatesReadError(t *testing.T) {
	want := errors.New("read failed")
	if _, err := readLastLineAt(failingReaderAt{err: want}, 1); !errors.Is(err, want) {
		t.Fatalf("readLastLineAt error = %v, want %v", err, want)
	}
}

func TestReadLastLineAtRejectsOversizedLatestRecord(t *testing.T) {
	r := &virtualTailReader{size: int64(maxScannerTokenSize + lastLineSeekWindow)}
	if _, err := readLastLineAt(r, r.size); err == nil {
		t.Fatal("readLastLineAt accepted a latest record larger than the configured ceiling")
	}
	if r.bytesRead > int64(4*maxScannerTokenSize) {
		t.Fatalf("oversized rejection read %d bytes, want a bounded reverse scan", r.bytesRead)
	}
}

func BenchmarkReadLastLineAtLargeHistory(b *testing.B) {
	latest := []byte(strings.Repeat("z", 1024*1024))
	r := &virtualTailReader{
		size:   8 << 30,
		suffix: append(append([]byte{'\n'}, latest...), '\n'),
	}
	b.SetBytes(int64(len(latest)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := readLastLineAt(r, r.size); err != nil {
			b.Fatal(err)
		}
	}
}
