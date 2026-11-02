package cborgen

import (
	"fmt"
	"go/format"
	"go/scanner"
	"go/token"
	"go/types"
	"slices"
	"strings"
)

const implementationType = "cborgen"

// emitter writes typed, finite helpers. It never imports or executes owner code.
// Helpers deliberately operate on concrete Go values, not an intermediate tree.
type emitter struct {
	s       *schema
	b       strings.Builder
	imports map[string]string
}

func emit(s *schema) ([]byte, error) {
	e := &emitter{s: s, imports: map[string]string{
		codecImportPath:                  "cborgenCodec",
		"github.com/jcalabro/atmos/cbor": "cborgenCBOR",
	}}
	// Collect type imports before writing the deterministic import block.
	for _, n := range s.nodes {
		e.typeName(n)
	}
	hasMap, hasFields := false, false
	for _, n := range s.nodes {
		hasMap = hasMap || (n.wire && n.kind == shapeMap)
		hasFields = hasFields || (n.wire && n.kind == shapeStruct && len(wireFields(n)) > 0)
	}
	if hasMap {
		e.imports["slices"] = "cborgenSlices"
	}
	if hasFields {
		e.imports["fmt"] = "cborgenFmt"
	}
	e.line("// %s groups this output's stateless helpers; policy stays per call.", implementationType)
	e.line("// Clone/Equal traverse whole values, including excluded fields; codec helpers traverse persisted fields only.")
	e.line("// Read helpers admit declarations before allocation; public entry points discard partial results on failure.")
	e.line("type %s struct{}", implementationType)
	for _, root := range s.roots {
		e.root(root)
	}
	e.line("// %sPolicy holds resolved limits, never the caller's dictionary.", implementationType)
	e.line("type %sPolicy struct { limits cborgenCodec.Limits", implementationType)
	for i, name := range s.names {
		e.line("named%d int // @%s", i, name)
	}
	e.line("}")
	// Whole-value traversal includes excluded fields so Clone/Equal cannot lose state;
	// only wire-reachable shapes receive persistence helpers.
	for _, n := range s.nodes {
		if t := e.valueType(n); t != e.typeName(n) {
			e.line("// %s avoids helper-local shadowing of %s.", t, e.typeName(n))
			e.line("type %s = %s", t, e.typeName(n))
		}
		commentType := types.TypeString(n.typ, func(p *types.Package) string {
			if p == s.loaded.Package.Types {
				return ""
			}
			return p.Name()
		})
		e.line("// Helpers %d: %s.", n.id, commentType)
		e.clone(n)
		e.equal(n)
		if n.wire {
			e.check(n)
			e.appendValue(n)
			e.decode(n)
		}
	}
	if err := e.checkReserved(); err != nil {
		return nil, err
	}
	var out strings.Builder
	fmt.Fprintf(&out, "%s\n\npackage %s\n\nimport (\n", GeneratedMarker, s.loaded.Package.Name)
	paths := make([]string, 0, len(e.imports))
	for path := range e.imports {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		fmt.Fprintf(&out, "%s %q\n", e.imports[path], path)
	}
	out.WriteString(")\n\n")
	out.WriteString(e.b.String())
	data, err := format.Source([]byte(out.String()))
	if err != nil {
		return nil, fmt.Errorf("format generated code: %w", err)
	}
	return data, nil
}

// checkReserved includes package-level declarations and file-local import aliases.
func (e *emitter) checkReserved() error {
	reserved := []string{implementationType, implementationType + "Policy"}
	for _, n := range e.s.nodes {
		if t := e.valueType(n); t != e.typeName(n) {
			reserved = append(reserved, t)
		}
	}
	for _, alias := range e.imports {
		reserved = append(reserved, alias)
	}
	slices.Sort(reserved)
	for _, name := range reserved {
		if e.s.loaded.Package.Types.Scope().Lookup(name) != nil {
			return fmt.Errorf("handwritten declaration collides with generated %s", name)
		}
	}
	return nil
}

func (e *emitter) line(f string, args ...any) { fmt.Fprintf(&e.b, f+"\n", args...) }
func (e *emitter) typeName(n *shape) string {
	return types.TypeString(n.typ, func(p *types.Package) string {
		if p == e.s.loaded.Package.Types {
			return ""
		}
		if alias := e.imports[p.Path()]; alias != "" {
			return alias
		}
		alias := fmt.Sprintf("cborgenType%d", len(e.imports))
		e.imports[p.Path()] = alias
		return alias
	})
}

