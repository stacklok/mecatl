package grpcdriver

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain installs a goroutine-leak gate over the grpcdriver adapter,
// mirroring internal/adapter/server's leakmain — every test here dials over
// in-memory google.golang.org/grpc/test/bufconn (no real sockets) and tears
// itself down via t.Cleanup (conn.Close + gs.Stop + lis.Close).
//
// There is NO ignore list: with nothing leaking as known noise, pinning an
// ignore would only blunt the gate. Anything that lingers is a real finding.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
