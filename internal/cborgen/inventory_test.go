package cborgen

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// copiedRepository exercises the shipped Taskfile, inventory, CLI and real runtime
// in independent module trees. No root dependency on the unpublished engine is used.
func copiedRepository(t *testing.T) string {
	t.Helper()
	isolateGoEnvironment(t)
	root := t.TempDir()
	source, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	copyFile := func(path string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(source, path))
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(root, path), string(data))
	}
	for _, path := range []string{
		"go.mod", "go.sum", "engine/go.mod", "engine/go.sum",
	} {
		copyFile(path)
	}
	copyTaskfiles(t, source, root)
	for _, dir := range []string{"internal/cborgen", "engine/id", "engine/kernel/codec", "engine/kernel/contentid", "engine/kernel/history/id", "engine/kernel/history/format"} {
		err := filepath.WalkDir(filepath.Join(source, dir), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") && entry.Name() != "zz_cbor_generated.go" {
				rel, err := filepath.Rel(source, path)
				if err != nil {
					return err
				}
				copyFile(rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// copyTaskfiles follows the source tree rather than duplicating the root include
// list, which can grow independently when CI merges the branch with main.
func copyTaskfiles(t *testing.T, source, destination string) {
	t.Helper()
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".scratch", ".tools", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "Taskfile.yml" && entry.Name() != "Taskfile.yaml" {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		writeFixture(t, filepath.Join(destination, rel), string(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCopiedTaskfilesIncludeNewTargets(t *testing.T) {
	task, err := exec.LookPath("task")
	if err != nil {
		t.Skip("task executable required for Taskfile integration")
	}
	source, destination := t.TempDir(), t.TempDir()
	writeFixture(t, filepath.Join(source, "Taskfile.yml"), `version: '3'
includes:
  idbroker: deploy/mecak8s-idbroker/Taskfile.yml
tasks:
  probe:
    cmds:
      - task: idbroker:probe
`)
	writeFixture(t, filepath.Join(source, "deploy/mecak8s-idbroker/Taskfile.yml"), `version: '3'
includes:
  nested: nested/Taskfile.yml
tasks:
  probe:
    cmds:
      - task: nested:probe
`)
	writeFixture(t, filepath.Join(source, "deploy/mecak8s-idbroker/nested/Taskfile.yml"), `version: '3'
tasks:
  probe:
    cmds:
      - echo copied-includes-executed
`)
	copyTaskfiles(t, source, destination)
	cmd := exec.Command(task, "--dir", destination, "probe")
	cmd.Dir = t.TempDir()
	if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "copied-includes-executed") {
		t.Fatalf("execute copied Taskfile includes: %s %v", out, err)
	}
}

type fileState struct {
	data    string
	mode    fs.FileMode
	modTime time.Time
}

func repositoryState(t *testing.T, root string) map[string]fileState {
	t.Helper()
	state := map[string]fileState{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		state[path] = fileState{string(data), info.Mode(), info.ModTime()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestRepositoryDirectiveValidation(t *testing.T) {
	const invocation = "//go:generate go -C .. run ./internal/cborgen/cmd/cborgen "
	for _, tc := range []struct{ name, directive, reason string }{
		{"variable", invocation + "-dir owner -types Record -out $GOFILE.go", "literal unquoted"},
		{"quoted roots", invocation + "-dir owner -types \"Record\" -out generated.go", "literal unquoted"},
		{"unknown option", invocation + "-directory owner", "flag provided but not defined"},
		{"extra argument", invocation + "-dir owner -types Record -out generated.go extra", "require declaring-package"},
		{"absolute directory", invocation + "-dir /owner -types Record -out generated.go", "require declaring-package"},
		{"escaping directory", invocation + "-dir ../owner -types Record -out generated.go", "require declaring-package"},
		{"escaping output", invocation + "-dir owner -types Record -out ../generated.go", "package-local"},
		{"empty output", invocation + "-dir owner -types Record", "package-local"},
		{"wrong working directory", strings.Replace(invocation, "-C ..", "-C .", 1), "expected go -C"},
		{"duplicate output", strings.Repeat(invocation+"-dir owner -types Record -out generated.go\n", 2), "one codec output per package"},
		{"multiple outputs", invocation + "-dir owner -types Record -out generated.go\n" + invocation + "-dir owner -types Other -out other.go\n", "one codec output per package"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, filepath.Join(root, "owner/owner.go"), tc.directive+"\npackage owner\n")
			before := repositoryState(t, root)
			for _, check := range []bool{false, true} {
				err := GenerateRepository(context.Background(), root, check)
				if err == nil || !strings.Contains(err.Error(), tc.reason) {
					t.Fatalf("check=%v: %v; want %s", check, err, tc.reason)
				}
				if !reflect.DeepEqual(before, repositoryState(t, root)) {
					t.Fatal("invalid directive modified repository")
				}
			}
		})
	}
}

func TestTaskGenerationAndFreshness(t *testing.T) {
	task, err := exec.LookPath("task")
	if err != nil {
		t.Skip("task executable required for Taskfile integration")
	}
	root := copiedRepository(t)
	owner := filepath.Join(root, "engine/kernel/codec/internal/fixture")
	output := filepath.Join(owner, "zz_cbor_generated.go")
	inventory := filepath.Join(owner, "doc.go")
	initialInventory, err := os.ReadFile(inventory)
	if err != nil {
		t.Fatal(err)
	}
	// Wrappers exercise all public generated APIs in the regeneration overlay.
	writeFixture(t, filepath.Join(owner, "wrapper.go"), `package fixture
import "github.com/stacklok/mecatl/engine/kernel/codec"
func roundtrip(v Sample, l codec.Limits) (Sample,error) {
 if err:=codec.Check(v,l);err!=nil{return Sample{},err}
 b,err:=codec.Encode(v.Clone(),l);if err!=nil{return Sample{},err}
 w,err:=codec.Decode[Sample](b,l);if err==nil&&!v.Equal(w){panic("unequal")};return w,err
}
`)
	trap := filepath.Join(owner, "init_trap.go")
	writeFixture(t, trap, "package fixture\nfunc init(){panic(\"generator executed owner initialization\")}\n")
	sibling := filepath.Join(owner, "authored.go")
	writeFixture(t, sibling, "package fixture\n// "+GeneratedMarker+"\nconst Authored = true\n")
	run := func(t *testing.T, target, want string, nonmutating bool) {
		t.Helper()
		before := repositoryState(t, root)
		cmd := exec.Command(task, "--dir", root, target)
		cmd.Dir = t.TempDir()
		cmd.Env = os.Environ()
		out, err := cmd.CombinedOutput()
		if want == "" {
			if err != nil {
				t.Fatalf("%s: %s %v", target, out, err)
			}
		} else if err == nil || !strings.Contains(string(out), want) {
			t.Fatalf("%s: got %s, %v; want %s", target, out, err, want)
		}
		if nonmutating && !reflect.DeepEqual(before, repositoryState(t, root)) {
			t.Fatalf("%s modified copied repository", target)
		}
	}
	run(t, "generate:codecs", "", false)
	first, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	run(t, "generate:codecs", "", false)
	second, err := os.ReadFile(output)
	if err != nil || string(first) != string(second) {
		t.Fatal("task regeneration is not deterministic", err)
	}
	run(t, "generate:codecs:check", "", true)

	// Bootstrap the complete shipped owner graph, not just a synthetic family or
	// one leaf artifact. Every declared output is absent or corrupt together.
	targets, err := discoverTargets(root)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := make(map[string]string, len(targets))
	for _, target := range targets {
		path := filepath.Join(target.Dir, target.Output)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		artifacts[path] = string(data)
	}
	for _, corrupt := range []bool{false, true} {
		for path := range artifacts {
			if corrupt {
				writeFixture(t, path, GeneratedMarker+"\nnot valid Go")
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}
		want := "missing codec output"
		if corrupt {
			want = "stale codec output"
		}
		run(t, "generate:codecs:check", want, true)
		run(t, "generate:codecs", "", false)
		for path, want := range artifacts {
			got, err := os.ReadFile(path)
			if err != nil || string(got) != want {
				t.Fatalf("whole-owner bootstrap changed %s: %v", path, err)
			}
		}
	}

	for _, tc := range []struct{ name, content string }{
		{"modified", string(first) + "\n// drift\n"},
		{"broken", GeneratedMarker + "\nnot Go syntax"},
		{"stale", GeneratedMarker + "\npackage fixture\nvar Old Undefined\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, output, tc.content)
			run(t, "generate:codecs:check", "stale codec output", true)
			run(t, "generate:codecs", "", false)
			got, err := os.ReadFile(output)
			if err != nil || string(got) != string(first) {
				t.Fatal("task failed to recover owned output", err)
			}
		})
	}
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	run(t, "generate:codecs:check", "missing codec output", true)
	run(t, "generate:codecs", "", false)

	for _, path := range []string{filepath.Join(owner, "extra.go"), filepath.Join(root, "engine/removed/obsolete.go")} {
		writeFixture(t, path, GeneratedMarker+"\npackage obsolete\n")
		run(t, "generate:codecs:check", "extra codec output", true)
		if strings.Contains(path, "removed") {
			// Orphan enforcement belongs to check, never local generation.
			run(t, "generate:codecs", "", false)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	// Removing a root changes bytes; removing the whole target changes inventory.
	writeFixture(t, inventory, strings.Replace(string(initialInventory), "Catalog,Sample", "Sample", 1))
	run(t, "generate:codecs:check", "stale codec output", true)
	writeFixture(t, inventory, "package fixture\n")
	run(t, "generate:codecs:check", "extra codec output", true)
	writeFixture(t, inventory, string(initialInventory))

	bad := filepath.Join(owner, "bad.go")
	writeFixture(t, bad, "package fixture\nvar Handwritten UndefinedHandwritten\n")
	run(t, "generate:codecs", "UndefinedHandwritten", true)
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, bad, "package fixture\ntype Unsupported struct { V int `cbor:\"v\"` }\n")
	writeFixture(t, inventory, strings.Replace(string(initialInventory), "Catalog,Sample", "Catalog,Sample,Unsupported", 1))
	run(t, "generate:codecs", "fixed-width", true)
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, inventory, string(initialInventory))
	for _, declaration := range []string{
		"func (*Sample) Clone() Sample { return Sample{} }",
		"func (Sample) AppendCBOR() {}",
	} {
		writeFixture(t, bad, "package fixture\n"+declaration+"\n")
		run(t, "generate:codecs", "collides with generated", true)
	}
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	run(t, "generate:codecs:check", "", true)

	// The literal directive must work with Go itself, not just discovery. Keep
	// artifact writes behind the copied Taskfile pathway.
	taskfile := filepath.Join(root, "Taskfile.yml")
	originalTaskfile, err := os.ReadFile(taskfile)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, taskfile, strings.Replace(string(originalTaskfile),
		"GOWORK=off go run ./internal/cborgen/cmd/cborgen -repo .\n",
		"cd engine && go generate ./kernel/codec/internal/fixture\n", 1))
	if err := os.Remove(output); err != nil {
		t.Fatal(err)
	}
	run(t, "generate:codecs", "", false)
	writeFixture(t, taskfile, string(originalTaskfile))
	run(t, "generate:codecs:check", "", true)

	if err := os.Remove(trap); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(owner, "generated_test.go"), `package fixture
import("testing";"github.com/stacklok/mecatl/engine/kernel/codec";"github.com/stacklok/mecatl/engine/kernel/contentid")
func TestGeneratedTaskOutput(t *testing.T){
 l:=codec.Limits{MaxBytes:10000,MaxStringBytes:100,MaxByteStringBytes:100,MaxArrayElements:100,MaxMapPairs:100,MaxDepth:20,MaxNodes:1000,MaxCollectionElements:1000}
 if _,err:=roundtrip(Sample{Label:"task"},l);err!=nil{t.Fatal(err)}
 c:=contentid.Sum([]byte("task"));v:=Link{Value:c}
 b,err:=codec.Encode(v.Clone(),l);if err!=nil||codec.Check(v,l)!=nil{t.Fatal("CID encode/check",err)}
 w,err:=codec.Decode[Link](b,l);if err!=nil||!v.Equal(w){t.Fatal("CID decode/equal",err)}
 if !Authored{t.Fatal("authored sibling lost")}
}
`)
	cmd := exec.Command("go", "test", "-mod=readonly", "./kernel/codec/internal/fixture")
	cmd.Dir = filepath.Join(root, "engine")
	cmd.Env = loadEnvironment()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("execute task output: %s %v", out, err)
	}
}
