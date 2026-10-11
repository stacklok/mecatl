package governance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode"
)

// scanToolArgs returns the top-level keys the production scan collects for args,
// as strings, after the checks CheckToolArgs applies. It lets the key-agreement
// assertion compare the scan with encoding/json.
func scanToolArgs(args json.RawMessage) ([]string, error) {
	keys, err := scanToolArgKeys(args, nil)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	out := make([]string, len(keys))
	for i, key := range keys {
		out[i] = string(key)
	}
	return out, nil
}

// The reference* functions are the map-based scan the allocation-free scan
// replaced. They are the oracle for the differential tests below: both must
// reach the same verdict with the same error for every payload.

func referenceCheckToolArgs(args json.RawMessage) error {
	_, err := referenceScanToolArgs(args)
	return err
}

func referenceDecodeToolArgs(args json.RawMessage, dst any) error {
	if len(args) == 0 {
		return nil
	}
	keys, err := referenceScanToolArgs(args)
	if err != nil {
		return err
	}
	if err := referenceCheckFieldSpelling(keys, dst); err != nil {
		return err
	}
	return json.Unmarshal(args, dst)
}

func referenceScanToolArgs(args json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, nil
	}
	keys, err := referenceScanObjectKeys(trimmed)
	if err != nil {
		return nil, err
	}
	folded := make(map[string]string, len(keys))
	for _, key := range keys {
		fold := referenceFold(key)
		if prior, seen := folded[fold]; seen {
			return nil, fmt.Errorf("%w: %s and %s name the same argument", ErrAmbiguousToolArgs, quoteArgName(prior), quoteArgName(key))
		}
		folded[fold] = key
		for _, policyName := range policyArgNames {
			name := string(policyName)
			if key != name && strings.EqualFold(key, name) {
				return nil, fmt.Errorf("%w: %s must be spelled %q", ErrAmbiguousToolArgs, quoteArgName(key), name)
			}
		}
	}
	return keys, nil
}

func referenceScanObjectKeys(doc []byte) ([]string, error) {
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
				var key string
				if err := json.Unmarshal(doc[i:end+1], &key); err != nil {
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

func referenceFold(name string) string {
	var b strings.Builder
	for _, r := range name {
		least := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			least = min(least, next)
		}
		b.WriteRune(least)
	}
	return b.String()
}

func referenceCheckFieldSpelling(keys []string, dst any) error {
	t := reflect.TypeOf(dst)
	if t == nil || t.Kind() != reflect.Pointer || t.Elem().Kind() != reflect.Struct {
		return nil
	}
	exact := map[string]struct{}{}
	folded := map[string]string{}
	var collect func(reflect.Type, int)
	collect = func(t reflect.Type, depth int) {
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
					collect(embedded, depth+1)
				}
				continue
			}
			if !field.IsExported() || name == "" {
				continue
			}
			exact[name] = struct{}{}
			folded[referenceFold(name)] = name
		}
	}
	collect(t.Elem(), 0)
	for _, key := range keys {
		if _, ok := exact[key]; ok {
			continue
		}
		if name, ok := folded[referenceFold(key)]; ok {
			return fmt.Errorf("%w: %s must be spelled %q", ErrAmbiguousToolArgs, quoteArgName(key), name)
		}
	}
	return nil
}

func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

// assertScanMatchesReference checks that the production scan and the reference
// scan agree on one payload: the CheckToolArgs verdict and error, the top-level
// keys, and DecodeToolArgs' error and decoded value for the argument structs
// the policy and the built-in tools decode into.
func assertScanMatchesReference(t *testing.T, args string) {
	t.Helper()
	raw := json.RawMessage(args)
	got, want := CheckToolArgs(raw), referenceCheckToolArgs(raw)
	if errText(got) != errText(want) || errors.Is(got, ErrAmbiguousToolArgs) != errors.Is(want, ErrAmbiguousToolArgs) {
		t.Errorf("CheckToolArgs(%q) = %v, reference %v", args, got, want)
	}
	keys, err := scanToolArgs(raw)
	wantKeys, wantErr := referenceScanToolArgs(raw)
	if !slices.Equal(keys, wantKeys) || errText(err) != errText(wantErr) {
		t.Errorf("scan keys of %q = %q (%v), reference %q (%v)", args, keys, err, wantKeys, wantErr)
	}
	for _, newDst := range []func() any{
		func() any { return new(shellExecArgs) },
		func() any { return new(pathExecArgs) },
		func() any { return new(copyExecArgs) },
		func() any { return new(shellCommandArgs) },
		func() any { return new(resourceArgs) },
		func() any { return new(map[string]json.RawMessage) },
	} {
		gotDst, wantDst := newDst(), newDst()
		got, want := DecodeToolArgs(raw, gotDst), referenceDecodeToolArgs(raw, wantDst)
		if errText(got) != errText(want) || !reflect.DeepEqual(gotDst, wantDst) {
			t.Errorf("DecodeToolArgs(%q, %T) = %v %+v, reference %v %+v", args, gotDst, got, gotDst, want, wantDst)
		}
	}
}

