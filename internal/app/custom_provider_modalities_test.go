package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

// TestCustomProviderLiveModalitiesAreAuthoritative proves an OpenAI-compatible
// custom provider's explicit input modalities override the adapter-wide protocol
// capability, while omitted modality metadata keeps the established fallback.
func TestCustomProviderLiveModalitiesAreAuthoritative(t *testing.T) {
	for _, flavor := range []string{"openai-responses", "openai-chat-completions"} {
		t.Run(flavor, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"data":[
					{"id":"text-only","display_name":"Text only","input":["text"],"output":["text"]},
					{"id":"vision","display_name":"Vision","input":["text","image"],"output":["text"]},
					{"id":"unknown-modalities","display_name":"Unknown modalities"}
				]}`))
			}))
			defer server.Close()

			cfg := customProviderConfig()
			definition := cfg.ProviderDefinitions["gateway-responses"]
			definition.APIFlavor = flavor
			definition.BaseURL = server.URL + "/v1"
			cfg.ProviderDefinitions[definition.ID] = definition
			reg, err := buildProviderRegistry(cfg, fakeEnv(nil))
			if err != nil {
				t.Fatalf("buildProviderRegistry: %v", err)
			}

			models := resolveProviderModels(context.Background(), port.NopDiagnostics{}, reg, definition.ID)
			projected := make(map[string]bool, len(models))
			for _, m := range models {
				projected[m.ID] = projectModelEntry(reg, definition.ID, m).GetImage()
			}
			if projected["text-only"] {
				t.Error("text-only model advertised image input despite its explicit live declaration")
			}
			if !projected["vision"] {
				t.Error("vision model did not advertise its explicitly declared image input")
			}
			if !projected["unknown-modalities"] {
				t.Error("model without modality metadata lost the adapter-capability fallback")
			}
		})
	}
}
