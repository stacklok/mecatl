package productmetrics

import (
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"

	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
)

func TestLoadOrCreateInstallIDCreatesOnFirstRun(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/tester", nil },
	}
	written := map[string][]byte{}
	readFile := func(path string) ([]byte, error) {
		data, ok := written[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return data, nil
	}
	writeFile := func(path string, data []byte, _ os.FileMode) error {
		written[path] = data
		return nil
	}
	mkdirAll := func(string, os.FileMode) error { return nil }

	id, firstRun, err := LoadOrCreateInstallID(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID: %v", err)
	}
	if !firstRun {
		t.Error("firstRun = false on an empty store, want true")
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Errorf("id %q is not a valid UUID: %v", id, perr)
	}

	// Second call reads back the SAME id and reports firstRun=false.
	id2, firstRun2, err := LoadOrCreateInstallID(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("second LoadOrCreateInstallID: %v", err)
	}
	if firstRun2 {
		t.Error("firstRun = true on second call, want false")
	}
	if id2 != id {
		t.Errorf("second call returned id %q, want %q (unchanged)", id2, id)
	}
}

func TestLoadOrCreateInstallIDRegeneratesOnCorruptFile(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "/home/tester", nil },
	}
	readFile := func(string) ([]byte, error) { return []byte("not-a-uuid"), nil }
	var gotWrite []byte
	writeFile := func(_ string, data []byte, _ os.FileMode) error { gotWrite = data; return nil }
	mkdirAll := func(string, os.FileMode) error { return nil }

	id, firstRun, err := LoadOrCreateInstallID(env, readFile, writeFile, mkdirAll)
	if err != nil {
		t.Fatalf("LoadOrCreateInstallID: %v", err)
	}
	if !firstRun {
		t.Error("firstRun = false on a corrupt file, want true (treated as absent)")
	}
	if _, perr := uuid.Parse(id); perr != nil {
		t.Errorf("id %q is not a valid UUID: %v", id, perr)
	}
	if string(gotWrite) != id {
		t.Errorf("written content %q != returned id %q", gotWrite, id)
	}
}

func TestLoadOrCreateInstallIDFailsClosedWithNoStateDir(t *testing.T) {
	env := xdgconfig.ResolveEnv{
		Getenv:      func(string) string { return "" },
		UserHomeDir: func() (string, error) { return "", errors.New("no home") },
	}
	_, _, err := LoadOrCreateInstallID(env, nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error when no state dir can be resolved, got nil")
	}
}