// valueType normally uses the actual Go type. Only shapes mentioning helper-local
// identifiers need a package alias, including containers of shadowed named types.
func (e *emitter) valueType(n *shape) string {
	t := e.typeName(n)
	var scan scanner.Scanner
	fset := token.NewFileSet()
	scan.Init(fset.AddFile("", -1, len(t)), []byte(t), nil, 0)
	for {
		_, tok, text := scan.Scan()
		if tok == token.EOF {
			return t
		}
		if tok == token.IDENT {
			switch text {
			case "h", "v", "w", "out", "limits", "data", "zero", "b", "err", "p", "r", "d", "cap", "x", "i", "k", "y", "ok", "count", "previous", "key", "keys", "seen", "a":
				return fmt.Sprintf("%sValue%d", implementationType, n.id)
			}
		}
	}
}

func (e *emitter) cap(n *shape) string {
	if n.kind == shapePointer {
		return e.cap(n.elem)
	}
	switch n.kind {
	case shapeString:
		return "p.limits.MaxStringBytes"
	case shapeBytes, shapeBytesArray:
		return "p.limits.MaxByteStringBytes"
	case shapeSlice:
		return "p.limits.MaxArrayElements"
	case shapeMap, shapeStruct:
		return "p.limits.MaxMapPairs"
	}
	return "1"
}
func (e *emitter) fieldCap(f field) string {
	maximum := e.cap(f.value)
	if f.cap != "" {
		return "int(min(uint64(" + maximum + "), uint64(" + f.cap + ")))"
	}
	if f.ref != "" {
		return fmt.Sprintf("p.named%d", slices.Index(e.s.names, f.ref))
	}
	return maximum
}
func wireFields(n *shape) []field {
	var fields []field
	for _, f := range n.fields {
		if !f.excluded {
			fields = append(fields, f)
		}
	}
	slices.SortFunc(fields, func(a, b field) int {
		if len(a.wireName) != len(b.wireName) {
			return len(a.wireName) - len(b.wireName)
		}
		return strings.Compare(a.wireName, b.wireName)
	})
	return fields
}

// references collects just this root's persisted policy names, not sibling roots.
func references(n *shape, seen map[*shape]bool, names map[string]bool) {
	if seen[n] {
		return
	}
	seen[n] = true
	for _, f := range n.fields {
		if !f.excluded {
			if f.ref != "" {
				names[f.ref] = true
			}
			references(f.value, seen, names)
		}
	}
	if n.elem != nil {
		references(n.elem, seen, names)
	}
	if n.key != nil {
		references(n.key, seen, names)
	}
}

