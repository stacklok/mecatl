package main

import (
	"crypto/tls"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
)

func TestBrokerPathRetirementRejectsRetiredTransportJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broker.json")
	for _, key := range []string{"handle_idle_timeout", "owner_retention", "sweep_interval", "cleanup_timeout", "max_handles", "max_owners", "max_receipts", "max_receipt_bytes", "max_pending_controls", "max_active_executes"} {
		data := strings.Replace(configJSON("fixture-cert", "fixture-key"), `"transport":{`, `"transport":{"`+key+`":1,`, 1)
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := readConfig(path); err == nil {
			t.Fatalf("retired transport key %s admitted", key)
		}
	}
}

func TestSessionAPIConfigRequired(t *testing.T) {
	cfg := validProtectedFileConfig(t)
	cfg.SessionAPI = &mcpbrokerserver.SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-a"}
	if err := cfg.validate(); err != nil {
		t.Fatal(err)
	}
	mapped := cfg.productionConfig(tls.Certificate{}, nil, nil)
	if mapped.SessionAPI == nil || *mapped.SessionAPI != *cfg.SessionAPI {
		t.Fatal("production opt-in lost")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(configJSON("fixture-cert", "fixture-key")), &document); err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]any{"profiles": cfg.Profiles, "protected_storage": cfg.ProtectedStorage, "session_api": cfg.SessionAPI} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		document[key] = encoded
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "broker.json")
	for name, fragment := range map[string]string{
		"null":               `"session_api":null`,
		"valid":              `"session_api":{"mode":"OWNERLESS","deployment":"offline-a"}`,
		"implicit":           `"session_api":{}`,
		"missing-deployment": `"session_api":{"mode":"OWNERLESS"}`,
		"missing-mode":       `"session_api":{"deployment":"offline-a"}`,
		"fallback":           `"session_api":{"mode":"AUTO","deployment":"offline-a"}`,
		"human":              `"session_api":{"mode":"OWNER","deployment":"offline-a"}`,
		"invalid-deployment": `"session_api":{"mode":"OWNERLESS","deployment":"../a"}`,
		"unknown":            `"session_api":{"mode":"OWNERLESS","deployment":"offline-a","owner_header":"unverified"}`,
		"duplicate":          `"session_api":{"mode":"OWNERLESS","mode":"AUTO","deployment":"offline-a"}`,
	} {
		t.Run(name, func(t *testing.T) {
			data := strings.Replace(string(raw), `"session_api":{"mode":"OWNERLESS","deployment":"offline-a"}`, fragment, 1)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readConfig(path)
			if (err == nil) != (name == "valid") {
				t.Fatalf("admission error = %v", err)
			}
		})
	}
	cfg.ProtectedStorage = nil
	if err := cfg.validate(); err == nil {
		t.Fatal("second/absent Redis ownership admitted")
	}
	cfg = validProtectedFileConfig(t)
	cfg.SessionAPI = &mcpbrokerserver.SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-a"}
	cfg.Profiles[0].Auth = "none"
	cfg.Profiles[0].OAuth = nil
	cfg.Profiles[0].Static = nil
	cfg.ProtectedStorage.Encryption = fileProtectedEncryption{}
	if err := cfg.validate(); err != nil {
		t.Fatalf("anonymous metadata topology: %v", err)
	}
	anonymous := cfg.productionConfig(tls.Certificate{}, nil, nil)
	if anonymous.ToolHive.ProtectedStorage != nil || anonymous.SessionMetadataRedis == nil || !anonymous.SessionMetadataRedis.ClientConfig.TLS {
		t.Fatal("anonymous metadata created credential storage or lost native TLS Redis config")
	}
	cfg = validProtectedFileConfig(t)
	cfg.SessionAPI = &mcpbrokerserver.SessionAPIConfig{Mode: "OWNERLESS", Deployment: "offline-a"}
	cfg.Profiles = append(cfg.Profiles, fileProfile{Name: "anonymous", URL: "https://anonymous.example/mcp", Auth: "none"})
	if err := cfg.validate(); err != nil {
		t.Fatalf("mixed profiles: %v", err)
	}
	mixed := cfg.productionConfig(tls.Certificate{}, nil, nil)
	if mixed.SessionMetadataRedis != nil || mixed.ToolHive.ProtectedStorage == nil {
		t.Fatal("mixed topology did not borrow native credential Redis")
	}
	cfg.SessionAPI = nil
	if err := cfg.validate(); err == nil {
		t.Fatal("missing required session_api admitted")
	}
}
