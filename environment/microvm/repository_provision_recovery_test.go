package microvm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recoverableFakeRepositoryRuntime struct{ *fakeRepositoryVMRuntime }

func (*recoverableFakeRepositoryRuntime) Reconcile(context.Context, RepositoryVMRecord) error {
	return nil
}

func TestRepositoryRegistryRejectsUnsupportedSchemaWithoutMutation(t *testing.T) {
	for _, version := range []int{-1, 0, 2, 3} {
		t.Run(fmt.Sprintf("version-%d", version), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "registry.json")
			original := []byte(fmt.Sprintf(`{"version":%d,"record":{}}`+"\n", version))
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			directory := &repositoryDirectory{file: file, path: root}
			_, err = readRepositoryRecord(directory)
			if closeErr := directory.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err == nil || !strings.Contains(err.Error(), "data was preserved") || !strings.Contains(err.Error(), "matching build") || !strings.Contains(err.Error(), "incompatible local-development state") {
				t.Fatalf("unsupported schema error = %v", err)
			}
			got, readErr := os.ReadFile(path)
			if readErr != nil || string(got) != string(original) {
				t.Fatalf("unsupported schema was mutated: %q, %v", got, readErr)
			}
		})
	}
}

func TestRepositoryRegistryWritesAndReadsInitialSchema(t *testing.T) {
	root := t.TempDir()
	file, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := &repositoryDirectory{file: file, path: root}
	defer directory.Close()

	record := RepositoryVMRecord{Boot: RepositoryBootRecord{Generation: 1, VMID: "boot-one", Endpoint: "endpoint-one", AuthorityDigest: "authority-one"}}
	record.syncBootFields()
	if err := writeRepositoryRecord(directory, record); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join(root, "registry.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":1,`) {
		t.Fatalf("registry marker = %s, want initial schema version 1", raw)
	}
	got, err := readRepositoryRecord(directory)
	if err != nil {
		t.Fatal(err)
	}
	if got != record {
		t.Fatalf("round trip = %+v, want %+v", got, record)
	}
}

func TestRepositoryBootRecordRejectsStaleMirrorOverwrite(t *testing.T) {
	root := t.TempDir()
	file, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	directory := &repositoryDirectory{file: file, path: root}
	defer directory.Close()
	record := RepositoryVMRecord{Boot: RepositoryBootRecord{Generation: 1, VMID: "boot-one", Endpoint: "endpoint-one", AuthorityDigest: "authority-one"}}
	record.syncBootFields()
	if err := writeRepositoryRecord(directory, record); err != nil {
		t.Fatal(err)
	}
	record.Boot.VMID = "boot-two"
	if err := writeRepositoryRecord(directory, record); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale mirror write = %v", err)
	}
	persisted, err := readRepositoryRecord(directory)
	if err != nil || persisted.Boot.VMID != "boot-one" {
		t.Fatalf("persisted boot = %+v, %v", persisted.Boot, err)
	}
}

func TestRepositoryFirstProvisionRootFSPublicationRecovers(t *testing.T) {
	for _, tc := range []struct {
		name          string
		prepare       func(*testing.T, *repositoryDirectory, *RepositoryVMRecord)
		preservePath  string
		preserveValue string
	}{
		{
			name: "rootfs absent after durable admission",
			prepare: func(t *testing.T, _ *repositoryDirectory, record *RepositoryVMRecord) {
				t.Helper()
				if err := os.RemoveAll(record.RootFSPath); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "partial staged rootfs",
			prepare: func(t *testing.T, directory *repositoryDirectory, record *RepositoryVMRecord) {
				t.Helper()
				if err := os.RemoveAll(record.RootFSPath); err != nil {
					t.Fatal(err)
				}
				stage := filepath.Join(directory.path, record.RootFSStagingName)
				if err := os.Mkdir(stage, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(stage, "partial"), []byte("incomplete"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:          "rootfs published before phase commit",
			preservePath:  "home/guest/publication-marker",
			preserveValue: "preserve-me",
			prepare: func(t *testing.T, _ *repositoryDirectory, record *RepositoryVMRecord) {
				t.Helper()
				path := filepath.Join(record.RootFSPath, "home", "guest", "publication-marker")
				if err := os.WriteFile(path, []byte("preserve-me"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			repository, _, _ := repositoryIdentityFixture(t, root, "repository")
			runtime := &recoverableFakeRepositoryRuntime{newFakeRepositoryVMRuntime()}
			registry, err := OpenRepositoryVMRegistry(filepath.Join(root, "state"), runtime)
			if err != nil {
				t.Fatal(err)
			}
			first, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository, Artifacts: testArtifactSnapshot(repositoryVerifiedArtifacts(t, root))})
			if err != nil {
				t.Fatal(err)
			}
			identity, err := ResolveRepositoryIdentity(t.Context(), "operator", repository, registry.stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			validated, err := registry.validateIdentity(identity)
			if err != nil {
				t.Fatal(err)
			}
			directory, err := registry.openIdentityDirectory(validated, false)
			if err != nil {
				t.Fatal(err)
			}
			record := first.Record
			record.State = EnvironmentProvisioning
			record.RootFSPhase = repositoryRootFSStaging
			record.RootFSStagingName, err = newRepositoryRootFSStagingName()
			if err != nil {
				t.Fatal(err)
			}
			tc.prepare(t, directory, &record)
			if err := writeRepositoryRecord(directory, record); err != nil {
				t.Fatal(err)
			}
			if err := directory.Close(); err != nil {
				t.Fatal(err)
			}
			runtime.mu.Lock()
			delete(runtime.statuses, record.VMID)
			delete(runtime.authorities, record.VMID)
			runtime.mu.Unlock()

			recovered, err := registry.Ensure(t.Context(), RepositoryVMRequest{Owner: "operator", Checkout: repository})
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Record.State != EnvironmentReady || recovered.Record.RootFSPhase != repositoryRootFSPublished || recovered.Record.RootFSStagingName != "" {
				t.Fatalf("recovered record = %+v", recovered.Record)
			}
			if tc.preservePath != "" {
				got, err := os.ReadFile(filepath.Join(recovered.Record.RootFSPath, tc.preservePath))
				if err != nil || string(got) != tc.preserveValue {
					t.Fatalf("published rootfs marker = %q, %v", got, err)
				}
			}
		})
	}
}
