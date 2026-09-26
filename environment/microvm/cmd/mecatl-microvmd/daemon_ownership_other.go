//go:build !linux && !darwin

package main

type daemonOwnership struct{}

func acquireDaemonOwnership(string) (*daemonOwnership, error) { return &daemonOwnership{}, nil }
func (*daemonOwnership) close()                               {}
