package session_test

import (
	"bytes"
	"context"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/history/mem"
	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

// Each axis grows independently: a large requested field must copy, but large
// siblings and collections must not make capture/navigation or field exports copy.
func allocationViewFixture(t *testing.T, fieldBytes, siblingBytes, count int) map[string]*session.Session {
	t.Helper()
	limits := codec.Limits{MaxBytes: 8 << 20, MaxStringBytes: 1 << 20, MaxByteStringBytes: 1 << 20,
		MaxArrayElements: 4096, MaxMapPairs: 32, MaxDepth: 32, MaxNodes: 1 << 20, MaxCollectionElements: 1 << 20}
	p := session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16,
		SoftMaxRetainedRecords: 1024, MaxRetainedRecords: 1024, SoftMaxPayloadBytes: 16 << 20, MaxPayloadBytes: 16 << 20,
		SoftMaxDecodedWork: 1 << 34, MaxDecodedWork: 1 << 34}
	h, err := history.New(mem.New(), history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: limits.MaxBytes, MaxPageCommits: 2})
	mustBatch(t, err)
	s := boundSession(t, h, p)
	scope := s.Snapshot().HistoryID()
	b := beginBatch(t, s)
	mustBatch(t, b.Create())
	run := batchRun(t, b)
	parts := make([]session.ContentPart, count)
	for i := range parts {
		size := siblingBytes
		if i == 0 {
			size = fieldBytes
		}
		parts[i] = session.ContentPart{Media: &session.Media{
			Kind: session.MediaKindDocument, MIMEType: "application/octet-stream", FormatHint: "raw", Filename: "notes.bin", Title: strings.Repeat("m", siblingBytes),
			Source: session.MediaSourceInline, InlineData: bytes.Repeat([]byte{0xa5}, size)}}
	}
	input, err := b.AddUserInput(session.UserInput{RunID: run, Parts: parts})
	mustBatch(t, err)
	source := session.ContentSource{Description: "prepared reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "docs/design.md"}}
	blocks := make([]session.InstructionBlock, count)
	tools := make([]session.ToolDefinition, count)
	items := make([]session.InputItem, count)
	for i := range count {
		blocks[i] = session.InstructionBlock{Parts: parts, Source: source}
		size := siblingBytes
		if i == 0 {
			size = fieldBytes
		}
		tools[i] = session.ToolDefinition{Name: "tool" + strconv.Itoa(i), Description: strings.Repeat("d", siblingBytes), ArgumentSchema: []byte(`"` + strings.Repeat("s", size-2) + `"`)}
		items[i] = session.InputItem{Inline: &session.AttributedInput{Actor: "extension/example", Purpose: session.PromptContext, Parts: parts, Source: source}}
	}
	items[0] = session.InputItem{UserInputID: &input}
	mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: scope, Blocks: blocks}))
	mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: scope, Tools: tools}))
	mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: scope, Route: strings.Repeat("r", siblingBytes), API: "dialect/v1", Model: "alias", Settings: []byte(`"` + strings.Repeat("v", fieldBytes-2) + `"`)}))
	mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: items}))
	submitBatch(t, s, b)

	loaded, err := session.Load(context.Background(), h, scope, p)
	mustBatch(t, err)
	return map[string]*session.Session{"Submit": s, "Load": loaded}
}

