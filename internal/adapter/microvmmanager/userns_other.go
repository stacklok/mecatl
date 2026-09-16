//go:build !linux

package microvmmanager

import "context"

func checkLinuxUserNamespaces(context.Context) error { return nil }
