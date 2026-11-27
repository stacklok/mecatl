package mcpbroker

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

var errNotIJSON = errors.New("mcpbroker: arguments are not I-JSON")

// jsonWhitespace is JSON's insignificant whitespace (RFC 8259 section 2).
const jsonWhitespace = " \t\n\r"

// argumentsSHA256 returns the lowercase hex SHA-256 of the RFC 8785 form of
// params.arguments as sent; absent (empty, JSON whitespace only, or JSON
// null) is {}. It refuses
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
	// Only JSON's own whitespace (RFC 8259 section 2) is insignificant;
	// bytes.TrimSpace would also drop Unicode spaces such as U+00A0, which
	// the vMCP refuses rather than reading as absent.
	trimmed := bytes.Trim(args, jsonWhitespace)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		args = json.RawMessage(`{}`)
	}
	if err := checkIJSONObject(args); err != nil {
		return nil, "", err
	}
	v := jsontext.Value(bytes.Clone(args))
	if v.Canonicalize() != nil {
		return nil, "", errNotIJSON
	}
	return args, string(v), nil
}

// checkIJSONObject refuses raw unless it is exactly one JSON object that is
// I-JSON (RFC 7493). jsontext's strict decoding refuses invalid UTF-8, lone
// surrogate escapes, and duplicate member names; the number range, which
// Canonicalize does not check, is checked here.
func checkIJSONObject(raw []byte) error {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	for first := true; first || dec.StackDepth() > 0; first = false {
		tok, err := dec.ReadToken()
		if err != nil {
			return errNotIJSON
		}
		switch {
		case first && tok.Kind() != '{':
			return fmt.Errorf("%w: not a JSON object", errNotIJSON)
		case tok.Kind() == '0':
			if err := checkNumber(tok.String()); err != nil {
				return err
			}
		}
	}
	if _, err := dec.ReadToken(); err != io.EOF {
		return errNotIJSON
	}
	return nil
}

// maxExactInteger is 2^53, the largest magnitude below which every integer is
// an IEEE 754 double (RFC 7493 section 2.2).
const maxExactInteger = 1 << 53

// checkNumber refuses a JSON number literal that is not finite as an IEEE 754
// double or is integer-valued with magnitude above 2^53; every double of
// magnitude 2^53 or more is integer-valued.
func checkNumber(lit string) error {
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return fmt.Errorf("%w: number out of range", errNotIJSON)
	}
	if a := math.Abs(f); a > maxExactInteger || (a == maxExactInteger && !isExactly2To53(lit)) {
		return fmt.Errorf("%w: integer beyond 2^53", errNotIJSON)
	}
	return nil
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
