package session

import "github.com/stacklok/mecatl/engine/kernel/codec"

// valueBudget is an allocation-free lower-bound sizing pass before public values
// become DTOs. It bounds every conversion allocation and all retained data before
// allocating or parsing JSON. Generated codec traversal subsequently enforces the
// full wire budget (including map keys, scalar nodes and depth). This pass cannot
// reject a value that would pass that traversal; it introduces no extra caps.
type valueBudget struct {
	limits                codec.Limits
	data, elements, nodes int
	err                   error
}

func newValueBudget(limits codec.Limits) valueBudget {
	_, err := codec.NewBudget(limits)
	return valueBudget{limits: limits, err: err}
}
func (b *valueBudget) dataSize(n, ceiling int) {
	if b.err != nil {
		return
	}
	if n > ceiling || n > b.limits.MaxBytes-b.data-9*b.nodes {
		b.err = codec.ErrLimitExceeded
		return
	}
	b.data += n
}
func (b *valueBudget) text(s string) { b.dataSize(len(s), b.limits.MaxStringBytes) }
func (b *valueBudget) bytes(n int)   { b.dataSize(n, b.limits.MaxByteStringBytes) }
func (b *valueBudget) collection(n int) {
	if b.err != nil {
		return
	}
	if n > b.limits.MaxArrayElements || n > b.limits.MaxCollectionElements-b.elements || n > b.limits.MaxNodes-b.nodes || n > (b.limits.MaxBytes-b.data)/9-b.nodes {
		b.err = codec.ErrLimitExceeded
		return
	}
	b.elements += n
	b.nodes += n
}
func (b *valueBudget) pointer() {
	if b.err != nil {
		return
	}
	if b.nodes == b.limits.MaxNodes || b.nodes == (b.limits.MaxBytes-b.data)/9 {
		b.err = codec.ErrLimitExceeded
		return
	}
	b.nodes++
}
