package jsonlstore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/session"
)

func TestSessionStorageContinuity_Scenario1_BoundedRepeatedSaves(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	s := driven(t)

	if err := st.Save(ctx, s); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	path := canonicalSnapshotPath(dir, s.ID)
	initial, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat current v2 snapshot: %v", err)
	}

	// A second family makes this assertion deliberately target-scoped: it must
	// neither count nor disturb artifacts belonging to other sessions.
	if err := st.Save(ctx, session.New("other-session", session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/other", Revision: "in-tree-v1"}, session.Limits{}, time.Now())); err != nil {
		t.Fatalf("Save other session: %v", err)
	}
	for range 999 {
		if err := st.Save(ctx, s); err != nil {
			t.Fatalf("repeated Save: %v", err)
		}
	}
	final, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat current v2 snapshot after saves: %v", err)
	}
	if final.Size() > initial.Size()+4096 {
		t.Fatalf("snapshot grew with save count: initial=%d final=%d", initial.Size(), final.Size())
	}
	if err := assertBoundedSnapshotArtifacts(dir, s.ID); err != nil {
		t.Fatal(err)
	}

	// Mutation proof: a snapshot leak in the old root namespace must trip the
	// same target-family assertion rather than being hidden by the sid-v1 scan.
	leakedLegacySnapshot := legacyFamilyPath(dir, s.ID, ".session.jsonl")
	if err := os.WriteFile(leakedLegacySnapshot, []byte("leaked\n"), 0o600); err != nil {
		t.Fatalf("plant legacy snapshot leak: %v", err)
	}
	if err := assertBoundedSnapshotArtifacts(dir, s.ID); err == nil {
		t.Fatal("legacy snapshot leak did not violate the bounded artifact contract")
	}
}

type snapshotArtifactCounts struct {
	canonicalV2 int
	canonicalV1 int
	legacyV2    int
	legacyV1    int
	temp        int
	lock        int
	sidecar     int
}

