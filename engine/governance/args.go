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
// would read the variant.
var policyArgNames = []string{
	"command", "cmd", "temp_scope",
	"path", "file_path", "pattern", "url", "query",
	"source", "destination",
	"server", "tool",
}

// maxArgNameInError bounds how much of a model-supplied key an error quotes.
const maxArgNameInError = 64

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
// closed. Its errors quote key names only, never argument values.
func CheckToolArgs(args json.RawMessage) error {
	_, err := scanToolArgs(args)
	return err
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
	keys, err := scanToolArgs(args)
	if err != nil {
		return err
	}
	if err := checkFieldSpelling(keys, dst); err != nil {
		return err
	}
	return json.Unmarshal(args, dst)
}

// scanToolArgs validates args (see CheckToolArgs) and returns its top-level
// keys when it is an object.
func scanToolArgs(args json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, nil
	}
	keys, err := scanObjectKeys(trimmed)
	if err != nil {
		return nil, err
	}
	folded := make(map[string]string, len(keys))
	for _, key := range keys {
		fold := foldArgName(key)
		if prior, seen := folded[fold]; seen {
			return nil, fmt.Errorf("%w: %s and %s name the same argument", ErrAmbiguousToolArgs, quoteArgName(prior), quoteArgName(key))
		}
		folded[fold] = key
		for _, name := range policyArgNames {
			if key != name && strings.EqualFold(key, name) {
				return nil, fmt.Errorf("%w: %s must be spelled %q", ErrAmbiguousToolArgs, quoteArgName(key), name)
			}
		}
	}
	return keys, nil
}

// scanObjectKeys walks a JSON document that json.Valid has accepted, rejecting a
// repeated key in any object, and returns the keys of the top-level object. It
// decodes only keys, so large string values cost no allocation.
func scanObjectKeys(doc []byte) ([]string, error) {
	type frame struct {
		object    bool
		expectKey bool
		seen      map[string]struct{}
	}
	var (
		stack []frame
		top   []string
	)
	for i := 0; i < len(doc); i++ {
		switch c := doc[i]; c {
		case '{':
			stack = append(stack, frame{object: true, expectKey: true})
		case '[':
			stack = append(stack, frame{})
		case '}', ']':
			stack = stack[:len(stack)-1]
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
				if _, repeated := f.seen[key]; repeated {
					return nil, fmt.Errorf("%w: %s is given more than once", ErrAmbiguousToolArgs, quoteArgName(key))
				}
				if f.seen == nil {
					f.seen = make(map[string]struct{})
				}
				f.seen[key] = struct{}{}
				if len(stack) == 1 {
					top = append(top, key)
				}
			}
			i = end
		}
	}
	return top, nil
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
// escapes and replacing invalid UTF-8.
func decodeJSONKey(quoted []byte) (string, error) {
	inner := quoted[1 : len(quoted)-1]
	if bytes.IndexByte(inner, '\\') < 0 && utf8.Valid(inner) {
		return string(inner), nil
	}
	var key string
	if err := json.Unmarshal(quoted, &key); err != nil {
		return "", err
	}
	return key, nil
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

// checkFieldSpelling rejects a key that names one of dst's explicitly tagged
// struct fields only under case folding. Untagged fields keep encoding/json's
// case-insensitive matching, since their Go name is not a wire spelling.
func checkFieldSpelling(keys []string, dst any) error {
	fields := taggedFieldNames(reflect.TypeOf(dst))
	if fields == nil {
		return nil
	}
	for _, key := range keys {
		if _, exact := fields.exact[key]; exact {
			continue
		}
		if name, ok := fields.folded[foldArgName(key)]; ok {
			return fmt.Errorf("%w: %s must be spelled %q", ErrAmbiguousToolArgs, quoteArgName(key), name)
		}
	}
	return nil
}

// taggedFields indexes a struct's explicitly tagged JSON field names exactly and
// by their case-folding class.
type taggedFields struct {
	exact  map[string]struct{}
	folded map[string]string
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
	fields := &taggedFields{exact: map[string]struct{}{}, folded: map[string]string{}}
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
		fields.exact[name] = struct{}{}
		fields.folded[foldArgName(name)] = name
	}
}
