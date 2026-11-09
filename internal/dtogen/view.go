package dtogen

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

const viewsImportPath = "github.com/stacklok/mecatl/engine/kernel/views"

func validateViewConfig(emit capabilities) error {
	if emit.view && (emit.cbor || emit.clone || emit.equal) {
		return fmt.Errorf("view declarations require view-only -emit view; select value capabilities independently")
	}
	return nil
}

// typedView ties a selected local handle to its private value pointer. Extra
// layout fields retain context and prohibit automatic child construction.
type typedView struct {
	name          string
	named         *types.Named
	dto           *types.Pointer
	fields        *types.Struct
	constructible bool
}

func (e *emitter) viewType(t types.Type) string { return e.typeName(&shape{typ: t}) }

// emitViews infers source-declared field policy without executing owner code.
func (e *emitter) emitViews(c LoadConfig) error {
	pkg := e.s.loaded.Package
	views, err := typedViews(c.Types, pkg)
	if err != nil {
		return err
	}
	e.writeViewFingerprint(views, pkg)
	for _, view := range views {
		if err := e.emitView(view, views, pkg); err != nil {
			return err
		}
	}
	return nil
}

func typedViews(names []string, pkg *packages.Package) ([]typedView, error) {
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	views := make([]typedView, 0, len(names))
	for _, name := range names {
		obj, ok := pkg.Types.Scope().Lookup(name).(*types.TypeName)
		if !ok || obj.IsAlias() {
			return nil, fmt.Errorf("view %s must be a handwritten local defined struct", name)
		}
		named, ok := obj.Type().(*types.Named)
		if !ok || named.TypeParams().Len() != 0 {
			return nil, fmt.Errorf("view %s must be nongeneric", name)
		}
		layout, ok := named.Underlying().(*types.Struct)
		if !ok {
			return nil, fmt.Errorf("view %s must be a struct", name)
		}
		var backing types.Type
		for i := 0; i < layout.NumFields(); i++ {
			field := layout.Field(i)
			if field.Exported() || field.Embedded() || field.Name() == "_" {
				return nil, fmt.Errorf("view %s layout must retain backing/context in private nonembedded fields", name)
			}
			if field.Name() == "value" {
				backing = field.Type()
			}
		}
		ptr, ok := backing.(*types.Pointer)
		if !ok {
			return nil, fmt.Errorf("view %s requires private value *DTO backing", name)
		}
		dto, ok := types.Unalias(ptr.Elem()).(*types.Named)
		if !ok || dto.TypeArgs().Len() != 0 {
			return nil, fmt.Errorf("view %s backing must point to a named nongeneric DTO", name)
		}
		fields, ok := dto.Underlying().(*types.Struct)
		if !ok {
			return nil, fmt.Errorf("view %s backing must point to a struct", name)
		}
		view := typedView{name, named, ptr, fields, layout.NumFields() == 1}
		for _, prior := range views {
			if view.constructible && prior.constructible && types.Identical(view.dto.Elem(), prior.dto.Elem()) {
				return nil, fmt.Errorf("ambiguous context-free views %s and %s for %s", prior.name, name, dto)
			}
		}
		views = append(views, view)
	}
	return views, nil
}

func (e *emitter) writeViewFingerprint(views []typedView, pkg *packages.Package) {
	hash := sha256.New()
	for _, view := range views {
		_, _ = fmt.Fprintf(hash, "\n%q %q", view.name, types.TypeString(view.named.Underlying(), func(p *types.Package) string { return p.Path() }))
		_, _ = fmt.Fprintf(hash, "\n%q", types.TypeString(view.dto, func(p *types.Package) string { return p.Path() }))
		_, _ = fmt.Fprintf(hash, "\n%q", types.TypeString(view.fields, func(p *types.Package) string { return p.Path() }))
		_, _ = fmt.Fprintf(hash, "\n%q", sourceDoc(pkg, view.dto.Elem(), ""))
		for i := 0; i < view.fields.NumFields(); i++ {
			_, _ = fmt.Fprintf(hash, "\n%q", sourceDoc(pkg, view.dto.Elem(), view.fields.Field(i).Name()))
		}
	}
	e.line("// Selected view layouts, source policy and documentation fingerprint: %x.", hash.Sum(nil))
}