func assertBoundedSnapshotArtifacts(dir string, id session.SessionID) error {
	canonicalV2 := filepath.Base(canonicalSnapshotPath(dir, id))
	canonicalStem := strings.TrimSuffix(canonicalV2, ".session.json")
	legacyStem := strings.TrimSuffix(filepath.Base(legacyFamilyPath(dir, id, ".session.jsonl")), ".session.jsonl")
	counts := snapshotArtifactCounts{}

	for _, namespace := range []struct {
		path      string
		stem      string
		canonical bool
	}{
		{path: dir, stem: legacyStem},
		{path: filepath.Join(dir, "sid-v1"), stem: canonicalStem, canonical: true},
	} {
		entries, err := os.ReadDir(namespace.path)
		if err != nil {
			return fmt.Errorf("read %s snapshot namespace: %w", namespace.path, err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			switch {
			case name == namespace.stem+".session.json":
				if namespace.canonical {
					counts.canonicalV2++ // The v2 envelope carries bounded session metadata.
				} else {
					counts.legacyV2++
				}
			case name == namespace.stem+".session.jsonl":
				if namespace.canonical {
					counts.canonicalV1++
				} else {
					counts.legacyV1++
				}
			case strings.HasPrefix(name, namespace.stem+".session.json.tmp-v1-"):
				counts.temp++
			case name == namespace.stem+".family.lock":
				counts.lock++
			case name == namespace.stem+".tools.jsonl" || name == namespace.stem+".events.jsonl":
				counts.sidecar++
			}
		}
	}

	if counts.canonicalV2 != 1 || counts.canonicalV1 != 0 || counts.legacyV2 != 0 || counts.legacyV1 != 0 ||
		counts.temp > 1 || counts.lock > 1 || counts.sidecar != 0 {
		return fmt.Errorf("target session %q artifacts = %+v, want one canonical v2 snapshot (with metadata), no v1/legacy/sidecars, and at most one temp and lock", id, counts)
	}
	return nil
}

func legacyFamilyPath(dir string, id session.SessionID, suffix string) string {
	if id == "" {
		return filepath.Join(dir, "_empty_"+suffix)
	}
	var b strings.Builder
	for _, r := range string(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	stem := b.String()
	if strings.HasPrefix(stem, ".") {
		stem = "_" + stem[1:]
	}
	return filepath.Join(dir, stem+suffix)
}

func TestSessionStorageContinuity_Scenario1_V1PromotionFidelity(t *testing.T) {
	ctx := context.Background()
	st, dir := newStore(t)
	id := session.SessionID("parallel-fidelity")
	want, err := session.NewParallelBranch(id, session.ModePlan, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/workspace", Revision: "in-tree-v1"}, session.Limits{
		MaxTurns: 9, MaxToolCalls: 13, MaxConsecutiveFailures: 3,
	}, time.Unix(1700000000, 123).UTC(), "parent-1", session.NewIncarnationID(), "call-1", 2)
	if err != nil {
		t.Fatalf("NewParallelBranch: %v", err)
	}
	want.Profile = "profile-x"
	want.ProviderID = "provider-x"
	want.ModelID = "model-x"
	want.ReasoningEffort = "high"
	want.EnvironmentRef = session.EnvironmentRef{Kind: session.EnvironmentKind("remote"), ID: "worker-7", Revision: "inventory-v1"}
	want.SetTitle("durable title")
	owner := &session.Principal{Issuer: "https://issuer.example", Subject: "subject-7", Name: "Owner"}
	if err := want.RestoreLabels(owner, session.Authority{}); err != nil {
		t.Fatalf("RestoreLabels: %v", err)
	}
	if err := want.BeginTurn(); err != nil {
		t.Fatalf("BeginTurn: %v", err)
	}
	if err := want.RecordUserPrompt("preserve everything", nil); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	if err := want.RecordAssistant(session.NewAssistantMessage("working", "reasoning", nil)); err != nil {
		t.Fatalf("RecordAssistant: %v", err)
	}
	if err := want.RecordUsage(session.Usage{InputTokens: 101, OutputTokens: 23, CacheReadTokens: 17, ReasoningTokens: 5}); err != nil {
		t.Fatalf("RecordUsage: %v", err)
	}

	v1, err := sessnap.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal v1: %v", err)
	}
	v1Path := canonicalFamilyPath(dir, id, ".session.jsonl")
	if err := os.WriteFile(v1Path, append(v1, '\n'), 0o600); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	logicalModified := time.Unix(1700000999, 0).UTC()
	if err := os.Chtimes(v1Path, logicalModified, logicalModified); err != nil {
		t.Fatalf("Chtimes v1: %v", err)
	}
	toolBytes := []byte("tool-one\ntool-two\n")
	eventBytes := []byte("event-one\nevent-two\n")
	toolPath := canonicalFamilyPath(dir, id, ".tools.jsonl")
	eventPath := canonicalFamilyPath(dir, id, ".events.jsonl")
	if err := os.WriteFile(toolPath, toolBytes, 0o600); err != nil {
		t.Fatalf("write tools: %v", err)
	}
	if err := os.WriteFile(eventPath, eventBytes, 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}

	loadedV1, err := st.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load v1: %v", err)
	}
	if err := st.Save(ctx, loadedV1); err != nil {
		t.Fatalf("promoting Save: %v", err)
	}
	loadedV2, err := st.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load promoted v2: %v", err)
	}
	before, _ := sessnap.Marshal(loadedV1)
	after, _ := sessnap.Marshal(loadedV2)
	if !bytes.Equal(after, before) {
		t.Fatalf("promoted aggregate changed:\nbefore %s\nafter  %s", before, after)
	}
	if loadedV2.Kind != session.SessionKindParallelBranch || loadedV2.Relationship.ParentSessionID != "parent-1" || loadedV2.Owner == nil || loadedV2.Owner.Subject != "subject-7" {
		t.Fatalf("durable metadata changed: kind=%q relationship=%+v owner=%+v", loadedV2.Kind, loadedV2.Relationship, loadedV2.Owner)
	}
	listed, err := st.List(ctx)
	if err != nil || len(listed) != 1 || !listed[0].ModifiedAt.Equal(logicalModified) {
		t.Fatalf("List after promotion = %+v, %v; want logical modified time %v", listed, err, logicalModified)
	}
	for path, wantBytes := range map[string][]byte{toolPath: toolBytes, eventPath: eventBytes} {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, wantBytes) {
			t.Fatalf("sidecar %s changed: %q, %v", path, got, readErr)
		}
	}

	// Once committed and verified, v2 remains authoritative even if a stale v1
	// record coexists and is newer on disk.
	stale := session.New(id, session.ModeDefault, session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/stale", Revision: "in-tree-v1"}, session.Limits{}, time.Unix(1, 0))
	stale.SetTitle("stale-v1")
	staleLine, _ := sessnap.Marshal(stale)
	if err := os.WriteFile(v1Path, append(staleLine, '\n'), 0o600); err != nil {
		t.Fatalf("replace coexisting v1: %v", err)
	}
	got, err := st.Load(ctx, id)
	if err != nil || got.Title != want.Title {
		t.Fatalf("v2 not authoritative over coexisting v1: title=%q err=%v", got.Title, err)
	}

	// A pre-kind v1 record must remain fail-closed unknown after promotion.
	unknownID := session.SessionID("legacy-unknown")
	unknownJSON := []byte(`{"id":"legacy-unknown","state":"idle","mode":"default","limits":{},"counters":{},"environment_ref":{"Kind":"local","ID":"/legacy","Revision":"in-tree-v1"},"created_at":"2023-11-14T22:13:20Z","messages":[]}`)
	if !json.Valid(unknownJSON) {
		t.Fatal("invalid unknown-kind fixture")
	}
	unknownV1Path := canonicalFamilyPath(dir, unknownID, ".session.jsonl")
	if err := os.WriteFile(unknownV1Path, append(unknownJSON, '\n'), 0o600); err != nil {
		t.Fatalf("write unknown v1: %v", err)
	}
	unknown, err := st.Load(ctx, unknownID)
	if err != nil {
		t.Fatalf("Load unknown v1: %v", err)
	}
	if err := st.Save(ctx, unknown); err != nil {
		t.Fatalf("promote unknown v1: %v", err)
	}
	unknown, err = st.Load(ctx, unknownID)
	if err != nil || unknown.Kind != session.SessionKindUnknown || unknown.Relationship != (session.SessionRelationship{}) {
		t.Fatalf("unknown metadata promoted unsafely: kind=%q relationship=%+v err=%v", unknown.Kind, unknown.Relationship, err)
	}
}
