package app

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/openaicodex"
)

// codexRemediationProvider restores only the local policy's bounded auth guidance
// after the protocol adapter has closed arbitrary upstream/transport displays.
// Keep this in composition: the standalone adapter cannot trust host error types.
type codexRemediationProvider struct{ port.LLMProvider }

func (p codexRemediationProvider) Stream(ctx context.Context, req port.LLMRequest) (iter.Seq2[port.Chunk, error], error) {
	stream, err := p.LLMProvider.Stream(ctx, req)
	if err != nil {
		return nil, codexRemediationError(err)
	}
	return func(yield func(port.Chunk, error) bool) {
		for chunk, err := range stream {
			if !yield(chunk, codexRemediationError(err)) {
				return
			}
		}
	}, nil
}

func codexRemediationError(err error) error {
	var status *openaicodex.StatusError
	if errors.As(err, &status) {
		return fmt.Errorf("%s: %w", status.Error(), err)
	}
	return err
}