func (e *emitter) root(n *shape) {
	t := e.typeName(n)
	id := n.id
	names := map[string]bool{}
	references(n, map[*shape]bool{}, names)
	// Each entry point resolves once, including Encode's internal Check pass.
	for _, op := range []string{"Check", "Encode", "Decode"} {
		zero := ""
		switch op {
		case "Check":
			e.line("// Check%s validates structure and explicit resource policy without sorting or output allocation.\n// Inputs and policy must remain stable; excluded fields and domain methods are not checked.", t)
			e.line("func Check%s(v %s, limits cborgenCodec.Limits) error {", t, t)
		case "Encode":
			e.line("// Encode%s checks structure and policy before emitting owned canonical bytes.\n// Failure returns nil bytes; no domain validation or custom methods are invoked.", t)
			e.line("func Encode%s(v %s, limits cborgenCodec.Limits) ([]byte,error) {", t, t)
			zero = "nil, "
		case "Decode":
			e.line("// Decode%s strictly decodes one canonical value into owned storage.\n// Required collections normalize to nonnil empty; excluded fields remain zero.\n// Failure returns the zero DTO. Domain semantics remain the caller's responsibility.", t)
			e.line("func Decode%s(data []byte, limits cborgenCodec.Limits) (%s,error) {", t, t)
			e.line("var zero %s", e.valueType(n))
			zero = "zero, "
		}
		e.line("b,err:=cborgenCodec.NewBudget(limits);if err!=nil{return %serr}", zero)
		e.line("// Resolve this root's names even for absent optional branches, so admission does not depend on presence.")
		e.line("p:=%sPolicy{limits:limits};p.limits.Named=nil", implementationType)
		e.line("h:=%s{}", implementationType)
		for i, name := range e.s.names {
			if names[name] {
				e.line("p.named%d,err=limits.NamedLimit(%q);if err!=nil{return %serr}", i, name, zero)
			}
		}
		if op == "Decode" {
			e.line("_ = b")
			e.line("r,err:=cborgenCodec.NewReader(data,limits);if err!=nil{return zero,err}")
			e.line("v,err:=h.read%d(&r,p,0,%s);if err!=nil{return zero,err}", id, e.cap(n))
			e.line("if err=r.Done();err!=nil{return zero,err};return v,nil")
		} else {
			if op == "Encode" {
				e.line("// Complete Check first: rejected values must not allocate output or sorting scratch.")
			}
			e.line("if err=h.check%d(v,p,&b,0,%s);err!=nil{return %serr}", id, e.cap(n), zero)
			if op == "Check" {
				e.line("return nil")
			} else {
				e.line("return h.append%d(make([]byte,0,b.EstimatedBytes()),v),nil", id)
			}
		}
		e.line("}")
	}
	e.line("// Clone returns an independent mutable copy of the whole DTO, including excluded fields.\n// It preserves nil collections and pointer absence, but not sharing topology or capacity.\n// It performs no validation or resource admission; the input must remain stable.")
	e.line("func (v %s) Clone() %s {return (%s{}).clone%d(v)}", t, t, implementationType, id)
	e.line("// Equal compares the whole DTO by value, including excluded fields, without validation.\n// Nil and empty collections compare equally; absent pointers differ from present zero values.\n// Both inputs must remain stable during the call.")
	e.line("func (v %s) Equal(other %s) bool {return (%s{}).equal%d(v,other)}", t, t, implementationType, id)
}

func (e *emitter) clone(n *shape) {
	t := e.valueType(n)
	e.line("func (h %s) clone%d(v %s) %s {", implementationType, n.id, t, t)
	switch n.kind {
	case shapePointer:
		e.line("if v==nil{return nil};x:=h.clone%d(*v);return (%s)(&x)", n.elem.id, t)
	case shapeSlice, shapeBytes:
		e.line("if v==nil{return nil};out:=make(%s,len(v));for i:=range v{out[i]=h.clone%d(v[i])};return out", t, n.elem.id)
	case shapeMap:
		e.line("if v==nil{return nil};out:=make(%s,len(v));for k,x:=range v{out[k]=h.clone%d(x)};return out", t, n.elem.id)
	case shapeStruct:
		e.line("out:=v")
		for _, f := range n.fields {
			e.line("out.%s=h.clone%d(v.%s)", f.name, f.value.id, f.name)
		}
		e.line("return out")
	default:
		e.line("return v")
	}
	e.line("}")
}
func (e *emitter) equal(n *shape) {
	t := e.valueType(n)
	e.line("func (h %s) equal%d(v,w %s) bool {", implementationType, n.id, t)
	switch n.kind {
	case shapePointer:
		e.line("if v==nil||w==nil{return v==nil&&w==nil};return h.equal%d(*v,*w)", n.elem.id)
	case shapeBytes, shapeBytesArray, shapeSlice:
		e.line("if len(v)!=len(w){return false};for i:=range v{if !h.equal%d(v[i],w[i]){return false}};return true", n.elem.id)
	case shapeMap:
		e.line("if len(v)!=len(w){return false};for k,x:=range v{y,ok:=w[k];if !ok||!h.equal%d(x,y){return false}};return true", n.elem.id)
	case shapeStruct:
		for _, f := range n.fields {
			e.line("if !h.equal%d(v.%s,w.%s){return false}", f.value.id, f.name, f.name)
		}
		e.line("return true")
	default:
		e.line("return v==w")
	}
	e.line("}")
}

