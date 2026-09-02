//go:build kind_e2e

package k8s_e2e_test

import (
	"encoding/json"
	"testing"
)

func TestK8sForkFixtureUsesSuccessorEndpoint(t *testing.T) {
	if got := forkSessionPath("source-1"); got != "/v1/sessions/source-1/fork" {
		t.Fatalf("fork path = %q, want dedicated server-owned successor endpoint", got)
	}
}

func TestK8sSessionRequestFixturesArePathFree(t *testing.T) {
	for name, body := range map[string][]byte{
		"create":               defaultSessionCreateBody(),
		"fork":                 forkSessionBody(),
		"schedule":             scheduleBody("daily"),
		"schedule-with-prompt": scheduleBodyWithPrompt("daily", "noop"),
	} {
		t.Run(name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatalf("fixture body is invalid JSON: %v", err)
			}
			if _, ok := fields["workspace"]; ok {
				t.Fatal("fixture sends obsolete client-owned workspace field")
			}
		})
	}
}
