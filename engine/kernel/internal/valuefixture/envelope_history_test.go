package valuefixture_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	"github.com/stacklok/mecatl/engine/kernel/internal/valuefixture"
	"github.com/stacklok/mecatl/engine/kernel/internal/valuefixture/fixtureview"
	"github.com/stacklok/mecatl/engine/kernel/value"
	"github.com/stacklok/mecatl/engine/kernel/views"
)

func TestEnvelopeHistoryPersistenceOwnership(t *testing.T) {
	ctx := context.Background()
	backend := mem.New()
	h, err := history.New(backend, history.Policy{
		CommitLimits: limits(), RetryWindow: 2, MaxPageBytes: 2 << 20, MaxPageCommits: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	items := []value.Value{value.String("text"), value.Null()}
	nested := value.Map{"enabled": value.Bool(true), "items": value.Array(items)}
	numbers := []value.Value{value.Int64(math.MinInt64), value.Int64(-2), value.Int64(0), value.Uint64(9007199254740993), value.Uint64(math.MaxUint64)}
	native := value.Map{
		"null": value.Null(), "text": value.String(""), "array": value.Array(nil), "object": value.Object(nil),
		"numbers": value.Array(numbers), "nested": value.Object(nested),
	}
	jsonInput := []byte(`{"nested":{"items":["text",null],"enabled":true},"numbers":[-9223372036854775808,-2,-0,9007199254740993,18446744073709551615],"object":{},"array":[],"text":"","null":null}`)
	parsed, err := value.ParseJSONObject(jsonInput, limits())
	if err != nil || !native.Equal(parsed) {
		t.Fatalf("strict JSON/native equality: %v", err)
	}
	clear(jsonInput)

	author := envelope()
	author.Value = value.Object(native)
	author.Map = value.Map{"snapshot": value.Object(native), "empty": value.Object(nil), "items": value.Array(items)}
	want := author.Clone()
	accepted, err := author.Value.Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	acceptedMap, err := author.Map.Admit(limits())
	if err != nil {
		t.Fatal(err)
	}
	payload, err := codec.Encode(author, limits())
	if err != nil {
		t.Fatal(err)
	}
	canonical := bytes.Clone(payload)
	fromJSON := envelope()
	fromJSON.Value = value.Object(parsed)
	fromJSON.Map = value.Map{"items": value.Array([]value.Value{value.String("text"), value.Null()}), "empty": value.Object(value.Map{}), "snapshot": value.Object(parsed)}
	jsonPayload, err := codec.Encode(fromJSON, limits())
	if err != nil || !bytes.Equal(jsonPayload, canonical) {
		t.Fatalf("JSON/native envelope canonical bytes differ: %v", err)
	}

	// Encode must detach every authoring owner, including shared primitive
	// subtrees. Mutation happens before Append, not merely after storage copies it.
	items[0] = value.Bool(false)
	numbers[0] = value.Null()
	nested["enabled"] = value.Null()
	native["new"] = value.String("later")
	author.Value = value.Null()
	author.Map["empty"] = value.Null()
	author.Labels["zero"] = "changed"
	author.Counts["count"] = 0
	author.Children["one"].Label = "changed"
	author.Children["one"].Data[0] = 99
	author.Children["one"].Children[0].Data[0] = 99
	author.Children["two"].Data[0] = 99
	author.Sibling[0] = 99
	if !bytes.Equal(payload, canonical) {
		t.Fatal("encoded bytes alias authoring inputs")
	}
	assertStructuredView(t, accepted, value.Borrow(want.Value))
	assertMapView(t, acceptedMap, value.BorrowMap(want.Map))
	assertJSONValue(t, accepted, want.Value)
	assertJSONMap(t, acceptedMap, want.Map)

	id := history.NewHistoryID()
	proposal := format.Commit{FormatVersion: 1, HistoryID: &id, Sequence: 1,
		Records: []format.Record{{Kind: "opaque", KindVersion: 1, Payload: payload}},
	}
	root, err := h.Append(ctx, id, proposal)
	if err != nil || root.Status != history.StatusAccepted {
		t.Fatalf("Append root: %+v, %v", root, err)
	}
	// Neither the caller's record storage nor its payload remains authoritative.
	clear(payload)
	proposal.Records[0].Kind = "changed"
	proposal.Records[0].Payload = []byte("changed")
	clear(author.Map)
	clear(author.Children)
	clear(parsed)

	request := history.ReadRequest{Through: root.Proposal, MaxCommits: 1}
	page, err := h.Read(ctx, id, request)
	if err != nil || len(page.Commits) != 1 || page.Next != root.Proposal || page.Commits[0].Ref != root.Proposal {
		t.Fatalf("Read root: %+v, %v", page, err)
	}
	record := &page.Commits[0].Commit.Records[0]
	if record.Kind != "opaque" || record.KindVersion != 1 || !bytes.Equal(record.Payload, canonical) {
		t.Fatal("stored opaque record changed")
	}
	decoded, err := codec.Decode[valuefixture.Envelope](record.Payload, limits())
	if err != nil {
		t.Fatal(err)
	}
	pinned := fixtureview.Borrow(&decoded)
	assertEnvelopeView(t, pinned, want)
	assertJSONValue(t, pinned.Value(), want.Value)
	assertJSONMap(t, pinned.Map(), want.Map)
	child, ok := pinned.Children().Get("one")
	if !ok {
		t.Fatal("missing child")
	}
	pinnedLeaf := child.Children().At(0).Data()
	detachedLeaf := pinnedLeaf.Copy()
	if !bytes.Equal(detachedLeaf, []byte{4, 5}) {
		t.Fatal("Copy did not select the nested leaf")
	}
	detachedLeaf[0] = 99
	assertEnvelopeView(t, pinned, want)

	// The decoded DTO owns its bytes independently of the input payload. A
	// subsequent read must also own storage independently of this returned page.
	clear(record.Payload)
	reread, err := h.Read(ctx, id, request)
	if err != nil || len(reread.Commits) != 1 || !bytes.Equal(reread.Commits[0].Commit.Records[0].Payload, canonical) {
		t.Fatalf("reread after returned-payload mutation: %v", err)
	}
	again, err := codec.Decode[valuefixture.Envelope](reread.Commits[0].Commit.Records[0].Payload, limits())
	if err != nil || !want.Equal(again) {
		t.Fatalf("reread envelope changed: %v", err)
	}
	reencoded, err := codec.Encode(again, limits())
	if err != nil || !bytes.Equal(reencoded, canonical) {
		t.Fatalf("canonical decode/re-encode changed: %v", err)
	}
	assertEnvelopeView(t, fixtureview.Borrow(&again), want)

	// Extend the real history with different contents; old accepted snapshots,
	// decoded DTOs and derived handles must remain valid across later operations.
	later := envelope()
	later.Children["one"].Data[0] = 88
	laterPayload, err := codec.Encode(later, limits())
	if err != nil {
		t.Fatal(err)
	}
	parent := root.Proposal.CID
	second, err := h.Append(ctx, id, format.Commit{FormatVersion: 1, Parent: &parent, Sequence: 2,
		Records: []format.Record{{Kind: "opaque", KindVersion: 1, Payload: laterPayload}},
	})
	if err != nil || second.Status != history.StatusAccepted {
		t.Fatalf("Append successor: %+v, %v", second, err)
	}
	clear(laterPayload)
	all, err := h.Read(ctx, id, history.ReadRequest{Through: second.Proposal, MaxCommits: 2})
	if err != nil || len(all.Commits) != 2 || all.Next != second.Proposal || !bytes.Equal(all.Commits[0].Commit.Records[0].Payload, canonical) {
		t.Fatalf("Read extended history: %v", err)
	}
	loadedLater, err := codec.Decode[valuefixture.Envelope](all.Commits[1].Commit.Records[0].Payload, limits())
	if err != nil {
		t.Fatal(err)
	}
	assertEnvelopeView(t, fixtureview.Borrow(&loadedLater), later)
	again.Children["one"].Data[0] = 77
	again.Children["one"].Children[0].Data[0] = 77
	again.Sibling[0] = 77
	assertEnvelopeView(t, pinned, want)
	assertBytesView(t, pinnedLeaf, []byte{4, 5})
	assertStructuredView(t, accepted, value.Borrow(want.Value))
	assertMapView(t, acceptedMap, value.BorrowMap(want.Map))
}

func assertEnvelopeView(t *testing.T, got fixtureview.EnvelopeView, want valuefixture.Envelope) {
	t.Helper()
	assertStructuredView(t, got.Value(), value.Borrow(want.Value))
	assertMapView(t, got.Map(), value.BorrowMap(want.Map))
	if got.Labels().Len() != len(want.Labels) || got.Counts().Len() != len(want.Counts) || got.Children().Len() != len(want.Children) {
		t.Fatal("generated map lengths changed")
	}
	if _, ok := got.Labels().Get("absent"); ok {
		t.Fatal("missing scalar map entry became present")
	}
	if _, ok := got.Counts().Get("absent"); ok {
		t.Fatal("missing count map entry became present")
	}
	if _, ok := got.Children().Get("absent"); ok {
		t.Fatal("missing DTO map entry became present")
	}
	labels, counts, children := 0, 0, 0
	for key, text := range got.Labels().All() {
		labels++
		if expected, ok := want.Labels[key]; !ok || text != expected {
			t.Fatalf("label %q changed", key)
		}
		if selected, ok := got.Labels().Get(key); !ok || selected != text {
			t.Fatalf("label %q lookup differs", key)
		}
	}
	for key, count := range got.Counts().All() {
		counts++
		if expected, ok := want.Counts[key]; !ok || count != expected {
			t.Fatalf("count %q changed", key)
		}
		if selected, ok := got.Counts().Get(key); !ok || selected != count {
			t.Fatalf("count %q lookup differs", key)
		}
	}
	for key, child := range got.Children().All() {
		children++
		expected, ok := want.Children[key]
		if !ok || child.Label() != expected.Label || child.Children().Len() != len(expected.Children) {
			t.Fatalf("child %q changed", key)
		}
		assertBytesView(t, child.Data(), expected.Data)
		leaves := 0
		for index, leaf := range child.Children().All() {
			leaves++
			assertBytesView(t, leaf.Data(), expected.Children[index].Data)
		}
		if leaves != len(expected.Children) {
			t.Fatalf("child %q traversal skipped leaves", key)
		}
	}
	if labels != len(want.Labels) || counts != len(want.Counts) || children != len(want.Children) {
		t.Fatal("generated map traversal skipped entries")
	}
	assertBytesView(t, got.Sibling(), want.Sibling)
}

func assertBytesView(t *testing.T, got views.Bytes, want []byte) {
	t.Helper()
	if got.Len() != len(want) {
		t.Fatalf("byte length = %d, want %d", got.Len(), len(want))
	}
	seen := 0
	for index, b := range got.All() {
		seen++
		if b != want[index] {
			t.Fatalf("byte %d = %d, want %d", index, b, want[index])
		}
	}
	if seen != len(want) {
		t.Fatal("byte traversal skipped elements")
	}
}

func assertJSONValue(t *testing.T, got json.Marshaler, want value.Value) {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	parsed, err := value.ParseJSON(encoded, limits())
	if err != nil || !parsed.Equal(want) {
		t.Fatalf("value JSON differs from owner: %s: %v", encoded, err)
	}
}

func assertJSONMap(t *testing.T, got json.Marshaler, want value.Map) {
	t.Helper()
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal map: %v", err)
	}
	parsed, err := value.ParseJSONObject(encoded, limits())
	if err != nil || !parsed.Equal(want) {
		t.Fatalf("map JSON differs from owner: %s: %v", encoded, err)
	}
}

func assertMapView(t *testing.T, got, want value.MapView) {
	t.Helper()
	if got.Len() != want.Len() {
		t.Fatalf("structured map length = %d, want %d", got.Len(), want.Len())
	}
	if missing, ok := got.Get("absent"); ok || missing.Kind() != value.InvalidKind {
		t.Fatal("missing structured entry is not absent/invalid")
	}
	seen := 0
	for key, child := range got.All() {
		seen++
		expected, ok := want.Get(key)
		if !ok {
			t.Fatalf("unexpected structured key %q", key)
		}
		selected, ok := got.Get(key)
		if !ok || selected.Kind() != expected.Kind() {
			t.Fatalf("iterated structured key %q missing or wrong kind", key)
		}
		assertStructuredView(t, child, expected)
	}
	if seen != want.Len() {
		t.Fatal("structured map traversal skipped entries")
	}
}

func assertStructuredView(t *testing.T, got, want value.View) {
	t.Helper()
	if got.Kind() != want.Kind() {
		t.Fatalf("kind = %q, want %q", got.Kind(), want.Kind())
	}
	switch want.Kind() {
	case value.NullKind:
	case value.BoolKind:
		x, ok := got.Bool()
		y, _ := want.Bool()
		if !ok || x != y {
			t.Fatal("boolean changed")
		}
	case value.StringKind:
		x, ok := got.String()
		y, _ := want.String()
		if !ok || x != y {
			t.Fatal("text changed")
		}
	case value.IntegerKind:
		x, xok := got.Int64()
		y, yok := want.Int64()
		u, uok := got.Uint64()
		v, vok := want.Uint64()
		if xok != yok || x != y || uok != vok || u != v {
			t.Fatal("integer changed or lossy conversion")
		}
	case value.ArrayKind:
		x, ok := got.Array()
		y, _ := want.Array()
		if !ok || x.Len() != y.Len() {
			t.Fatal("array changed")
		}
		seen := 0
		for index, child := range x.All() {
			seen++
			assertStructuredView(t, child, y.At(index))
		}
		if seen != y.Len() {
			t.Fatal("structured array traversal skipped elements")
		}
	case value.MapKind:
		x, ok := got.Object()
		y, _ := want.Object()
		if !ok {
			t.Fatal("object absent")
		}
		assertMapView(t, x, y)
	default:
		t.Fatalf("unexpected invalid fixture value: %q", want.Kind())
	}
}
