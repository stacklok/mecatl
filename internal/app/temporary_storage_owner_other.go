//go:build !linux

package app

import "fmt"

func validateControlledRoot(string) error {
	return fmt.Errorf("temporary storage managed roots are supported only on linux")
}
