//go:build !unix

package app

import "fmt"

func validateControlledRoot(string) error {
	return fmt.Errorf("temporary storage managed roots are supported only on linux and macOS")
}

func managedWorkspaceIdentity(string) (string, error) {
	return "", fmt.Errorf("managed temporary storage is supported only on linux and macOS")
}
