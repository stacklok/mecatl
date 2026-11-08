package dtogen

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
)

const viewsImportPath = "github.com/stacklok/mecatl/engine/kernel/views"

// viewProjection classifies every DTO field. Backing names a handwritten private
// pointer field; its type selects the DTO without a second import/name language.
// Context methods are checked but never generated or used as collection adapters.
type viewProjection struct {
	View    string       `json:"view"`
	Backing string       `json:"backing"`
	Fields  []viewField  `json:"fields"`
	Context []viewMethod `json:"context,omitempty"`
}
type viewField struct {
	Field    string `json:"field"`
	Method   string `json:"method,omitempty"`
	Exclude  bool   `json:"exclude,omitempty"`
	Override string `json:"override,omitempty"` // exact Go func type, including semantic presence
}
type viewMethod struct {
	Method    string `json:"method"`
	Signature string `json:"signature"`
}

func validateViewConfig(c LoadConfig, emit capabilities) error {
	if emit.view != (c.Views != "") {
		return fmt.Errorf("view capability requires -views projection map (and vice versa)")
	}
	if c.Views != "" && (filepath.Base(c.Views) != c.Views || !strings.HasSuffix(c.Views, ".json")) {
		return fmt.Errorf("-views must be a package-local JSON filename")
	}
	if (emit.cbor || emit.clone || emit.equal) && len(c.Types) == 0 {
		return fmt.Errorf("value/codec capabilities require local -types roots")
	}
	if len(c.Types) > 0 && !emit.cbor && !emit.clone && !emit.equal {
		return fmt.Errorf("view-only destinations select backing through -views, not -types")
	}
	return nil
}

func readViewMap(c LoadConfig) ([]viewProjection, []byte, error) {
	if filepath.Base(c.Views) != c.Views || !strings.HasSuffix(c.Views, ".json") {
		return nil, nil, fmt.Errorf("-views must be a package-local JSON filename")
	}
	data, err := os.ReadFile(filepath.Join(c.Dir, c.Views))
	if err != nil {
		return nil, nil, err
	}
	var projections []viewProjection
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&projections); err != nil {
		return nil, nil, fmt.Errorf("view map: %w", err)
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, nil, fmt.Errorf("view map must contain one JSON array")
	}
	if len(projections) == 0 {
		return nil, nil, fmt.Errorf("view map must select at least one projection")
	}
	return projections, data, nil
}

// typedView ties one local immutable handle to its concrete DTO backing. Extra
// layout fields imply enclosing context and prohibit automatic child construction.
type typedView struct {
	projection    viewProjection
	named         *types.Named
	dto           *types.Pointer
	fields        *types.Struct
	constructible bool
}

func (e *emitter) viewType(t types.Type) string { return e.typeName(&shape{typ: t}) }

// emitViews validates declarative projections and renders concrete borrowed
// accessors; it never evaluates handwritten method bodies.
func (e *emitter) emitViews(c LoadConfig) error {
	projections, data, err := readViewMap(c)
	if err != nil {
		return err
	}
	pkg := e.s.loaded.Package
	views, err := typedViews(projections, pkg)
	if err != nil {
		return err
	}
	e.writeViewFingerprint(data, views, pkg)
	for _, view := range views {
		if err := e.emitView(view, views, pkg); err != nil {
			return err
		}
	}
	return nil
}

func typedViews(projections []viewProjection, pkg *packages.Package) ([]typedView, error) {
	views := make([]typedView, 0, len(projections))
	seen := map[string]bool{}
	for _, projection := range projections {
		if !validViewName(projection.View) || !validViewName(projection.Backing) || token.IsExported(projection.Backing) || seen[projection.View] {
			return nil, fmt.Errorf("invalid or duplicate view/backing %s.%s", projection.View, projection.Backing)
		}
		seen[projection.View] = true
		obj, ok := pkg.Types.Scope().Lookup(projection.View).(*types.TypeName)
		if !ok || obj.IsAlias() {
			return nil, fmt.Errorf("view %s must be a handwritten local defined struct", projection.View)
		}
		named, ok := obj.Type().(*types.Named)
		if !ok || named.TypeParams().Len() != 0 {
			return nil, fmt.Errorf("view %s must be nongeneric", projection.View)
		}
		layout, ok := named.Underlying().(*types.Struct)
		if !ok {
			return nil, fmt.Errorf("view %s must be a struct", projection.View)
		}
		backing, err := viewBacking(layout, projection)
		if err != nil {
			return nil, err
		}
		ptr, ok := backing.(*types.Pointer)
		if !ok {
			return nil, fmt.Errorf("view %s backing %s must be a DTO pointer", projection.View, projection.Backing)
		}
		fields, ok := ptr.Elem().Underlying().(*types.Struct)
		if !ok {
			return nil, fmt.Errorf("view %s backing must point to a struct", projection.View)
		}
		views = append(views, typedView{projection, named, ptr, fields, layout.NumFields() == 1})
	}
	return views, nil
}

