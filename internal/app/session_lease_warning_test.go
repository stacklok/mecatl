package app

import (
	"strings"
	"testing"

	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/port"
)

// sharedStoreStub stands in for a store other processes may share (a remote
// driver, Redis): it is not the process-private memstore and provides no lease.
type sharedStoreStub struct{ port.SessionStore }

// TestNoLeaseForSharedStoreWarns pins that Build warns when a store replicas may
// share resolves no session lease, since nothing then excludes two replicas from
// the same session, and stays quiet for the stores that are safe without an
// explicit lease: the process-private memstore and a local StoreDir (which gets
// the flock lease).
func TestNoLeaseForSharedStoreWarns(t *testing.T) {
	const warning = "session lease: none for a store other processes may share"
	for _, tc := range []struct {
		name  string
		cfg   Config
		store port.SessionStore
		warns bool
	}{
		{"shared store without a lease", Config{}, sharedStoreStub{memstore.New()}, true},
		{"process-private memstore", Config{}, memstore.New(), false},
		{"local store dir", Config{StoreDir: t.TempDir()}, sharedStoreStub{memstore.New()}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := slogdiagBuffer(t)
			tc.cfg.Diagnostics = rec.diag
			lease, _, closeLease, err := buildSessionLease(tc.cfg, tc.store)
			if err != nil {
				t.Fatalf("buildSessionLease: %v", err)
			}
			closeLease()
			line := rec.lineContaining(warning)
			if tc.warns && (line == "" || lease != nil || !strings.Contains(line, "level=WARN")) {
				t.Fatalf("want one WARN naming the missing lease; lease=%v log:\n%s", lease, rec.String())
			}
			if !tc.warns && line != "" {
				t.Fatalf("unexpected missing-lease warning: %q", line)
			}
		})
	}
}
