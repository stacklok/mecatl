//go:build unix

package toolhivellm

import (
	"os"
	"syscall"
)

// statOwner extracts the owning uid from a os.FileInfo's platform-specific
// Sys() value (see the doc comment on the seam in detect.go). Production
// asserts the real syscall.Stat_t; a failed type-assert (an exotic Sys()
// value) is treated as "unknown" and fails CLOSED (ok=false), never as
// "trust it".
var statOwner = func(fi os.FileInfo) (uid uint32, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}