func (e *emitter) emitView(v typedView, views []typedView, pkg *packages.Package) error {
	e.viewDoc("Backing contract for "+v.name+".", sourceDoc(pkg, v.dto.Elem(), ""))
	if v.constructible {
		name := "dtogenView" + v.name
		if err := e.reserveViewName(name); err != nil {
			return err
		}
		e.line("// %s borrows stable backing; it neither freezes nor synchronizes access.", name)
		e.line("func %s(value %s) %s { return %s{value:value} }", name, e.viewType(v.dto), v.name, v.name)
	}
	// Catch stale field shapes before the deterministic freshness check, where nameable.
	if viewShapeNameable(v.fields, pkg.Types) {
		e.line("var _ = (*%s)((%s)(nil))", e.viewType(v.fields), e.viewType(v.dto))
	}
	for i := 0; i < v.fields.NumFields(); i++ {
		field := v.fields.Field(i)
		tags, err := selectedTags(v.fields.Tag(i), "view")
		if err != nil {
			return fmt.Errorf("%s.%s: %w", v.name, field.Name(), err)
		}
		policy, present := tags["view"]
		switch {
		case !present:
			if !field.Exported() && field.Pkg() != pkg.Types {
				return fmt.Errorf("%s.%s: inaccessible DTO field", v.name, field.Name())
			}
			if err := e.noViewMethod(v, field.Name()); err != nil {
				return err
			}
			if err := e.emitViewField(v, field, views); err != nil {
				return err
			}
		case policy == "-":
			continue
		case policy == "manual":
			if err := e.checkViewMethod(v, field.Name()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.%s: unknown view tag %q; use - or manual", v.name, field.Name(), policy)
		}
	}
	return nil
}

// viewShapeNameable reports whether an anonymous structural assertion can spell
// every identity without changing private field/method scopes. Named exported
// types stay opaque: their private internals need not be reproduced.
//
//nolint:gocyclo // Keep the exhaustive Go type switch and recursive visibility checks together.
func viewShapeNameable(t types.Type, local *types.Package) bool {
	var obj *types.TypeName
	var args *types.TypeList
	switch t := t.(type) {
	case *types.Named:
		obj, args = t.Obj(), t.TypeArgs()
	case *types.Alias:
		obj, args = t.Obj(), t.TypeArgs()
	}
	if obj != nil {
		if obj.Pkg() != nil && obj.Pkg() != local && !obj.Exported() {
			return false
		}
		for i := 0; i < args.Len(); i++ {
			if !viewShapeNameable(args.At(i), local) {
				return false
			}
		}
		return true
	}
	switch t := t.(type) {
	case *types.Basic:
		return t.Kind() != types.Invalid
	case *types.Pointer:
		return viewShapeNameable(t.Elem(), local)
	case *types.Slice:
		return viewShapeNameable(t.Elem(), local)
	case *types.Array:
		return viewShapeNameable(t.Elem(), local)
	case *types.Chan:
		return viewShapeNameable(t.Elem(), local)
	case *types.Map:
		return viewShapeNameable(t.Key(), local) && viewShapeNameable(t.Elem(), local)
	case *types.Signature:
		return viewShapeNameable(t.Params(), local) && viewShapeNameable(t.Results(), local)
	case *types.Tuple:
		for i := 0; i < t.Len(); i++ {
			if !viewShapeNameable(t.At(i).Type(), local) {
				return false
			}
		}
		return true
	case *types.Struct:
		for i := 0; i < t.NumFields(); i++ {
			f := t.Field(i)
			if (!f.Exported() && f.Pkg() != local) || !viewShapeNameable(f.Type(), local) {
				return false
			}
		}
		return true
	case *types.Interface:
		for i := 0; i < t.NumExplicitMethods(); i++ {
			m := t.ExplicitMethod(i)
			if (!m.Exported() && m.Pkg() != local) || !viewShapeNameable(m.Type(), local) {
				return false
			}
		}
		for i := 0; i < t.NumEmbeddeds(); i++ {
			if !viewShapeNameable(t.EmbeddedType(i), local) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func (e *emitter) reserveViewName(name string) error {
	if obj := e.s.loaded.Package.Types.Scope().Lookup(name); obj != nil && !e.generatedPosition(obj.Pos()) {
		return fmt.Errorf("handwritten declaration collides with generated %s", name)
	}
	return nil
}
func (e *emitter) generatedPosition(pos token.Pos) bool {
	return filepath.Clean(e.s.loaded.Package.Fset.Position(pos).Filename) == e.s.loaded.Output
}

func (e *emitter) noViewMethod(v typedView, name string) error {
	obj, _, _ := types.LookupFieldOrMethod(v.named, true, e.s.loaded.Package.Types, name)
	if obj != nil && !e.generatedPosition(obj.Pos()) {
		return fmt.Errorf("%s.%s: handwritten method/field collision; tag the DTO field view:\"manual\"", v.name, name)
	}
	return nil
}

// checkViewMethod requires a directly declared handwritten value-receiver getter.
// Go owns its semantic signature; final closure typechecking validates its callers.
func (e *emitter) checkViewMethod(v typedView, name string) error {
	for i := 0; i < v.named.NumMethods(); i++ {
		fn := v.named.Method(i)
		if fn.Name() != name || e.generatedPosition(fn.Pos()) {
			continue
		}
		sig := fn.Type().(*types.Signature)
		if !types.Identical(sig.Recv().Type(), v.named) || sig.Params().Len() != 0 || sig.Results().Len() == 0 || sig.Variadic() {
			return fmt.Errorf("%s.%s: manual getter must have a value receiver, no parameters and at least one result", v.name, name)
		}
		return nil
	}
	return fmt.Errorf("%s.%s: missing directly declared handwritten manual getter", v.name, name)
}

//nolint:gocyclo // Keep the exhaustive supported view shapes and their ownership adapters together.
func (e *emitter) emitViewField(v typedView, field *types.Var, all []typedView) error {
	name := field.Name()
	t := field.Type()
	optional := false
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		optional = true
		t = ptr.Elem()
	}
	expr := "v.value." + name
	value := expr
	if optional {
		value = "*" + expr
	}
	var result, body, zero, ownership string
	if primitive := primitiveName(t); primitive != "" {
		if err := verifyPrimitive(t, e.s.loaded.Package); err != nil {
			return fmt.Errorf("%s.%s: %w", v.name, name, err)
		}
		owner := e.viewType(types.Unalias(t))
		prefix := owner[:strings.LastIndex(owner, ".")+1]
		result, body = prefix+"View", prefix+"Borrow("+value+")"
		if primitive == "Map" {
			result, body = prefix+"MapView", prefix+"BorrowMap("+value+")"
		}
		zero = result + "{}"
		ownership = "The returned view borrows stable structured-value backing for every derived view's lifetime."
	} else {
		switch u := t.Underlying().(type) {
		case *types.Basic:
			if u.Kind() == types.UnsafePointer || u.Kind() == types.Invalid {
				return fmt.Errorf("%s.%s: unsupported scalar", v.name, name)
			}
			result = e.viewType(t)
			zero = "*new(" + result + ")"
			body = value
			ownership = "The receiver retains stable backing for its lifetime."
		case *types.Struct:
			child, err := childViewFor(t, all)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", v.name, name, err)
			}
			result = child.name
			zero = result + "{}"
			address := "&" + expr
			if optional {
				address = expr
			}
			body = "dtogenView" + result + "(" + address + ")"
			ownership = "The returned child borrows backing, which must remain stable for every derived view's lifetime."
		case *types.Slice:
			runtime := e.imports[viewsImportPath]
			if runtime == "" {
				runtime = "dtogenViews"
				e.imports[viewsImportPath] = runtime
			}
			if types.Identical(u.Elem(), types.Typ[types.Byte]) {
				result = runtime + ".Bytes"
				zero = result + "{}"
				body = runtime + ".BorrowBytes(" + value + ")"
				ownership = "The returned bytes borrow stable backing for their lifetime; only Copy detaches bytes."
			} else {
				child, err := childViewFor(u.Elem(), all)
				if err != nil {
					return fmt.Errorf("%s.%s: %w", v.name, name, err)
				}
				result = runtime + ".SliceView[" + e.viewType(u.Elem()) + ", " + child.name + "]"
				zero = result + "{}"
				body = runtime + ".BorrowSliceView(" + value + ", dtogenView" + child.name + ")"
				ownership = "The collection borrows stable backing and adapts elements lazily; derived views retain it."
			}
		case *types.Map:
			var err error
			result, body, err = e.mapView(u, value, all)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", v.name, name, err)
			}
			zero = result + "{}"
			ownership = "The map borrows stable backing and adapts present entries lazily in unspecified order; derived views retain it."
		default:
			return fmt.Errorf("%s.%s: unsupported view shape %s; use view:\"manual\" or view:\"-\"", v.name, name, t)
		}
	}
	guard := "v.value == nil"
	if optional {
		guard += " || " + expr + " == nil"
	}
	docs := name + " reads " + name + " in O(1) without copying sibling payloads."
	if optional {
		docs += "\nA zero view or nil field returns zero, false; present zero/empty values return true."
	} else {
		docs += "\nA zero view returns zero."
	}
	docs += "\n" + ownership + "\nAccess neither freezes nor synchronizes backing."
	source := sourceDoc(e.s.loaded.Package, v.dto.Elem(), "")
	if fieldDoc := sourceDoc(e.s.loaded.Package, v.dto.Elem(), name); fieldDoc != "" {
		source += "\n\n" + fieldDoc
	}
	e.viewDoc(docs, source)
	if optional {
		e.line("func (v %s) %s() (%s, bool) {if %s {return %s,false};return %s,true}", v.name, name, result, guard, zero, body)
	} else {
		e.line("func (v %s) %s() %s {if %s {return %s};return %s}", v.name, name, result, guard, zero, body)
	}
	return nil
}

// mapView never takes an address of a copied map entry. DTO adapters receive
// existing pointers, and only context-free selected children can be constructed.
func (e *emitter) mapView(m *types.Map, value string, all []typedView) (string, string, error) {
	key, ok := m.Key().Underlying().(*types.Basic)
	if !ok || key.Kind() != types.String {
		return "", "", fmt.Errorf("view maps require string-underlying keys")
	}
	t := m.Elem()
	elem := e.viewType(t)
	result, adapter := "", ""
	if scalar, ok := t.Underlying().(*types.Basic); ok && scalar.Kind() != types.UnsafePointer && scalar.Kind() != types.Invalid {
		result, adapter = elem, "func(x "+elem+") "+elem+" {return x}"
	} else if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		child, err := childViewFor(ptr.Elem(), all)
		if err != nil {
			return "", "", err
		}
		result, adapter = child.name, "dtogenView"+child.name
	} else {
		return "", "", fmt.Errorf("unsupported map element %s; use scalar values or pointers to selected context-free DTOs, or view:\"manual\"", t)
	}
	runtime := e.imports[viewsImportPath]
	if runtime == "" {
		runtime = "dtogenViews"
		e.imports[viewsImportPath] = runtime
	}
	return runtime + ".MapView[" + e.viewType(m.Key()) + ", " + elem + ", " + result + "]", runtime + ".BorrowMapView(" + value + ", " + adapter + ")", nil
}

