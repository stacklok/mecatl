package cborgen

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/tools/go/packages"
)

// ownerDeclaration records source provenance, never inferred interface compatibility.
// Names includes the selected local named closure, including excluded value state.
type ownerDeclaration struct {
	Config     LoadConfig
	Wire       map[string]bool
	Names      []string
	Validators map[string]string
}

func generatedMethod(name string) bool {
	return slices.Contains([]string{"CheckCBOR", "AppendCBOR", "ReadCBOR", "Clone", "Equal"}, name)
}

// ownerTargets discovers source-local registries within the containing source tree.
// In standalone copied modules the outermost containing module is the registry root.
func ownerTargets(dir string, selected LoadConfig) ([]LoadConfig, error) {
	root := dir
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
			root = p
		}
		if _, err := os.Stat(filepath.Join(p, "internal/cborgen")); err == nil {
			root = p
			break
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	targets, err := discoverTargets(root)
	if err != nil {
		return nil, err
	}
	selected.Dir = dir
	found := false
	for i, t := range targets {
		if t.Dir == dir {
			targets[i] = selected
			found = true
		}
	}
	if !found {
		targets = append(targets, selected)
	}
	slices.SortFunc(targets, func(a, b LoadConfig) int { return strings.Compare(a.Dir, b.Dir) })
	return targets, nil
}

// visitDependencies visits each imported package once in stable dependency order.
func visitDependencies(root *packages.Package, visit func(*packages.Package)) {
	seen := map[string]bool{}
	var walk func(*packages.Package)
	walk = func(pkg *packages.Package) {
		if seen[pkg.ID] {
			return
		}
		seen[pkg.ID] = true
		paths := make([]string, 0, len(pkg.Imports))
		for path := range pkg.Imports {
			paths = append(paths, path)
		}
		slices.Sort(paths)
		for _, path := range paths {
			walk(pkg.Imports[path])
		}
		visit(pkg)
	}
	walk(root)
}

// ownerOverlays replaces every declared artifact with load-only signatures before
// type checking. No owner initialization, generated body or custom hook is executed.
func ownerOverlays(ctx context.Context, targets []LoadConfig, excluded map[string][]byte) (map[string]*ownerDeclaration, map[string][]byte, error) {
	owners := map[string]*ownerDeclaration{}
	overlay := map[string][]byte{}
	for k, v := range excluded {
		overlay[k] = v
	}
	for _, target := range targets {
		cfg := &packages.Config{Context: ctx, Dir: target.Dir, Env: loadEnvironment(), Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles, Overlay: excluded}
		pkg, err := loadOne(cfg)
		if err != nil {
			return nil, nil, err
		}
		out := filepath.Join(target.Dir, target.Output)
		declaration, err := sourceOwner(pkg.CompiledGoFiles, out, target.Types)
		if err != nil {
			return nil, nil, err
		}
		if err := checkDeclarations(pkg.CompiledGoFiles, out, declaration.Names); err != nil {
			return nil, nil, err
		}
		declaration.Config = target
		declaration.Config.Types = slices.Clone(target.Types)
		slices.Sort(declaration.Config.Types)
		declaration.Config.Types = slices.Compact(declaration.Config.Types)
		owners[pkg.PkgPath] = declaration
		var stub strings.Builder
		fmt.Fprintf(&stub, "package %s\nimport cborgenCodec %q\n", pkg.Name, codecImportPath)
		for _, name := range declaration.Names {
			if declaration.Wire[name] {
				fmt.Fprintf(&stub, `func (v %[1]s) CheckCBOR(b *cborgenCodec.Budget,depth,fieldMax int) error {panic("load-only")}
func (v %[1]s) AppendCBOR(dst []byte) []byte {panic("load-only")}
func (v *%[1]s) ReadCBOR(r *cborgenCodec.Reader,depth,fieldMax int) error {panic("load-only")}
`, name)
			}
			fmt.Fprintf(&stub, `func (v %[1]s) Clone() %[1]s {panic("load-only")}
func (v %[1]s) Equal(other %[1]s) bool {panic("load-only")}
`, name)
		}
		overlay[out] = []byte(stub.String())
	}
	return owners, overlay, nil
}

// sourceOwner computes a syntactic local type closure for bootstrapping. Typed
// schema validation remains authoritative for supported shapes and wire reachability.
func sourceOwner(files []string, out string, roots []string) (*ownerDeclaration, error) {
	specs := map[string]*ast.TypeSpec{}
	validators := map[string]string{}
	fset := token.NewFileSet()
	for _, path := range files {
		if path == out {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		if err := ownerDeclarations(file, fset, specs, validators); err != nil {
			return nil, err
		}
	}
	c := &ownerClosure{specs: specs, selected: map[string]bool{}, wireNames: map[string]bool{}, visited: map[string]uint8{}}
	for _, root := range roots {
		if err := c.visit(root, true); err != nil {
			return nil, err
		}
	}
	d := &ownerDeclaration{Validators: validators, Wire: c.wireNames}
	for name := range c.selected {
		d.Names = append(d.Names, name)
	}
	slices.Sort(d.Names)
	return d, nil
}

// ownerDeclarations binds directives only to unambiguous named declarations.
func ownerDeclarations(file *ast.File, fset *token.FileSet, specs map[string]*ast.TypeSpec, validators map[string]string) error {
	bound := map[token.Pos]bool{}
	for _, decl := range file.Decls {
		g, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, sp := range g.Specs {
			spec, ok := sp.(*ast.TypeSpec)
			if !ok {
				continue
			}
			specs[spec.Name.Name] = spec
			groups := []*ast.CommentGroup{spec.Doc}
			if len(g.Specs) == 1 {
				groups = append(groups, g.Doc)
			}
			if err := validatorDirectives(spec, groups, fset, validators, bound); err != nil {
				return err
			}
		}
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "//cborgen:") && !bound[c.Pos()] {
				return fmt.Errorf("%s: ambiguous or misplaced cborgen directive; annotate one named type declaration", fset.Position(c.Pos()))
			}
		}
	}
	return nil
}