func viewBacking(layout *types.Struct, projection viewProjection) (types.Type, error) {
	var backing types.Type
	for i := 0; i < layout.NumFields(); i++ {
		field := layout.Field(i)
		if field.Exported() || field.Embedded() {
			return nil, fmt.Errorf("view %s layout must retain backing/context in private nonembedded fields", projection.View)
		}
		if field.Name() == projection.Backing {
			backing = field.Type()
		}
	}
	return backing, nil
}

func (e *emitter) writeViewFingerprint(data []byte, views []typedView, pkg *packages.Package) {
	hash := sha256.New()
	_, _ = hash.Write(data)
	for _, view := range views {
		_, _ = fmt.Fprintf(hash, "\n%q", types.TypeString(view.dto, func(p *types.Package) string { return p.Path() }))
		_, _ = fmt.Fprintf(hash, "\n%q", types.TypeString(view.fields, func(p *types.Package) string { return p.Path() }))
		_, _ = fmt.Fprintf(hash, "\n%q", sourceDoc(pkg, view.dto.Elem(), ""))
		for i := 0; i < view.fields.NumFields(); i++ {
			_, _ = fmt.Fprintf(hash, "\n%q", sourceDoc(pkg, view.dto.Elem(), view.fields.Field(i).Name()))
		}
	}
	e.line("// Projection map and source documentation fingerprint: %x.", hash.Sum(nil))
}

func (e *emitter) emitView(v typedView, views []typedView, pkg *packages.Package) error {
	projection := v.projection
	// Type docs live alongside the adapter when layouts stay handwritten.
	e.viewDoc("Backing contract for "+projection.View+".", sourceDoc(pkg, v.dto.Elem(), ""))
	if v.constructible {
		name := "dtogenView" + projection.View
		if err := e.reserveViewName(name); err != nil {
			return err
		}
		e.line("// %s borrows stable backing; it neither freezes nor synchronizes access.", name)
		e.line("func %s(value %s) %s { return %s{%s:value} }", name, e.viewType(v.dto), projection.View, projection.View, projection.Backing)
	}
	// A pointer conversion catches additions, removals and field type changes at
	// compile time, even before the deterministic source freshness check is run.
	if viewShapeNameable(v.fields, pkg.Types) {
		e.line("var _ = (*%s)((%s)(nil))", e.viewType(v.fields), e.viewType(v.dto))
	}
	methods, err := e.emitViewFields(v, views, pkg)
	if err != nil {
		return err
	}
	if err := e.checkViewContext(v, methods); err != nil {
		return err
	}
	for i := 0; i < v.named.NumMethods(); i++ {
		method := v.named.Method(i)
		if !methods[method.Name()] && !e.generatedPosition(method.Pos()) {
			return fmt.Errorf("%s.%s: unclassified handwritten method; declare a context method or override", projection.View, method.Name())
		}
	}
	return nil
}

func (e *emitter) emitViewFields(v typedView, views []typedView, pkg *packages.Package) (map[string]bool, error) {
	covered := map[string]bool{}
	methods := map[string]bool{}
	for _, f := range v.projection.Fields {
		if !validViewName(f.Field) || covered[f.Field] {
			return nil, fmt.Errorf("%s: invalid or duplicate field %s", v.projection.View, f.Field)
		}
		covered[f.Field] = true
		field := viewFieldByName(v.fields, f.Field)
		if field == nil {
			return nil, fmt.Errorf("%s: unknown field %s", v.projection.View, f.Field)
		}
		if f.Exclude {
			if f.Method != "" || f.Override != "" {
				return nil, fmt.Errorf("%s.%s: excluded field cannot have a method", v.projection.View, f.Field)
			}
			continue
		}
		if !validViewName(f.Method) || methods[f.Method] {
			return nil, fmt.Errorf("%s: invalid or duplicate method %s", v.projection.View, f.Method)
		}
		methods[f.Method] = true
		if f.Override != "" {
			if err := e.checkViewMethod(v, f.Method, f.Override); err != nil {
				return nil, err
			}
			// Handwritten documentation is authoritative; it is never copied over.
			continue
		}
		if !field.Exported() && field.Pkg() != pkg.Types {
			return nil, fmt.Errorf("%s.%s: inaccessible DTO field", v.projection.View, f.Field)
		}
		if err := e.noViewMethod(v, f.Method); err != nil {
			return nil, err
		}
		if err := e.emitViewField(v, f, field, views); err != nil {
			return nil, err
		}
	}
	for i := 0; i < v.fields.NumFields(); i++ {
		if !covered[v.fields.Field(i).Name()] {
			return nil, fmt.Errorf("%s: uncovered backing field %s", v.projection.View, v.fields.Field(i).Name())
		}
	}
	return methods, nil
}

func viewFieldByName(fields *types.Struct, name string) *types.Var {
	for i := 0; i < fields.NumFields(); i++ {
		if fields.Field(i).Name() == name {
			return fields.Field(i)
		}
	}
	return nil
}

