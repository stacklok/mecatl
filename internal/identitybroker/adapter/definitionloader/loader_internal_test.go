package definitionloader

import (
	"testing"
	"time"
)

// TestFileRevision pins the revision to UTC whatever zone the time carries.
// os.Stat reports a modification time in the process's zone, so a broker run
// with TZ set would otherwise print offsets. Setting time.Local in a test
// would race with the parallel tests, so the zone is set on the value.
func TestFileRevision(t *testing.T) {
	t.Parallel()
	modTime := time.Date(2026, 11, 17, 9, 12, 44, 123456789, time.FixedZone("CET", 3600))
	if got, want := fileRevision(modTime), "2026-11-17T08:12:44.123456789Z"; got != want {
		t.Errorf("fileRevision = %q, want %q", got, want)
	}
}
