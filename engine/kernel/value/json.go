package value

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/stacklok/mecatl/engine/kernel/codec"
)

// ParseJSONObject is ParseJSON restricted to object roots. It returns a nil Map
// on any failure; success owns mutable storage independently of data.
func ParseJSONObject(data []byte, limits codec.Limits) (Map, error) {
	v, err := ParseJSON(data, limits)
	if err != nil {
		return nil, err
	}
	if v.kind != MapKind {
		return nil, codec.ErrInvalidData
	}
	return v.object, nil
}

// ParseJSON converts one complete JSON value to independently owned mutable
// authoring storage. It accepts every native root kind, integer-form numbers in
// [-2^63, 2^64-1] (normalizing -0 to zero), and unique decoded object keys.
// Like encoding/json, it replaces malformed UTF-8 and unpaired UTF-16 surrogates
// with U+FFFD; replacement collisions in keys are duplicates. Decimal/exponent
// numbers and trailing data are rejected. Failure returns a zero Value.
// Keep data stable throughout the operation; use Value.Admit for immutable access.
//
// Limits are validated and the complete input byte cap checked before parsing.
// Decoder token/string temporaries are bounded by that input; retained values
// additionally obey incremental codec accounting and the fixed local depth 128.
// The conservative nine-byte-per-node size estimate is not heap accounting:
// Go representations have additional constant-factor overhead.
func ParseJSON(data []byte, limits codec.Limits) (Value, error) {
	if _, err := codec.NewBudget(limits); err != nil {
		return Value{}, err
	}
	if len(data) > limits.MaxBytes {
		return Value{}, codec.ErrLimitExceeded
	}
	p := jsonParser{decoder: json.NewDecoder(bytes.NewReader(data)), limits: limits}
	p.decoder.UseNumber()
	if err := p.reserve(1); err != nil {
		return Value{}, err
	}
	v, err := p.read(0)
	if err != nil {
		return Value{}, err
	}
	if _, err := p.decoder.Token(); err != io.EOF {
		if err == nil {
			err = codec.ErrInvalidData
		}
		return Value{}, err
	}
	if err := codec.Check(v, limits); err != nil {
		return Value{}, err
	}
	return v, nil
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
	if depth == maxLocalDepth {
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

// marshalJSON traverses views so authoring and admitted values share encoding
// without coupling the encoder to mutable storage. It publishes output only after
// success. One local depth counter bounds all descendants (including cycles),
// without operational budgets, subtree validation passes or intermediate JSON.
func marshalJSON(v View) ([]byte, error) {
	out, err := appendJSON(nil, v, 0)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func appendJSON(dst []byte, v View, depth int) ([]byte, error) {
	if depth > maxLocalDepth {
		return nil, ErrDepthExceeded
	}
	switch v.Kind() {
	case NullKind:
		return append(dst, "null"...), nil
	case BoolKind:
		flag, _ := v.Bool()
		return strconv.AppendBool(dst, flag), nil
	case StringKind:
		text, _ := v.String()
		return appendJSONString(dst, text)
	case IntegerKind:
		if n, ok := v.Uint64(); ok {
			return strconv.AppendUint(dst, n, 10), nil
		}
		if n, ok := v.Int64(); ok {
			return strconv.AppendInt(dst, n, 10), nil
		}
		return nil, codec.ErrInvalidData
	case ArrayKind:
		array, _ := v.Array()
		if depth == maxLocalDepth && array.Len() != 0 {
			return nil, ErrDepthExceeded
		}
		dst = append(dst, '[')
		for i := range array.Len() {
			if i != 0 {
				dst = append(dst, ',')
			}
			var err error
			dst, err = appendJSON(dst, array.At(i), depth+1)
			if err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case MapKind:
		object, _ := v.Object()
		return appendJSONObject(dst, object, depth)
	default:
		return nil, codec.ErrInvalidData
	}
}

// appendJSONObject shares its caller's local depth; the map adds no wrapper node.
func appendJSONObject(dst []byte, m MapView, depth int) ([]byte, error) {
	if depth == maxLocalDepth && m.Len() != 0 {
		return nil, ErrDepthExceeded
	}
	keys := make([]string, 0, m.Len())
	for key := range m.All() {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	dst = append(dst, '{')
	for i, key := range keys {
		if i != 0 {
			dst = append(dst, ',')
		}
		var err error
		dst, err = appendJSONString(dst, key)
		if err != nil {
			return nil, err
		}
		dst = append(dst, ':')
		child, _ := m.Get(key)
		dst, err = appendJSON(dst, child, depth+1)
		if err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}

// appendJSONString uses the standard library's escaping, but rejects invalid
// native UTF-8 rather than silently replacing authoring data during encoding.
func appendJSONString(dst []byte, s string) ([]byte, error) {
	if !utf8.ValidString(s) {
		return nil, codec.ErrInvalidData
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	return append(dst, encoded...), nil
}
