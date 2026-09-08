package port

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

func TestSessionLoadFailureClassificationFromWrappedErrors(t *testing.T) {
	cause := errors.New("backend detail")
	for _, tc := range []struct {
		name  string
		class SessionLoadFailureClass
	}{
		{name: "store", class: SessionLoadFailureStore},
		{name: "snapshot", class: SessionLoadFailureSnapshot},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("adapter wrapper: %w", NewSessionLoadFailure(tc.class, cause))
			if got := ClassifySessionLoadFailure(err); got != tc.class {
				t.Fatalf("class = %s, want %s", got, tc.class)
			}
			if !errors.Is(err, ErrSessionLoadFailure) || !errors.Is(err, cause) {
				t.Fatalf("typed wrapping lost errors.Is contract: %v", err)
			}
			var classified *SessionLoadFailureError
			if !errors.As(err, &classified) || classified.Class() != tc.class {
				t.Fatalf("typed wrapping lost errors.As contract: %#v", classified)
			}
		})
	}

	for _, err := range []error{
		nil,
		ErrSessionNotFound,
		fmt.Errorf("wrapped: %w", ErrSessionNotFound),
		errors.New("SessionLoadFailureStore snapshot redis not found"),
	} {
		if got := ClassifySessionLoadFailure(err); got != SessionLoadFailureUnknown {
			t.Fatalf("ClassifySessionLoadFailure(%v) = %s, want unknown", err, got)
		}
	}
	if err := NewSessionLoadFailure(SessionLoadFailureStore, ErrSessionNotFound); !errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrSessionLoadFailure) {
		t.Fatalf("genuine not-found was classified: %v", err)
	}
}

func TestSessionLoadFailureClassificationIsClosedAndPortOwned(t *testing.T) {
	want := map[SessionLoadFailureClass]string{
		SessionLoadFailureUnknown:  "unknown",
		SessionLoadFailureStore:    "store",
		SessionLoadFailureSnapshot: "snapshot",
	}
	for class, text := range want {
		if class.String() != text || !class.Valid() {
			t.Fatalf("class %d = (%q, valid=%t), want (%q, true)", class, class.String(), class.Valid(), text)
		}
	}
	if class := SessionLoadFailureClass(255); class.Valid() || class.String() != "unknown" {
		t.Fatalf("open-ended class accepted: %d %q", class, class.String())
	}

	root := filepath.Join("..", "..", "internal", "adapter", "server")
	files, err := filepath.Glob(filepath.Join(root, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		file, parseErr := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if ok && (sel.Sel.Name == "Contains" || sel.Sel.Name == "HasPrefix" || sel.Sel.Name == "HasSuffix") {
				for _, arg := range call.Args {
					if lit, literal := arg.(*ast.BasicLit); literal && (lit.Value == `"store"` || lit.Value == `"snapshot"`) {
						t.Errorf("server-side load classification by string in %s", name)
					}
				}
			}
			return true
		})
	}
}
