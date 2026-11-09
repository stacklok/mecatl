// Package value owns a closed, recursive dynamic tree for payloads whose shape is
// data rather than a typed generated DTO. It represents a bounded subset of the
// codec's CBOR values: explicit null, booleans, UTF-8 text, exact integers, arrays,
// and string-keyed maps. It deliberately does not represent every CBOR type; byte
// strings and CID links remain with their own resource and content-identity owners.
//
// Versioned persistence schemas and DTOs belong to their owning format packages.
// Value supplies an untyped payload component for those DTOs, while immutable views
// borrow their retained backing for inspection without exporting mutable storage.
//
// Authoring collections are ordinary mutable storage and may share backing. Inputs
// must remain stable throughout Check, Encode, Read, Clone, Equal and admission.
// Admit runs codec.Check, which calls CheckCBOR, before Clone detaches mutable
// descendants and publishes only an immutable view. Borrow wraps only backing the
// caller keeps stable for every derived view's lifetime; it never freezes or
// synchronizes data.
//
// The local structured-value depth ceiling is 128, independently of enclosing wire
// depth: the primitive root is zero, each child adds one, and a leaf or empty
// container at 128 is supported. Operational Limits.MaxDepth instead starts at the
// enclosing wire root. Fallible operations return ErrDepthExceeded beyond the local
// ceiling; Clone and Equal panic with that error if traversal crosses it. Shared
// subtrees count per occurrence; cycles fail through the same bounded traversal.
package value
