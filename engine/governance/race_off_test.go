//go:build !race

package governance

// raceEnabled is false in a normal test build; see race_on_test.go.
const raceEnabled = false
