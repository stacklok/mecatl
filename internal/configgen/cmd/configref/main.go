// Command configref is the BUILD-TIME generator (issue #140) that emits the two
// committed configuration artifacts from the permconfig YAML schema:
//
//   - the commented settings.yaml skeleton (--skeleton), embedded by `config init`, and
//   - the Markdown configuration reference (--reference), linked from usage.md.
//
// It is the ONLY place go/ast is used to harvest the schema's field doc-comments;
// the shipped mecated binary never imports go/ast (it embeds the skeleton this
// program writes). Invoked by `task docs:configref`; a CI step regenerates to a temp
// dir and diffs against the committed files, failing on drift.
//
// The model is built by configgen.BuildModel (reflection only); this program only
// adds the go/ast doc-comment harvest. So the skeleton, the reference, AND the
// configgen tests all walk the SAME model — none can disagree, and reflecting over
// the permconfig structs means none can drift from the code.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/stacklok/mecatl/internal/configgen"
)

func main() {
	skeletonPath := flag.String("skeleton", "", "path to write the commented settings.yaml skeleton")
	referencePath := flag.String("reference", "", "path to write the Markdown configuration reference")
	flag.Parse()
	if *skeletonPath == "" && *referencePath == "" {
		fmt.Fprintln(os.Stderr, "usage: configref --skeleton <path> --reference <path>")
		os.Exit(2)
	}
	if err := generate(*skeletonPath, *referencePath); err != nil {
		fmt.Fprintf(os.Stderr, "configref: %v\n", err)
		os.Exit(1)
	}
}

// generate harvests the schema doc-comments, builds the model, and writes the skeleton
// and/or reference to the given paths (an empty path skips that artifact). Factored out
// of main so a smoke test can drive it directly to a temp dir.
func generate(skeletonPath, referencePath string) error {
	docs, err := harvestDocs()
	if err != nil {
		return fmt.Errorf("harvesting schema doc-comments: %w", err)
	}
	model := configgen.BuildModel(docs)
	if skeletonPath != "" {
		if err := os.WriteFile(skeletonPath, []byte(configgen.RenderSkeleton(model)), 0o644); err != nil {
			return fmt.Errorf("writing skeleton: %w", err)
		}
	}
	if referencePath != "" {
		if err := os.WriteFile(referencePath, []byte(configgen.RenderReference(model)), 0o644); err != nil {
			return fmt.Errorf("writing reference: %w", err)
		}
	}
	return nil
}

// harvestDocs parses schema.go and collects every struct field's doc-comment, keyed
// by "Struct.Field" — the keying configgen.BuildModel expects.
func harvestDocs() (configgen.Docs, error) {
	schemaPath, err := schemaFilePath()
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, schemaPath, nil, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	docs := configgen.Docs{}
	ast.Inspect(file, func(n ast.Node) bool {
		ts, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if field.Doc == nil || len(field.Names) == 0 {
				continue
			}
			docs[ts.Name.Name+"."+field.Names[0].Name] = commentText(field.Doc)
		}
		return true
	})
	return docs, nil
}

// schemaFilePath resolves internal/adapter/permconfig/schema.go relative to THIS
// source file (so the generator works regardless of the caller's cwd).
func schemaFilePath() (string, error) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("cannot resolve generator source path")
	}
	// self = .../internal/configgen/cmd/configref/main.go ; the repo root is four up.
	root := filepath.Clean(filepath.Join(filepath.Dir(self), "..", "..", "..", ".."))
	return filepath.Join(root, "internal", "adapter", "permconfig", "schema.go"), nil
}

// commentText joins a comment group into a single space-collapsed paragraph, dropping
// the leading "// " markers. The renderers rewrap to width, so the original (mid-
// sentence) line breaks of a wrapped Go comment must NOT be preserved as hard breaks.
func commentText(g *ast.CommentGroup) string {
	var words []string
	for _, c := range g.List {
		t := strings.TrimPrefix(c.Text, "//")
		words = append(words, strings.Fields(t)...)
	}
	return strings.Join(words, " ")
}
