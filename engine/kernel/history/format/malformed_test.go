package format_test

import (
	"bytes"
	"fmt"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
)

// replaceWire replaces one unique literal fragment, keeping malformed fixtures
// independent of the implementation's CBOR encoder and schema helpers.
func replaceWire(t testing.TB, wire []byte, old, replacement string) []byte {
	t.Helper()
	if bytes.Count(wire, []byte(old)) != 1 {
		t.Fatalf("fixture replacement is not unique: %x", old)
	}
	return bytes.Replace(wire, []byte(old), []byte(replacement), 1)
}

func TestCommitDecodeMalformed(t *testing.T) {
	root, successor := wireFixtures(t)
	const (
		history    = "\x78\x19HST1111111111111111111111"
		record     = "\xa4\x61k\x61x\x61p\x42\xff\x00\x61t\x20\x61v\x01"
		rootPrefix = "\x61h" + history + "\x61i\x01"
		tail       = "\x61s\x01\x61t\x21\x61v\x01"
		parent     = "\x61p\xd8\x2a\x58\x25\x00\x01\x71\x12\x20\xe3\xb0\xc4\x42\x98\xfc\x1c\x14\x9a\xfb\xf4\xc8\x99\x6f\xb9\x24\x27\xae\x41\xe4\x64\x9b\x93\x4c\xa4\x95\x99\x1b\x78\x52\xb8\x55"
	)
	wrapRecord := func(r string) []byte { return []byte("\xa6" + rootPrefix + "\x61r\x81" + r + tail) }
	rootWithField := func(field string) []byte {
		return []byte("\xa7" + rootPrefix + field + string(root[len("\xa6"+rootPrefix):]))
	}
	huge := "\xff\xff\xff\xff\xff\xff\xff\xff"
	cases := map[string][]byte{
		"empty":                         nil,
		"wrong envelope type":           []byte("\x84"),
		"empty map":                     []byte("\xa0"),
		"unknown envelope field":        append([]byte("\xa7"), append(root[1:], []byte("\x61z\x00")...)...),
		"indefinite map":                append([]byte("\xbf"), root[1:]...),
		"nonminimal map":                append([]byte("\xb8\x06"), root[1:]...),
		"unknown first key":             replaceWire(t, root, "\x61h", "\x61a"),
		"duplicate root key":            replaceWire(t, root, "\x61r", "\x61h"),
		"nonminimal key":                replaceWire(t, root, "\x61h", "\x78\x01h"),
		"bytes key":                     replaceWire(t, root, "\x61h", "\x41h"),
		"null key":                      replaceWire(t, root, "\x61h", "\xf6"),
		"null history":                  replaceWire(t, root, history, "\xf6"),
		"empty history":                 replaceWire(t, root, history, "\x60"),
		"invalid history":               replaceWire(t, root, "HST1111111111111111111111", "HST0000000000000000000000"),
		"nonhistory ID":                 replaceWire(t, root, "HST1111111111111111111111", "SES1111111111111111111111"),
		"invalid history UTF8":          replaceWire(t, root, "HST1111111111111111111111", "\xffST1111111111111111111111"),
		"bytes history":                 replaceWire(t, root, "\x78\x19HST", "\x58\x19HST"),
		"nonminimal history length":     replaceWire(t, root, "\x78\x19HST", "\x79\x00\x19HST"),
		"huge history":                  replaceWire(t, root, history, "\x7b"+huge),
		"root parent bool":              rootWithField("\x61p\xf4"),
		"root explicit null parent":     rootWithField("\x61p\xf6"),
		"noncanonical null":             rootWithField("\x61p\xf8\x16"),
		"both identities":               rootWithField(parent),
		"successor null parent":         replaceWire(t, successor, parent, "\x61p\xf6"),
		"successor empty parent":        replaceWire(t, successor, parent, "\x61p\x40"),
		"successor text parent":         replaceWire(t, successor, parent, "\x61p\x60"),
		"successor extra field count":   append(append([]byte("\xa7"), successor[1:]...), "\x61z\x00"...),
		"successor duplicate key":       replaceWire(t, successor, "\x61s\x02", "\x61p\x02"),
		"successor unsorted keys":       replaceWire(t, successor, "\x61s\x02\x61t\x21", "\x61t\x21\x61s\x02"),
		"neither identity":              []byte("\xa5\x61i\x01\x61r\x81" + record + tail),
		"null records":                  []byte("\xa6" + rootPrefix + "\x61r\xf6" + tail),
		"map records":                   []byte("\xa6" + rootPrefix + "\x61r\xa0" + tail),
		"empty records":                 []byte("\xa6" + rootPrefix + "\x61r\x80" + tail),
		"nonminimal array":              []byte("\xa6" + rootPrefix + "\x61r\x98\x01" + record + tail),
		"indefinite array":              []byte("\xa6" + rootPrefix + "\x61r\x9f" + record + "\xff" + tail),
		"huge array":                    []byte("\xa6" + rootPrefix + "\x61r\x9b" + huge + tail),
		"implausible array":             []byte("\xa6" + rootPrefix + "\x61r\x98\xff" + tail),
		"null record":                   wrapRecord("\xf6"),
		"array record":                  wrapRecord("\x84\x61x\x40\x20\x01"),
		"missing record field":          wrapRecord("\xa3\x61k\x61x\x61t\x20\x61v\x01"),
		"unknown record field":          wrapRecord("\xa5\x61k\x61x\x61p\x40\x61t\x20\x61v\x01\x61z\x00"),
		"unknown record key":            wrapRecord("\xa4\x61j\x61x\x61p\x40\x61t\x20\x61v\x01"),
		"duplicate record key":          wrapRecord("\xa4\x61k\x61x\x61k\x40\x61t\x20\x61v\x01"),
		"unsorted record keys":          wrapRecord("\xa4\x61p\x40\x61k\x61x\x61t\x20\x61v\x01"),
		"nonminimal record map":         wrapRecord("\xb8\x04" + record[1:]),
		"null kind":                     wrapRecord("\xa4\x61k\xf6\x61p\x40\x61t\x20\x61v\x01"),
		"bytes kind":                    wrapRecord("\xa4\x61k\x41x\x61p\x40\x61t\x20\x61v\x01"),
		"empty kind":                    wrapRecord("\xa4\x61k\x60\x61p\x40\x61t\x20\x61v\x01"),
		"invalid kind UTF8":             wrapRecord("\xa4\x61k\x61\xff\x61p\x40\x61t\x20\x61v\x01"),
		"huge kind":                     wrapRecord("\xa4\x61k\x7b" + huge),
		"indefinite kind":               wrapRecord("\xa4\x61k\x7f\x61x\xff\x61p\x40\x61t\x20\x61v\x01"),
		"null payload":                  wrapRecord("\xa4\x61k\x61x\x61p\xf6\x61t\x20\x61v\x01"),
		"text payload":                  wrapRecord("\xa4\x61k\x61x\x61p\x62hi\x61t\x20\x61v\x01"),
		"array payload":                 wrapRecord("\xa4\x61k\x61x\x61p\x80\x61t\x20\x61v\x01"),
		"missing payload":               wrapRecord("\xa3\x61k\x61x\x61t\x20\x61v\x01"),
		"huge payload":                  wrapRecord("\xa4\x61k\x61x\x61p\x5b" + huge),
		"truncated payload":             wrapRecord("\xa4\x61k\x61x\x61p\x58\xff"),
		"nonminimal payload length":     wrapRecord("\xa4\x61k\x61x\x61p\x58\x00\x61t\x20\x61v\x01"),
		"indefinite payload":            wrapRecord("\xa4\x61k\x61x\x61p\x5f\x40\xff\x61t\x20\x61v\x01"),
		"missing envelope version":      []byte("\xa5" + rootPrefix + "\x61r\x81" + record + "\x61s\x01\x61t\x21"),
		"missing sequence":              []byte("\xa5" + rootPrefix + "\x61r\x81" + record + "\x61t\x21\x61v\x01"),
		"duplicate sequence":            replaceWire(t, root, "\x61s\x01\x61t\x21", "\x61s\x01\x61s\x01"),
		"misplaced sequence":            replaceWire(t, root, "\x61r", "\x61s"),
		"root sequence not one":         replaceWire(t, root, "\x61s\x01", "\x61s\x02"),
		"successor sequence one":        replaceWire(t, successor, "\x61s\x02", "\x61s\x01"),
		"successor sequence zero":       replaceWire(t, successor, "\x61s\x02", "\x61s\x00"),
		"missing sequence three fields": []byte("\xa5" + rootPrefix + "\x61r\x81" + record + "\x61t\x21\x61v\x01"),
		"unsorted root keys":            []byte("\xa6\x61r\x81" + record + rootPrefix + tail),
		"trailing null":                 append(bytes.Clone(root), 0xf6),
		"trailing second commit":        append(bytes.Clone(root), root...),
		"old root without metadata":     []byte("\xa4\x61h" + history + "\x61r\x81\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x01\x61s\x01\x61v\x01"),
		"missing commit timestamp":      []byte("\xa5" + rootPrefix + "\x61r\x81" + record + "\x61s\x01\x61v\x01"),
		"missing instance ID":           []byte("\xa5\x61h" + history + "\x61r\x81" + record + tail),
		"zero instance ID":              replaceWire(t, root, "\x61i\x01", "\x61i\x00"),
		"missing record timestamp":      wrapRecord("\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x01"),
		"record timestamp text":         replaceWire(t, root, "\x61t\x20", "\x61t\x61x"),
		"commit timestamp text":         replaceWire(t, root, "\x61t\x21", "\x61t\x61x"),
		"instance ID negative":          replaceWire(t, root, "\x61i\x01", "\x61i\x20"),
		"instance ID nonminimal":        replaceWire(t, root, "\x61i\x01", "\x61i\x18\x01"),
	}
	for name, version := range map[string]string{
		"zero": "\x00", "negative": "\x20", "null": "\xf6", "text": "\x611", "bool": "\xf5", "bytes": "\x410", "float": "\xfb\x3f\xf0\x00\x00\x00\x00\x00\x00",
		"overflow": "\x1b\x80\x00\x00\x00\x00\x00\x00\x00", "maxuint": "\x1b" + huge,
		"array": "\x80", "map": "\xa0", "tag": "\xc0\x01", "indefinite": "\x7f\xff",
		"nonminimal uint8": "\x18\x01", "nonminimal uint16": "\x19\x00\x01", "nonminimal uint32": "\x1a\x00\x00\x00\x01", "nonminimal uint64": "\x1b\x00\x00\x00\x00\x00\x00\x00\x01",
	} {
		cases["record version "+name] = wrapRecord(record[:len(record)-1] + version)
		cases["envelope version "+name] = append(bytes.Clone(root[:len(root)-1]), []byte(version)...)
		cases["sequence "+name] = replaceWire(t, root, "\x61s\x01", "\x61s"+version)
	}
	for _, sequence := range []string{"\x18\x02", "\x19\x00\x02", "\x1a\x00\x00\x00\x02", "\x1b\x00\x00\x00\x00\x00\x00\x00\x02"} {
		cases[fmt.Sprintf("successor nonminimal sequence %x", sequence)] = replaceWire(t, successor, "\x61s\x02", "\x61s"+sequence)
	}
	for _, version := range []string{"\x02", "\x1b\x7f\xff\xff\xff\xff\xff\xff\xff"} {
		cases[fmt.Sprintf("unsupported envelope version %x", version)] = append(bytes.Clone(root[:len(root)-1]), []byte(version)...)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) { assertDecodeError(t, data, testLimits()) })
	}
	unrestrictedLimits := codec.Limits{MaxBytes: math.MaxInt, MaxArrayElements: math.MaxInt, MaxStringBytes: math.MaxInt, MaxByteStringBytes: math.MaxInt,
		MaxMapPairs: math.MaxInt, MaxDepth: math.MaxInt, MaxNodes: math.MaxInt, MaxCollectionElements: math.MaxInt}
	for _, name := range []string{"huge history", "huge kind", "huge payload", "huge array", "implausible array"} {
		t.Run(name+"/max-limits", func(t *testing.T) { assertDecodeError(t, cases[name], unrestrictedLimits) })
	}
	for _, wire := range [][]byte{root, successor} {
		for i := range len(wire) {
			assertDecodeError(t, wire[:i], testLimits())
		}
	}
}

