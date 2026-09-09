// Command mecatl-artifact-digest prints the canonical strict-admission tree digest.
package main

import (
	"fmt"
	"os"

	microvm "github.com/stacklok/mecatl/environment/microvm"
)

func main() {
	if len(os.Args) != 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: mecatl-artifact-digest TREE")
		os.Exit(2)
	}
	digest, err := microvm.ArtifactTreeDigest(os.Args[1])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_, _ = fmt.Fprintln(os.Stdout, digest)
}
