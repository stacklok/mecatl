package cborgen

import (
	"fmt"
	"go/types"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// schema is a finite, acyclic static type graph. Whole-value edges include excluded
// fields; wire edges are validated separately so excluded subtrees need no tags.
type schema struct {
	loaded *Loaded
	nodes  []*shape
	byType map[types.Type]*shape
	roots  []*shape
}

type shapeKind uint8

const (
	shapeBool shapeKind = iota
	shapeString
	shapeInt
	shapeUint
	shapePointer
	shapeSlice
	shapeBytes
	shapeBytesArray
	shapeMap
	shapeStruct
	shapeOwner
)

// shape owns one concrete helper type and its supported underlying vocabulary.
// wire marks nodes reached by persistence; all nodes participate in value methods.
type shape struct {
	typ            types.Type
	id             int
	kind           shapeKind
	bits           int
	length         int64
	elem, key      *shape
	fields         []field
	wire           bool
	validator      string
	representation string
}

// field keeps declaration order for whole-value helpers, and explicit wire metadata
// for separately sorted persistence traversal.
type field struct {
	name, wireName     string
	value              *shape
	optional, excluded bool
	cap                string
}

func buildSchema(loaded *Loaded) (*schema, error) {
	s := &schema{loaded: loaded, byType: make(map[types.Type]*shape)}
	for _, root := range loaded.Roots {
		n, err := s.visit(root, make(map[types.Type]bool))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", loaded.Package.Fset.Position(root.Obj().Pos()), err)
		}
		s.roots = append(s.roots, n)
	}
	for _, root := range s.roots {
		if err := s.persist(root); err != nil {
			return nil, fmt.Errorf("%s: %w", loaded.Package.Fset.Position(root.typ.(*types.Named).Obj().Pos()), err)
		}
	}
	return s, nil
}

