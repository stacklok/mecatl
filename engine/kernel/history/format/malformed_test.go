package format_test

import (
	"bytes"
	"fmt"
	"math"
	"testing"

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

func TestDecodeCommitMalformed(t *testing.T) {
	root, successor := wireFixtures(t)
	prefix := "\xa4\x61h\x78\x19HST1111111111111111111111\x61p\xf6\x61r"
	record := "\xa3\x61k\x61x\x61p\x42\xff\x00\x61v\x01"
	tail := "\x61v\x01"
	wrapRecord := func(r string) []byte { return []byte(prefix + "\x81" + r + tail) }
	huge := "\xff\xff\xff\xff\xff\xff\xff\xff"
	cases := map[string][]byte{
		"empty":                     nil,
		"wrong envelope type":       []byte("\x84"),
		"empty map":                 []byte("\xa0"),
		"unknown envelope field":    append([]byte("\xa5"), root[1:]...),
		"indefinite map":            append([]byte("\xbf"), root[1:]...),
		"nonminimal map":            append([]byte("\xb8\x04"), root[1:]...),
		"unknown first key":         replaceWire(t, root, "\x61h", "\x61a"),
		"duplicate root key":        replaceWire(t, root, "\x61p\xf6", "\x61h\xf6"),
		"nonminimal key":            replaceWire(t, root, "\x61h", "\x78\x01h"),
		"bytes key":                 replaceWire(t, root, "\x61h", "\x41h"),
		"null key":                  replaceWire(t, root, "\x61h", "\xf6"),
		"null history":              replaceWire(t, root, "\x78\x19HST1111111111111111111111", "\xf6"),
		"empty history":             replaceWire(t, root, "\x78\x19HST1111111111111111111111", "\x60"),
		"invalid history":           replaceWire(t, root, "HST1111111111111111111111", "HST0000000000000000000000"),
		"nonhistory ID":             replaceWire(t, root, "HST1111111111111111111111", "SES1111111111111111111111"),
		"invalid history UTF8":      replaceWire(t, root, "HST1111111111111111111111", "\xffST1111111111111111111111"),
		"bytes history":             replaceWire(t, root, "\x78\x19HST", "\x58\x19HST"),
		"nonminimal history length": replaceWire(t, root, "\x78\x19HST", "\x79\x00\x19HST"),
		"huge history":              replaceWire(t, root, "\x78\x19HST1111111111111111111111", "\x7b"+huge),
		"root parent not null":      replaceWire(t, root, "\x61p\xf6", "\x61p\xf4"),
		"noncanonical null":         replaceWire(t, root, "\x61p\xf6", "\x61p\xf8\x16"),
		"root with link":            replaceWire(t, root, "\x61p\xf6", "\x61p"+string(successor[3:44])),
		"successor null parent":     append([]byte("\xa3\x61p\xf6"), successor[44:]...),
		"successor empty parent":    append([]byte("\xa3\x61p\x40"), successor[44:]...),
		"successor text parent":     append([]byte("\xa3\x61p\x60"), successor[44:]...),
		"missing root history":      append([]byte("\xa3"), root[30:]...),
		"null records":              []byte(prefix + "\xf6" + tail),
		"map records":               []byte(prefix + "\xa0" + tail),
		"empty records":             []byte(prefix + "\x80" + tail),
		"nonminimal array":          []byte(prefix + "\x98\x01" + record + tail),
		"indefinite array":          []byte(prefix + "\x9f" + record + "\xff" + tail),
		"huge array":                []byte(prefix + "\x9b" + huge + tail),
		"implausible array":         []byte(prefix + "\x98\xff" + tail),
		"null record":               wrapRecord("\xf6"),
		"array record":              wrapRecord("\x83\x61x\x40\x01"),
		"missing record field":      wrapRecord("\xa2\x61k\x61x\x61v\x01"),
		"unknown record field":      wrapRecord("\xa4\x61k\x61x\x61p\x40\x61v\x01\x61z\x00"),
		"unknown record key":        wrapRecord("\xa3\x61j\x61x\x61p\x40\x61v\x01"),
		"duplicate record key":      wrapRecord("\xa3\x61k\x61x\x61k\x40\x61v\x01"),
		"unsorted record keys":      wrapRecord("\xa3\x61p\x40\x61k\x61x\x61v\x01"),
		"nonminimal record map":     wrapRecord("\xb8\x03" + record[1:]),
		"null kind":                 wrapRecord("\xa3\x61k\xf6\x61p\x40\x61v\x01"),
		"bytes kind":                wrapRecord("\xa3\x61k\x41x\x61p\x40\x61v\x01"),
		"empty kind":                wrapRecord("\xa3\x61k\x60\x61p\x40\x61v\x01"),
		"invalid kind UTF8":         wrapRecord("\xa3\x61k\x61\xff\x61p\x40\x61v\x01"),
		"huge kind":                 wrapRecord("\xa3\x61k\x7b" + huge),
		"indefinite kind":           wrapRecord("\xa3\x61k\x7f\x61x\xff\x61p\x40\x61v\x01"),
		"null payload":              wrapRecord("\xa3\x61k\x61x\x61p\xf6\x61v\x01"),
		"text payload":              wrapRecord("\xa3\x61k\x61x\x61p\x62hi\x61v\x01"),
		"array payload":             wrapRecord("\xa3\x61k\x61x\x61p\x80\x61v\x01"),
		"missing payload":           wrapRecord("\xa3\x61k\x61x\x61v\x01"),
		"huge payload":              wrapRecord("\xa3\x61k\x61x\x61p\x5b" + huge),
		"truncated payload":         wrapRecord("\xa3\x61k\x61x\x61p\x58\xff"),
		"nonminimal payload length": wrapRecord("\xa3\x61k\x61x\x61p\x58\x00\x61v\x01"),
		"indefinite payload":        wrapRecord("\xa3\x61k\x61x\x61p\x5f\x40\xff\x61v\x01"),
		"missing envelope version":  root[:len(root)-3],
		"unsorted root keys":        []byte("\xa4\x61p\xf6\x61h\x78\x19HST1111111111111111111111\x61r\x81" + record + tail),
		"trailing null":             append(bytes.Clone(root), 0xf6),
		"trailing second commit":    append(bytes.Clone(root), root...),
	}
	for name, version := range map[string]string{
		"zero": "\x00", "negative": "\x20", "null": "\xf6", "text": "\x611", "bool": "\xf5", "bytes": "\x410", "float": "\xfb\x3f\xf0\x00\x00\x00\x00\x00\x00",
		"overflow": "\x1b\x80\x00\x00\x00\x00\x00\x00\x00", "maxuint": "\x1b" + huge,
		"nonminimal uint8": "\x18\x01", "nonminimal uint16": "\x19\x00\x01", "nonminimal uint32": "\x1a\x00\x00\x00\x01", "nonminimal uint64": "\x1b\x00\x00\x00\x00\x00\x00\x00\x01",
	} {
		cases["record version "+name] = wrapRecord(record[:len(record)-1] + version)
		cases["envelope version "+name] = append(bytes.Clone(root[:len(root)-1]), []byte(version)...)
	}
	for _, version := range []string{"\x02", "\x1b\x7f\xff\xff\xff\xff\xff\xff\xff"} {
		cases[fmt.Sprintf("unsupported envelope version %x", version)] = append(bytes.Clone(root[:len(root)-1]), []byte(version)...)
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) { assertDecodeError(t, data, testLimits()) })
	}
	// Huge claims must also fail under maximal budgets before allocation or
	// uint64-to-int conversion, not just because the usual test limits are small.
	unrestrictedLimits := format.Limits{MaxCommitBytes: math.MaxInt, MaxRecords: math.MaxInt, MaxKindBytes: math.MaxInt, MaxPayloadBytes: math.MaxInt}
	for _, name := range []string{"huge history", "huge kind", "huge payload", "huge array", "implausible array"} {
		t.Run(name+"/max-limits", func(t *testing.T) { assertDecodeError(t, cases[name], unrestrictedLimits) })
	}
	for _, wire := range [][]byte{root, successor} {
		for i := range len(wire) {
			assertDecodeError(t, wire[:i], testLimits())
		}
	}
}