// argKeyPool mixes the policy names with case, Unicode-fold and escaped
// spellings of them, plain keys that fold onto each other, non-ASCII and
// invalid UTF-8 keys, and a key long enough to be truncated in an error.
var argKeyPool = []string{
	`"command"`, `"cmd"`, `"temp_scope"`, `"timeout_ms"`, `"background"`,
	`"path"`, `"file_path"`, `"pattern"`, `"url"`, `"query"`,
	`"source"`, `"destination"`, `"server"`, `"tool"`, `"content"`,
	`"Command"`, `"COMMAND"`, `"CMD"`, `"Temp_Scope"`, `"PATH"`, `"pAth"`, `"File_Path"`,
	`"URL"`, `"Query"`, `"Tool"`, `"Server"`, `"Destination"`, `"Timeout_MS"`,
	`"ſource"`, `"de\u017ftination"`, `"\u0063ommand"`, `"\u0043ommand"`, `"pat\u0068"`,
	`"key"`, `"Key"`, `"Key"`, `"\u212aey"`, `"x"`, `"X"`, `"a\"b"`, `"a\\\"b"`,
	`"ü"`, `"Ü"`, `"straße"`, `"STRASSE"`, `"bad` + "\xff" + `key"`, `"bad` + "\xfe" + `key"`,
	`"\ud800"`, `"\udc00"`, `"\ufffd"`, `"�"`, `""`,
	`"` + strings.Repeat("k", maxArgNameInError+8) + `"`,
}

// argPayload builds a random argument document from argKeyPool. Objects reach
// past the stack-held key and frame counts and past maxPairwiseKeys, so the
// heap and map fallbacks are compared too.
func argPayload(r *rand.Rand, depth int) string {
	var b strings.Builder
	writeArgValue(&b, r, depth, true)
	return b.String()
}

func writeArgValue(b *strings.Builder, r *rand.Rand, depth int, object bool) {
	switch {
	case object || (depth > 0 && r.IntN(4) == 0):
		b.WriteByte('{')
		n := r.IntN(6)
		if r.IntN(5) == 0 {
			n = maxPairwiseKeys + r.IntN(2*maxPairwiseKeys)
		}
		// A large object mostly carries generated keys: distinct ones, so the
		// map-indexed checks run to the end, or random ones, which repeat.
		distinct := r.IntN(2) == 0
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			switch {
			case n > 8 && r.IntN(24) == 0:
				fmt.Fprintf(b, `"K%d"`, r.IntN(n))
			case n > 8 && distinct && r.IntN(8) > 0:
				fmt.Fprintf(b, `"k%d"`, i)
			case n > 8 && !distinct && r.IntN(3) > 0:
				fmt.Fprintf(b, `"k%d"`, r.IntN(4*n))
			default:
				b.WriteString(argKeyPool[r.IntN(len(argKeyPool))])
			}
			b.WriteByte(':')
			writeArgValue(b, r, depth-1, false)
		}
		b.WriteByte('}')
	case depth > 0 && r.IntN(5) == 0:
		b.WriteByte('[')
		for i := range r.IntN(4) {
			if i > 0 {
				b.WriteByte(',')
			}
			writeArgValue(b, r, depth-1, false)
		}
		b.WriteByte(']')
	default:
		b.WriteString([]string{`"ls"`, `1`, `null`, `true`, `"{\"command\":1}"`, `"a\\\"b"`, `[]`, `{}`}[r.IntN(8)])
	}
}