// visit validates whole-value accessibility and supported shapes, even when no
// wire traversal reaches a field. Active edges reject cycles before memoization.
//
//nolint:gocyclo // Keep the exhaustive supported Go-shape switch and its per-shape rejection rules together.
func (s *schema) visit(t types.Type, active map[types.Type]bool) (*shape, error) {
	if genericAlias(t) {
		return nil, fmt.Errorf("%s: generic types unsupported", t)
	}
	t = types.Unalias(t)
	if active[t] {
		return nil, fmt.Errorf("%s: recursive type graph", t)
	}
	if n := s.byType[t]; n != nil {
		return n, nil
	}
	if named, ok := t.(*types.Named); ok {
		if named.TypeParams().Len() != 0 || named.TypeArgs().Len() != 0 {
			return nil, fmt.Errorf("%s: generic types unsupported", t)
		}
		if obj := named.Obj(); obj.Pkg() != nil && obj.Pkg() != s.loaded.Package.Types && !obj.Exported() {
			return nil, fmt.Errorf("%s: inaccessible imported named type", t)
		}
	}
	n := &shape{typ: t, id: len(s.nodes)}
	s.nodes = append(s.nodes, n)
	s.byType[t] = n
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj.Pkg() != nil && obj.Pkg() != s.loaded.Package.Types {
			owner := s.loaded.Owners[obj.Pkg().Path()]
			if owner == nil || !slices.Contains(owner.Names, obj.Name()) {
				return nil, fmt.Errorf("%s: imported type requires an owner go:generate declaration selecting its family", t)
			}
			n.kind = shapeOwner
			return n, nil
		}
		n.validator = s.loaded.Validators[obj.Name()]
		if n.validator != "" {
			m, _, _ := types.LookupFieldOrMethod(named, false, obj.Pkg(), n.validator)
			fn, ok := m.(*types.Func)
			if !ok {
				return nil, fmt.Errorf("%s: validator %s must be a local value receiver method", t, n.validator)
			}
			sig := fn.Type().(*types.Signature)
			if !types.Identical(sig.Recv().Type(), named) || sig.Params().Len() != 0 || !returnsError(sig) {
				return nil, fmt.Errorf("%s: validator %s must have signature func (T) %s() error", t, n.validator, n.validator)
			}
		}
		if _, ok := named.Underlying().(*types.Pointer); ok {
			return nil, fmt.Errorf("%s: defined pointer types cannot have receiver methods; use ordinary pointers or aliases", t)
		}
	}
	active[t] = true
	defer delete(active, t)
	child := func(t types.Type) (*shape, error) { return s.visit(t, active) }
	var err error
	switch u := t.Underlying().(type) {
	case *types.Basic:
		switch u.Kind() {
		case types.Bool:
			n.kind = shapeBool
		case types.String:
			n.kind = shapeString
		case types.Int8, types.Int16, types.Int32, types.Int64:
			n.kind = shapeInt
			n.bits = map[types.BasicKind]int{types.Int8: 8, types.Int16: 16, types.Int32: 32, types.Int64: 64}[u.Kind()]
		case types.Uint8, types.Uint16, types.Uint32, types.Uint64:
			n.kind = shapeUint
			n.bits = map[types.BasicKind]int{types.Uint8: 8, types.Uint16: 16, types.Uint32: 32, types.Uint64: 64}[u.Kind()]
		default:
			err = fmt.Errorf("unsupported scalar %s (use fixed-width integers, bool or string)", t)
		}
	case *types.Pointer:
		n.kind = shapePointer
		if _, ok := types.Unalias(u.Elem()).Underlying().(*types.Pointer); ok {
			err = fmt.Errorf("double-pointer chains unsupported")
			break
		}
		n.elem, err = child(u.Elem())
	case *types.Slice:
		n.kind = shapeSlice
		n.elem, err = child(u.Elem())
		if isByte(u.Elem()) {
			n.kind = shapeBytes
		}
	case *types.Array:
		n.kind = shapeBytesArray
		n.length = u.Len()
		if !isByte(u.Elem()) {
			err = fmt.Errorf("only fixed byte arrays supported")
			break
		}
		n.elem, err = child(u.Elem())
	case *types.Map:
		n.kind = shapeMap
		if b, ok := types.Unalias(u.Key()).Underlying().(*types.Basic); !ok || b.Kind() != types.String {
			err = fmt.Errorf("map keys must have string underlying type")
			break
		}
		n.key, err = child(u.Key())
		if err == nil {
			n.elem, err = child(u.Elem())
		}
	case *types.Struct:
		n.kind = shapeStruct
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if f.Embedded() {
				err = fmt.Errorf("field %s: embedding unsupported", f.Name())
				break
			}
			if f.Name() == "_" || (!f.Exported() && f.Pkg() != s.loaded.Package.Types) {
				err = fmt.Errorf("field %s: inaccessible field", f.Name())
				break
			}
			v, e := child(f.Type())
			if e != nil {
				err = fmt.Errorf("field %s: %w", f.Name(), e)
				break
			}
			n.fields = append(n.fields, field{name: f.Name(), value: v})
		}
	default:
		err = fmt.Errorf("unsupported shape %s", t)
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", t, err)
	}
	return n, nil
}

// genericAlias finds generic declarations or instantiations before Unalias erases
// intermediate aliases. An ordinary alias chain may otherwise conceal a generic
// instantiation.
func genericAlias(t types.Type) bool {
	for {
		alias, ok := t.(*types.Alias)
		if !ok {
			return false
		}
		if alias.TypeParams().Len() != 0 || alias.TypeArgs().Len() != 0 {
			return true
		}
		t = alias.Rhs()
	}
}

func (s *schema) hasValidator(n *shape) bool {
	if n.validator != "" {
		return true
	}
	if named, ok := n.typ.(*types.Named); ok && named.Obj().Pkg() != nil && named.Obj().Pkg() != s.loaded.Package.Types {
		owner := s.loaded.Owners[named.Obj().Pkg().Path()]
		return owner != nil && owner.Validators[named.Obj().Name()] != ""
	}
	return false
}

func isByte(t types.Type) bool {
	b, ok := types.Unalias(t).Underlying().(*types.Basic)
	return ok && b.Kind() == types.Uint8
}

