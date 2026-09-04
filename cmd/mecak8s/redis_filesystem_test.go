package main

import (
	"testing"

	"github.com/stacklok/mecatl/engine/port"
)

func TestRedisFilesystemFlags(t *testing.T) {
	cfg, err := parseFlags([]string{"--redis-url", "redis:6379", "--redis-filesystem", "--redis-read-ledger"})
	if err != nil {
		t.Fatal(err)
	}
	built := appConfig(cfg, port.NopDiagnostics{}, observability{})
	if !built.RedisFilesystem || !built.RedisReadLedger || !built.NoBash {
		t.Fatalf("app config filesystem=%v ledger=%v noBash=%v", built.RedisFilesystem, built.RedisReadLedger, built.NoBash)
	}
}

func TestRedisFilesystemFlagValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--redis-filesystem"},
		{"--redis-read-ledger"},
		{"--redis-url", "redis:6379", "--redis-filesystem", "--workspace", "/mnt/work"},
		{"--redis-url", "redis:6379", "--redis-filesystem", "--enable-parallel"},
		{"--redis-url", "redis:6379", "--redis-filesystem", "--enable-teams"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("parseFlags(%v) succeeded", args)
		}
	}
}
