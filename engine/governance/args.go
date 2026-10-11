package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ErrAmbiguousToolArgs reports a tool call whose argument object does not name
// each argument exactly once under one spelling. CheckToolArgs and
// DecodeToolArgs wrap it; callers test for it with errors.Is.
var ErrAmbiguousToolArgs = errors.New("ambiguous tool arguments")

// policyArgNames are the argument names the permission and authority checks
// read to decide a call: the Shell command (and its cmd alias) and temporary
// scope, the resource fields of the built-in file, network and search tools,
// the Copy/Move operands, and the CallMcpWithQuery target. A top-level key that
// matches one of them only under case folding is ambiguous for every tool,
// because the policy reads the exact name while a case-insensitive decoder
// would read the variant. They are bytes so a key compares without conversion.
var policyArgNames = argNames(
	"command", "cmd", "temp_scope",
	"path", "file_path", "pattern", "url", "query",
	"source", "destination",
	"server", "tool",
)

func argNames(names ...string) [][]byte {
	out := make([][]byte, len(names))
	for i, name := range names {
		out[i] = []byte(name)
	}
	return out
}

// maxArgNameInError bounds how much of a model-supplied key an error quotes.
const maxArgNameInError = 64

// The key scan holds up to argKeysOnStack keys and argFramesOnStack levels of
// nesting in stack arrays, so it checks an ordinary tool call's arguments
// without allocating. An object with more than maxPairwiseKeys keys is indexed
// in a map rather than compared pairwise, so a large object stays linear.
const (
	argKeysOnStack   = 16
	argFramesOnStack = 16
	maxPairwiseKeys  = 16
)

// CheckToolArgs reports whether a tool call's raw arguments are in the
// canonical form that permission evaluation, approval display and execution
// all read identically. It returns an error wrapping ErrAmbiguousToolArgs when:
//
//   - any object, at any depth, repeats a key (including a repeat spelled with
//     JSON escapes);
//   - two top-level keys are equal under Unicode case folding, the matching
//     encoding/json applies to struct fields; or
//   - a top-level key matches an argument name the policy reads (see
//     policyArgNames) only under case folding.
//
// Empty, non-object and syntactically invalid payloads pass: no reader can
// extract a value from them, and the decoders that consume them already fail
// closed. Its errors quote key names only, never argument values. It does not
// allocate for a valid object whose keys carry no JSON escapes.
func CheckToolArgs(args json.RawMessage) error {
	return checkToolArgs(args, nil)
}

// DecodeToolArgs is the single decode of a tool call's arguments shared by
// permission evaluation, approval display and tool execution. It applies
// CheckToolArgs, then rejects a top-level key that names an explicitly tagged
// field of the destination struct only under case folding, and finally decodes
// with encoding/json. Because ambiguous objects are refused before decoding,
// encoding/json's case-insensitive matching and last-duplicate-wins rule never
// select a value: every reader that accepts the payload sees the same fields.
//
// An empty payload leaves dst untouched and returns nil, so tools with
// all-optional arguments accept a call with no arguments.
func DecodeToolArgs(args json.RawMessage, dst any) error {
	if len(args) == 0 {
		return nil
	}
	if err := checkToolArgs(args, taggedFieldNames(reflect.TypeOf(dst))); err != nil {
		return err
	}
	return json.Unmarshal(args, dst)
}

// checkToolArgs applies CheckToolArgs and then, when fields is non-nil, rejects
// a top-level key that names one of fields only under case folding.
func checkToolArgs(args json.RawMessage, fields *taggedFields) error {
	var buf [argKeysOnStack][]byte
	keys, err := scanToolArgKeys(args, buf[:0])
	if err != nil {
		return err
	}
	return fields.checkSpelling(keys)
}

// scanToolArgKeys validates args (see CheckToolArgs) and appends the top-level
// keys of an object payload to keys.
func scanToolArgKeys(args json.RawMessage, keys [][]byte) ([][]byte, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return keys, nil
	}
	keys, err := scanObjectKeys(trimmed, keys)
	if err != nil {
		return nil, err
	}
	if err := checkTopLevelKeys(keys); err != nil {
		return nil, err
	}
	return keys, nil
}

