//go:build linux

package clientauth

import (
	"context"
	"os"
)

// Handle the private same-executable protocol before any application's main.
// No other argument shape is interpreted as a helper invocation.
func init() {
	if len(os.Args) == 2 && os.Args[1] == secretServiceHelperArg {
		os.Exit(secretServiceHelper(context.Background()))
	}
}
