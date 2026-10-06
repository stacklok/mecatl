package microvm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

func TestRepositoryAttachmentMetadataStaysOutsideGuestExport(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attach(t)
	defer attachment.Close()
	environmentID, generation, err := parseEnvironmentRef(attachment.Logical.Ref)
	if err != nil {
		t.Fatal(err)
	}
	binding := control.Binding{Owner: "operator", SessionID: "session", EnvironmentID: environmentID, Ref: attachment.Logical.Ref.ID, Generation: generation}
	if err := fixture.composition.Attachments.register(binding, attachment); err != nil {
		t.Fatal(err)
	}
	logicalRoot := filepath.Dir(attachment.Logical.WorktreePath)
	if _, err := os.Lstat(filepath.Join(logicalRoot, "attachment.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("guest export contains attachment metadata: %v", err)
	}
	metadata := filepath.Join(filepath.Dir(filepath.Dir(logicalRoot)), "attachments", filepath.Base(logicalRoot)+".json")
	if info, err := os.Lstat(metadata); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("host-only attachment metadata = %v, %v", info, err)
	}
	restored, err := newRepositoryAttachmentManager(fixture.composition.Logical)
	if err != nil {
		t.Fatalf("restore host-only attachment inventory: %v", err)
	}
	if inventory := restored.inventory("operator"); len(inventory) != 1 || inventory[0].binding != binding {
		t.Fatalf("restored attachment inventory = %+v", inventory)
	}
	launch := fixture.backend.launch
	for _, mount := range launch.Mounts {
		rel, relErr := filepath.Rel(mount.HostPath, metadata)
		if relErr == nil && rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("attachment metadata %q is reachable through guest mount %q", metadata, mount.HostPath)
		}
	}
}

func TestRepositoryAttachmentStoreIgnoresInterruptedStagingPublication(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	defer attachment.Close()
	logicalRoot := filepath.Dir(attachment.Logical.WorktreePath)
	attachments := filepath.Join(filepath.Dir(filepath.Dir(logicalRoot)), "attachments")
	staging := filepath.Join(attachments, ".staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "interrupted"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := newRepositoryAttachmentManager(fixture.composition.Logical)
	if err != nil {
		t.Fatalf("restore around interrupted publish: %v", err)
	}
	if inventory := restored.inventory("operator"); len(inventory) != 1 || inventory[0].binding.Ref != attachment.Logical.Ref.ID {
		t.Fatalf("restored inventory = %+v", inventory)
	}
}

func TestRepositoryAttachmentStoreInterruptedDeleteDoesNotBlockUnrelatedRecord(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	first := fixture.attachPersisted(t)
	defer first.Close()
	second := fixture.attachPersisted(t)
	defer second.Close()
	firstRoot := filepath.Dir(first.Logical.WorktreePath)
	firstRecord := filepath.Join(filepath.Dir(filepath.Dir(firstRoot)), "attachments", filepath.Base(firstRoot)+".json")
	if err := os.Remove(firstRecord); err != nil {
		t.Fatal(err)
	}
	restored, err := newRepositoryAttachmentManager(fixture.composition.Logical)
	if err != nil {
		t.Fatalf("restore around interrupted delete: %v", err)
	}
	inventory := restored.inventory("operator")
	if len(inventory) != 1 || inventory[0].binding.Ref != second.Logical.Ref.ID {
		t.Fatalf("unrelated restored inventory = %+v", inventory)
	}
}

func TestRepositoryAttachmentStoreRejectsCorruptCommittedRecord(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attachPersisted(t)
	defer attachment.Close()
	logicalRoot := filepath.Dir(attachment.Logical.WorktreePath)
	record := filepath.Join(filepath.Dir(filepath.Dir(logicalRoot)), "attachments", filepath.Base(logicalRoot)+".json")
	if err := os.WriteFile(record, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newRepositoryAttachmentManager(fixture.composition.Logical); err == nil || !strings.Contains(err.Error(), "decode repository attachment metadata") {
		t.Fatalf("corrupt committed record = %v", err)
	}
	if got, err := os.ReadFile(record); err != nil || string(got) != "{corrupt" {
		t.Fatalf("corrupt committed record was not preserved: %q, %v", got, err)
	}
}

func TestRepositoryAttachmentStoreRejectsPriorDirectoryLayout(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attach(t)
	defer attachment.Close()
	logicalRoot := filepath.Dir(attachment.Logical.WorktreePath)
	legacy := filepath.Join(filepath.Dir(filepath.Dir(logicalRoot)), "attachments", filepath.Base(logicalRoot))
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "attachment.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newRepositoryAttachmentManager(fixture.composition.Logical); err == nil || !strings.Contains(err.Error(), "unsupported repository attachment directory layout") {
		t.Fatalf("prior directory layout = %v", err)
	}
	if _, err := os.Stat(filepath.Join(legacy, "attachment.json")); err != nil {
		t.Fatalf("prior metadata was not preserved: %v", err)
	}
}

func TestRepositoryAttachmentStoreRejectsLegacyExportedMetadata(t *testing.T) {
	fixture := newRepositoryAttachmentFixture(t)
	attachment := fixture.attach(t)
	defer attachment.Close()
	legacy := filepath.Join(filepath.Dir(attachment.Logical.WorktreePath), "attachment.json")
	if err := os.WriteFile(legacy, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newRepositoryAttachmentManager(fixture.composition.Logical); err == nil || err.Error() != "unsupported repository attachment metadata in guest-exported logical namespace" {
		t.Fatalf("legacy layout load = %v", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy metadata was not preserved: %v", err)
	}
}