func (e *emitter) checkViewContext(v typedView, methods map[string]bool) error {
	for _, method := range v.projection.Context {
		if !validViewName(method.Method) || methods[method.Method] {
			return fmt.Errorf("%s: invalid or duplicate context method %s", v.projection.View, method.Method)
		}
		methods[method.Method] = true
		if err := e.checkViewMethod(v, method.Method, method.Signature); err != nil {
			return err
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

func validViewName(s string) bool {
	return token.IsIdentifier(s) && s != "_" && !token.Lookup(s).IsKeyword()
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
		return fmt.Errorf("%s.%s: handwritten method/field collision; declare an explicit override", v.projection.View, name)
	}
	return nil
}

// checkViewMethod requires an explicitly declared, exact value-receiver method.
// Evaluating a func type in the view declaration's source scope resolves ordinary
// Go import aliases without adding an executable configuration language.
func (e *emitter) checkViewMethod(v typedView, name, signature string) error {
	pkg := e.s.loaded.Package
	obj, _, _ := types.LookupFieldOrMethod(v.named, false, pkg.Types, name)
	fn, ok := obj.(*types.Func)
	if !ok || e.generatedPosition(fn.Pos()) {
		return fmt.Errorf("%s.%s: missing/stale handwritten override or context method", v.projection.View, name)
	}
	actual := fn.Type().(*types.Signature)
	expected, err := types.Eval(pkg.Fset, pkg.Types, v.named.Obj().Pos(), signature)
	if err != nil {
		return fmt.Errorf("%s.%s: invalid declared signature: %w", v.projection.View, name, err)
	}
	sig, ok := expected.Type.(*types.Signature)
	if !ok || !expected.IsType() || !types.Identical(actual.Recv().Type(), v.named) || !types.Identical(actual, sig) {
		return fmt.Errorf("%s.%s: override must have exact declared signature %s and a value receiver", v.projection.View, name, signature)
	}
	return nil
}

func (e *emitter) emitViewField(v typedView, f viewField, field *types.Var, all []typedView) error {
	p := v.projection
	t := field.Type()
	optional := false
	if ptr, ok := types.Unalias(t).(*types.Pointer); ok {
		optional = true
		t = ptr.Elem()
	}
	expr := "v." + p.Backing + "." + f.Field
	value := expr
	if optional {
		value = "*" + expr
	}
	var result, body, zero, ownership string
	switch u := t.Underlying().(type) {
	case *types.Basic:
		if u.Kind() == types.UnsafePointer || u.Kind() == types.Invalid {
			return fmt.Errorf("%s.%s: unsupported scalar", p.View, f.Field)
		}
		result = e.viewType(t)
		zero = "*new(" + result + ")"
		body = value
		ownership = "The receiver retains stable backing for its lifetime."
	case *types.Struct:
		child, err := projectionFor(t, all)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", p.View, f.Field, err)
		}
		result = child.projection.View
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
			child, err := projectionFor(u.Elem(), all)
			if err != nil {
				return fmt.Errorf("%s.%s: %w", p.View, f.Field, err)
			}
			result = runtime + ".SliceView[" + e.viewType(u.Elem()) + ", " + child.projection.View + "]"
			zero = result + "{}"
			body = runtime + ".BorrowSliceView(" + value + ", dtogenView" + child.projection.View + ")"
			ownership = "The collection borrows stable backing and adapts elements lazily; derived views retain it."
		}
	default:
		return fmt.Errorf("%s.%s: unsupported view shape %s; explicitly override or exclude it", p.View, f.Field, t)
	}
	guard := "v." + p.Backing + " == nil"
	if optional {
		guard += " || " + expr + " == nil"
	}
	docs := f.Method + " reads " + f.Field + " in O(1) without copying sibling payloads."
	if optional {
		docs += "\nA zero view or nil field returns zero, false; present zero/empty values return true."
	} else {
		docs += "\nA zero view returns zero."
	}
	docs += "\n" + ownership + "\nAccess neither freezes nor synchronizes backing."
	source := sourceDoc(e.s.loaded.Package, v.dto.Elem(), "")
	if fieldDoc := sourceDoc(e.s.loaded.Package, v.dto.Elem(), f.Field); fieldDoc != "" {
		source += "\n\n" + fieldDoc
	}
	e.viewDoc(docs, source)
	if optional {
		e.line("func (v %s) %s() (%s, bool) {if %s {return %s,false};return %s,true}", p.View, f.Method, result, guard, zero, body)
	} else {
		e.line("func (v %s) %s() %s {if %s {return %s};return %s}", p.View, f.Method, result, guard, zero, body)
	}
	return nil
}
func projectionFor(t types.Type, all []typedView) (typedView, error) {
	var found *typedView
	for i := range all {
		v := &all[i]
		if types.Identical(t, v.dto.Elem()) && v.constructible {
			if found != nil {
				return typedView{}, fmt.Errorf("ambiguous child projections for %s", t)
			}
			found = v
		}
	}
	if found == nil {
		return typedView{}, fmt.Errorf("no context-free child projection for %s", t)
	}
	return *found, nil
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
