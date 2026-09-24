package pdfartifact

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

// TestADR_0360_PDFBytesStayOutsideSessionState examines the Redis snapshot
// and event stream after the same externalization used by prompt and tool
// paths. The probes below confirm that both surfaces catch planted leakage.
func TestADR_0360_PDFBytesStayOutsideSessionState(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	const id session.SessionID = "pdf-reference-only"
	sess := session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())
	if err := metadata.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	artifacts := New(metadata, objects)
	promptBytes := []byte("%PDF-1.7\nprompt-private-6380\n%%EOF")
	toolBytes := []byte("%PDF-1.7\ntool-private-9361\n%%EOF")
	prompt, err := artifacts.Stage(t.Context(), id, "prompt.pdf", bytes.NewReader(promptBytes))
	if err != nil {
		t.Fatal(err)
	}
	promptPart, err := session.NewPDFContent(prompt.ID, prompt.Name, prompt.Size, prompt.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	result, err := (ResultProcessor{Artifacts: artifacts}).ProcessToolResult(t.Context(), id, pdfResult(toolBytes, ""))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Parts) != 3 || result.Parts[1].BlockKind != session.BlockPDFArtifact {
		t.Fatalf("tool result was not externalized: %+v", result)
	}
	history := []session.Message{
		session.NewUserMessageWithParts("read", []session.Content{promptPart}),
		session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall(result.CallID, "PDFTool", nil)}),
		session.NewToolMessage(result),
	}
	if err := sess.SeedHistory(history); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Save(t.Context(), sess); err != nil {
		t.Fatal(err)
	}
	if err := artifacts.CommitPrompt(t.Context(), id, []string{prompt.ID}); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Append(t.Context(), id, session.Event{Type: session.EvUserPrompt, UserPrompt: &session.UserPromptPayload{Text: "read", Parts: []session.Content{promptPart}}}); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Append(t.Context(), id, session.Event{Type: session.EvToolResult, ToolResult: &result}); err != nil {
		t.Fatal(err)
	}

	for _, blob := range [][]byte{promptBytes, toolBytes} {
		for _, forbidden := range []string{string(blob), base64.StdEncoding.EncodeToString(blob)} {
			if redisPDFStateContains(t, mr, id, forbidden) {
				t.Fatalf("Redis session or event state retained forbidden PDF payload of %d bytes", len(blob))
			}
		}
	}
	snapshot := mr.HGet("mecatl:session:"+string(id), "blob")
	entries, err := mr.Stream("mecatl:events:" + string(id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snapshot, prompt.ID) || !strings.Contains(snapshot, result.Parts[1].ArtifactID) || len(entries) != 2 {
		t.Fatal("Redis did not retain the expected reference-only snapshot and event entries")
	}
	for _, artifactID := range []string{prompt.ID, result.Parts[1].ArtifactID} {
		found := false
		for _, entry := range entries {
			if strings.Contains(strings.Join(entry.Values, ""), artifactID) {
				found = true
			}
		}
		if !found {
			t.Fatalf("Redis event stream omitted artifact metadata %q", artifactID)
		}
	}

	// A negative assertion is only useful if these actual Redis surfaces can
	// make it fail. Probe raw bytes in the snapshot and base64 in an event.
	mr.HSet("mecatl:session:"+string(id), "forbidden_probe", string(promptBytes))
	if !redisPDFStateContains(t, mr, id, string(promptBytes)) {
		t.Fatal("snapshot leak probe went undetected")
	}
	mr.HDel("mecatl:session:"+string(id), "forbidden_probe")
	if _, err := mr.XAdd("mecatl:events:"+string(id), "*", []string{"r", base64.StdEncoding.EncodeToString(toolBytes)}); err != nil {
		t.Fatal(err)
	}
	if !redisPDFStateContains(t, mr, id, base64.StdEncoding.EncodeToString(toolBytes)) {
		t.Fatal("event base64 leak probe went undetected")
	}
}

func redisPDFStateContains(t *testing.T, mr *miniredis.Miniredis, id session.SessionID, forbidden string) bool {
	t.Helper()
	snapshotKey := "mecatl:session:" + string(id)
	fields, err := mr.HKeys(snapshotKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if strings.Contains(mr.HGet(snapshotKey, field), forbidden) {
			return true
		}
	}
	entries, err := mr.Stream("mecatl:events:" + string(id))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		for _, value := range entry.Values {
			if strings.Contains(value, forbidden) {
				return true
			}
		}
	}
	return false
}

