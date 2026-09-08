package ui

import (
	"context"
	"testing"

	"github.com/stacklok/mecatl/cmd/mecatui/client"
)

type legacyConverserExtension struct{}

func (legacyConverserExtension) OpenConverse(context.Context) (*client.Stream, error) {
	return client.NewStream(nil, nil), nil
}

func TestADR_0294_MecatuiConverserExtensionCompatibility(t *testing.T) {
	var extension Converser = legacyConverserExtension{}
	if _, ok := extension.(SessionBoundConverser); ok {
		t.Fatal("legacy Converser unexpectedly requires the additive session-bound capability")
	}
	if _, err := extension.OpenConverse(context.Background()); err != nil {
		t.Fatalf("legacy Converser failed: %v", err)
	}
}
