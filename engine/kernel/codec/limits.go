package codec

import "errors"

// ErrInvalidLimits identifies incomplete or nonpositive operational policy.
var ErrInvalidLimits = errors.New("invalid codec limits")

// ErrLimitExceeded identifies resource rejection, not schema invalidity.
var ErrLimitExceeded = errors.New("codec limit exceeded")

// ErrInvalidData identifies malformed wire data or invalid schema values.
var ErrInvalidData = errors.New("invalid codec data")

// Limits is explicit operational policy, separate from schema/domain validity.
// Every numeric field must be positive; there are no defaults or unlimited values.
// Tightening policy can reject previously accepted values without changing bytes.
// Named and input data must remain stable during a call. Operations never mutate,
// retain, copy, or globally cache the dictionary; concurrent read-only reuse is safe.
type Limits struct {
	// MaxBytes bounds complete input and the conservative output estimate.
	MaxBytes int
	// MaxStringBytes bounds each UTF-8 string or map key, in bytes.
	MaxStringBytes int
	// MaxByteStringBytes bounds each opaque byte string, including fixed arrays.
	MaxByteStringBytes int
	// MaxArrayElements bounds each array's immediate element count.
	MaxArrayElements int
	// MaxMapPairs bounds each map's immediate pair count, including struct maps.
	MaxMapPairs int
	// MaxDepth bounds wire nesting, with the root at depth zero.
	MaxDepth int
	// MaxNodes bounds aggregate wire nodes, including map keys, not pointers.
	MaxNodes int
	// MaxCollectionElements bounds aggregate declared array elements and map pairs.
	MaxCollectionElements int
	// Named supplies schema-referenced positive field maxima. Unreferenced entries
	// are ignored; a nil dictionary is valid when no references are needed.
	Named map[string]int
}

// NamedLimit resolves one schema-known name without scanning the dictionary.
// Generated entry points resolve their finite reference set once, before input
// inspection, including references on absent optional fields. The returned value
// must supplement, never replace, the corresponding global cap. Schema generation
// validates identifier spelling; this lookup requires only an exact positive entry.
func (l Limits) NamedLimit(name string) (int, error) {
	n := l.Named[name]
	if n <= 0 {
		return 0, ErrInvalidLimits
	}
	return n, nil
}

// validate checks globals only; generated code knows which Named entries apply.
func (l Limits) validate() error {
	if l.MaxBytes <= 0 || l.MaxStringBytes <= 0 || l.MaxByteStringBytes <= 0 ||
		l.MaxArrayElements <= 0 || l.MaxMapPairs <= 0 || l.MaxDepth <= 0 ||
		l.MaxNodes <= 0 || l.MaxCollectionElements <= 0 {
		return ErrInvalidLimits
	}
	return nil
}
