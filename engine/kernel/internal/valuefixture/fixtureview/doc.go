// Package fixtureview supplies borrowed navigation over valuefixture DTOs.
// Views do not freeze or synchronize backing; owners keep all reachable storage
// stable for the lifetime of every derived view. Navigation is O(1) per child.
package fixtureview
