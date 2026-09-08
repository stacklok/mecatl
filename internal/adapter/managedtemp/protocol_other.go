//go:build !linux && !darwin

package managedtemp

import (
	"context"
	"errors"
	"time"
)

var errUnsupported = errors.New("managedtemp: managed storage is supported only on unix")

type Namespace struct{}
type Workspace struct{}
type Lease struct{}

func Open(string) (*Namespace, error) { return nil, errUnsupported }
func (*Namespace) Close() error       { return nil }
func (*Namespace) OpenWorkspace(string, string, string) (*Workspace, error) {
	return nil, errUnsupported
}
func (*Workspace) Close() error                    { return nil }
func (*Workspace) Allocate(string) (*Lease, error) { return nil, errUnsupported }
func (*Lease) Path() string                        { return "" }
func (*Lease) TempDir() string                     { return "" }
func (*Lease) Started(int) error                   { return errUnsupported }
func (*Lease) Terminal() error                     { return errUnsupported }
func (*Lease) Remove() error                       { return errUnsupported }
func (*Lease) Close() error                        { return nil }
func ValidTestHomeMarker(string) bool              { return false }

type SweepOptions struct {
	Now              time.Time
	Interval         time.Duration
	CommandReapAfter time.Duration
}

type SweepResult struct {
	Scanned bool
	Deleted int
}

func (*Namespace) Sweep(context.Context, SweepOptions) (SweepResult, error) {
	return SweepResult{}, errUnsupported
}