func TestDecodeCommitRejectsCIDAlternatives(t *testing.T) {
	_, successor := wireFixtures(t)
	binary := bytes.Clone(successor[8:44])
	for name, link := range map[string][]byte{
		"wrong tag":              append([]byte{0xd8, 0x2b}, successor[5:44]...),
		"nonminimal tag":         append([]byte{0xd9, 0, 0x2a}, successor[5:44]...),
		"nonminimal link length": append([]byte{0xd8, 0x2a, 0x59, 0, 37}, successor[7:44]...),
		"no tag":                 bytes.Clone(successor[5:44]),
		"missing multibase":      append([]byte{0xd8, 0x2a, 0x58, 36}, binary...),
		"wrong multibase":        append([]byte{0xd8, 0x2a, 0x58, 37, 1}, binary...),
	} {
		t.Run(name, func(t *testing.T) {
			wire := append([]byte("\xa3\x61p"), link...)
			wire = append(wire, successor[44:]...)
			assertDecodeError(t, wire, testLimits())
		})
	}
	alternatives := map[string][]byte{
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
			wire := append([]byte{0xa3, 0x61, 'p', 0xd8, 0x2a, 0x58, byte(len(bad) + 1), 0}, bad...)
			wire = append(wire, successor[44:]...)
			assertDecodeError(t, wire, testLimits())
		})
	}
}
