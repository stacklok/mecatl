package dtogen

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
	Overrides  map[string]capabilities // explicitly handwritten whole-type operations
}

func generatedMethod(name string) bool {
	return slices.Contains([]string{"CheckCBOR", "AppendCBOR", "ReadCBOR", methodClone, methodEqual}, name)
}

// ownerTargets discovers source-local registries within the containing source tree.
// In standalone copied modules the outermost containing module is the registry root.
func ownerTargets(dir string, selected LoadConfig) ([]LoadConfig, error) {
	root := dir
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Stat(filepath.Join(p, "go.mod")); err == nil {
			root = p
		}
		if _, err := os.Stat(filepath.Join(p, "internal/dtogen")); err == nil {
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
	for path, data := range excluded {
		overlay[path] = data
	}
	for i, target := range targets {
		pkgPath, declaration, stub, err := ownerOverlay(ctx, target, excluded)
		if err != nil {
			return nil, nil, err
		}
		targets[i] = declaration.Config
		owners[pkgPath] = declaration
		overlay[filepath.Join(target.Dir, target.Output)] = stub
	}
	if err := bootstrapViewOverlays(ctx, targets, overlay); err != nil {
		return nil, nil, err
	}
	return owners, overlay, nil
}

func ownerOverlay(ctx context.Context, target LoadConfig, excluded map[string][]byte) (string, *ownerDeclaration, []byte, error) {
	cfg := &packages.Config{Context: ctx, Dir: target.Dir, Env: loadEnvironment(), Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles, Overlay: excluded}
	pkg, err := loadOne(cfg)
	if err != nil {
		return "", nil, nil, err
	}
	emit, err := parseEmit(target.Emit)
	if err != nil {
		return "", nil, nil, err
	}
	if err := validateViewConfig(emit); err != nil {
		return "", nil, nil, err
	}
	out := filepath.Join(target.Dir, target.Output)
	declaration, err := sourceOwner(pkg.CompiledGoFiles, out, target.Types, emit)
	if err != nil {
		return "", nil, nil, err
	}
	target.Types = declaration.Config.Types
	if err := checkDeclarations(pkg.CompiledGoFiles, out, target.Types, emit, declaration.Overrides); err != nil {
		return "", nil, nil, err
	}
	if err := checkDeclarations(pkg.CompiledGoFiles, out, declaration.Names, emit, declaration.Overrides); err != nil {
		return "", nil, nil, err
	}
	declaration.Config = target
	return pkg.PkgPath, declaration, ownerStub(pkg.Name, declaration, emit), nil
}

func ownerStub(packageName string, declaration *ownerDeclaration, emit capabilities) []byte {
	var stub strings.Builder
	_, _ = fmt.Fprintf(&stub, "package %s\n", packageName)
	if emit.cbor {
		_, _ = fmt.Fprintf(&stub, "import dtogenCodec %q\n", codecImportPath)
	}
	for _, name := range declaration.Names {
		if declaration.Wire[name] {
			_, _ = fmt.Fprintf(&stub, `func (v %[1]s) CheckCBOR(b *dtogenCodec.Budget,depth,fieldMax int) error {panic("load-only")}
func (v %[1]s) AppendCBOR(dst []byte) []byte {panic("load-only")}
func (v *%[1]s) ReadCBOR(r *dtogenCodec.Reader,depth,fieldMax int) error {panic("load-only")}
`, name)
		}
		if emit.clone && !declaration.Overrides[name].clone {
			_, _ = fmt.Fprintf(&stub, "func (v %[1]s) Clone() %[1]s {panic(\"load-only\")}\n", name)
		}
		if emit.equal && !declaration.Overrides[name].equal {
			_, _ = fmt.Fprintf(&stub, "func (v %[1]s) Equal(other %[1]s) bool {panic(\"load-only\")}\n", name)
		}
	}
	return []byte(stub.String())
}

func bootstrapViewOverlays(ctx context.Context, targets []LoadConfig, overlay map[string][]byte) error {
	// Views need typed DTO fields to infer signatures. A diagnostic-tolerant pass
	// supplies those types while handwritten callers still lack generated methods.
	// Only this load-only bootstrap tolerates errors; Load and renderTargets perform
	// strict checks with the complete overlay before any installation.
	for _, target := range targets {
		selected, err := parseEmit(target.Emit)
		if err != nil {
			return err
		}
		if !selected.view {
			continue
		}
		pkgs, err := packages.Load(&packages.Config{Context: ctx, Dir: target.Dir, Env: loadEnvironment(),
			Mode:    packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedImports | packages.NeedDeps | packages.NeedModule,
			Overlay: overlay}, ".")
		if err != nil || len(pkgs) != 1 {
			return fmt.Errorf("load view bootstrap %s: %v", target.Dir, err)
		}
		loaded := &Loaded{Package: pkgs[0], Output: filepath.Join(target.Dir, target.Output)}
		e := &emitter{s: &schema{loaded: loaded}, imports: map[string]string{}}
		if err := e.emitViews(target); err != nil {
			return err
		}
		if err := e.checkReserved(); err != nil {
			return err
		}
		data, err := e.source()
		if err != nil {
			return err
		}
		// Merge the value signatures and view declarations into one valid Go file.
		overlay[loaded.Output], err = mergeViewOverlay(overlay[loaded.Output], data)
		if err != nil {
			return err
		}
	}
	return nil
}

// sourceOwner resolves sorted roots from explicit selection or active source markers
// and computes their syntactic local closure for bootstrapping. Typed schema
// validation remains authoritative for supported shapes and wire reachability.
func sourceOwner(files []string, out string, roots []string, emit capabilities) (*ownerDeclaration, error) {
	specs := map[string]*ast.TypeSpec{}
	includes := map[string]bool{}
	validators := map[string]string{}
	overrides := map[string]capabilities{}
	fset := token.NewFileSet()
	for _, path := range files {
		if path == out {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		// Generated declarations remain typechecked and can belong to a selected
		// handwritten root's closure, but dtogen directives are source directives.
		if err := ownerDeclarations(file, fset, specs, includes, validators, overrides, !ast.IsGenerated(file)); err != nil {
			return nil, err
		}
	}
	roots, err := ownerRootNames(roots, includes)
	if err != nil {
		return nil, err
	}
	c := &ownerClosure{specs: specs, selected: map[string]bool{}, wireNames: map[string]bool{}, visited: map[string]uint8{}}
	if !emit.view { // view roots are handles, not private DTO/context value closures
		for _, root := range roots {
			if err := c.visit(root, emit.cbor); err != nil {
				return nil, err
			}
		}
	}
	for name, methods := range overrides {
		if !c.selected[name] {
			return nil, fmt.Errorf("%s: stale dtogen override target is outside the selected local closure", name)
		}
		for _, method := range []string{methodClone, methodEqual} {
			if methods.method(method) && !emit.method(method) {
				return nil, fmt.Errorf("%s: dtogen override %s requires its selected -emit capability", name, method)
			}
		}
	}
	d := &ownerDeclaration{Config: LoadConfig{Types: roots}, Validators: validators, Overrides: overrides, Wire: c.wireNames}
	for name := range c.selected {
		d.Names = append(d.Names, name)
	}
	slices.Sort(d.Names)
	return d, nil
}

// ownerRootNames validates the mutually exclusive selection sources and returns
// sorted, unique root names without retaining the caller's slice.
func ownerRootNames(roots []string, includes map[string]bool) ([]string, error) {
	if len(includes) != 0 && len(roots) != 0 {
		return nil, fmt.Errorf("dtogen include markers cannot be combined with explicit -types")
	}
	roots = slices.Clone(roots)
	for name := range includes {
		roots = append(roots, name)
	}
	slices.Sort(roots)
	roots = slices.Compact(roots)
	if len(roots) == 0 {
		return nil, fmt.Errorf("select at least one root type with dtogen:include or -types")
	}
	for _, name := range roots {
		if !token.IsIdentifier(name) || name == "_" {
			return nil, fmt.Errorf("root %q must name a local type", name)
		}
	}
	return roots, nil
}

// ownerDeclarations records local declarations and, for handwritten source,
// binds directives only to unambiguous named declarations.
func ownerDeclarations(file *ast.File, fset *token.FileSet, specs map[string]*ast.TypeSpec, includes map[string]bool, validators map[string]string, overrides map[string]capabilities, directives bool) error {
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
			if !directives {
				continue
			}
			groups := []*ast.CommentGroup{spec.Doc}
			if len(g.Specs) == 1 {
				groups = append(groups, g.Doc)
			}
			if err := typeDirectives(spec, groups, fset, includes, validators, overrides, bound); err != nil {
				return err
			}
		}
	}
	if !directives {
		return nil
	}
	for _, group := range file.Comments {
		for _, c := range group.List {
			if strings.HasPrefix(c.Text, "//cborgen:") {
				return fmt.Errorf("%s: obsolete cborgen directive; use dtogen", fset.Position(c.Pos()))
			}
			if strings.HasPrefix(c.Text, "//dtogen:") && !bound[c.Pos()] {
				return fmt.Errorf("%s: ambiguous or misplaced dtogen directive; annotate one named type declaration", fset.Position(c.Pos()))
			}
		}
	}
	return nil
}

