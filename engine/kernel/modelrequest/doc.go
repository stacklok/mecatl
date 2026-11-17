//go:generate go -C ../../.. run ./internal/dtogen/cmd/dtogen -dir engine/kernel/modelrequest -emit view -out zz_views_generated.go

// Package modelrequest owns immutable provider-neutral logical model requests and
// their content identity in the isolated kernel experiment. Logical requests are
// not SDK wire bytes or invocation authority. Construction and session authoring
// can produce only provisional facts; a successful session acceptance is required
// before any execution boundary may use a commitment, and this package executes
// nothing. Mutable schemas live in format; requests retain owned backing, while
// nested views borrow that immutable backing for their lifetime. Navigation is O(1);
// only explicit Format and byte Copy operations detach storage. Zero requests and
// views are safe and empty. Construction and historical reconstruction are separate
// from session admission and production entry points.
package modelrequest
