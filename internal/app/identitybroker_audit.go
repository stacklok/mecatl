package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/auditsink"
)

// errIdentityBrokerAuditFileWithoutBroker refuses an audit file for an
// identity broker that is off.
var errIdentityBrokerAuditFileWithoutBroker = errors.New("an identity broker audit file (--identity-broker-audit-file) requires the identity broker (--enable-identity-broker)")

// openIdentityBrokerAudit opens the identity broker's audit destination,
// cfg.IdentityBrokerAuditFile or standard output, and returns the sink the
// broker records through and a function that closes the destination. The
// sink writes each record through toolhive-core's audit logger, recorded at
// this host by the broker of config.
//
// Shortcut: definition-load and agent-instance credential issuance events are not recorded; removed by #2848.
func openIdentityBrokerAudit(cfg Config, config identitybroker.Config) (*auditsink.Sink, func(), error) {
	origin := auditsink.Origin{Deployment: config.Audience, TrustDomain: config.TrustDomain}
	// The replica is the host, which is the pod in Kubernetes; a host whose
	// name cannot be read is recorded as unavailable.
	if host, err := os.Hostname(); err == nil {
		origin.Replica = host
	}
	if cfg.identityBrokerAuditWriter != nil {
		return auditsink.New(cfg.identityBrokerAuditWriter, origin), func() {}, nil
	}
	if cfg.IdentityBrokerAuditFile == "" {
		// Standard output is a pipe or terminal, which cannot sync, so a
		// record is accepted once written; the log shipper reading it may
		// still lose it (audit.md, "Lossless delivery").
		return auditsink.New(stdoutWriter{}, origin), func() {}, nil
	}
	file, err := os.OpenFile(filepath.Clean(cfg.IdentityBrokerAuditFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, fmt.Errorf("open the identity broker audit file: %w", err)
	}
	// An *os.File syncs each record before the sink accepts it.
	return auditsink.New(file, origin), func() { _ = file.Close() }, nil
}

// auditWriter is a destination of the identity broker's audit records.
type auditWriter = io.Writer

// stdoutWriter writes to standard output, without the Sync an *os.File
// offers, which fails on a pipe.
type stdoutWriter struct{}

func (stdoutWriter) Write(p []byte) (int, error) { return os.Stdout.Write(p) }

var _ io.Writer = stdoutWriter{}
