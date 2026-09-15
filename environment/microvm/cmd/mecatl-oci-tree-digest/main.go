// Command mecatl-oci-tree-digest pulls one digest-pinned platform image through
// the production go-microvm extractor and prints its materialized tree identity.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/stacklok/mecatl/environment/microvm"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mecatl-oci-tree-digest REPO@SHA256 CACHE_DIR")
		os.Exit(2)
	}
	ref := os.Args[1]
	at := len(ref) - len("sha256:") - 64 - 1
	if at <= 0 || at >= len(ref) || ref[at] != '@' {
		fmt.Fprintln(os.Stderr, "OCI reference must be digest-pinned")
		os.Exit(2)
	}
	manifest := ref[at+1:]
	resolver := microvm.NewOCIExecutionImageResolver(os.Args[2], nil, map[string]microvm.VerificationEvidence{
		ref: {Bundle: []byte("release-tree-digest")},
	})
	resolved, err := resolver.Resolve(context.Background(), microvm.ArtifactRequest{
		Kind: microvm.ArtifactExecutionImage, Reference: ref, ManifestDigest: manifest,
	})
	if err != nil {
		if errors.Is(err, microvm.ErrMutableArtifact) {
			fmt.Fprintln(os.Stderr, "OCI reference must be digest-pinned")
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(1)
	}
	fmt.Println(resolved.Digest)
}