// TestCheckToolArgsMatchesReference compares the production scan with the
// reference scan over the seeds, hand-picked edge cases and generated payloads.
func TestCheckToolArgsMatchesReference(t *testing.T) {
	t.Parallel()
	nested := func(depth int, inner string) string {
		return strings.Repeat(`{"a":[`, depth) + inner + strings.Repeat(`]}`, depth)
	}
	manyKeys := func(n int, extra string) string {
		var b strings.Builder
		b.WriteByte('{')
		for i := range n {
			fmt.Fprintf(&b, `"k%d":%d,`, i, i)
		}
		b.WriteString(extra)
		b.WriteByte('}')
		return b.String()
	}
	cases := slices.Clone(toolArgsSeeds)
	cases = append(cases,
		" \t\r\n{\"command\":\"ls\",\"Command\":\"pwd\"} \n",
		"\u00a0{\"command\":\"ls\",\"Command\":\"pwd\"}",
		"\u0085{\"Path\":\"a\"}\u0085",
		`{"command":"ls"}x`,
		`{"command":"ls","Command"`,
		`{"a":{"b":{"c":1,"c":2}},"a2":1}`,
		`{"a":[{"x":1},{"x":2}],"b":{"x":1},"x":1}`,
		`{"a":{"x":1,"y":{"x":1}},"x":{"x":{"x":1}}}`,
		`{"\u0063ommand":"ls","command":"pwd"}`,
		`{"command":"ls","c\u006fmmand":"pwd"}`,
		`{"a\"b":1,"a\u0022b":2}`,
		`{"Key":1,"key":2}`,
		`{"KEY":1,"Key":2}`,
		`{"\u212aey":1,"key":2}`,
		`{"KEY":1,"\u212aey":2}`,
		`{"s":1,"\u017f":2}`,
		`{"straße":1,"STRASSE":2}`,
		`{"ü":1,"Ü":2}`,
		"{\"bad\xffkey\":1,\"bad\xfekey\":2}",
		"{\"bad\xffkey\":1,\"bad\\ufffdkey\":2}",
		`{"\ud800":1,"\udc00":2}`,
		`{"":1,"":2}`,
		nested(argFramesOnStack+4, `{"x":1,"x":2}`),
		nested(argFramesOnStack+4, `{"x":1,"X":2}`),
		nested(3*argFramesOnStack, `{"command":"ls"}`),
		manyKeys(maxPairwiseKeys, `"k3":0`),
		manyKeys(maxPairwiseKeys+1, `"k3":0`),
		manyKeys(maxPairwiseKeys-1, `"K3":0`),
		manyKeys(maxPairwiseKeys, `"K3":0`),
		manyKeys(3*maxPairwiseKeys, `"K40":0`),
		manyKeys(3*maxPairwiseKeys, `"Command":0`),
		manyKeys(3*maxPairwiseKeys, `"x":{"k1":1,"k1":2}`),
		manyKeys(3*maxPairwiseKeys, `"x":`+manyKeys(3*maxPairwiseKeys, `"k7":7`)),
		manyKeys(3*maxPairwiseKeys, `"x":`+manyKeys(3*maxPairwiseKeys, `"K7":7`)),
		`{"`+strings.Repeat("ſ", maxArgNameInError+2)+`":1,"`+strings.Repeat("S", maxArgNameInError+2)+`":2}`,
	)
	r := rand.New(rand.NewPCG(2265, 1))
	for range 20_000 {
		args := argPayload(r, 1+r.IntN(4))
		switch r.IntN(10) {
		case 0:
			args = " \n" + args + "\t"
		case 1:
			args = args[:r.IntN(len(args)+1)]
		case 2:
			args = "\u00a0" + args
		}
		cases = append(cases, args)
	}
	for _, args := range cases {
		assertScanMatchesReference(t, args)
	}
}

// FuzzCheckToolArgsMatchesReference compares the production scan with the
// reference scan for arbitrary payloads.
func FuzzCheckToolArgsMatchesReference(f *testing.F) {
	for _, seed := range toolArgsSeeds {
		f.Add(seed)
	}
	r := rand.New(rand.NewPCG(2265, 2))
	for range 64 {
		f.Add(argPayload(r, 3))
	}
	f.Fuzz(func(t *testing.T, args string) {
		assertScanMatchesReference(t, args)
	})
}

// TestCheckToolArgsDoesNotAllocate pins that the canonical-form check, which
// every dispatched call and every policy evaluation runs, costs no allocation
// for the argument objects real tool calls carry.
func TestCheckToolArgsDoesNotAllocate(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	for _, tc := range []struct{ name, args string }{
		{"shell", `{"command":"go test ./... && git status --short","timeout_ms":120000,"background":false,"temp_scope":"managed"}`},
		{"file tool", `{"file_path":"/proj/internal/app/main.go","old_string":"return nil","new_string":"return err","replace_all":false}`},
		{"nested", `{"path":"src","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":"d"}],"meta":{"tag":"x"}}`},
		{"non-ASCII", `{"path":"docs/straße.md","query":"größe"}`},
	} {
		args := json.RawMessage(tc.args)
		if err := CheckToolArgs(args); err != nil {
			t.Fatalf("%s: CheckToolArgs(%s) = %v, want nil", tc.name, tc.args, err)
		}
		if allocs := testing.AllocsPerRun(1000, func() { _ = CheckToolArgs(args) }); allocs != 0 {
			t.Errorf("%s: CheckToolArgs allocations = %v, want 0", tc.name, allocs)
		}
	}
}

// TestDecodeToolArgsAllocatesLikeUnmarshal pins that DecodeToolArgs costs no
// allocation beyond the encoding/json decode it wraps.
func TestDecodeToolArgsAllocatesLikeUnmarshal(t *testing.T) {
	if raceEnabled {
		t.Skip("allocation counts are not meaningful under the race detector")
	}
	args := json.RawMessage(`{"command":"go test ./...","timeout_ms":5000,"temp_scope":"managed"}`)
	decode := testing.AllocsPerRun(1000, func() {
		var dst shellExecArgs
		_ = DecodeToolArgs(args, &dst)
	})
	unmarshal := testing.AllocsPerRun(1000, func() {
		var dst shellExecArgs
		_ = json.Unmarshal(args, &dst)
	})
	if decode > unmarshal {
		t.Errorf("DecodeToolArgs allocations = %v, json.Unmarshal %v", decode, unmarshal)
	}
}
