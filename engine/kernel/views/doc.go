// Package views provides small read-only handles over caller-owned backing data.
//
// Views borrow rather than freeze their backing. Callers of BorrowSliceView,
// BorrowMapView and BorrowBytes must retain ownership and keep backing stable for each view's
// lifetime. Views neither synchronize access nor make an adapter's result
// immutable; adapter authors must return safe immutable views. Use Copy when a
// caller needs owned bytes.
package views
