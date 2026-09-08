package osfs

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestGlobWalkStopsImmediatelyOnVisitorError(t *testing.T) {
	ws, err := NewWorkspace(t.TempDir())
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}
	for _, path := range []string{"01-first.txt", "02-second.txt", "03-unvisited.txt", "04-also-unvisited.txt"} {
		if err := ws.Write(context.Background(), path, []byte("hit\n")); err != nil {
			t.Fatalf("Write(%q): %v", path, err)
		}
	}

	stop := errors.New("stop glob walk")
	var visited []string
	err = ws.fs.globWalk(context.Background(), "*.txt", func(path string, _ fs.DirEntry) error {
		visited = append(visited, path)
		if len(visited) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("globWalk error = %v, want sentinel", err)
	}
	if want := []string{"01-first.txt", "02-second.txt"}; !slices.Equal(visited, want) {
		t.Errorf("visited = %v, want %v; later files must not be visited", visited, want)
	}
}

func TestGrepGlobAppliesCumulativeBudgetsWhileWalking(t *testing.T) {
	tests := []struct {
		name      string
		maxFiles  int
		maxBytes  int64
		wantFiles int
		wantBytes int64
	}{
		{name: "bytes", maxFiles: 10, maxBytes: 8, wantFiles: 3, wantBytes: 8},
		{name: "files", maxFiles: 2, maxBytes: 100, wantFiles: 3, wantBytes: 8},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ws, err := NewWorkspace(t.TempDir())
			if err != nil {
				t.Fatalf("NewWorkspace: %v", err)
			}
			for _, path := range []string{"a/one.txt", "b/two.txt", "c/three.txt", "d/four.txt"} {
				if err := ws.Write(context.Background(), path, []byte("hit\n")); err != nil {
					t.Fatalf("Write(%q): %v", path, err)
				}
			}

			search := grepSearch{
				ctx:      context.Background(),
				re:       regexp.MustCompile("hit"),
				maxFiles: test.maxFiles,
				maxBytes: test.maxBytes,
			}
			err = ws.grepGlob("**", &search)
			if err == nil || !strings.Contains(err.Error(), grepSafetyBudgetMessage) {
				t.Fatalf("grepGlob error = %v, want safety-budget error", err)
			}
			if search.files != test.wantFiles || search.readBytes != test.wantBytes {
				t.Errorf("budget counters = (%d files, %d bytes), want (%d, %d)", search.files, search.readBytes, test.wantFiles, test.wantBytes)
			}
			gotPaths := make([]string, 0, len(search.matches))
			for _, match := range search.matches {
				gotPaths = append(gotPaths, match.Path)
			}
			wantPaths := []string{"a/one.txt", "b/two.txt"}
			if !slices.Equal(gotPaths, wantPaths) {
				t.Errorf("paths scanned before boundary = %v, want %v", gotPaths, wantPaths)
			}
		})
	}
}
