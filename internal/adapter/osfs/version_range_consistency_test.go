package osfs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/tool"
)

type rangeTestInfo struct {
	size int64
	mod  time.Time
}

func (rangeTestInfo) Name() string         { return "evidence.txt" }
func (i rangeTestInfo) Size() int64        { return i.size }
func (rangeTestInfo) Mode() fs.FileMode    { return 0o600 }
func (i rangeTestInfo) ModTime() time.Time { return i.mod }
func (rangeTestInfo) IsDir() bool          { return false }
func (rangeTestInfo) Sys() any             { return nil }

type mutatingRangeReader struct {
	first, second []byte
	offset        int
	reads         int
	mod           time.Time
	mutate        bool
}

func (r *mutatingRangeReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.first) {
		return 0, io.EOF
	}
	source := r.first
	if r.reads > 0 {
		source = r.second
	}
	n := copy(p, source[r.offset:])
	r.offset += n
	r.reads++
	return n, nil
}

func (r *mutatingRangeReader) Stat() (fs.FileInfo, error) {
	mod := r.mod
	if r.mutate && r.reads > 1 {
		mod = mod.Add(time.Second)
	}
	return rangeTestInfo{size: int64(len(r.first)), mod: mod}, nil
}

func TestReadVersionRangeRejectsSameSizeRewriteDuringTraversal(t *testing.T) {
	benign := bytes.Repeat([]byte("a"), 64*1024)
	malicious := bytes.Repeat([]byte("z"), len(benign))
	reader := &mutatingRangeReader{first: benign, second: malicious, mod: time.Unix(1, 0), mutate: true}
	if _, _, _, err := readVersionRangeBoundedFile(context.Background(), "evidence.txt", reader, 0, 1024, int64(len(benign))); err == nil {
		t.Fatal("same-size rewrite during range traversal was accepted")
	}
}

func TestReadVersionRangePageMatchesHashedTraversal(t *testing.T) {
	content := bytes.Repeat([]byte("0123456789"), 7000)
	reader := &mutatingRangeReader{first: content, second: content, mod: time.Unix(1, 0)}
	page, version, total, err := readVersionRangeBoundedFile(context.Background(), "evidence.txt", reader, 123, 4567, int64(len(content)))
	if err != nil {
		t.Fatal(err)
	}
	wantHash := sha256.Sum256(content)
	if !bytes.Equal(page, content[123:123+4567]) || total != int64(len(content)) || !version.Equal(tool.NewFileVersion(hex.EncodeToString(wantHash[:]))) {
		t.Fatalf("range result was not derived from one traversal: page=%d total=%d", len(page), total)
	}
}