func childViewFor(t types.Type, all []typedView) (typedView, error) {
	// typedViews has already rejected multiple context-free handles for one DTO.
	for _, v := range all {
		if v.constructible && types.Identical(t, v.dto.Elem()) {
			return v, nil
		}
	}
	return typedView{}, fmt.Errorf("no context-free child view for %s", t)
}

func (e *emitter) viewDoc(contract, source string) {
	for _, line := range strings.Split(contract, "\n") {
		e.line("// %s", line)
	}
	if strings.TrimSpace(source) != "" {
		e.line("//")
		e.line("// Source contract (DTO semantics, not a mutable accessor):")
		for _, line := range strings.Split(strings.TrimSpace(source), "\n") {
			e.line("// %s", line)
		}
	}
}

// sourceDoc finds owner comments through the loaded graph, preserving provisional
// notes verbatim while clearly separating mutable DTO semantics from accessors.
func sourceDoc(root *packages.Package, t types.Type, field string) string {
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return ""
	}
	var text string
	visitDependencies(root, func(pkg *packages.Package) {
		if pkg.Types != n.Obj().Pkg() {
			return
		}
		for _, file := range pkg.Syntax {
			for _, decl := range file.Decls {
				g, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, sp := range g.Specs {
					ts, ok := sp.(*ast.TypeSpec)
					if !ok || ts.Name.Name != n.Obj().Name() {
						continue
					}
					if field == "" {
						if ts.Doc != nil {
							text = ts.Doc.Text()
						} else if len(g.Specs) == 1 && g.Doc != nil {
							text = g.Doc.Text()
						}
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					for _, f := range st.Fields.List {
						for _, name := range f.Names {
							if name.Name == field {
								if f.Doc != nil {
									text = f.Doc.Text()
								}
								if f.Comment != nil {
									text += f.Comment.Text()
								}
							}
						}
					}
				}
			}
		}
	})
	return text
}

// mergeViewOverlay combines inferred view signatures with existing value
// signatures. All function bodies become load-only stubs; the final strict pass
// checks the actual rendered output.
func mergeViewOverlay(value, view []byte) ([]byte, error) {
	fs := token.NewFileSet()
	a, err := parser.ParseFile(fs, "value.go", value, 0)
	if err != nil {
		return nil, err
	}
	b, err := parser.ParseFile(fs, "view.go", view, 0)
	if err != nil {
		return nil, err
	}
	var imports, decls []ast.Decl
	for _, file := range []*ast.File{a, b} {
		for _, d := range file.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				imports = append(imports, d)
			} else {
				if fn, ok := d.(*ast.FuncDecl); ok {
					fn.Body = &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.CallExpr{
						Fun: ast.NewIdent("panic"), Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"load-only"`}},
					}}}}
				}
				decls = append(decls, d)
			}
		}
	}
	a.Decls = append(imports, decls...)
	a.Imports = nil
	var out bytes.Buffer
	if err := format.Node(&out, fs, a); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
