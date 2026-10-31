package format_test

import (
	"bytes"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func TestLimits(t *testing.T) {
	c := validRoot(t)
	c.Records[0].Kind = "kind"
	c.Records[0].Payload = []byte{1, 2, 3, 4, 5}
	c.Records = append(c.Records, c.Records[0].Clone())
	data, _, err := format.EncodeCommit(c, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	exact := format.Limits{MaxCommitBytes: len(data), MaxRecords: 2, MaxKindBytes: 4, MaxPayloadBytes: 5}
	for field := range 4 {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("field-%d/delta-%d", field, delta), func(t *testing.T) {
				l := exact
				fields := []*int{&l.MaxCommitBytes, &l.MaxRecords, &l.MaxKindBytes, &l.MaxPayloadBytes}
				*fields[field] += delta
				if delta < 0 {
					assertEncodeError(t, c, l)
					assertDecodeError(t, data, l)
					return
				}
				encoded, cid, err := format.EncodeCommit(c, l)
				if err != nil {
					t.Fatal(err)
				}
				decoded, dcid, err := format.DecodeCommit(data, l)
				if err != nil || !decoded.Equal(c) || !bytes.Equal(encoded, data) || dcid != cid {
					t.Fatalf("boundary rejected: %v", err)
				}
			})
		}
	}
	for field := range 4 {
		for _, bad := range []int{0, -1, math.MinInt} {
			l := exact
			fields := []*int{&l.MaxCommitBytes, &l.MaxRecords, &l.MaxKindBytes, &l.MaxPayloadBytes}
			*fields[field] = bad
			assertEncodeError(t, c, l)
			assertEncodeError(t, format.Commit{}, l)
			assertDecodeError(t, data, l)
			assertDecodeError(t, nil, l)
		}
	}
	for _, invalid := range []format.Limits{{}, {MaxCommitBytes: -1, MaxRecords: -1, MaxKindBytes: -1, MaxPayloadBytes: -1}} {
		assertEncodeError(t, c, invalid)
		assertDecodeError(t, nil, invalid)
	}
	unrestrictedLimits := format.Limits{MaxCommitBytes: math.MaxInt, MaxRecords: math.MaxInt, MaxKindBytes: math.MaxInt, MaxPayloadBytes: math.MaxInt}
	for _, n := range []int{1, 23, 24, 255, 256, 65535, 65536} {
		wide := validRoot(t)
		wide.Records[0].Kind = strings.Repeat("k", n)
		wide.Records[0].Payload = bytes.Repeat([]byte{0xff}, n)
		for _, version := range []int64{1, 23, 24, 255, 256, 65535, 65536, math.MaxUint32, math.MaxUint32 + 1, math.MaxInt64} {
			wide.Records[0].KindVersion = version
			encoded, _, err := format.EncodeCommit(wide, unrestrictedLimits)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) != cap(encoded) {
				t.Fatal("incorrect exact size")
			}
			exact := unrestrictedLimits
			exact.MaxCommitBytes = len(encoded)
			if _, _, err := format.EncodeCommit(wide, exact); err != nil {
				t.Fatal(err)
			}
			decoded, _, err := format.DecodeCommit(encoded, exact)
			if err != nil || !decoded.Equal(wide) {
				t.Fatalf("max limits/size transition: %v", err)
			}
			exact.MaxCommitBytes--
			assertEncodeError(t, wide, exact)
			assertDecodeError(t, encoded, exact)
		}
	}
	for _, count := range []int{23, 24, 255, 256} {
		group := validRoot(t)
		group.Records = make([]format.Record, count)
		for i := range group.Records {
			group.Records[i] = format.Record{Kind: "x", KindVersion: 1}
		}
		encoded, _, err := format.EncodeCommit(group, unrestrictedLimits)
		if err != nil {
			t.Fatal(err)
		}
		if len(encoded) != cap(encoded) {
			t.Fatal("incorrect array header sizing")
		}
		if decoded, _, err := format.DecodeCommit(encoded, unrestrictedLimits); err != nil || !decoded.Equal(group) {
			t.Fatalf("array header transition: %v", err)
		}
	}
}
