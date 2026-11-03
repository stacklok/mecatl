package format_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

func limitFields(l *codec.Limits) []*int {
	return []*int{&l.MaxBytes, &l.MaxArrayElements, &l.MaxStringBytes, &l.MaxByteStringBytes,
		&l.MaxMapPairs, &l.MaxDepth, &l.MaxNodes, &l.MaxCollectionElements}
}

func maximumLimits() codec.Limits {
	l := testLimits()
	for _, f := range limitFields(&l) {
		*f = math.MaxInt
	}
	return l
}

func TestLimits(t *testing.T) {
	c := validRoot(t)
	c.Records[0].Kind = strings.Repeat("k", 40) // Longer than the history ID.
	c.Records[0].Payload = bytes.Repeat([]byte{0xff}, 50)
	c.Records = append(c.Records, c.Records[0].Clone())
	data, err := codec.Encode(c, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	// Find each tight global bound independently; both passes use conservative
	// byte accounting in addition to the whole-input size cap.
	for field := range len(limitFields(new(codec.Limits))) {
		t.Run(fmt.Sprintf("field-%d", field), func(t *testing.T) {
			for _, decode := range []bool{false, true} {
				l := testLimits()
				bound := *limitFields(&l)[field]
				found := false
				for n := 1; n <= bound; n++ {
					*limitFields(&l)[field] = n
					var err error
					if decode {
						_, err = codec.Decode[format.Commit](data, l)
					} else {
						err = codec.Check(c, l)
					}
					if err != nil {
						if !errors.Is(err, codec.ErrLimitExceeded) {
							t.Fatal(err)
						}
						if decode {
							assertDecodeError(t, data, l)
						} else {
							assertEncodeError(t, c, l)
						}
						continue
					}
					if n == 1 {
						t.Fatal("fixture did not exercise bound")
					}
					if decode {
						got, err := codec.Decode[format.Commit](data, l)
						if err != nil || !got.Equal(c) {
							t.Fatal("tight input bound rejected")
						}
					} else {
						got, err := codec.Encode(c, l)
						if err != nil || !bytes.Equal(got, data) {
							t.Fatal("tight admission bound rejected")
						}
					}
					found = true
					*limitFields(&l)[field] = n + 1
					if decode {
						_, err = codec.Decode[format.Commit](data, l)
					} else {
						err = codec.Check(c, l)
					}
					if err != nil {
						t.Fatal(err)
					}
					break
				}
				if !found {
					t.Fatal("fixture never admitted within test policy")
				}
			}
			for _, bad := range []int{0, -1, math.MinInt} {
				l := testLimits()
				*limitFields(&l)[field] = bad
				before := l
				assertEncodeError(t, c, l)
				assertEncodeError(t, format.Commit{}, l)
				assertDecodeError(t, data, l)
				assertDecodeError(t, nil, l)
				if _, err := codec.NewBudget(l); !errors.Is(err, codec.ErrInvalidLimits) || l != before {
					t.Fatal("invalid policy contract")
				}
			}
		})
	}
}

func TestCodecHeaderTransitions(t *testing.T) {
	l := maximumLimits()
	roundtrip := func(c format.Commit) {
		t.Helper()
		data, err := codec.Encode(c, l)
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.Decode[format.Commit](data, l)
		if err != nil || !got.Equal(c) {
			t.Fatalf("header transition: %v", err)
		}
		short := l
		short.MaxBytes = len(data) - 1
		assertEncodeError(t, c, short)
		assertDecodeError(t, data, short)
		// Both passes share conservative admission; complete input length is
		// also bounded, but is not the sole decode resource charge.
		budget, err := codec.NewBudget(l)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.CheckCBOR(&budget, 0, math.MaxInt); err != nil {
			t.Fatal(err)
		}
		exact := l
		exact.MaxBytes = budget.EstimatedBytes()
		if got, err := codec.Decode[format.Commit](data, exact); err != nil || !got.Equal(c) {
			t.Fatalf("exact admission: %v", err)
		}
		if got, err := codec.Encode(c, exact); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("exact encode admission: %v", err)
		}
		exact.MaxBytes--
		assertEncodeError(t, c, exact)
		assertDecodeError(t, data, exact)
	}
	for _, n := range []int{1, 23, 24, 255, 256, 65535, 65536} {
		wide := validRoot(t)
		wide.Records[0].Kind, wide.Records[0].Payload = strings.Repeat("k", n), bytes.Repeat([]byte{0xff}, n)
		for _, version := range []int64{1, 23, 24, 255, 256, 65535, 65536, math.MaxUint32, math.MaxUint32 + 1, math.MaxInt64} {
			wide.Records[0].KindVersion = version
			roundtrip(wide)
		}
	}
	for _, sequence := range []int64{2, 23, 24, 255, 256, 65535, 65536, math.MaxUint32, math.MaxUint32 + 1, math.MaxInt64} {
		roundtrip(successorOf(validRoot(t), contentid.Sum(nil), sequence))
	}
	for _, count := range []int{23, 24, 255, 256} {
		group := validRoot(t)
		group.Records = make([]format.Record, count)
		for i := range group.Records {
			group.Records[i] = format.Record{Kind: "x", KindVersion: 1}
		}
		roundtrip(group)
	}
}
