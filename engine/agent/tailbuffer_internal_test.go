package agent

import (
	"strings"
	"sync"
	"testing"
)

func TestTailBufferRetainsLastBytes(t *testing.T) {
	buf := newTailBuffer(8)
	buf.Write([]byte("0123456789abcdef")) // 16 bytes, keep last 8
	if got := buf.Snapshot(); got != "89abcdef" {
		t.Fatalf("snapshot = %q, want %q", got, "89abcdef")
	}
	if !buf.Truncated() {
		t.Fatal("truncated = false, want true after dropping bytes")
	}
}

func TestTailBufferTruncatedFlagExact(t *testing.T) {
	buf := newTailBuffer(8)
	buf.Write([]byte("1234"))
	if buf.Truncated() {
		t.Fatal("truncated flipped before any byte was dropped")
	}
	buf.Write([]byte("5678")) // now exactly full, still nothing dropped
	if buf.Truncated() {
		t.Fatal("truncated flipped at exact capacity with no drop")
	}
	buf.Write([]byte("9")) // drops one byte
	if !buf.Truncated() {
		t.Fatal("truncated did not flip when a byte was dropped")
	}
	if got := buf.Snapshot(); got != "23456789" {
		t.Fatalf("snapshot = %q, want %q", got, "23456789")
	}
}

func TestTailBufferExactCapacityBoundary(t *testing.T) {
	// write == capacity: retained verbatim, not truncated.
	buf := newTailBuffer(8)
	buf.Write([]byte("abcdefgh"))
	if got := buf.Snapshot(); got != "abcdefgh" {
		t.Fatalf("snapshot = %q, want %q", got, "abcdefgh")
	}
	if buf.Truncated() {
		t.Fatal("truncated on exact-capacity write")
	}

	// write == capacity+1: keeps the LAST capacity bytes.
	buf = newTailBuffer(8)
	buf.Write([]byte("abcdefghi"))
	if got := buf.Snapshot(); got != "bcdefghi" {
		t.Fatalf("snapshot = %q, want %q", got, "bcdefghi")
	}
	if !buf.Truncated() {
		t.Fatal("not truncated on capacity+1 write")
	}
}

func TestTailBufferEmptySnapshot(t *testing.T) {
	buf := newTailBuffer(8)
	if got := buf.Snapshot(); got != "" {
		t.Fatalf("snapshot = %q, want empty", got)
	}
	buf.Write(nil)
	buf.Write([]byte{})
	if got := buf.Snapshot(); got != "" {
		t.Fatalf("snapshot after empty writes = %q, want empty", got)
	}
	if buf.Truncated() {
		t.Fatal("truncated after empty writes")
	}
}

func TestTailBufferManySmallWrites(t *testing.T) {
	buf := newTailBuffer(16)
	var want strings.Builder
	for i := 0; i < 100; i++ {
		s := strings.Repeat(string(rune('a'+i%26)), 3)
		buf.Write([]byte(s))
		want.WriteString(s)
	}
	full := want.String()
	if got := buf.Snapshot(); got != full[len(full)-16:] {
		t.Fatalf("snapshot = %q, want tail %q", got, full[len(full)-16:])
	}
	if !buf.Truncated() {
		t.Fatal("not truncated after 300 bytes into 16")
	}
}

func TestTailBufferConcurrent(t *testing.T) {
	buf := newTailBuffer(1024)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			chunk := []byte(strings.Repeat(string(rune('a'+id)), 64))
			for i := 0; i < 200; i++ {
				buf.Write(chunk)
			}
		}(w)
	}
	for r := 0; r < 2; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				_ = buf.Snapshot()
				_ = buf.Truncated()
			}
		}()
	}
	wg.Wait()
	if got := len(buf.Snapshot()); got != 1024 {
		t.Fatalf("len(snapshot) = %d, want 1024", got)
	}
	if !buf.Truncated() {
		t.Fatal("not truncated after 51200 bytes into 1024")
	}
}