// persist applies mapping rules only along persisted edges. Custom contracts are
// rejected rather than executed; ordinary domain methods have no effect.
func (s *schema) persist(n *shape) error {
	if n.wire {
		return nil
	}
	n.wire = true
	if (n.kind == shapeBytes || n.kind == shapeBytesArray) && s.hasValidator(n.elem) {
		return fmt.Errorf("%s: packed named-byte element has a declared validator; use ordinary bytes or unpacked values", n.typ)
	}
	if n.kind == shapeOwner {
		named := n.typ.(*types.Named)
		if !s.loaded.Owners[named.Obj().Pkg().Path()].Wire[named.Obj().Name()] {
			return fmt.Errorf("%s: imported type is excluded-only in its owner; select it as a persisted root", n.typ)
		}
		return nil
	}
	for _, t := range []types.Type{n.typ, types.NewPointer(n.typ)} {
		ms := types.NewMethodSet(t)
		for i := 0; i < ms.Len(); i++ {
			method := ms.At(i).Obj()
			if isCustomMarshalContract(method.Name(), method.Type().(*types.Signature)) {
				return fmt.Errorf("%s: custom marshal/unmarshal contract %s unsupported", n.typ, method.Name())
			}
		}
	}
	if n.kind == shapeStruct {
		return s.persistStruct(n)
	}
	if n.elem != nil {
		if err := s.persist(n.elem); err != nil {
			return err
		}
	}
	if n.key != nil {
		return s.persist(n.key)
	}
	return nil
}

