//go:build !linux && !darwin

package microvmmanager

func daemonOwnershipHeld(string) (bool, error) { return false, nil }
