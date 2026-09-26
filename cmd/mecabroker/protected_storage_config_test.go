package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validProtectedFileConfig(t *testing.T) fileConfig {
	t.Helper()
	cfg := validBrokerConfig()
	cfg.Profiles = []fileProfile{{Name: "upstream", URL: "https://upstream.example/mcp", Auth: "oauth", OAuth: &fileOAuth{ClientMode: "preregistered", Issuer: "https://issuer.example", ClientID: "client", ClientSecretFile: filepath.Join(t.TempDir(), "secret")}}}
	if err := os.WriteFile(cfg.Profiles[0].OAuth.ClientSecretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "kek")
	cfg.ProtectedStorage = &fileProtectedStorage{
		Redis:      fileProtectedRedis{Address: "redis.example:6379", PasswordFile: "/run/redis/password"},
		Encryption: fileProtectedEncryption{ActiveID: "active", Keys: []fileProtectedKey{{ID: "active", File: key}}},
	}
	return cfg
}

func TestProtectedStorageConfigDurationDefaultsAndBounds(t *testing.T) {
	cfg := validProtectedFileConfig(t)
	if err := cfg.validateProtectedStorage(); err != nil {
		t.Fatal(err)
	}
	cfg.ProtectedStorage.Redis.HealthTimeout = ptrDuration(0)
	if err := cfg.validateProtectedStorage(); err == nil {
		t.Fatal("explicit zero health timeout accepted")
	}
	cfg = validProtectedFileConfig(t)
	cfg.ProtectedStorage.Redis.DialTimeout = ptrDuration(31 * time.Second)
	if err := cfg.validateProtectedStorage(); err == nil {
		t.Fatal("dial timeout over 30 seconds accepted")
	}
	cfg = validProtectedFileConfig(t)
	cfg.ProtectedStorage.Redis.HealthTimeout = ptrDuration(3 * time.Second)
	cfg.ProtectedStorage.Redis.OperationTimeout = ptrDuration(2 * time.Second)
	if err := cfg.validateProtectedStorage(); err == nil {
		t.Fatal("health timeout greater than operation timeout accepted")
	}
}

func TestProtectedStorageConfigAddressAndTLSValidation(t *testing.T) {
	for _, address := range []string{"redis://redis.example:6379", "user@redis.example:6379", "redis.example:port"} {
		cfg := validProtectedFileConfig(t)
		cfg.ProtectedStorage.Redis.Address = address
		if err := cfg.validateProtectedStorage(); err == nil {
			t.Errorf("address %q accepted", address)
		}
	}
	cfg := validProtectedFileConfig(t)
	cfg.ProtectedStorage.Redis.PasswordFile = ""
	if err := cfg.validateProtectedStorage(); err == nil {
		t.Fatal("missing password file accepted")
	}
}

func TestProtectedStorageKeyRingValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*fileProtectedEncryption)
	}{
		{"invalid active id", func(e *fileProtectedEncryption) { e.ActiveID = "bad/id" }},
		{"duplicate id", func(e *fileProtectedEncryption) { e.Keys = append(e.Keys, e.Keys[0]) }},
		{"missing active id", func(e *fileProtectedEncryption) { e.ActiveID = "missing" }},
		{"too many keys", func(e *fileProtectedEncryption) {
			for i := 0; i < 16; i++ {
				e.Keys = append(e.Keys, fileProtectedKey{ID: string(rune('b' + i)), File: "k"})
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validProtectedFileConfig(t)
			tc.edit(&cfg.ProtectedStorage.Encryption)
			if err := cfg.validateProtectedStorage(); err == nil {
				t.Fatal("invalid key ring accepted")
			}
		})
	}
	cfg := validBrokerConfig()
	cfg.ProtectedStorage = &fileProtectedStorage{}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "requires an OAuth") {
		t.Fatalf("protected storage without OAuth = %v", err)
	}
}

func TestProtectedStorageToolHiveMappingUsesVerifiedTLS(t *testing.T) {
	cfg := validProtectedFileConfig(t)
	mapped := cfg.toolHive()
	if mapped.ProtectedStorage == nil {
		t.Fatal("protected storage mapping missing")
	}
	client := mapped.ProtectedStorage.Redis.ClientConfig
	if !client.TLS || client.AllowPlaintext {
		t.Fatalf("protected Redis security = TLS:%v plaintext:%v", client.TLS, client.AllowPlaintext)
	}
	if client.Addr != cfg.ProtectedStorage.Redis.Address || client.PasswordFile != cfg.ProtectedStorage.Redis.PasswordFile {
		t.Fatalf("protected Redis mapping = %+v", client)
	}
}

func ptrDuration(value time.Duration) *duration {
	out := duration(value)
	return &out
}