func (s *schema) persistStruct(n *shape) error {
	u := n.typ.Underlying().(*types.Struct)
	seen := map[string]bool{}
	for i := range n.fields {
		f := &n.fields[i]
		tag, err := mappingTags(u.Tag(i))
		if err != nil {
			return fmt.Errorf("%s.%s: %w", n.typ, f.name, err)
		}
		mapping, ok := tag["cbor"]
		budget, hasBudget := tag["cborlimit"]
		if mapping == "-" {
			f.excluded = true
			if hasBudget {
				return fmt.Errorf("%s.%s: budget on excluded field", n.typ, f.name)
			}
			if err := excludedBudgets(f.value, map[*shape]bool{}); err != nil {
				return err
			}
			continue
		}
		if !ok {
			return fmt.Errorf("%s.%s: missing explicit cbor tag", n.typ, f.name)
		}
		parts := strings.Split(mapping, ",")
		if mapping == ",value" || mapping == ",cid" {
			if err := s.persistRepresentation(n, f, parts[1], budget, hasBudget); err != nil {
				return err
			}
			continue
		}
		if err := mappedField(n, f, parts, seen); err != nil {
			return err
		}
		if hasBudget {
			if err := fieldBudget(f, budget); err != nil {
				return fmt.Errorf("%s.%s: %w", n.typ, f.name, err)
			}
		}
		if err := s.persist(f.value); err != nil {
			return fmt.Errorf("%s.%s: %w", n.typ, f.name, err)
		}
	}
	if n.representation != "" {
		count := 0
		for _, f := range n.fields {
			if !f.excluded {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("%s: representation requires exactly one persisted field; exclude other fields explicitly", n.typ)
		}
	}
	return nil
}

// mappedField validates the ordinary struct-map name and presence grammar.
func mappedField(n *shape, f *field, parts []string, seen map[string]bool) error {
	if parts[0] == "" || parts[0] == "-" || !utf8.ValidString(parts[0]) {
		return fmt.Errorf("%s.%s: invalid wire name", n.typ, f.name)
	}
	if len(parts) > 2 || (len(parts) == 2 && parts[1] != "omitempty") {
		return fmt.Errorf("%s.%s: unknown or repeated cbor option", n.typ, f.name)
	}
	f.wireName = parts[0]
	f.optional = len(parts) == 2
	if f.optional && f.value.kind != shapePointer {
		return fmt.Errorf("%s.%s: omitempty requires pointer", n.typ, f.name)
	}
	if seen[f.wireName] {
		return fmt.Errorf("%s.%s: duplicate wire name", n.typ, f.name)
	}
	seen[f.wireName] = true
	return nil
}

// persistRepresentation validates the single transparent or raw CID field.
func (s *schema) persistRepresentation(n *shape, f *field, representation, budget string, hasBudget bool) error {
	if n.representation != "" {
		return fmt.Errorf("%s: exactly one representation field required", n.typ)
	}
	n.representation = representation
	f.wireName = ""
	if representation == "cid" {
		if !types.Identical(f.value.typ, types.NewArray(types.Typ[types.Byte], 36)) || hasBudget {
			return fmt.Errorf("%s.%s: cid requires [36]byte and no cap", n.typ, f.name)
		}
		return nil
	}
	if hasBudget {
		if err := fieldBudget(f, budget); err != nil {
			return fmt.Errorf("%s.%s: %w", n.typ, f.name, err)
		}
	}
	return s.persist(f.value)
}

// isCustomMarshalContract recognizes only supported serialization interfaces, not
// unrelated domain methods that happen to share their conventional names.
func isCustomMarshalContract(name string, sig *types.Signature) bool {
	switch name {
	case "MarshalCBOR":
		return returnsBytesAndError(sig) || takesIOAndReturnsError(sig, "Writer")
	case "UnmarshalCBOR":
		return takesBytesAndReturnsError(sig) || takesIOAndReturnsError(sig, "Reader")
	case "MarshalText", "MarshalBinary":
		return returnsBytesAndError(sig)
	case "UnmarshalText", "UnmarshalBinary":
		return takesBytesAndReturnsError(sig)
	default:
		return false
	}
}

func returnsBytesAndError(sig *types.Signature) bool {
	return sig.Params().Len() == 0 && sig.Results().Len() == 2 && isByteSlice(sig.Results().At(0).Type()) && isError(sig.Results().At(1).Type())
}

func takesBytesAndReturnsError(sig *types.Signature) bool {
	return !sig.Variadic() && sig.Params().Len() == 1 && isByteSlice(sig.Params().At(0).Type()) && returnsError(sig)
}

func takesIOAndReturnsError(sig *types.Signature, name string) bool {
	return !sig.Variadic() && sig.Params().Len() == 1 && isIO(sig.Params().At(0).Type(), name) && returnsError(sig)
}

func returnsError(sig *types.Signature) bool {
	return sig.Results().Len() == 1 && isError(sig.Results().At(0).Type())
}

func isByteSlice(t types.Type) bool {
	return types.Identical(t, types.NewSlice(types.Typ[types.Byte]))
}

func isError(t types.Type) bool {
	return types.Identical(t, types.Universe.Lookup("error").Type())
}

func isIO(t types.Type, name string) bool {
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj().Name() != name || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "io" {
		return false
	}
	return true
}

// excludedBudgets rejects policy metadata that would otherwise silently have no
// effect. Mapping tags within a wholly excluded subtree are intentionally ignored.
func excludedBudgets(n *shape, seen map[*shape]bool) error {
	if seen[n] {
		return nil
	}
	seen[n] = true
	if n.kind == shapeStruct {
		u := n.typ.Underlying().(*types.Struct)
		for i, f := range n.fields {
			tag, err := mappingTags(u.Tag(i))
			if err != nil {
				return fmt.Errorf("%s.%s: %w", n.typ, f.name, err)
			}
			if _, ok := tag["cborlimit"]; ok {
				return fmt.Errorf("%s.%s: budget in excluded subtree", n.typ, f.name)
			}
			if err := excludedBudgets(f.value, seen); err != nil {
				return err
			}
		}
	}
	if n.elem != nil {
		return excludedBudgets(n.elem, seen)
	}
	return nil
}

func fieldBudget(f *field, text string) error {
	key, value, ok := strings.Cut(text, "=")
	n := f.value
	for n.kind == shapePointer {
		n = n.elem
	}
	want := budgetKind(n.typ)
	if !ok || want == "" || key != want {
		return fmt.Errorf("invalid budget option or shape")
	}
	if value == "" || strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return fmt.Errorf("budget must be positive decimal literal")
	}
	v, err := strconv.ParseUint(value, 10, 64)
	if err != nil || v == 0 {
		return fmt.Errorf("nonpositive or overflowing budget literal")
	}
	f.cap = strconv.FormatUint(v, 10)
	return nil
}

// budgetKind follows only explicit transparent representations to the immediate wire kind.
func budgetKind(t types.Type) string {
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return budgetKind(u.Elem())
	case *types.Basic:
		if u.Kind() == types.String {
			return "bytes"
		}
	case *types.Array:
		if isByte(u.Elem()) {
			return "bytes"
		}
	case *types.Slice:
		if isByte(u.Elem()) {
			return "bytes"
		}
		return "items"
	case *types.Map:
		return "items"
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			tags, err := mappingTags(u.Tag(i))
			if err == nil && tags["cbor"] == ",value" {
				return budgetKind(u.Field(i).Type())
			}
		}
	}
	return ""
}
