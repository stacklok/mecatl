//go:build !linux

package app

import "fmt"

func validateControlledRoot(string) error {
	return fmt.Errorf("temporary storage managed roots are supported only on linux")
}

func managedWorkspaceIdentity(string) (identity, currentPath string, err error) {
	return "", "", fmt.Errorf("managed temporary storage is supported only on linux")
}