func (e *emitter) check(n *shape) {
	e.line("func (h %s) check%d(v %s,p %sPolicy,b *cborgenCodec.Budget,d,cap int) error {", implementationType, n.id, e.valueType(n), implementationType)
	switch n.kind {
	case shapeBool, shapeInt, shapeUint:
		e.line("return b.CheckScalar(d)")
	case shapeString:
		e.line("return b.CheckString(string(v),d,cap)")
	case shapeBytes, shapeBytesArray:
		e.line("return b.CheckBytes(len(v),d,cap)")
	case shapePointer:
		e.line("if v==nil{return cborgenCodec.ErrInvalidData};return h.check%d(*v,p,b,d,cap)", n.elem.id)
	case shapeSlice:
		e.line("if err:=b.CheckArray(len(v),d,cap);err!=nil{return err}")
		e.line("for i,x:=range v{if err:=h.check%d(x,p,b,d+1,%s);err!=nil{return cborgenFmt.Errorf(\"index %%d: %%w\",i,err)}};return nil", n.elem.id, e.cap(n.elem))
	case shapeMap:
		e.line("if err:=b.CheckMap(len(v),d,cap);err!=nil{return err}")
		e.line("for k,x:=range v{if err:=b.CheckString(string(k),d+1,p.limits.MaxStringBytes);err!=nil{return err};if err:=h.check%d(x,p,b,d+1,%s);err!=nil{return err}};return nil", n.elem.id, e.cap(n.elem))
	case shapeStruct:
		fields := wireFields(n)
		e.count(fields, "v")
		e.line("if err:=b.CheckMap(count,d,cap);err!=nil{return err}")
		for _, f := range fields {
			if f.optional {
				e.line("if v.%s!=nil {", f.name)
			}
			e.line("if err:=b.CheckString(%q,d+1,p.limits.MaxStringBytes);err!=nil{return cborgenFmt.Errorf(%q,err)}", f.wireName, f.name+": %w")
			e.line("if err:=h.check%d(v.%s,p,b,d+1,%s);err!=nil{return cborgenFmt.Errorf(%q,err)}", f.value.id, f.name, e.fieldCap(f), f.name+": %w")
			if f.optional {
				e.line("}")
			}
		}
		e.line("return nil")
	}
	e.line("}")
}
func (e *emitter) count(fields []field, v string) {
	required := 0
	for _, f := range fields {
		if !f.optional {
			required++
		}
	}
	e.line("count:=%d", required)
	for _, f := range fields {
		if f.optional {
			e.line("if %s.%s!=nil{count++}", v, f.name)
		}
	}
}

func (e *emitter) appendValue(n *shape) {
	e.line("func (h %s) append%d(out []byte,v %s) []byte {", implementationType, n.id, e.valueType(n))
	switch n.kind {
	case shapeBool:
		e.line("return cborgenCBOR.AppendBool(out,bool(v))")
	case shapeInt:
		e.line("return cborgenCBOR.AppendInt(out,int64(v))")
	case shapeUint:
		e.line("return cborgenCBOR.AppendUint(out,uint64(v))")
	case shapeString:
		e.line("return cborgenCBOR.AppendText(out,string(v))")
	case shapeBytes, shapeBytesArray:
		// []NamedByte cannot convert to []byte. Emit its header and elements directly.
		e.line("out=cborgenCBOR.AppendBytesHeader(out,uint64(len(v)));for _,x:=range v{out=append(out,byte(x))};return out")
	case shapePointer:
		e.line("return h.append%d(out,*v)", n.elem.id)
	case shapeSlice:
		e.line("out=cborgenCBOR.AppendArrayHeader(out,uint64(len(v)));for _,x:=range v{out=h.append%d(out,x)};return out", n.elem.id)
	case shapeMap:
		e.line("keys:=make([]%s,0,len(v));for k:=range v{keys=append(keys,k)}", e.valueType(n.key))
		e.line("cborgenSlices.SortFunc(keys,func(a,b %s)int{if len(a)!=len(b){return len(a)-len(b)};if a<b{return -1};if a>b{return 1};return 0})", e.valueType(n.key))
		e.line("out=cborgenCBOR.AppendMapHeader(out,uint64(len(v)));for _,k:=range keys{out=cborgenCBOR.AppendText(out,string(k));out=h.append%d(out,v[k])};return out", n.elem.id)
	case shapeStruct:
		fields := wireFields(n)
		e.count(fields, "v")
		e.line("out=cborgenCBOR.AppendMapHeader(out,uint64(count))")
		for _, f := range fields {
			if f.optional {
				e.line("if v.%s!=nil{", f.name)
			}
			e.line("out=cborgenCBOR.AppendText(out,%q);out=h.append%d(out,v.%s)", f.wireName, f.value.id, f.name)
			if f.optional {
				e.line("}")
			}
		}
		e.line("return out")
	}
	e.line("}")
}

