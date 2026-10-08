package telemetry

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
)

// NewAdminMux builds the loopback admin mux: /metrics (read-only, secret-free)
// plus the runtime-introspection surface — pprof, expvar (/debug/vars), and,
// when recorder is non-nil, the FlightRecorder snapshot (/debug/flightrecorder).
//
// It lives in the telemetry adapter (not a cmd main) so BOTH composition roots —
// the standalone cmd/mecated daemon and the cmd/mecatui embedded server —
// serve the IDENTICAL admin surface
// from one helper, rather than each hand-rolling a mux that could drift.
//
// recorder may be nil (FlightRecorder disabled), in which case
// /debug/flightrecorder is NOT mounted and a GET returns 404.
//
// SECURITY: pprof/FlightRecorder/expvar output can embed prompt text, file paths,
// and goroutine stacks. The returned mux MUST be served only on a loopback-bound
// listener — never on the public gRPC/HTTP service surface.
func NewAdminMux(reg *prometheus.Registry, recorder *FlightRecorder) *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/metrics", MetricsHandler(reg))
	RegisterPprof(mux)
	mux.Handle("/debug/vars", ExpvarHandler())
	if recorder != nil {
		mux.HandleFunc("/debug/flightrecorder", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			if _, err := recorder.Snapshot(w); err != nil {
				http.Error(w, "flight recorder snapshot: "+err.Error(), http.StatusServiceUnavailable)
			}
		})
	}
	return mux
}
