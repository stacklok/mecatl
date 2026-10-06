package ui

import "strconv"

// aggregateScopedID is a collision-free, stable list identity for an entity whose
// name is scoped by a parent aggregate. Each opaque component is byte-length
// prefixed, so separators inside either component cannot alias another pair.
func aggregateScopedID(aggregate, entity string) string {
	return strconv.Itoa(len(aggregate)) + ":" + aggregate + strconv.Itoa(len(entity)) + ":" + entity
}
