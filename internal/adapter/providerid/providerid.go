// Package providerid owns the root-internal provider identifier grammar shared
// by operator configuration and credential persistence.
package providerid

import "regexp"

var pattern = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Valid reports whether id is a syntactically valid provider identifier.
func Valid(id string) bool { return pattern.MatchString(id) }
