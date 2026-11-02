package codec_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
	"github.com/stacklok/mecatl/engine/kernel/contentid"
)

func TestOwnerExactByteIdentityAndSemantics(t *testing.T) {
	unsupported := errors.New("unsupported owner version")
	semanticCalls := 0
	// The owner opts into identity independently of the generated codec. Verify
	// ORIGINAL bytes, never reencode the decoded value to check its identity.
	accept := func(original []byte, expected contentid.CID) (fixture.Sample, error) {
		v, err := fixture.DecodeSample(original, generatedLimits())
		if err != nil {
			return fixture.Sample{}, err
		}
		if err := contentid.Verify(original, expected); err != nil {
			return fixture.Sample{}, err
		}
		semanticCalls++
		if v.Label != "v1" {
			return fixture.Sample{}, unsupported
		}
		return v, nil
	}
	v := fixture.Sample{Label: "v1"}
	encoded, err := fixture.EncodeSample(v, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	expected := contentid.Sum(encoded)
	accepted, err := accept(encoded, expected)
	if err != nil || !accepted.Equal(v) || semanticCalls != 1 {
		t.Fatalf("accept = %#v, %v; semantic calls %d", accepted, err, semanticCalls)
	}
	future, err := fixture.EncodeSample(fixture.Sample{Label: "v2"}, generatedLimits())
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name     string
		data     []byte
		expected contentid.CID
		wantErr  error
		calls    int
	}{
		{"unsupported semantics with valid identity", future, contentid.Sum(future), unsupported, 1},
		{"mismatched identity before semantics", future, expected, contentid.ErrMismatch, 0},
		{"unset identity before semantics", encoded, contentid.CID{}, contentid.ErrInvalidExpected, 0},
		{"trailing bytes with matching identity", append(append([]byte{}, encoded...), 0), contentid.Sum(append(append([]byte{}, encoded...), 0)), codec.ErrInvalidData, 0},
		{"arbitrary bytes with matching identity", []byte{0xff, 0}, contentid.Sum([]byte{0xff, 0}), codec.ErrInvalidData, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			before := semanticCalls
			got, err := accept(tt.data, tt.expected)
			if !errors.Is(err, tt.wantErr) || !reflect.DeepEqual(got, fixture.Sample{}) || semanticCalls-before != tt.calls {
				t.Fatalf("accept = %#v, %v; semantic calls %d", got, err, semanticCalls-before)
			}
		})
	}
}
