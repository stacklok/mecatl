package codec

import (
	"bytes"
	"testing"
)

func TestReaderFixedBytes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		data        []byte
		length, cap int
		want        error
	}{
		{"exact", []byte{0x43, 0, 255, 128}, 3, 3, nil},
		{"empty", []byte{0x40}, 0, 1, nil},
		{"short", []byte{0x42, 0, 1}, 3, 3, ErrInvalidData},
		{"long declaration only", []byte{0x44}, 3, 4, ErrInvalidData},
		{"short declaration only", []byte{0x42}, 3, 3, ErrInvalidData},
		{"field cap", []byte{0x43, 0, 1, 2}, 3, 2, ErrLimitExceeded},
		{"negative length", []byte{0x40}, -1, 1, ErrInvalidLimits},
		{"invalid cap", []byte{0x40}, 0, 0, ErrInvalidLimits},
		{"wrong type", []byte{0x60}, 0, 1, ErrInvalidData},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testReader(t, tc.data, testLimits())
			got, err := r.FixedBytes(0, tc.cap, tc.length)
			requireError(t, err, tc.want)
			if tc.want != nil {
				if got != nil {
					t.Fatal("non-nil failure output")
				}
				return
			}
			if got == nil || !bytes.Equal(got, tc.data[1:]) {
				t.Fatal("incorrect owning result", got)
			}
			if len(got) > 0 {
				got[0] = 99
				if tc.data[1] == 99 {
					t.Fatal("result borrows input")
				}
			}
			if err := r.Done(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