// argFrame is an object or array the key scan is inside.
type argFrame struct {
	object    bool
	expectKey bool
	// start is where the object's keys begin in the scan's key list.
	start int
	// seen indexes the object's keys once it has more than maxPairwiseKeys.
	seen map[string]struct{}
}

// scanObjectKeys walks a JSON document that json.Valid has accepted, rejecting a
// repeated key in any object, and appends the keys of the top-level object to
// keys. A key is a subslice of doc unless it carries an escape or invalid UTF-8,
// and a nested object's keys are dropped when it closes, so values and plain
// keys cost no allocation.
func scanObjectKeys(doc []byte, keys [][]byte) ([][]byte, error) {
	var frames [argFramesOnStack]argFrame
	stack := frames[:0]
	for i := 0; i < len(doc); i++ {
		switch doc[i] {
		case '{':
			stack = append(stack, argFrame{object: true, expectKey: true, start: len(keys)})
		case '[':
			stack = append(stack, argFrame{})
		case '}', ']':
			closed := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if closed.object && len(stack) > 0 {
				keys = keys[:closed.start]
			}
		case ',':
			if f := &stack[len(stack)-1]; f.object {
				f.expectKey = true
			}
		case '"':
			end := endOfJSONString(doc, i)
			if f := &stack[len(stack)-1]; f.object && f.expectKey {
				f.expectKey = false
				key, err := decodeJSONKey(doc[i : end+1])
				if err != nil {
					return nil, err
				}
				if f.repeats(keys[f.start:], key) {
					return nil, fmt.Errorf("%w: %s is given more than once", ErrAmbiguousToolArgs, quoteArgName(string(key)))
				}
				keys = append(keys, key)
			}
			i = end
		}
	}
	return keys, nil
}

// repeats reports whether key repeats one of own, the keys the object has given
// so far. It compares pairwise until the object has more than maxPairwiseKeys
// keys, then indexes them in f.seen.
func (f *argFrame) repeats(own [][]byte, key []byte) bool {
	if f.seen == nil && len(own) < maxPairwiseKeys {
		for _, prior := range own {
			if bytes.Equal(prior, key) {
				return true
			}
		}
		return false
	}
	if f.seen == nil {
		f.seen = make(map[string]struct{}, 2*len(own))
		for _, prior := range own {
			f.seen[string(prior)] = struct{}{}
		}
	}
	if _, repeated := f.seen[string(key)]; repeated {
		return true
	}
	f.seen[string(key)] = struct{}{}
	return false
}

// endOfJSONString returns the index of the closing quote of the JSON string
// whose opening quote is at doc[start].
func endOfJSONString(doc []byte, start int) int {
	for i := start + 1; i < len(doc); i++ {
		switch doc[i] {
		case '\\':
			i++
		case '"':
			return i
		}
	}
	return len(doc) - 1
}

// decodeJSONKey returns a quoted JSON key as encoding/json decodes it, resolving
// escapes and replacing invalid UTF-8. A key with neither is returned as a
// subslice of quoted.
func decodeJSONKey(quoted []byte) ([]byte, error) {
	inner := quoted[1 : len(quoted)-1]
	if bytes.IndexByte(inner, '\\') < 0 && utf8.Valid(inner) {
		return inner, nil
	}
	var key string
	if err := json.Unmarshal(quoted, &key); err != nil {
		return nil, err
	}
	return []byte(key), nil
}

// checkTopLevelKeys rejects two top-level keys that are equal under case
// folding, and a key that matches a policy argument name only under case
// folding.
func checkTopLevelKeys(keys [][]byte) error {
	var folded map[string][]byte
	if len(keys) > maxPairwiseKeys {
		folded = make(map[string][]byte, len(keys))
	}
	for i, key := range keys {
		if prior, ok := foldedEarlierKey(keys[:i], key, folded); ok {
			return fmt.Errorf("%w: %s and %s name the same argument", ErrAmbiguousToolArgs, quoteArgName(string(prior)), quoteArgName(string(key)))
		}
		for _, name := range policyArgNames {
			if !bytes.Equal(key, name) && bytes.EqualFold(key, name) {
				return fmt.Errorf("%w: %s must be spelled %q", ErrAmbiguousToolArgs, quoteArgName(string(key)), name)
			}
		}
	}
	return nil
}