func typeDirectives(spec *ast.TypeSpec, groups []*ast.CommentGroup, fset *token.FileSet, includes map[string]bool, validators map[string]string, overrides map[string]capabilities, bound map[token.Pos]bool) error {
	for _, group := range groups {
		if group == nil {
			continue
		}
		for _, c := range group.List {
			if !strings.HasPrefix(c.Text, "//dtogen:") {
				continue
			}
			if spec.Assign.IsValid() {
				return fmt.Errorf("%s: directive must annotate a defined type, not an alias", fset.Position(c.Pos()))
			}
			bound[c.Pos()] = true
			words := strings.Fields(strings.TrimPrefix(c.Text, "//dtogen:"))
			if len(words) == 1 && words[0] == "include" {
				if includes[spec.Name.Name] {
					return fmt.Errorf("%s: duplicate dtogen include marker", fset.Position(c.Pos()))
				}
				includes[spec.Name.Name] = true
				continue
			}
			if len(words) == 2 && words[0] == "override" {
				methods := overrides[spec.Name.Name]
				switch words[1] {
				case "Clone", "Equal":
					if methods.method(words[1]) {
						return fmt.Errorf("%s: duplicate dtogen override %s", fset.Position(c.Pos()), words[1])
					}
					if words[1] == "Clone" {
						methods.clone = true
					} else {
						methods.equal = true
					}
					overrides[spec.Name.Name] = methods
					continue
				default:
					return fmt.Errorf("%s: dtogen override supports only Clone or Equal", fset.Position(c.Pos()))
				}
			}
			if len(words) != 2 || words[0] != "validate" || !token.IsIdentifier(words[1]) || words[1] == "_" || generatedMethod(words[1]) {
				return fmt.Errorf("%s: invalid or unknown dtogen directive", fset.Position(c.Pos()))
			}
			if validators[spec.Name.Name] != "" {
				return fmt.Errorf("%s: duplicate dtogen validator directive", fset.Position(c.Pos()))
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
			if wire && f.Tag != nil {
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