func TestViewNavigationAllocations(t *testing.T) {
	for _, axis := range []struct {
		name                  string
		field, sibling, count int
	}{{"small", 32, 32, 2}, {"payload", 64 << 10, 32, 2}, {"siblings", 32, 64 << 10, 2}, {"collections", 32, 32, 32}} {
		t.Run(axis.name, func(t *testing.T) {
			for seam, s := range allocationViewFixture(t, axis.field, axis.sibling, axis.count) {
				t.Run(seam, func(t *testing.T) {
					var snapshot session.Snapshot
					var record session.RecordView
					var run session.RunView
					var input session.UserInputView
					var effective session.EffectiveInputView
					var instructions session.InstructionsView
					var catalog session.ToolCatalogView
					var binding session.ModelBindingView
					var inline session.AttributedInputView
					var part session.ContentPartView
					var media session.MediaView
					var file session.WorkspaceFileSourceView
					var definition session.ToolDefinitionView
					var text string
					var count int
					var ok bool
					capture := func() {
						snapshot = s.Snapshot()
						run = snapshot.RunAt(snapshot.RunCount() - 1)
						record = run.RecordAt(run.RecordCount() - 1)
						effective, ok = record.EffectiveInput()
						instructions, _ = snapshot.Instructions()
						catalog, _ = snapshot.ToolCatalog()
						binding, _ = snapshot.ModelBinding()
						fromRecordI, _ := snapshot.RecordAt(3).Instructions()
						fromRecordT, _ := snapshot.RecordAt(4).ToolCatalog()
						fromRecordM, _ := snapshot.RecordAt(5).ModelBinding()
						if !ok || fromRecordI != instructions || fromRecordT != catalog || fromRecordM != binding {
							t.Fatal("record and selection handles disagree")
						}
					}
					follow := func() {
						id, present := effective.InputAt(0).UserInputID()
						input, ok = snapshot.UserInputByID(id)
						ok = ok && present
						part = input.PartAt(input.PartCount() - 1)
						media, _ = part.Media()
						text = media.Title()
					}
					nested := func() {
						inline, ok = effective.InputAt(effective.InputCount() - 1).Inline()
						part = inline.PartAt(inline.PartCount() - 1)
						media, _ = part.Media()
						file, _ = inline.Source().WorkspaceFile()
						block := instructions.BlockAt(instructions.BlockCount() - 1)
						instructionMedia, instructionMediaOK := block.PartAt(block.PartCount() - 1).Media()
						definition = catalog.ToolAt(catalog.ToolCount() - 1)
						count = effective.InputCount() + inline.PartCount() + input.PartCount() + instructions.BlockCount() + block.PartCount() + catalog.ToolCount()
						text = string(inline.Actor())
						if !instructionMediaOK || instructionMedia.Filename() != "notes.bin" || media.Kind() != session.MediaKindDocument || inline.Purpose() != session.PromptContext || inline.Source().Description() != "prepared reference" || file.Path() != "docs/design.md" || block.Source().Description() != "prepared reference" || media.MIMEType() != "application/octet-stream" || media.FormatHint() != "raw" || media.Filename() != "notes.bin" || media.Source() != session.MediaSourceInline || definition.Name() == "" || definition.Description() == "" || binding.Route() == "" || binding.API() != "dialect/v1" || binding.Model() != "alias" {
							t.Fatal("lost nested metadata")
						}
					}
					capture()
					follow()
					for _, check := range []struct {
						name      string
						operation func()
						parent    int
					}{{"capture", capture, 3}, {"reference", follow, 2}, {"nested", nested, 6}} {
						if n := testing.AllocsPerRun(100, check.operation); n != 0 {
							t.Fatalf("%s allocated %g times", check.name, n)
						}
						// Independently inject the forbidden payload copy into each
						// measured path. Keep it observable instead of discarding it.
						payload := snapshot.RecordAt(check.parent).Format().Payload
						var copied []byte
						if n := testing.AllocsPerRun(10, func() {
							check.operation()
							copied = bytes.Clone(payload)
						}); n == 0 {
							t.Fatalf("%s payload-copy control was optimized away", check.name)
						}
						runtime.KeepAlive(copied)
					}
					if !ok || input.ID().RunID() != snapshot.ActiveRun() || count != 6*axis.count || text != "extension/example" {
						t.Fatal("navigation did not reach expected independent fields/counts")
					}
					runtime.KeepAlive(snapshot)
					runtime.KeepAlive(part)
				})
			}
		})
	}
}

// allocatedViewBytes measures only already-built field exports. No tests here
// run in parallel. A size-class/noise allowance stays far below the 64 KiB
// siblings; the independent one-allocation control proves counts alone cannot
// detect accidentally copying an enclosing payload.
func allocatedViewBytes(operation func()) uint64 {
	operation()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range 64 {
		operation()
	}
	runtime.ReadMemStats(&after)
	return (after.TotalAlloc - before.TotalAlloc) / 64
}

