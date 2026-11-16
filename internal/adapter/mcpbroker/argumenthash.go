package mcpbroker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var errNotIJSON = errors.New("mcpbroker: arguments are not I-JSON")

// argumentsSHA256 returns the lowercase hex SHA-256 of the RFC 8785 form of
// params.arguments as sent; absent (empty or JSON null) is {}. It refuses
// anything that is not a JSON object, invalid UTF-8, lone surrogate escapes,
// duplicate member names, non-finite numbers, and integer-valued numbers
// with magnitude > 2^53. sent is the value to send: {} when absent, otherwise
// args unchanged.
//
// This is the argument hash of docs/drafts/agent-identity/credentials-and-lifecycle.md,
// step (8) "The argument hash"; the vMCP computes the same hash over what it
// receives, so both sides must refuse rather than disagree.
func argumentsSHA256(args json.RawMessage) (sent json.RawMessage, sum string, err error) {
	sent, canonical, err := canonicalArguments(args)
	if err != nil {
		return nil, "", err
	}
	h := sha256.Sum256([]byte(canonical))
	return sent, hex.EncodeToString(h[:]), nil
}

// canonicalArguments returns the value to send and its RFC 8785 form, with the
// refusals of argumentsSHA256.
func canonicalArguments(args json.RawMessage) (sent json.RawMessage, canonical string, err error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		args = json.RawMessage(`{}`)
	}
	// encoding/json replaces invalid UTF-8 and lone surrogates with U+FFFD, so
	// both are checked on the raw text first. json.Valid also bounds nesting
	// depth before the recursive canonicalization below.
	if !utf8.Valid(args) || !json.Valid(args) {
		return nil, "", errNotIJSON
	}
	if err := checkSurrogateEscapes(args); err != nil {
		return nil, "", err
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, "", errNotIJSON
	}
	if tok != json.Delim('{') {
		return nil, "", fmt.Errorf("%w: not a JSON object", errNotIJSON)
	}
	var b strings.Builder
	if err := canonicalObject(dec, &b); err != nil {
		return nil, "", err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, "", errNotIJSON
	}
	return args, b.String(), nil
}

// checkSurrogateEscapes refuses \uD800-\uDFFF escapes that are not a high
// surrogate immediately followed by a low one. raw must be valid JSON.
func checkSurrogateEscapes(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if !inString {
			inString = c == '"'
			continue
		}
		switch c {
		case '"':
			inString = false
		case '\\':
			i++
			if raw[i] != 'u' {
				continue
			}
			r := hexRune(raw[i+1 : i+5])
			i += 4
			switch {
			case r >= 0xDC00 && r <= 0xDFFF:
				return fmt.Errorf("%w: lone surrogate escape", errNotIJSON)
			case r >= 0xD800 && r <= 0xDBFF:
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return fmt.Errorf("%w: lone surrogate escape", errNotIJSON)
				}
				if low := hexRune(raw[i+3 : i+7]); low < 0xDC00 || low > 0xDFFF {
					return fmt.Errorf("%w: lone surrogate escape", errNotIJSON)
				}
				i += 6
			}
		}
	}
	return nil
}

func hexRune(h []byte) rune {
	n, _ := strconv.ParseUint(string(h), 16, 16) // valid JSON guarantees 4 hex digits
	return rune(n)
}

type member struct {
	key   string
	utf16 []uint16
	value string
}

// canonicalObject writes the object whose '{' dec has just returned.
func canonicalObject(dec *json.Decoder, b *strings.Builder) error {
	var members []member
	seen := map[string]struct{}{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return errNotIJSON
		}
		key := tok.(string)
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: duplicate member name", errNotIJSON)
		}
		seen[key] = struct{}{}
		var v strings.Builder
		if err := canonicalValue(dec, &v); err != nil {
			return err
		}
		members = append(members, member{key: key, utf16: utf16.Encode([]rune(key)), value: v.String()})
	}
	if _, err := dec.Token(); err != nil { // '}'
		return errNotIJSON
	}
	slices.SortFunc(members, func(a, b member) int { return slices.Compare(a.utf16, b.utf16) })
	b.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			b.WriteByte(',')
		}
		writeString(b, m.key)
		b.WriteByte(':')
		b.WriteString(m.value)
	}
	b.WriteByte('}')
	return nil
}

func canonicalValue(dec *json.Decoder, b *strings.Builder) error {
	tok, err := dec.Token()
	if err != nil {
		return errNotIJSON
	}
	switch v := tok.(type) {
	case json.Delim:
		if v == '{' {
			return canonicalObject(dec, b)
		}
		b.WriteByte('[')
		for i := 0; dec.More(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := canonicalValue(dec, b); err != nil {
				return err
			}
		}
		if _, err := dec.Token(); err != nil { // ']'
			return errNotIJSON
		}
		b.WriteByte(']')
	case string:
		writeString(b, v)
	case json.Number:
		n, err := canonicalNumber(string(v))
		if err != nil {
			return err
		}
		b.WriteString(n)
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case nil:
		b.WriteString("null")
	}
	return nil
}

// maxExactInteger is 2^53, the largest magnitude below which every integer is
// an IEEE 754 double (RFC 7493 section 2.2).
const maxExactInteger = 1 << 53

// canonicalNumber parses a JSON number literal as an IEEE 754 double and
// formats it as ECMAScript Number.prototype.toString does (RFC 8785 section
// 3.2.2.3). It refuses non-finite values and integer-valued magnitudes above
// 2^53; every double of magnitude 2^53 or more is integer-valued.
func canonicalNumber(lit string) (string, error) {
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", fmt.Errorf("%w: number out of range", errNotIJSON)
	}
	if a := math.Abs(f); a > maxExactInteger || (a == maxExactInteger && !isExactly2To53(lit)) {
		return "", fmt.Errorf("%w: integer beyond 2^53", errNotIJSON)
	}
	return formatES6(f), nil
}

// isExactly2To53 reports whether the decimal literal lit, which rounds to
// ±2^53, denotes exactly ±2^53 rather than a neighbour such as 2^53+1.
func isExactly2To53(lit string) bool {
	lit = strings.TrimPrefix(lit, "-")
	mant, exp := lit, 0
	if i := strings.IndexAny(lit, "eE"); i >= 0 {
		e, err := strconv.Atoi(lit[i+1:])
		if err != nil {
			return false
		}
		mant, exp = lit[:i], e
	}
	intPart, frac, _ := strings.Cut(mant, ".")
	digits := intPart + frac
	point := len(intPart) + exp // digits[:point] is the integer part
	trimmed := strings.TrimLeft(digits, "0")
	point -= len(digits) - len(trimmed)
	return strings.TrimRight(trimmed, "0") == "9007199254740992" && point == 16
}

// formatES6 formats a finite double per ECMAScript Number::toString: the
// shortest round-trip digits, fixed notation for decimal exponents in
// [-6, 21), otherwise d.ddde±N. -0 formats as 0.
func formatES6(f float64) string {
	if f == 0 {
		return "0"
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// d.dddde±XX: digits are the shortest round-trip significand.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expText, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expText)
	k, n := len(digits), exp+1 // value = 0.digits × 10^n
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	expSign := "+"
	if exp < 0 {
		expSign, exp = "-", -exp
	}
	if k == 1 {
		return sign + digits + "e" + expSign + strconv.Itoa(exp)
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + expSign + strconv.Itoa(exp)
}

// writeString writes s as an RFC 8785 string: only '"', '\\' and the control
// characters are escaped, with the short forms where JSON has them and
// lowercase \u00xx otherwise.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				fmt.Fprintf(b, `\u%04x`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
}
