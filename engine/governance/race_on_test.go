//go:build race

package governance

// raceEnabled reports whether the test binary was built with the race detector.
// Allocation counts mean nothing then: sync.Pool drops a share of the items put
// back, so json.Valid's pooled scanner is reallocated at random.
const raceEnabled = true
