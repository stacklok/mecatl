// Package value owns bounded native structured values and string-keyed maps.
// Values contain only explicit null, booleans, UTF-8 text, exact integers, arrays
// and maps; they are not versioned record envelopes or arbitrary Go conversions.
//
// Authoring collections are ordinary mutable storage and may share backing.
// Inputs must remain stable throughout Check, Encode, Read, Clone, Equal and
// admission. Admission checks explicit codec limits before detaching mutable
// descendants, then publishes only immutable views. Borrowed views instead rely
// on their owner's immutability discipline and never freeze or synchronize data.
//
// The local structured-value depth ceiling is 128, independently of enclosing
// wire depth: the root is zero, each child adds one, and a leaf or empty container
// at 128 is supported. Fallible operations return ErrDepthExceeded beyond it;
// Clone and Equal panic with that error if traversal crosses the ceiling. Shared
// subtrees count per occurrence; cycles fail through the same bounded traversal.
package value
