//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package credentialstore

import (
	"bytes"
	"errors"
	"testing"
)

func TestEnvelopeStrictParsingAndAuthentication(t *testing.T) {
	key := bytes.Repeat([]byte{0x55}, 32)
	namespace := []byte("namespace")
	recordKey := []byte{0, '/', 0xff}
	envelope, version, err := sealEnvelope(key, namespace, recordKey, []byte("secret"), bytes.NewReader(bytes.Repeat([]byte{1}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	plaintext, reopened, err := openEnvelope(key, namespace, recordKey, envelope)
	if err != nil || string(plaintext) != "secret" || !version.Equal(reopened) {
		t.Fatalf("open = %q, %v", plaintext, err)
	}

	mutations := map[string]func([]byte) []byte{
		"magic":           func(b []byte) []byte { b[0] ^= 1; return b },
		"version":         func(b []byte) []byte { b[8]++; return b },
		"algorithm":       func(b []byte) []byte { b[9]++; return b },
		"nonce length":    func(b []byte) []byte { b[10]++; return b },
		"flags":           func(b []byte) []byte { b[11] = 1; return b },
		"declared length": func(b []byte) []byte { b[15]++; return b },
		"nonce":           func(b []byte) []byte { b[16] ^= 1; return b },
		"ciphertext":      func(b []byte) []byte { b[28] ^= 1; return b },
		"tag":             func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		"trailing":        func(b []byte) []byte { return append(b, 0) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := mutate(bytes.Clone(envelope))
			if _, _, err := openEnvelope(key, namespace, recordKey, candidate); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("open mutation = %v", err)
			}
		})
	}
	for length := 0; length < 44; length++ {
		if _, _, err := openEnvelope(key, namespace, recordKey, envelope[:length]); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("truncation %d = %v", length, err)
		}
	}
	if _, _, err := openEnvelope(key, []byte("other"), recordKey, envelope); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong namespace = %v", err)
	}
	if _, _, err := openEnvelope(key, namespace, []byte("other"), envelope); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong record key = %v", err)
	}
}

func FuzzDecodeEnvelope(f *testing.F) {
	key := bytes.Repeat([]byte{9}, 32)
	valid, _, err := sealEnvelope(key, []byte("namespace"), []byte("key"), []byte("value"), bytes.NewReader(bytes.Repeat([]byte{1}, 64)))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte("garbage"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > MaxEnvelopeBytes+1 {
			return
		}
		plaintext, _, err := openEnvelope(key, []byte("namespace"), []byte("key"), data)
		if err == nil && len(plaintext) > MaxValueBytes {
			t.Fatalf("decoded oversized plaintext: %d", len(plaintext))
		}
	})
}

func FuzzValidateNamespace(f *testing.F) {
	for _, seed := range []string{"namespace", "", " leading", "binary\x00"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, namespace string) {
		err := validateNamespace(namespace)
		if err == nil {
			first := namespacePhysicalName([]byte(namespace))
			second := namespacePhysicalName([]byte(namespace))
			if first != second || len(first) != len("ns-v1-")+64 {
				t.Fatalf("physical name is not fixed and deterministic: %q", first)
			}
		} else if !errors.Is(err, ErrInvalidNamespace) {
			t.Fatalf("validation = %v", err)
		}
	})
}

func FuzzValidateRecordKey(f *testing.F) {
	f.Add([]byte("key"))
	f.Add([]byte{})
	f.Add([]byte{0, '/', 0xff})
	f.Fuzz(func(t *testing.T, key []byte) {
		err := validateRecordKey(key)
		switch {
		case len(key) == 0 && !errors.Is(err, ErrInvalidKey):
			t.Fatalf("empty = %v", err)
		case len(key) > MaxRecordKeyBytes && !errors.Is(err, ErrTooLarge):
			t.Fatalf("oversized = %v", err)
		case len(key) > 0 && len(key) <= MaxRecordKeyBytes && err != nil:
			t.Fatalf("valid = %v", err)
		}
	})
}

func FuzzValidateEncryptionKey(f *testing.F) {
	f.Add([]byte{})
	f.Add(make([]byte, 32))
	f.Add(make([]byte, 33))
	f.Fuzz(func(t *testing.T, key []byte) {
		err := validateEncryptionKey(key)
		if (len(key) == 32) != (err == nil) {
			t.Fatalf("len=%d err=%v", len(key), err)
		}
	})
}
