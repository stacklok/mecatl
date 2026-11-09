package value

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// ParseJSONObject converts one complete JSON object to mutable authoring storage,
// suitable for direct DTO assignment. It accepts only integer-form numbers in
// [-2^63, 2^64-1], unique decoded object keys, and valid Unicode string syntax.
// Decimal/exponent numbers, nonobject roots and trailing data are rejected.
// Failure returns a nil Map; success owns all storage independently of data.
// Keep data stable throughout the operation. Use Map.Admit for immutable access.
//
// Limits are validated and the complete input byte cap checked before parsing.
// Decoder token/string temporaries are bounded by that input; retained values
// additionally obey incremental codec accounting and the fixed local depth 128.
// The conservative nine-byte-per-node size estimate is not heap accounting:
// Go representations have additional constant-factor overhead.
func ParseJSONObject(data []byte, limits codec.Limits) (Map, error) {
	if _, err := codec.NewBudget(limits); err != nil {
		return nil, err
	}
	if len(data) > limits.MaxBytes {
		return nil, codec.ErrLimitExceeded
	}
	if err := checkJSONUnicode(data); err != nil {
		return nil, err
	}
	p := jsonParser{decoder: json.NewDecoder(bytes.NewReader(data)), limits: limits}
	p.decoder.UseNumber()
	if err := p.reserve(1); err != nil {
		return nil, err
	}
	v, err := p.read(0)
	if err != nil {
		return nil, err
	}
	if _, err := p.decoder.Token(); err != io.EOF {
		if err == nil {
			err = codec.ErrInvalidData
		}
		return nil, err
	}
	if err := codec.Check(v.object, limits); err != nil {
		return nil, err
	}
	return v.object, nil
}

// jsonParser reserves children incrementally because JSON has no declared counts.
// Each node/header is reserved before reading it; each collection item before
// retaining it. Keys count as nodes, pairs as one collection element, as in Budget.
// Failed accounting may consume counters; discard the parser after any error.
type jsonParser struct {
	decoder  *json.Decoder
	limits   codec.Limits
	nodes    int
	size     int
	elements int
}

func (p *jsonParser) reserve(n int) error {
	if n > p.limits.MaxNodes-p.nodes || n > (p.limits.MaxBytes-p.size)/9 {
		return codec.ErrLimitExceeded
	}
	p.nodes += n
	p.size += 9 * n
	return nil
}

func (p *jsonParser) text(s string) error {
	if len(s) > p.limits.MaxStringBytes || len(s) > p.limits.MaxBytes-p.size {
		return codec.ErrLimitExceeded
	}
	p.size += len(s)
	return nil
}

// item checks child depth and reserves an array element or map pair before any
// child traversal or retained insertion. Subtraction/division avoid overflow.
func (p *jsonParser) item(depth, count, maximum, nodes int) error {
	if depth == maxDepth {
		return ErrDepthExceeded
	}
	if depth >= p.limits.MaxDepth || count >= maximum || p.elements >= p.limits.MaxCollectionElements {
		return codec.ErrLimitExceeded
	}
	if err := p.reserve(nodes); err != nil {
		return err
	}
	p.elements++
	return nil
}

func (p *jsonParser) read(depth int) (Value, error) {
	token, err := p.decoder.Token()
	if err != nil {
		return Value{}, err
	}
	if depth == 0 && token != json.Delim('{') {
		return Value{}, codec.ErrInvalidData
	}
	switch x := token.(type) {
	case nil:
		return Null(), nil
	case bool:
		return Bool(x), nil
	case string:
		if err := p.text(x); err != nil {
			return Value{}, err
		}
		return String(x), nil
	case json.Number:
		s := x.String()
		if strings.ContainsAny(s, ".eE") {
			return Value{}, codec.ErrInvalidData
		}
		if strings.HasPrefix(s, "-") {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return Value{}, err
			}
			return Int64(n), nil
		}
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			return Value{}, err
		}
		return Uint64(n), nil
	case json.Delim:
		switch x {
		case '{':
			return p.readObject(depth)
		case '[':
			return p.readArray(depth)
		}
	}
	return Value{}, codec.ErrInvalidData
}

func (p *jsonParser) readObject(depth int) (Value, error) {
	out := make(Map)
	for p.decoder.More() {
		if err := p.item(depth, len(out), p.limits.MaxMapPairs, 2); err != nil {
			return Value{}, err
		}
		token, err := p.decoder.Token()
		if err != nil {
			return Value{}, err
		}
		key, ok := token.(string)
		if !ok {
			return Value{}, codec.ErrInvalidData
		}
		if err := p.text(key); err != nil {
			return Value{}, err
		}
		if _, exists := out[key]; exists {
			return Value{}, codec.ErrInvalidData
		}
		child, err := p.read(depth + 1)
		if err != nil {
			return Value{}, err
		}
		out[key] = child
	}
	if err := p.close('}'); err != nil {
		return Value{}, err
	}
	return Object(out), nil
}

func (p *jsonParser) readArray(depth int) (Value, error) {
	var out []Value
	for p.decoder.More() {
		if err := p.item(depth, len(out), p.limits.MaxArrayElements, 1); err != nil {
			return Value{}, err
		}
		child, err := p.read(depth + 1)
		if err != nil {
			return Value{}, err
		}
		out = append(out, child)
	}
	if err := p.close(']'); err != nil {
		return Value{}, err
	}
	return Array(out), nil
}

func (p *jsonParser) close(want json.Delim) error {
	token, err := p.decoder.Token()
	if err != nil {
		return err
	}
	if token != want {
		return codec.ErrInvalidData
	}
	return nil
}

// checkJSONUnicode examines original string syntax before encoding/json can
// replace invalid UTF-8 or unpaired UTF-16 surrogates. Other JSON syntax remains
// the decoder's responsibility. Escaped backslashes/quotes are skipped together.
func checkJSONUnicode(data []byte) error {
	if !utf8.Valid(data) {
		return codec.ErrInvalidData
	}
	inString := false
	for i := 0; i < len(data); i++ {
		switch data[i] {
		case '"':
			inString = !inString
		case '\\':
			if !inString {
				continue
			}
			i++
			if i >= len(data) {
				return codec.ErrInvalidData
			}
			if data[i] != 'u' {
				continue
			}
			n, err := jsonHex(data[i+1:])
			if err != nil {
				return err
			}
			i += 4
			if n >= 0xdc00 && n <= 0xdfff {
				return codec.ErrInvalidData
			}
			if n >= 0xd800 && n <= 0xdbff {
				if len(data)-i < 7 || data[i+1] != '\\' || data[i+2] != 'u' {
					return codec.ErrInvalidData
				}
				low, err := jsonHex(data[i+3:])
				if err != nil || low < 0xdc00 || low > 0xdfff {
					return codec.ErrInvalidData
				}
				i += 6
			}
		}
	}
	return nil
}

func jsonHex(data []byte) (uint64, error) {
	if len(data) < 4 {
		return 0, codec.ErrInvalidData
	}
	n, err := strconv.ParseUint(string(data[:4]), 16, 16)
	if err != nil {
		return 0, codec.ErrInvalidData
	}
	return n, nil
}
