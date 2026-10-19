package server_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the server adapter — the Run
// registry and stream lifecycle in service.go spin goroutines per run. A
// leaked stream or registry goroutine fails the suite.
//
// There is NO ignore list: the gRPC wire tests dial over in-memory
// google.golang.org/grpc/test/bufconn (no real sockets, hence no net/http
// idle-conn reaper) and each tear themselves down via the cleanup func
// (conn.Close + gs.Stop + lis.Close). With nothing leaking as known noise,
// pinning an ignore would only blunt the gate. Anything that lingers is a
// real finding.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
