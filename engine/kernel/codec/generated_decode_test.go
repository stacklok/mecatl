package codec_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/codec/internal/fixture"
)

func TestGeneratedUntrustedDeclarationsBeforeNarrowing(t *testing.T) {
	// uint64 declarations cannot narrow to machine int or govern allocation until
	// both field/global caps pass. No content bytes are needed for this rejection.
	for _, hex := range []string{
		"bbffffffffffffffff",
		"ab62693800626f6bf4627538006369313600636933320063693634006375313600637533320063753634006566697865645bffffffffffffffff",
		"ab62693800626f6bf46275380063693136006369333200636936340063753136006375333200637536340065666978656443000000656c6162656c7bffffffffffffffff",
	} {
		v, err := fixture.DecodeSample(unhex(t, hex), generatedLimits())
		if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(v, fixture.Sample{}) {
			t.Fatalf("%s: %#v %v", hex, v, err)
		}
	}
	for _, hex := range []string{
		"a4656e616d65739bffffffffffffffff",
		"a4656e616d65738067656e7472696573bbffffffffffffffff",
	} {
		v, err := fixture.DecodeCatalog(unhex(t, hex), generatedLimits())
		if !errors.Is(err, codec.ErrLimitExceeded) || !reflect.DeepEqual(v, fixture.Catalog{}) {
			t.Fatalf("%s: %#v %v", hex, v, err)
		}
	}
}
