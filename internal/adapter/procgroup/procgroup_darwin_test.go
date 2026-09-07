//go:build darwin

package procgroup

import (
	"errors"
	"testing"
)

// TestDarwinGroupHasLiveProcess pins Darwin's zombie-aware process-group
// inspection after signal-0 observes a group.
func TestDarwinGroupHasLiveProcess(t *testing.T) {
	original := darwinGroupProcesses
	t.Cleanup(func() { darwinGroupProcesses = original })

	tests := []struct {
		name     string
		statuses []int8
		err      error
		want     bool
	}{
		{name: "all zombies", statuses: []int8{darwinZombieState, darwinZombieState}, want: false},
		{name: "live member", statuses: []int8{darwinZombieState, 2}, want: true},
		{name: "query error", err: errors.New("sysctl failed"), want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			darwinGroupProcesses = func(int) ([]int8, error) { return tt.statuses, tt.err }
			if got := darwinGroupHasLiveProcess(123); got != tt.want {
				t.Fatalf("darwinGroupHasLiveProcess() = %v, want %v", got, tt.want)
			}
		})
	}
}
