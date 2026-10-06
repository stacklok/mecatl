//go:build !linux

package main

import "context"

func checkLinuxUserNamespaceCapability(context.Context) error { return nil }
