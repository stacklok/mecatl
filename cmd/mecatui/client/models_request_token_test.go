package client

import (
	"context"
	"errors"
	"testing"
)

type tokenModelLister struct {
	models []ModelInfo
	err    error
}

func (l tokenModelLister) ListModels(context.Context) ([]ModelInfo, []ProviderStatus, error) {
	return l.models, nil, l.err
}

func TestListModelsCmdCopiesRequestToken(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		msg := ListModelsCmd(context.Background(), tokenModelLister{models: []ModelInfo{{ID: "model"}}}, 42)()
		result, ok := msg.(ModelsMsg)
		if !ok {
			t.Fatalf("message = %T, want ModelsMsg", msg)
		}
		if result.RequestToken != 42 {
			t.Fatalf("request token = %d, want 42", result.RequestToken)
		}
	})
	t.Run("error", func(t *testing.T) {
		msg := ListModelsCmd(context.Background(), tokenModelLister{err: errors.New("boom")}, 43)()
		result, ok := msg.(ModelsMsg)
		if !ok {
			t.Fatalf("message = %T, want ModelsMsg", msg)
		}
		if result.RequestToken != 43 {
			t.Fatalf("request token = %d, want 43", result.RequestToken)
		}
	})
}
