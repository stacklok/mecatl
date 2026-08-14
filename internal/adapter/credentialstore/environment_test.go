package credentialstore_test

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
	"github.com/stacklok/mecatl/internal/adapter/envscrub"
)

func TestEnvironmentReader(t *testing.T) {
	key := []byte{0, 1, 2, 0xff}
	value := []byte{0, 0xff, 'x'}
	var lookedUp string
	reader, err := credentialstore.NewEnvironment("tenant", key, "MECATL_MCP_CREDENTIAL", func(name string) (string, bool) {
		lookedUp = name
		return base64.StdEncoding.EncodeToString(value), true
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := reader.Capabilities(); got.Persistent || got.CrossProcessCAS {
		t.Fatalf("Capabilities() = %+v, want read-only process environment", got)
	}

	record, err := reader.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if lookedUp != "MECATL_MCP_CREDENTIAL" {
		t.Fatalf("lookup name = %q", lookedUp)
	}
	if string(record.Value) != string(value) {
		t.Fatalf("value = %v, want %v", record.Value, value)
	}
	record.Value[0] = 42
	again, err := reader.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Value) != string(value) {
		t.Fatal("Get exposed mutable backing storage")
	}
	if !record.Version.Equal(again.Version) {
		t.Fatal("unchanged environment value produced a different version")
	}

	if _, err := reader.Get(context.Background(), []byte("other")); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("wrong key error = %v, want not found", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := reader.Get(context.Background(), key); !errors.Is(err, credentialstore.ErrClosed) {
		t.Fatalf("Get after Close error = %v, want closed", err)
	}
}

func TestEnvironmentReaderCredentialVariableIsScrubbedFromAgentShell(t *testing.T) {
	const name = "MECATL_MCP_OAUTH_CREDENTIAL"
	key := []byte("record")
	reader, err := credentialstore.NewEnvironment("tenant", key, name, func(want string) (string, bool) {
		for _, entry := range envscrub.Scrub([]string{name + "=opaque-value"}) {
			if entry == want+"=opaque-value" {
				return "opaque-value", true
			}
		}
		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Get(context.Background(), key); !errors.Is(err, credentialstore.ErrNotFound) {
		t.Fatalf("Get after shell scrubbing error = %v, want not found", err)
	}
}

func TestEnvironmentReaderCloseDoesNotWaitForLookup(t *testing.T) {
	key := []byte("record")
	lookupStarted := make(chan struct{})
	releaseLookup := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(releaseLookup)
		}
	}()
	reader, err := credentialstore.NewEnvironment("tenant", key, "MECATL_MCP_CREDENTIAL", func(string) (string, bool) {
		close(lookupStarted)
		<-releaseLookup
		return base64.StdEncoding.EncodeToString([]byte("secret")), true
	})
	if err != nil {
		t.Fatal(err)
	}

	getDone := make(chan error, 1)
	go func() {
		record, err := reader.Get(context.Background(), key)
		if len(record.Value) != 0 {
			getDone <- errors.New("Get returned a value after Close")
			return
		}
		getDone <- err
	}()
	<-lookupStarted

	closeDone := make(chan error, 1)
	go func() { closeDone <- reader.Close() }()
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked on in-flight lookup")
	}
	close(releaseLookup)
	released = true
	select {
	case err := <-getDone:
		if !errors.Is(err, credentialstore.ErrClosed) {
			t.Fatalf("in-flight Get error = %v, want closed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Get did not finish after lookup was released")
	}
}

func TestEnvironmentReaderLookupMayCloseReader(t *testing.T) {
	key := []byte("record")
	var reader *credentialstore.EnvironmentReader
	var err error
	reader, err = credentialstore.NewEnvironment("tenant", key, "MECATL_MCP_CREDENTIAL", func(string) (string, bool) {
		_ = reader.Close()
		return base64.StdEncoding.EncodeToString([]byte("secret")), true
	})
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var record credentialstore.Record
	var getErr error
	go func() {
		record, getErr = reader.Get(context.Background(), key)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("reentrant lookup deadlocked")
	}
	if !errors.Is(getErr, credentialstore.ErrClosed) {
		t.Fatalf("Get error = %v, want closed", getErr)
	}
	if len(record.Value) != 0 {
		t.Fatal("Get returned a value after reentrant Close")
	}
}

func TestEnvironmentReaderErrorsAreTypedAndSecretSafe(t *testing.T) {
	const canary = "ENV-CREDENTIAL-CANARY-MUST-NOT-LEAK"
	key := []byte("record")
	tests := []struct {
		name   string
		env    string
		lookup credentialstore.EnvironmentLookup
		want   error
	}{
		{name: "missing", env: "MECATL_VALID_NAME", lookup: func(string) (string, bool) { return "", false }, want: credentialstore.ErrNotFound},
		{name: "malformed", env: "MECATL_VALID_NAME", lookup: func(string) (string, bool) { return canary + "!", true }, want: credentialstore.ErrCorrupt},
		{name: "oversized encoded", env: "MECATL_VALID_NAME", lookup: func(string) (string, bool) {
			return strings.Repeat("A", base64.StdEncoding.EncodedLen(credentialstore.MaxValueBytes)+1), true
		}, want: credentialstore.ErrTooLarge},
		{name: "oversized decoded", env: "MECATL_VALID_NAME", lookup: func(string) (string, bool) {
			return base64.StdEncoding.EncodeToString(make([]byte, credentialstore.MaxValueBytes+1)), true
		}, want: credentialstore.ErrTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader, err := credentialstore.NewEnvironment("tenant", key, tt.env, tt.lookup)
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.Get(context.Background(), key)
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
			if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), tt.env) {
				t.Fatalf("error leaked env name or value: %v", err)
			}
		})
	}
}

func TestEnvironmentReaderValidatesBeforeLookup(t *testing.T) {
	var calls atomic.Int32
	lookup := func(string) (string, bool) {
		calls.Add(1)
		return "", false
	}
	for _, env := range []string{"", "MCP_CREDENTIAL", "lower", "9START", "HAS-DASH", strings.Repeat("A", 129), "SECRET\nNAME"} {
		_, err := credentialstore.NewEnvironment("tenant", []byte("record"), env, lookup)
		if !errors.Is(err, credentialstore.ErrInvalidEnvironment) {
			t.Fatalf("NewEnvironment(%q) error = %v, want invalid environment", env, err)
		}
		if err != nil && env != "" && strings.Contains(err.Error(), env) {
			t.Fatalf("error leaked invalid env name: %v", err)
		}
	}
	if _, err := credentialstore.NewEnvironment("tenant", nil, "MECATL_VALID_NAME", lookup); !errors.Is(err, credentialstore.ErrInvalidKey) {
		t.Fatalf("invalid key error = %v", err)
	}
	if _, err := credentialstore.NewEnvironment("tenant", []byte("record"), "MECATL_VALID_NAME", nil); !errors.Is(err, credentialstore.ErrInvalidEnvironment) {
		t.Fatalf("nil lookup error = %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("constructor performed %d environment lookups", calls.Load())
	}
}
