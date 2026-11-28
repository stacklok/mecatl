// Package auditsink is the identity broker's record sink: it writes each
// broker record as a toolhive-core audit.AuditEvent in Mecatl's AuditEvent
// profile (docs/drafts/agent-identity/audit.md, "AuditEvent profile") through
// the toolhive-core audit logger, and reports a record it could not write to
// the broker, which toolhive-core's logger does not.
package auditsink

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/stacklok/toolhive-core/audit"

	"github.com/stacklok/mecatl/internal/identitybroker"
)

// Recorder identity: the Component of every event this sink writes, and the
// clock its occurrence times are read from.
const (
	Component = "mecatl-broker"
	// Profile names the AuditEvent profile and its version, in
	// Metadata.Extra["mecatl.profile"].
	Profile = "mecatl-agent-identity/1"
)

// Origin says where the sink records: the recording replica, the deployment,
// and the broker's SPIFFE trust domain (audit.md, "Where it was recorded").
// An empty field is recorded as unavailable.
type Origin struct {
	Replica     string
	Deployment  string
	TrustDomain string
}

// Syncer is a destination that can make what it accepted durable, such as
// an *os.File. A Sink whose destination is a Syncer accepts a record only
// once Sync returns, so a lossless destination's acceptance means the record
// is durably stored (audit.md, "Lossless delivery").
type Syncer interface {
	Sync() error
}

// Sink is an identitybroker.Recorder that writes records to one destination,
// one at a time, through the toolhive-core audit logger. It is safe for
// concurrent use.
type Sink struct {
	origin Origin
	dest   *destination
	logger *slog.Logger
	// turn is the one-slot semaphore writers take, so a writer waiting for a
	// write stuck at the destination can give up when its context ends.
	turn chan struct{}
}

// New returns a Sink writing to w. When w is a Syncer, each record is synced
// before it is accepted.
func New(w io.Writer, origin Origin) *Sink {
	if w == nil {
		panic("auditsink: nil destination")
	}
	dest := &destination{w: w}
	if s, ok := w.(Syncer); ok {
		dest.sync = s
	}
	return &Sink{origin: origin, dest: dest, logger: audit.NewAuditLogger(dest), turn: make(chan struct{}, 1)}
}

// ErrNotWritten wraps the reason a record was not written: the destination
// refused it, it timed out, or the record broke the profile.
var ErrNotWritten = errors.New("audit record not written")

// Record implements identitybroker.Recorder. It returns nil once the
// destination accepted rec, and an error wrapping ErrNotWritten when rec does
// not fit the profile, the destination failed the write, or ctx ended first.
// A write still in progress when ctx ends finishes in the background, so the
// destination may keep a record reported as not written; the broker then
// records a refusal that supersedes it.
func (s *Sink) Record(ctx context.Context, rec identitybroker.Record) error {
	ev, err := Event(rec, s.origin)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrNotWritten, err)
	}
	select {
	case s.turn <- struct{}{}:
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrNotWritten, ctx.Err())
	}
	done := make(chan error, 1)
	go func() {
		defer func() { <-s.turn }()
		done <- s.dest.write(func() { ev.LogTo(context.WithoutCancel(ctx), s.logger, audit.LevelAudit) })
	}()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNotWritten, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %w", ErrNotWritten, ctx.Err())
	}
}

// destination is the sink's writer. toolhive-core's logger drops the error
// of a failed write, so destination keeps it for the record being written.
// Only the writer holding the sink's turn calls write.
type destination struct {
	w    io.Writer
	sync Syncer
	// err is the first error of the write in progress.
	err error
	// wrote reports that the logger wrote the record in progress.
	wrote bool
}

// errNothingWritten is the error of a record the logger did not write at
// all, as when its handler failed to encode it.
var errNothingWritten = errors.New("the audit logger wrote nothing")

// write runs log, which writes one record through the logger, and returns
// the error of that record's write and sync.
func (d *destination) write(log func()) error {
	d.err, d.wrote = nil, false
	log()
	switch {
	case d.err != nil:
		return d.err
	case !d.wrote:
		return errNothingWritten
	case d.sync != nil:
		return d.sync.Sync()
	}
	return nil
}

// Write implements io.Writer for the logger, which writes each record in one
// call.
func (d *destination) Write(p []byte) (int, error) {
	d.wrote = true
	n, err := d.w.Write(p)
	if err == nil && n < len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && d.err == nil {
		d.err = err
	}
	return n, err
}