func TestCommitDecodeTimestampBounds(t *testing.T) {
	for _, timestamp := range []string{
		"\x00", "\x20", "\x1b\x7f\xff\xff\xff\xff\xff\xff\xff", "\x3b\x7f\xff\xff\xff\xff\xff\xff\xff",
	} {
		root, _ := wireFixtures(t)
		data := replaceWire(t, root, "\x61t\x20", "\x61t"+timestamp)
		data = replaceWire(t, data, "\x61t\x21", "\x61t"+timestamp)
		if _, err := codec.Decode[format.Commit](data, testLimits()); err != nil {
			t.Fatalf("timestamp %x: %v", timestamp, err)
		}
	}
}

func TestCommitDecodeRejectsCIDAlternatives(t *testing.T) {
	_, successor := wireFixtures(t)
	const parent = "\x61p\xd8\x2a\x58\x25\x00\x01\x71\x12\x20\xe3\xb0\xc4\x42\x98\xfc\x1c\x14\x9a\xfb\xf4\xc8\x99\x6f\xb9\x24\x27\xae\x41\xe4\x64\x9b\x93\x4c\xa4\x95\x99\x1b\x78\x52\xb8\x55"
	binary := []byte(parent[len("\x61p\xd8\x2a\x58\x25\x00"):])
	for name, link := range map[string][]byte{
		"wrong tag":              append([]byte{0xd8, 0x2b}, []byte(parent[4:])...),
		"nonminimal tag":         append([]byte{0xd9, 0, 0x2a}, []byte(parent[4:])...),
		"nonminimal link length": append([]byte{0xd8, 0x2a, 0x59, 0, 37}, []byte(parent[6:])...),
		"no tag":                 []byte(parent[4:]),
		"missing multibase":      append([]byte{0xd8, 0x2a, 0x58, 36}, binary...),
		"wrong multibase":        append([]byte{0xd8, 0x2a, 0x58, 37, 1}, binary...),
	} {
		t.Run(name, func(t *testing.T) {
			assertDecodeError(t, replaceWire(t, successor, parent, "\x61p"+string(link)), testLimits())
		})
	}
	alternatives := map[string][]byte{
		"unset parent":     make([]byte, 36),
		"CIDv0":            append([]byte{0x12, 32}, binary[4:]...),
		"truncated digest": binary[:35],
		"extended digest":  append(bytes.Clone(binary), 0),
	}
	for i, name := range []string{"version", "codec", "hash", "digest length"} {
		bad := bytes.Clone(binary)
		bad[i]++
		alternatives[name] = bad
		overlong := append(bytes.Clone(binary[:i]), binary[i]|0x80, 0)
		overlong = append(overlong, binary[i+1:]...)
		alternatives["nonminimal varint "+name] = overlong
	}
	raw := bytes.Clone(binary)
	raw[1] = 0x55
	alternatives["raw codec"] = raw
	for name, bad := range alternatives {
		t.Run(name, func(t *testing.T) {
			link := append([]byte{0xd8, 0x2a, 0x58, byte(len(bad) + 1), 0}, bad...)
			assertDecodeError(t, replaceWire(t, successor, parent, "\x61p"+string(link)), testLimits())
		})
	}
}
