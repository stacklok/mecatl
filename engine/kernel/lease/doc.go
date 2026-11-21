// Package lease defines trusted composition contracts for group-scoped ownership
// and fenced history storage in the isolated kernel. It implements neither a
// provider nor group maintenance and does not grant authority through identity
// alone. Holds and their backends belong to the group owner, not ordinary session
// consumers; passive reconstruction does not grant execution admission.
//
// Provider implementations must return safe, payload-free error text. Private
// dependency causes may remain available through Unwrap for errors.Is/errors.As,
// but must not be interpolated into Error output or logged without scrubbing.
// These contracts are provisional pending concrete provider and group usage;
// revisit API spelling with that evidence, without weakening fencing or cleanup.
package lease