func validatorDirectives(spec *ast.TypeSpec, groups []*ast.CommentGroup, fset *token.FileSet, validators map[string]string, bound map[token.Pos]bool) error {
	for _, group := range groups {
		if group == nil {
			continue
		}
		for _, c := range group.List {
			if !strings.HasPrefix(c.Text, "//cborgen:") {
				continue
			}
			if spec.Assign.IsValid() {
				return fmt.Errorf("%s: validator must annotate a defined type, not an alias", fset.Position(c.Pos()))
			}
			bound[c.Pos()] = true
			words := strings.Fields(strings.TrimPrefix(c.Text, "//cborgen:"))
			if len(words) != 2 || words[0] != "validate" || !token.IsIdentifier(words[1]) || words[1] == "_" || generatedMethod(words[1]) {
				return fmt.Errorf("%s: invalid or unknown cborgen directive", fset.Position(c.Pos()))
			}
			if validators[spec.Name.Name] != "" {
				return fmt.Errorf("%s: duplicate cborgen validator directive", fset.Position(c.Pos()))
			}
			validators[spec.Name.Name] = words[1]
		}
	}
	return nil
}

// ownerClosure tracks syntactic value and wire reachability independently while
// bootstrapping signatures; the typed schema remains authoritative.
type ownerClosure struct {
	specs               map[string]*ast.TypeSpec
	selected, wireNames map[string]bool
	visited             map[string]uint8
}

// underlying resolves defined declarations without inheriting the other named
// type's methods or validator.
func (c *ownerClosure) underlying(expr ast.Expr) (ast.Expr, error) {
	seen := map[string]bool{}
	for {
		if p, ok := expr.(*ast.ParenExpr); ok {
			expr = p.X
			continue
		}
		id, ok := expr.(*ast.Ident)
		if !ok || c.specs[id.Name] == nil {
			return expr, nil
		}
		if seen[id.Name] {
			return nil, fmt.Errorf("%s: recursive type graph", id.Name)
		}
		seen[id.Name] = true
		sp := c.specs[id.Name]
		if sp.TypeParams != nil {
			return nil, fmt.Errorf("generic types unsupported: %s", id.Name)
		}
		expr = sp.Type
	}
}

func (c *ownerClosure) walk(expr ast.Expr, wire bool) error {
	switch n := expr.(type) {
	case *ast.Ident:
		if c.specs[n.Name] != nil {
			return c.visit(n.Name, wire)
		}
	case *ast.ParenExpr:
		return c.walk(n.X, wire)
	case *ast.StarExpr:
		return c.walk(n.X, wire)
	case *ast.ArrayType:
		return c.walk(n.Elt, wire)
	case *ast.MapType:
		if err := c.walk(n.Key, wire); err != nil {
			return err
		}
		return c.walk(n.Value, wire)
	case *ast.StructType:
		for _, f := range n.Fields.List {
			persisted := wire
			if f.Tag != nil {
				text, err := strconv.Unquote(f.Tag.Value)
				if err != nil {
					return err
				}
				tags, err := mappingTags(text)
				if err != nil {
					return err
				}
				if tags["cbor"] == "-" {
					persisted = false
				}
			}
			if err := c.walk(f.Type, persisted); err != nil {
				return err
			}
		}
	case *ast.IndexExpr, *ast.IndexListExpr:
		return fmt.Errorf("generic types unsupported")
	}
	return nil
}

func (c *ownerClosure) visit(name string, wire bool) error {
	mask := uint8(1)
	if wire {
		mask = 2
	}
	if c.visited[name]&mask != 0 {
		return nil
	}
	c.visited[name] |= mask
	sp := c.specs[name]
	if sp == nil {
		return fmt.Errorf("root %s is not a locally declared type", name)
	}
	if sp.TypeParams != nil {
		return fmt.Errorf("generic types unsupported: root %s is generic", name)
	}
	if sp.Assign.IsValid() {
		return c.walk(sp.Type, wire)
	}
	expr, err := c.underlying(sp.Type)
	if err != nil {
		return err
	}
	if _, ok := expr.(*ast.StarExpr); ok {
		return fmt.Errorf("%s: defined pointer types cannot have receiver methods; use ordinary pointers or aliases", name)
	}
	c.selected[name] = true
	c.wireNames[name] = c.wireNames[name] || wire
	return c.walk(expr, wire)
}