// TestSDKPDFArtifacts_Scenario4_ForkAndCleanup pins private copies and
// reference rewrites before a successor snapshot can be published.
func TestSDKPDFArtifacts_Scenario4_ForkAndCleanup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	const source, fork = session.SessionID("pdf-source"), session.SessionID("pdf-fork")
	if err := metadata.Save(t.Context(), session.New(source, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	storage := New(metadata, objects)
	promptBytes := []byte("%PDF-1.7\nprompt-secret\n%%EOF")
	toolBytes := []byte("%PDF-1.7\ntool-secret\n%%EOF")
	prompt, err := storage.Stage(t.Context(), source, "prompt.pdf", bytes.NewReader(promptBytes))
	if err != nil {
		t.Fatal(err)
	}
	tool, err := storage.Stage(t.Context(), source, "tool.pdf", bytes.NewReader(toolBytes))
	if err != nil {
		t.Fatal(err)
	}
	promptPart, err := session.NewPDFContent(prompt.ID, prompt.Name, prompt.Size, prompt.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	toolPart, err := session.NewPDFArtifactBlock(tool.ID, tool.Name, tool.Size, tool.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	const callID session.ToolCallID = "pdf-call"
	history := []session.Message{
		session.NewUserMessageWithParts("read", []session.Content{promptPart}),
		session.NewAssistantMessage("", "", []session.ToolCall{session.NewToolCall(callID, "PDFTool", nil)}),
		session.NewToolMessage(session.NewToolResultWithParts(callID, "PDF artifact", []session.Content{toolPart})),
	}
	copied, err := storage.CopyFork(t.Context(), source, fork, history)
	if err != nil {
		t.Fatalf("copy before successor publication: %v", err)
	}
	if len(copied) != len(history) || copied[0].Parts[0].ArtifactID == prompt.ID || copied[2].ToolResult.Parts[0].ArtifactID == tool.ID {
		t.Fatalf("fork references were not rewritten: %+v", copied)
	}
	if history[0].Parts[0].ArtifactID != prompt.ID || history[2].ToolResult.Parts[0].ArtifactID != tool.ID {
		t.Fatal("copy mutated source message parts")
	}
	for _, tc := range []struct {
		id   string
		want []byte
	}{
		{id: copied[0].Parts[0].ArtifactID, want: promptBytes},
		{id: copied[2].ToolResult.Parts[0].ArtifactID, want: toolBytes},
	} {
		reader, err := objects.Open(context.Background(), objectKey(fork, tc.id))
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil || !bytes.Equal(got, tc.want) {
			t.Fatalf("private fork copy %q = %q, %v", tc.id, got, readErr)
		}
	}
	// A pod can stop after copying but before publishing the successor. The
	// durable outbox survives it, while an active source lease protects the
	// unpublished copy from another replica's cleanup pass.
	if _, err := mr.ZAdd("mecatl:pdf-artifacts:delete-outbox", float64(time.Now().Add(-10*time.Minute).Unix()), string(fork)); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(36 * time.Minute) // expire the active-write TTL without sleeping
	restartedMetadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedMetadata.Close() })
	active := true
	restarted := New(restartedMetadata, objects)
	restarted.LeaseActive = func(_ context.Context, id session.SessionID) (bool, error) {
		return id == source && active, nil
	}
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 4 {
		t.Fatal("active source fork lease did not protect private copies")
	}
	active = false
	flaky := &deleteOnceObjects{memoryObjects: *objects, fail: true}
	restarted.objects = flaky
	if err := restarted.Reconcile(t.Context()); err == nil || len(objects.data) != 4 {
		t.Fatalf("failed cleanup did not retain objects for retry: err=%v objects=%d", err, len(objects.data))
	}
	finalReplica := New(restartedMetadata, objects)
	finalReplica.LeaseActive = restarted.LeaseActive
	if err := finalReplica.Reconcile(t.Context()); err != nil {
		t.Fatalf("restart cleanup retry: %v", err)
	}
	if len(objects.data) != 2 {
		t.Fatalf("abandoned fork cleanup removed wrong objects: %d remain", len(objects.data))
	}
	if pending, err := restartedMetadata.PDFDeletionBatch(t.Context(), time.Now()); err != nil || len(pending) != 0 {
		t.Fatalf("abandoned fork outbox after cleanup = %v, %v", pending, err)
	}
}

type deleteOnceObjects struct {
	memoryObjects
	fail bool
}

func (o *deleteOnceObjects) Delete(ctx context.Context, key string) error {
	if o.fail {
		o.fail = false
		return ErrStorage
	}
	return o.memoryObjects.Delete(ctx, key)
}

func TestPDFArtifactStorage_DeletionAndRetentionRevokeAndReconcile(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	objects := &memoryObjects{data: make(map[string][]byte)}
	storage := New(metadata, objects)
	storage.LeaseActive = func(context.Context, session.SessionID) (bool, error) { return false, nil }
	ids := []session.SessionID{"direct-deletion", "retention-pruning"}
	artifacts := make(map[session.SessionID]struct{ id, key string })
	for _, id := range ids {
		if err := metadata.Save(t.Context(), session.New(id, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
			t.Fatal(err)
		}
		artifact, err := storage.Stage(t.Context(), id, "report.pdf", bytes.NewReader([]byte("%PDF-1.7\nretained-private\n%%EOF")))
		if err != nil {
			t.Fatal(err)
		}
		artifacts[id] = struct{ id, key string }{artifact.ID, objectKey(id, artifact.ID)}
	}
	if err := metadata.Delete(t.Context(), ids[0]); err != nil {
		t.Fatal(err)
	}
	page, err := metadata.PageSessionMetadata(t.Context(), port.SessionMetadataPageRequest{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var candidate port.SessionDiscoveryMeta
	for _, row := range page.Sessions {
		if row.ID == ids[1] {
			candidate = row
		}
	}
	if candidate.ID == "" {
		t.Fatal("retention candidate was not indexed")
	}
	deleted, err := metadata.DeleteSessionIfUnchanged(t.Context(), candidate)
	if err != nil || !deleted {
		t.Fatalf("conditional retention pruning = %t, %v", deleted, err)
	}
	for _, id := range ids {
		if _, _, err := storage.Open(t.Context(), id, artifacts[id].id); err != ErrNotFound {
			t.Fatalf("new download after deletion of %q = %v", id, err)
		}
		if _, ok := objects.data[artifacts[id].key]; !ok {
			t.Fatalf("object disappeared before durable cleanup of %q", id)
		}
		if _, err := mr.ZAdd("mecatl:pdf-artifacts:delete-outbox", float64(time.Now().Add(-10*time.Minute).Unix()), string(id)); err != nil {
			t.Fatal(err)
		}
	}
	// New client and lifecycle instance emulate a replacement pod draining
	// deletion and retention intent after both snapshots have disappeared.
	restartedMetadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedMetadata.Close() })
	restarted := New(restartedMetadata, objects)
	restarted.LeaseActive = storage.LeaseActive
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 0 {
		t.Fatalf("deleted session objects survived reconciliation: %d", len(objects.data))
	}
	if pending, err := restartedMetadata.PDFDeletionBatch(t.Context(), time.Now()); err != nil || len(pending) != 0 {
		t.Fatalf("deletion outbox after cleanup = %v, %v", pending, err)
	}
}

func TestPDFArtifactStorage_InFlightForkSurvivesOutboxPass(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	const source, fork = session.SessionID("fork-in-flight-source"), session.SessionID("fork-in-flight-target")
	if err := metadata.Save(t.Context(), session.New(source, session.ModeAccept, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	storage := New(metadata, objects)
	meta, err := storage.Stage(t.Context(), source, "report.pdf", bytes.NewReader([]byte("%PDF-1.7\nprivate-fork-copy\n%%EOF")))
	if err != nil {
		t.Fatal(err)
	}
	part, err := session.NewPDFContent(meta.ID, meta.Name, meta.Size, meta.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	held := &heldObjects{memoryObjects: *objects, started: make(chan struct{}), release: make(chan struct{})}
	copying := New(metadata, held)
	copying.LeaseActive = func(context.Context, session.SessionID) (bool, error) { return false, nil }
	type copyResult struct {
		messages []session.Message
		err      error
	}
	done := make(chan copyResult, 1)
	go func() {
		messages, err := copying.CopyFork(context.Background(), source, fork, []session.Message{session.NewUserMessageWithParts("read", []session.Content{part})})
		done <- copyResult{messages: messages, err: err}
	}()
	select {
	case <-held.started:
	case <-time.After(5 * time.Second):
		t.Fatal("fork object write did not start")
	}
	if _, err := mr.ZAdd("mecatl:pdf-artifacts:delete-outbox", float64(time.Now().Add(-10*time.Minute).Unix()), string(fork)); err != nil {
		t.Fatal(err)
	}
	if err := copying.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 1 {
		t.Fatal("reconciliation touched an in-flight fork copy")
	}
	close(held.release)
	select {
	case copied := <-done:
		if copied.err != nil || len(copied.messages) != 1 || copied.messages[0].Parts[0].ArtifactID == meta.ID {
			t.Fatalf("in-flight fork failed to finish: %+v", copied)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fork copy did not settle")
	}
	if len(objects.data) != 2 {
		t.Fatal("settled fork copy did not publish a private object")
	}
	mr.FastForward(36 * time.Minute)
	restartedMetadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedMetadata.Close() })
	restarted := New(restartedMetadata, objects)
	restarted.LeaseActive = copying.LeaseActive
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(objects.data) != 1 {
		t.Fatalf("unpublished fork left private objects after restart: %d", len(objects.data))
	}
}

func TestPDFArtifactStorage_PublishedForkSnapshotProtectsReadyCopy(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mr.Close)
	metadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	const source, fork = session.SessionID("published-fork-source"), session.SessionID("published-fork-target")
	ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/work", Revision: "in-tree-v1"}
	if err := metadata.Save(t.Context(), session.New(source, session.ModeAccept, ref, session.Limits{}, time.Now())); err != nil {
		t.Fatal(err)
	}
	objects := &memoryObjects{data: make(map[string][]byte)}
	storage := New(metadata, objects)
	want := []byte("%PDF-1.7\npublished-fork-private\n%%EOF")
	meta, err := storage.Stage(t.Context(), source, "report.pdf", bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	part, err := session.NewPDFContent(meta.ID, meta.Name, meta.Size, meta.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	copied, err := storage.CopyFork(t.Context(), source, fork, []session.Message{session.NewUserMessageWithParts("read", []session.Content{part})})
	if err != nil {
		t.Fatal(err)
	}
	forkSession := session.New(fork, session.ModeAccept, ref, session.Limits{}, time.Now())
	if err := forkSession.SeedHistory(copied); err != nil {
		t.Fatal(err)
	}
	if err := metadata.Save(t.Context(), forkSession); err != nil {
		t.Fatal(err)
	}
	// Omit CommitPrompt to emulate a Redis marker-update failure after the
	// authoritative successor snapshot was saved.
	if _, err := mr.ZAdd("mecatl:pdf-artifacts:delete-outbox", float64(time.Now().Add(-10*time.Minute).Unix()), string(fork)); err != nil {
		t.Fatal(err)
	}
	mr.FastForward(36 * time.Minute)
	restartedMetadata, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true, PDFArtifactsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restartedMetadata.Close() })
	restarted := New(restartedMetadata, objects)
	restarted.LeaseActive = func(context.Context, session.SessionID) (bool, error) { return false, nil }
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	privateID := copied[0].Parts[0].ArtifactID
	_, reader, err := restarted.Open(t.Context(), fork, privateID)
	if err != nil {
		t.Fatalf("published fork lost private PDF: %v", err)
	}
	got, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || !bytes.Equal(got, want) {
		t.Fatalf("published fork PDF changed: size=%d err=%v", len(got), readErr)
	}
	if _, pending, err := restartedMetadata.PDFPendingForkSource(t.Context(), fork); err != nil || pending {
		t.Fatalf("published fork still has cleanup intent: pending=%t err=%v", pending, err)
	}
	restarted.now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if err := restarted.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	record, ok, err := restartedMetadata.PDFRecordForSession(t.Context(), fork, privateID)
	if err != nil || !ok || record.State != redisstore.PDFCommitted {
		t.Fatalf("published fork marker was not repaired: record=%+v ok=%t err=%v", record, ok, err)
	}
}
