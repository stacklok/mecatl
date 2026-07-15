//go:build !unix

package toolhivellm

import "os"

// statOwner is the non-unix fallback (see the doc comment on the seam in
// detect.go): there is no portable uid-ownership concept to check here, so it
// fails CLOSED unconditionally (ok=false always) — DetectConfig therefore
// always misses on non-unix, which disables config-file auto-detection but
// never affects the explicit --toolhive-llm-base-url path (it never calls
// DetectConfig).
var statOwner = func(os.FileInfo) (uint32, bool) { return 0, false }
