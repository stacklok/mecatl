package codec

import "unicode/utf8"

// Budget accounts one generated traversal, without reflection or output/sorting
// allocation. Its zero value is invalid; construct it with NewBudget. Copying an
// active Budget forks accounting, so pass it by pointer within one operation.
// It does not validate schema traversal: callers must visit exactly the children
// declared by each container, at depth+1, and must not omit or invent nodes.
// Discard the Budget after an error; failed checks may have consumed resources.
// Checks on a nil or zero Budget return ErrInvalidLimits.
type Budget struct {
	limits   Limits
	nodes    int // All reserved nodes, including children not yet visited.
	visited  int
	elements int
	size     int // Nine bytes per reserved node plus visited scalar contents.
}

// NewBudget validates all global policy before any input inspection.
// Invalid policy returns a zero Budget and ErrInvalidLimits.
func NewBudget(limits Limits) (Budget, error) {
	if err := limits.validate(); err != nil {
		return Budget{}, err
	}
	return Budget{limits: limits}, nil
}

// EstimatedBytes returns the conservative output size accounted so far. After a
// complete successful traversal it can bound encoder preallocation. It is not an
// exact CBOR byte length and does not prove traversal completeness. A nil or zero
// Budget returns zero.
func (b *Budget) EstimatedBytes() int {
	if b == nil {
		return 0
	}
	return b.size
}

// reserveNodes reserves headers as well as nodes before collection allocation.
// Division/subtraction avoid overflow even for machine-sized policy maxima.
func (b *Budget) reserveNodes(n int) error {
	if n > b.limits.MaxNodes-b.nodes || n > (b.limits.MaxBytes-b.size)/9 {
		return ErrLimitExceeded
	}
	b.nodes += n
	b.size += 9 * n
	return nil
}

// visit consumes a previously reserved node; the root is reserved lazily so a
// positive MaxBytes below nine is valid policy but rejects any value.
func (b *Budget) visit(depth int) error {
	if b == nil || b.limits.MaxBytes == 0 {
		return ErrInvalidLimits
	}
	if depth < 0 {
		return ErrInvalidData
	}
	if depth > b.limits.MaxDepth {
		return ErrLimitExceeded
	}
	if b.nodes == 0 {
		if err := b.reserveNodes(1); err != nil {
			return err
		}
	}
	if b.visited == b.nodes {
		return ErrInvalidData
	}
	b.visited++
	return nil
}

// CheckScalar accounts one bool or fixed-width integer node, independent of its
// value. All int64/uint64 values are supported; generated code owns narrower
// destination ranges. Root depth is zero. No output is allocated.
func (b *Budget) CheckScalar(depth int) error { return b.visit(depth) }

// scalar checks byte caps and accounting before UTF-8 scans or owning copies.
func (b *Budget) scalar(length, depth, fieldMax, globalMax int) error {
	if b == nil || fieldMax <= 0 || b.limits.MaxBytes == 0 {
		return ErrInvalidLimits
	}
	if length < 0 {
		return ErrInvalidData
	}
	if length > fieldMax || length > globalMax {
		return ErrLimitExceeded
	}
	if err := b.visit(depth); err != nil {
		return err
	}
	if length > b.limits.MaxBytes-b.size {
		return ErrLimitExceeded
	}
	b.size += length
	return nil
}

// CheckString bounds a text value or map key before scanning for valid UTF-8.
// fieldMax is positive; pass MaxStringBytes when no field cap applies. It does
// not retain or mutate value. Depth counts wire containers, not pointers.
func (b *Budget) CheckString(value string, depth, fieldMax int) error {
	if b == nil {
		return ErrInvalidLimits
	}
	if err := b.scalar(len(value), depth, fieldMax, b.limits.MaxStringBytes); err != nil {
		return err
	}
	if !utf8.ValidString(value) {
		return ErrInvalidData
	}
	return nil
}

// CheckBytes accounts an opaque byte-string length without inspecting contents.
// fieldMax is positive; pass MaxByteStringBytes when no field cap applies.
// Call before copying or allocating output, including for fixed byte arrays.
func (b *Budget) CheckBytes(length, depth, fieldMax int) error {
	if b == nil {
		return ErrInvalidLimits
	}
	return b.scalar(length, depth, fieldMax, b.limits.MaxByteStringBytes)
}

// collection reserves immediate declarations and child nodes/headers. A nonempty
// container at maximum depth fails here, before any typed allocation. Map keys
// and values each reserve a node, but a pair is one collection declaration.
func (b *Budget) collection(length, depth, fieldMax, globalMax, nodesPerItem int) error {
	if b == nil || fieldMax <= 0 || b.limits.MaxBytes == 0 {
		return ErrInvalidLimits
	}
	if length < 0 {
		return ErrInvalidData
	}
	if length > fieldMax || length > globalMax || length > b.limits.MaxCollectionElements-b.elements {
		return ErrLimitExceeded
	}
	if err := b.visit(depth); err != nil {
		return err
	}
	if length > 0 && depth >= b.limits.MaxDepth {
		return ErrLimitExceeded
	}
	if length > (b.limits.MaxNodes-b.nodes)/nodesPerItem {
		return ErrLimitExceeded
	}
	if err := b.reserveNodes(length * nodesPerItem); err != nil {
		return err
	}
	b.elements += length
	return nil
}

// CheckArray accounts an array node and reserves length child nodes and aggregate
// declarations BEFORE element allocation/traversal. fieldMax is positive; pass
// MaxArrayElements when untagged. Visit each element once at depth+1.
func (b *Budget) CheckArray(length, depth, fieldMax int) error {
	if b == nil {
		return ErrInvalidLimits
	}
	return b.collection(length, depth, fieldMax, b.limits.MaxArrayElements, 1)
}

// CheckMap accounts a struct/data map and reserves length pairs, twice as many
// child nodes (keys and values), and their headers BEFORE allocation or sorting.
// fieldMax is positive; pass MaxMapPairs when untagged. Visit each key and value
// once at depth+1; generated code owns key uniqueness and canonical ordering.
func (b *Budget) CheckMap(length, depth, fieldMax int) error {
	if b == nil {
		return ErrInvalidLimits
	}
	return b.collection(length, depth, fieldMax, b.limits.MaxMapPairs, 2)
}
