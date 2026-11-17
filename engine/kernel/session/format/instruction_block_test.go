package format_test

import (
	"bytes"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/session/format"
)

func TestInstructionBlockKey(t *testing.T) {
	l := limits()
	l.MaxStringBytes = 1024
	for _, tc := range []struct {
		name string
		key  string
		want error
	}{
		{"required", "", codec.ErrInvalidData},
		{"invalid UTF-8", "a\xff", codec.ErrInvalidData},
		{"space", "harness/core ", codec.ErrInvalidData},
		{"leading space", " harness/core", codec.ErrInvalidData},
		{"tab", "a\tb", codec.ErrInvalidData},
		{"newline", "a\nb", codec.ErrInvalidData},
		{"unicode space", "a\u2003b", codec.ErrInvalidData},
		{"nonbreaking space", "a\u00a0b", codec.ErrInvalidData},
		{"NUL", "a\x00b", codec.ErrInvalidData},
		{"DEL", "a\x7fb", codec.ErrInvalidData},
		{"unicode control", "a\u0085b", codec.ErrInvalidData},
		{"control not space", "a\u009fb", codec.ErrInvalidData},
		{"ASCII boundary", strings.Repeat("a", 256), nil},
		{"ASCII over boundary", strings.Repeat("a", 257), codec.ErrLimitExceeded},
		{"multibyte boundary", strings.Repeat("é", 128), nil},
		{"multibyte over boundary", strings.Repeat("é", 128) + "a", codec.ErrLimitExceeded},
		{"single byte", "x", nil},
		{"open vocabulary", "CUSTOM:尾/?!", nil},
		{"format rune allowed", "a\u200bb", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := format.InstructionBlock{Key: tc.key, Source: format.ContentSource{Description: "empty contribution"}}
			if err := v.Validate(); (err == nil) != (tc.want == nil) {
				t.Fatalf("Validate = %v; codec expectation %v", err, tc.want)
			}
			if err := codec.Check(v, l); (tc.want == nil && err != nil) || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("CheckCBOR = %v; want %v", err, tc.want)
			}
			wire, err := codec.Encode(v, l)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || wire != nil {
					t.Fatalf("Encode = %x, %v; want %v", wire, err, tc.want)
				}
			} else if err != nil || !bytes.Equal(wire, v.AppendCBOR(nil)) {
				t.Fatalf("Encode = %x, %v", wire, err)
			}
			got, err := codec.Decode[format.InstructionBlock](v.AppendCBOR(nil), l)
			if tc.want != nil {
				if !errors.Is(err, tc.want) || !reflect.DeepEqual(got, format.InstructionBlock{}) {
					t.Fatalf("malformed Decode = %#v, %v; want %v", got, err, tc.want)
				}
			} else if err != nil || !got.Equal(v) || got.Key != tc.key {
				t.Fatalf("Decode changed exact key: %#v, %v", got, err)
			}
		})
	}
	if (format.InstructionBlock{}).Validate() == nil {
		t.Fatal("zero block validated")
	}
	// A formerly valid block without k is not a second supported wire schema.
	missingKey := []byte("\xa2\x61c\x80\x61s\xa1\x61d\x61d")
	if got, err := codec.Decode[format.InstructionBlock](missingKey, l); !errors.Is(err, codec.ErrInvalidData) || !reflect.DeepEqual(got, format.InstructionBlock{}) {
		t.Fatalf("missing key: %#v, %v", got, err)
	}
}

func TestInstructionBlockCollectionKeys(t *testing.T) {
	source := format.ContentSource{Description: "prepared"}
	for _, tc := range []struct {
		name   string
		blocks []format.InstructionBlock
		valid  bool
	}{
		{"nil", nil, true},
		{"empty", []format.InstructionBlock{}, true},
		{"same contents distinct keys", []format.InstructionBlock{
			{Key: "harness/core", Parts: []format.ContentPart{{Text: new("same")}}, Source: source},
			{Key: "operator/persona", Parts: []format.ContentPart{{Text: new("same")}}, Source: source},
		}, true},
		{"exact case and canonical equivalence", []format.InstructionBlock{
			{Key: "A", Source: source}, {Key: "a", Source: source},
			{Key: "é", Source: source}, {Key: "e\u0301", Source: source},
		}, true},
		{"duplicate different contents", []format.InstructionBlock{
			{Key: "harness/core", Parts: []format.ContentPart{{Text: new("first")}}, Source: source},
			{Key: "harness/core", Parts: []format.ContentPart{{Text: new("second")}}, Source: source},
		}, false},
		{"duplicate identical contents", []format.InstructionBlock{
			{Key: "harness/core", Parts: []format.ContentPart{{Text: new("same")}}, Source: source},
			{Key: "harness/core", Parts: []format.ContentPart{{Text: new("same")}}, Source: source},
		}, false},
		{"duplicate empty contributions", []format.InstructionBlock{
			{Key: "harness/core", Source: source}, {Key: "harness/core", Source: source},
		}, false},
		{"duplicate empty and nonempty", []format.InstructionBlock{
			{Key: "harness/core", Source: source},
			{Key: "harness/core", Parts: []format.ContentPart{{Text: new("text")}}, Source: source},
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := format.RootInstructions{Blocks: tc.blocks}
			user := format.UserInstructions{Blocks: tc.blocks}
			if (root.Validate() == nil) != tc.valid || (user.Validate() == nil) != tc.valid {
				t.Fatal("direct validation disagrees")
			}
			rootWire, rootErr := codec.Encode(root, limits())
			userWire, userErr := codec.Encode(user, limits())
			rootGot, rootDecodeErr := codec.Decode[format.RootInstructions](root.AppendCBOR(nil), limits())
			userGot, userDecodeErr := codec.Decode[format.UserInstructions](user.AppendCBOR(nil), limits())
			if tc.valid {
				if rootErr != nil || userErr != nil || rootDecodeErr != nil || userDecodeErr != nil || !rootGot.Equal(root) || !userGot.Equal(user) {
					t.Fatalf("roundtrip: root %v/%v, user %v/%v", rootErr, rootDecodeErr, userErr, userDecodeErr)
				}
			} else if !errors.Is(rootErr, codec.ErrInvalidData) || !errors.Is(userErr, codec.ErrInvalidData) || !errors.Is(rootDecodeErr, codec.ErrInvalidData) || !errors.Is(userDecodeErr, codec.ErrInvalidData) || rootWire != nil || userWire != nil || !reflect.DeepEqual(rootGot, format.RootInstructions{}) || !reflect.DeepEqual(userGot, format.UserInstructions{}) {
				t.Fatalf("duplicate rejection: root %v/%v, user %v/%v", rootErr, rootDecodeErr, userErr, userDecodeErr)
			}
		})
	}
}