// foldedEarlierKey returns the key in earlier that equals key under case
// folding. With folded nil it compares pairwise with bytes.EqualFold, the
// simple folding foldArgName represents; otherwise it looks key up in folded,
// the index of earlier, and records it there.
func foldedEarlierKey(earlier [][]byte, key []byte, folded map[string][]byte) ([]byte, bool) {
	if folded == nil {
		for _, prior := range earlier {
			if bytes.EqualFold(prior, key) {
				return prior, true
			}
		}
		return nil, false
	}
	fold := foldArgName(string(key))
	if prior, seen := folded[fold]; seen {
		return prior, true
	}
	folded[fold] = key
	return nil, false
}

// foldArgName maps name to a representative of its case-folding class: two
// names fold to the same string exactly when strings.EqualFold reports them
// equal, which is the key matching encoding/json applies to struct fields.
func foldArgName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		b.WriteRune(minFoldRune(r))
	}
	return b.String()
}

// minFoldRune returns the smallest rune in r's simple case-folding orbit. For an
// ASCII letter that is its upper case, so U+212A KELVIN SIGN and 'k' both map
// to 'K'.
func minFoldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			return r - ('a' - 'A')
		}
		return r
	}
	least := r
	for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
		if next < least {
			least = next
		}
	}
	return least
}

func quoteArgName(name string) string {
	if utf8.RuneCountInString(name) > maxArgNameInError {
		runes := []rune(name)
		name = string(runes[:maxArgNameInError]) + "…"
	}
	return fmt.Sprintf("%q", name)
}

// taggedFields holds a struct's explicitly tagged JSON field names.
type taggedFields struct {
	names [][]byte
}

// checkSpelling rejects a key that names one of the tagged fields only under
// case folding. Untagged fields keep encoding/json's case-insensitive matching,
// since their Go name is not a wire spelling. A nil f checks nothing.
func (f *taggedFields) checkSpelling(keys [][]byte) error {
	if f == nil {
		return nil
	}
	for _, key := range keys {
		if f.has(key) {
			continue
		}
		for _, name := range f.names {
			if bytes.EqualFold(key, name) {
				return fmt.Errorf("%w: %s must be spelled %q", ErrAmbiguousToolArgs, quoteArgName(string(key)), name)
			}
		}
	}
	return nil
}

// has reports whether key is exactly one of the tagged names.
func (f *taggedFields) has(key []byte) bool {
	for _, name := range f.names {
		if bytes.Equal(key, name) {
			return true
		}
	}
	return false
}

var taggedFieldCache sync.Map // reflect.Type -> *taggedFields

// taggedFieldNames returns the explicitly tagged JSON field names of the struct
// t points to, including those promoted from untagged embedded structs, or nil
// when t is not a pointer to a struct.
func taggedFieldNames(t reflect.Type) *taggedFields {
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil
	}
	if cached, ok := taggedFieldCache.Load(t); ok {
		return cached.(*taggedFields)
	}
	fields := &taggedFields{}
	collectTaggedFields(t.Elem(), fields, 0)
	taggedFieldCache.Store(t, fields)
	return fields
}

// maxEmbeddingDepth bounds the walk through embedded structs.
const maxEmbeddingDepth = 8

func collectTaggedFields(t reflect.Type, fields *taggedFields, depth int) {
	if depth > maxEmbeddingDepth {
		return
	}
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if field.Anonymous && name == "" {
			embedded := field.Type
			if embedded.Kind() == reflect.Pointer {
				embedded = embedded.Elem()
			}
			if embedded.Kind() == reflect.Struct {
				collectTaggedFields(embedded, fields, depth+1)
			}
			continue
		}
		if !field.IsExported() || name == "" {
			continue
		}
		fields.names = append(fields.names, []byte(name))
	}
}