func TestViewByteExportsCopyOnlyRequestedField(t *testing.T) {
	for _, axis := range []struct {
		name           string
		field, sibling int
	}{{"small", 32, 32}, {"requested-large", 64 << 10, 32}, {"sibling-large", 32, 64 << 10}} {
		t.Run(axis.name, func(t *testing.T) {
			for seam, s := range allocationViewFixture(t, axis.field, axis.sibling, 2) {
				t.Run(seam, func(t *testing.T) {
					v := s.Snapshot()
					input := lookupInput(t, v, v.ActiveRun(), 1)
					media, _ := input.PartAt(0).Media()
					catalog, _ := v.ToolCatalog()
					binding, _ := v.ModelBinding()
					effective, _ := v.RecordAt(6).EffectiveInput()
					inline, _ := effective.InputAt(1).Inline()
					inlineMedia, _ := inline.PartAt(0).Media()
					instructions, _ := v.Instructions()
					instructionMedia, _ := instructions.BlockAt(0).PartAt(0).Media()
					data := func(m session.MediaView) func() []byte {
						return func() []byte { b, _ := m.InlineData(); return b.Copy() }
					}
					for _, field := range []struct {
						name   string
						export func() []byte
						parent int
					}{
						{"authored-media", data(media), 2}, {"inline-media", data(inlineMedia), 6}, {"instruction-media", data(instructionMedia), 3},
						{"schema", catalog.ToolAt(0).ArgumentSchema, 4}, {"settings", binding.Settings, 5},
					} {
						t.Run(field.name, func(t *testing.T) {
							var copied []byte
							operation := func() { copied = field.export() }
							if n := testing.AllocsPerRun(100, operation); n != 1 {
								t.Fatalf("field export allocations = %g; want exactly one", n)
							}
							if len(copied) != axis.field {
								t.Fatalf("field length = %d; want %d", len(copied), axis.field)
							}
							if n := allocatedViewBytes(operation); n < uint64(axis.field) || n > uint64(axis.field+4096) {
								t.Fatalf("field export allocated %d bytes/op for %d-byte field", n, axis.field)
							}
							first := copied[0]
							copied[0] ^= 0xff
							if next := field.export(); next[0] != first || &next[0] == &copied[0] {
								t.Fatal("byte export shares retained or returned storage")
							}
							runtime.KeepAlive(copied)
							if axis.name == "sibling-large" {
								// A wrong implementation can allocate exactly once while
								// copying the enclosing payload, not just this field.
								payload := v.RecordAt(field.parent).Format().Payload
								var wrong []byte
								control := func() { wrong = bytes.Clone(payload) }
								if n := testing.AllocsPerRun(100, control); n != 1 {
									t.Fatalf("whole-payload control allocations = %g", n)
								}
								if n := allocatedViewBytes(control); n <= uint64(axis.field+4096) {
									t.Fatalf("byte-size control failed to detect whole-payload copy: %d", n)
								}
								runtime.KeepAlive(wrong)
							}
						})
					}
				})
			}
		})
	}
}

func TestViewPinnedReferenceChildrenAcrossReplacement(t *testing.T) {
	s := allocationViewFixture(t, 32, 32, 2)["Submit"]
	old := s.Snapshot()
	effective, _ := old.RecordAt(6).EffectiveInput()
	ref, _ := effective.InputAt(0).UserInputID()
	input, _ := old.UserInputByID(ref)
	part := input.PartAt(0)
	media, _ := part.Media()
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 4 {
		wg.Go(func() {
			<-start
			for range 1000 {
				resolved, ok := old.UserInputByID(ref)
				data, present := media.InlineData()
				if !ok || !present || resolved != input || input.PartAt(0) != part || media.Title() != "mmmmmmmmmmmmmmmmmmmmmmmmmmmmmmmm" || data.Len() != 32 || data.At(0) != 0xa5 || old.RecordCount() != 7 {
					t.Error("pinned reference child changed")
					return
				}
				data.Copy()[0] = 0 // Concurrent exports must not alias each other either.
			}
		})
	}
	close(start)
	for range 32 {
		b := beginBatch(t, s)
		_, err := b.AddUserInput(session.UserInput{RunID: old.ActiveRun(), Parts: byteParts([]byte("later"))})
		mustBatch(t, err)
		mustBatch(t, b.ReplaceInstructions(session.Instructions{HistoryID: old.HistoryID()}))
		mustBatch(t, b.ReplaceToolCatalog(session.ToolCatalog{HistoryID: old.HistoryID()}))
		mustBatch(t, b.ReplaceModelBinding(session.ModelBinding{HistoryID: old.HistoryID(), Route: "replacement", API: "a", Model: "m", Settings: []byte("true")}))
		mustBatch(t, b.AddEffectiveInput(session.EffectiveInput{RunID: old.ActiveRun()}))
		submitBatch(t, s, b)
	}
	wg.Wait()
}