func TestInstructionBlockRejectsUnicodeSpaceAndControl(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if !unicode.IsSpace(r) && !unicode.IsControl(r) {
			continue
		}
		block := format.InstructionBlock{Key: "key/" + string(r), Source: format.ContentSource{Description: "prepared"}}
		if block.Validate() == nil {
			t.Fatalf("accepted whitespace or control rune %U in key", r)
		}
	}
}

func TestInstructionBlockBoundsBeforeKeySetAllocation(t *testing.T) {
	l := limits()
	l.MaxArrayElements = 256
	l.MaxCollectionElements = 4096
	l.MaxNodes = 8192
	l.MaxBytes = 1 << 20
	blocks := make([]format.InstructionBlock, 256)
	for i := range blocks {
		blocks[i] = format.InstructionBlock{Key: "key/" + strconv.Itoa(i), Source: format.ContentSource{Description: "prepared"}}
	}
	// Only the first child is traversable under policy. An eagerly allocated
	// collection-wide key set would nevertheless allocate for all 256 siblings.
	blocks[0].Parts = []format.ContentPart{{Text: new(strings.Repeat("x", l.MaxStringBytes+1))}}
	for _, tc := range []struct {
		name  string
		value interface {
			codec.Marshaler
			Validate() error
		}
	}{
		{"root", format.RootInstructions{Blocks: blocks}},
		{"user", format.UserInstructions{Blocks: blocks}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, eager := range []bool{false, true} {
				result := testing.Benchmark(func(b *testing.B) {
					for b.Loop() {
						if eager {
							// Positive control: unbounded validation allocates the key set
							// before the codec checks the first child's string limit.
							if err := tc.value.Validate(); err != nil {
								b.Fatal(err)
							}
						}
						if err := codec.Check(tc.value, l); !errors.Is(err, codec.ErrLimitExceeded) {
							b.Fatal(err)
						}
					}
				})
				// Allow fixed budget/error-path storage, not a 256-entry key set.
				if bytes := result.AllocedBytesPerOp(); (bytes > 2048) != eager {
					t.Fatalf("eager=%t: bounded rejection allocated %d bytes/op", eager, bytes)
				}
			}
		})
	}
}

func TestInstructionBlockValidationAndBounds(t *testing.T) {
	v := format.InstructionBlock{Key: "harness/core", Parts: []format.ContentPart{{Text: new("long text")}}, Source: format.ContentSource{Description: "source"}}
	l := limits()
	l.MaxStringBytes = 4
	if err := v.Validate(); err != nil {
		t.Fatalf("direct Validate imposed operational policy: %v", err)
	}
	if err := codec.Check(v, l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("CheckCBOR ignored bounds: %v", err)
	}
	// Duplicate keys must not allocate their set until bounded children pass.
	root := format.RootInstructions{Blocks: []format.InstructionBlock{v, v}}
	user := format.UserInstructions(root)
	for _, value := range []codec.Marshaler{root, user} {
		if out, err := codec.Encode(value, l); out != nil || !errors.Is(err, codec.ErrLimitExceeded) {
			t.Fatalf("child bounds must precede duplicate validation: %x, %v", out, err)
		}
	}
	if _, err := codec.Decode[format.RootInstructions](root.AppendCBOR(nil), l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("root child read bounds must precede duplicate validation: %v", err)
	}
	if _, err := codec.Decode[format.UserInstructions](user.AppendCBOR(nil), l); !errors.Is(err, codec.ErrLimitExceeded) {
		t.Fatalf("user child read bounds must precede duplicate validation: %v", err)
	}
	v.Parts = []format.ContentPart{{}}
	if v.Validate() == nil {
		t.Fatal("direct validation omitted parts")
	}
	v.Parts = nil
	v.Source = format.ContentSource{}
	if v.Validate() == nil {
		t.Fatal("direct validation omitted provenance")
	}
}
