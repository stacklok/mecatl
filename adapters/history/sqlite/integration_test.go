package sqlite

import (
	"bytes"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/stacklok/mecatl/engine/kernel/codec"
	"github.com/stacklok/mecatl/engine/kernel/history"
	"github.com/stacklok/mecatl/engine/kernel/history/format"
	"github.com/stacklok/mecatl/engine/kernel/session"
)

func integrationPolicies() (history.Policy, session.Policy) {
	limits := codec.Limits{MaxBytes: 8192, MaxStringBytes: 256, MaxByteStringBytes: 4096, MaxArrayElements: 32, MaxMapPairs: 32, MaxDepth: 16, MaxNodes: 1024, MaxCollectionElements: 256}
	return history.Policy{CommitLimits: limits, RetryWindow: 16, MaxPageBytes: 8192, MaxPageCommits: 1}, session.Policy{CommitLimits: limits, PayloadLimits: limits, MaxRecordsPerCommit: 16, SoftMaxRetainedRecords: 128, SoftMaxPayloadBytes: 32768, SoftMaxDecodedWork: 1 << 24, MaxRetainedRecords: 128, MaxPayloadBytes: 32768, MaxDecodedWork: 1 << 24}
}
func integrationHistory(t *testing.T, b *Backend) *history.History {
	t.Helper()
	hp, _ := integrationPolicies()
	h, err := history.New(b, hp)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestLostAcknowledgmentResolveAcrossReopen(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	id := history.NewHistoryID()
	h := integrationHistory(t, b)
	dto := format.Commit{FormatVersion: 1, HistoryID: &id, Sequence: 1, Records: []format.Record{{Kind: "opaque", KindVersion: 1, Payload: []byte{0, 0xff, 0x80}}}}
	accepted, err := h.Append(t.Context(), id, dto)
	if err != nil {
		t.Fatal(err)
	}
	dto.HistoryID = nil
	dto.Parent = &accepted.Head.CID
	dto.Sequence = 2
	dto.Records[0].Payload = []byte("retained opaque payload")
	ref, err := h.Identify(id, dto)
	if err != nil {
		t.Fatal(err)
	}
	var commits atomic.Int32
	lost := errors.New("test dropped commit acknowledgment")
	installReceipt(t, b, id, receiptConnector{failure: lost, commits: &commits})
	result, err := h.Append(t.Context(), id, dto)
	if !errors.Is(err, lost) || result != (history.Resolution{}) || commits.Load() != 1 {
		t.Fatalf("unknown acceptance: %+v %v commits=%d", result, err, commits.Load())
	}
	if err = b.Close(); err != nil {
		t.Fatal(err)
	}
	b = newBackend(t, root)
	h = integrationHistory(t, b)
	result, err = h.Resolve(t.Context(), id, ref)
	if err != nil || result.Status != history.StatusAccepted || result.Proposal != ref || result.Head != ref {
		t.Fatalf("resolve: %+v %v", result, err)
	}
	page, err := h.Read(t.Context(), id, history.ReadRequest{After: accepted.Head, Through: ref, MaxCommits: 1})
	if err != nil || len(page.Commits) != 1 || !page.Commits[0].Commit.Equal(dto) {
		t.Fatalf("read: %+v %v", page, err)
	}
	// Resolve/read did not resubmit or advance history.
	head, err := h.Head(t.Context(), id)
	if err != nil || head != ref {
		t.Fatalf("head: %+v %v", head, err)
	}
}

func TestSessionPassiveFactsAcrossReopen(t *testing.T) {
	root := t.TempDir()
	b := newBackend(t, root)
	h := integrationHistory(t, b)
	id := history.NewHistoryID()
	_, policy := integrationPolicies()
	s, err := session.New(h, id, policy)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := s.BeginCommit()
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Discard()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(batch.Create())
	run, err := batch.StartRun()
	must(err)
	input, err := batch.AddUserInput(session.AddUserInputRequest{RunID: run, Parts: []session.ContentPart{{Text: new("first")}, {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceInline, MIMEType: "image/x-test", InlineData: []byte{0, 0xff, 0x80}}}}})
	must(err)
	// These provenance paths deliberately do not exist. Passive Load must retain
	// the supplied facts, not open files, fetch remote media, or execute work.
	must(batch.ReplaceInstructions(session.Instructions{HistoryID: id, Blocks: []session.InstructionBlock{{Parts: []session.ContentPart{{Text: new("Use retained vocabulary.")}}, Source: session.ContentSource{Description: "adopted instructions", WorkspaceFile: &session.WorkspaceFileSource{Path: "absent/AGENTS.md"}}}}}))
	must(batch.AddEffectiveInput(session.EffectiveInput{RunID: run, Inputs: []session.InputItem{{UserInput: &input}, {Inline: &session.AttributedInput{Actor: "harness", Purpose: session.PromptContext, Parts: []session.ContentPart{{Text: new("retained reference")}, {Media: &session.Media{Kind: session.MediaKindImage, Source: session.MediaSourceRemote, RemoteURI: "https://example.invalid/never-fetched"}}}, Source: session.ContentSource{Description: "prepared reference", WorkspaceFile: &session.WorkspaceFileSource{Path: "absent/design.md"}}}}}}))
	if _, err = s.SubmitCommit(t.Context(), batch); err != nil {
		t.Fatal(err)
	}
	old := s.Snapshot()
	// Use a second commit so Load crosses the History one-commit page boundary.
	batch2, err := s.BeginCommit()
	must(err)
	defer batch2.Discard()
	_, err = batch2.AddUserInput(session.AddUserInputRequest{RunID: run, Parts: []session.ContentPart{{Text: new("second")}}})
	must(err)
	if _, err = s.SubmitCommit(t.Context(), batch2); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	must(b.Close())
	b = newBackend(t, root)
	h = integrationHistory(t, b)
	loaded, err := session.Load(t.Context(), h, id, policy)
	must(err)
	view := loaded.Snapshot()
	if !reflect.DeepEqual(before, view) || view.ActiveRun() != run || view.Head().Sequence != 2 || view.RecordCount() != 6 {
		t.Fatal("loaded snapshot differs from accepted passive facts")
	}
	original, ok := view.UserInputByID(input)
	if !ok {
		t.Fatal("missing authored input")
	}
	text, ok := original.PartAt(0).Text()
	if !ok || text != "first" {
		t.Fatalf("text %q", text)
	}
	media, ok := original.PartAt(1).Media()
	data, inlineData := media.InlineData()
	if !ok || !inlineData || !bytes.Equal(data, []byte{0, 0xff, 0x80}) {
		t.Fatal("inline bytes changed")
	}
	instructions, ok := view.Instructions()
	if !ok {
		t.Fatal("missing instructions")
	}
	file, ok := instructions.BlockAt(0).Source().WorkspaceFile()
	if !ok || file.Path() != "absent/AGENTS.md" {
		t.Fatal("provenance changed")
	}
	effective, ok := view.RecordAt(4).EffectiveInput()
	if !ok || effective.InputCount() != 2 {
		t.Fatal("missing effective input")
	}
	inline, ok := effective.InputAt(1).Inline()
	if !ok || inline.Purpose() != session.PromptContext {
		t.Fatal("prepared attribution changed")
	}
	if head, e := h.Head(t.Context(), id); e != nil || head != before.Head() {
		t.Fatalf("Load wrote history: %+v %v", head, e)
	}
	closeout, err := loaded.BeginCommit()
	must(err)
	defer closeout.Discard()
	must(closeout.Interrupt())
	if _, err = loaded.SubmitCommit(t.Context(), closeout); err != nil {
		t.Fatal(err)
	}
	if old.Head().Sequence != 1 || old.RecordCount() != 5 || old.ActiveRun() != run || view.Head() != before.Head() || view.ActiveRun() != run {
		t.Fatal("accepted snapshots mutated")
	}
	must(b.Close())
	b = newBackend(t, root)
	loaded, err = session.Load(t.Context(), integrationHistory(t, b), id, policy)
	must(err)
	if loaded.Snapshot().Head().Sequence != 3 || loaded.Snapshot().ActiveRun() != (session.RunID{}) {
		t.Fatal("explicit closeout not retained")
	}
}