func (e *emitter) decode(n *shape) {
	t := e.valueType(n)
	e.line("func (h %s) read%d(r *cborgenCodec.Reader,p %sPolicy,d,cap int) (v %s,err error) {", implementationType, n.id, implementationType, t)
	switch n.kind {
	case shapeBool, shapeString, shapeInt, shapeUint:
		method := map[shapeKind]string{shapeBool: "Bool", shapeString: "String", shapeInt: "Int", shapeUint: "Uint"}[n.kind]
		args := "d"
		if n.kind == shapeString {
			args += ",cap"
		}
		e.line("x,err:=r.%s(%s);if err!=nil{return v,err}", method, args)
		if n.kind == shapeInt && n.bits < 64 {
			e.line("if x < -(1<<%d)||x>(1<<%d)-1{return v,cborgenCodec.ErrInvalidData}", n.bits-1, n.bits-1)
		}
		if n.kind == shapeUint && n.bits < 64 {
			e.line("if x>(1<<%d)-1{return v,cborgenCodec.ErrInvalidData}", n.bits)
		}
		e.line("return %s(x),nil", t)
	case shapeBytes, shapeBytesArray:
		if n.kind == shapeBytesArray {
			e.line("x,err:=r.FixedBytes(d,cap,%d);if err!=nil{return v,err}", n.length)
		} else {
			e.line("x,err:=r.Bytes(d,cap);if err!=nil{return v,err}")
			e.line("v=make(%s,len(x))", t)
		}
		e.line("for i:=range x{v[i]=%s(x[i])};return v,nil", e.valueType(n.elem))
	case shapePointer:
		e.line("x,err:=h.read%d(r,p,d,cap);if err!=nil{return v,err};return (%s)(&x),nil", n.elem.id, t)
	case shapeSlice:
		e.line("count,err:=r.Array(d,cap);if err!=nil{return v,err};v=make(%s,count)", t)
		e.line("for i:=range v{v[i],err=h.read%d(r,p,d+1,%s);if err!=nil{return v,cborgenFmt.Errorf(\"index %%d: %%w\",i,err)}};return v,nil", n.elem.id, e.cap(n.elem))
	case shapeMap:
		e.line("count,err:=r.Map(d,cap);if err!=nil{return v,err};v=make(%s,count);previous:=\"\"", t)
		e.line("for i:=0;i<count;i++{key,err:=r.String(d+1,p.limits.MaxStringBytes);if err!=nil{return v,err}")
		e.order()
		e.line("x,err:=h.read%d(r,p,d+1,%s);if err!=nil{return v,err};v[%s(key)]=x};return v,nil", n.elem.id, e.cap(n.elem), e.valueType(n.key))
	case shapeStruct:
		fields := wireFields(n)
		e.line("count,err:=r.Map(d,cap);if err!=nil{return v,err};previous:=\"\"")
		if len(fields) > 0 {
			e.line("var seen [%d]bool", len(fields))
		}
		e.line("for i:=0;i<count;i++{key,err:=r.String(d+1,p.limits.MaxStringBytes);if err!=nil{return v,err}")
		e.order()
		e.line("switch key {")
		for i, f := range fields {
			e.line("case %q:seen[%d]=true;v.%s,err=h.read%d(r,p,d+1,%s);if err!=nil{return v,cborgenFmt.Errorf(%q,err)}", f.wireName, i, f.name, f.value.id, e.fieldCap(f), f.name+": %w")
		}
		e.line("default:return v,cborgenCodec.ErrInvalidData\n}}")
		for i, f := range fields {
			if !f.optional {
				e.line("if !seen[%d]{return v,cborgenCodec.ErrInvalidData}", i)
			}
		}
		e.line("return v,nil")
	}
	e.line("}")
}
func (e *emitter) order() {
	e.line("// Strict canonical key ordering also rejects duplicates before assignment can overwrite them.")
	e.line("if i>0&&(len(key)<len(previous)||(len(key)==len(previous)&&key<=previous)){return v,cborgenCodec.ErrInvalidData};previous=key")
}
