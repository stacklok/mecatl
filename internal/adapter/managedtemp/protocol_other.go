//go:build !linux

package managedtemp

import "errors"

var errUnsupported = errors.New("managedtemp: managed storage is supported only on linux")

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
