package redisstore_test

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	"github.com/stacklok/mecatl/internal/adapter/redisstore"
)

func TestSecureRedisConnectionModes(t *testing.T) {
	t.Run("address only remains local plaintext compatibility", func(t *testing.T) {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mr.Close)
		st, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), AllowPlaintext: true})
		if err != nil {
			t.Fatalf("address-only connection: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
	})

	t.Run("address only requires explicit plaintext opt-in", func(t *testing.T) {
		mr, err := miniredis.Run()
		if err != nil {
			t.Fatal(err)
		}
		defer mr.Close()
		if _, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr()}); err == nil {
			t.Fatal("address-only connection succeeded without plaintext opt-in")
		}
	})

	t.Run("CA-only TLS", func(t *testing.T) {
		serverTLS, ca := redisTLSFixture(t)
		mr, err := miniredis.RunTLS(serverTLS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mr.Close)
		dir := t.TempDir()
		st, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), CAFile: writeFixtureFile(t, dir, "ca.pem", ca)})
		if err != nil {
			t.Fatalf("CA-only TLS connection: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
	})

	t.Run("trusted CA rejects wrong server name", func(t *testing.T) {
		serverTLS, ca := redisTLSFixture(t)
		mr, err := miniredis.RunTLS(serverTLS)
		if err != nil {
			t.Fatal(err)
		}
		defer mr.Close()
		_, port, err := net.SplitHostPort(mr.Addr())
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if _, err := redisstore.NewWithConfig(redisstore.Config{Addr: net.JoinHostPort("localhost", port), CAFile: writeFixtureFile(t, dir, "ca.pem", ca)}); err == nil {
			t.Fatal("trusted CA accepted a server certificate for the wrong name")
		}
	})

	t.Run("password uses default ACL user and normalizes one terminal newline", func(t *testing.T) {
		serverTLS, ca := redisTLSFixture(t)
		mr, err := miniredis.RunTLS(serverTLS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mr.Close)
		password := randomFixturePassword(t)
		mr.RequireAuth(password)
		dir := t.TempDir()
		st, err := redisstore.NewWithConfig(redisstore.Config{
			Addr: mr.Addr(), CAFile: writeFixtureFile(t, dir, "ca.pem", ca),
			PasswordFile: writeFixtureFile(t, dir, "password", password+"\r\n"),
		})
		if err != nil {
			t.Fatalf("default ACL password TLS connection: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
	})

	t.Run("credential whitespace is preserved except one terminal newline", func(t *testing.T) {
		serverTLS, ca := redisTLSFixture(t)
		mr, err := miniredis.RunTLS(serverTLS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mr.Close)
		password := randomFixturePassword(t)
		mr.RequireAuth(password)
		dir := t.TempDir()
		whitespacePassword := password + " \n"
		_, err = redisstore.NewWithConfig(redisstore.Config{
			Addr: mr.Addr(), CAFile: writeFixtureFile(t, dir, "ca.pem", ca),
			PasswordFile: writeFixtureFile(t, dir, "password", whitespacePassword),
		})
		if err == nil {
			t.Fatal("connection accepted a password with preserved trailing space")
		}
		if strings.Contains(err.Error(), whitespacePassword) {
			t.Fatal("connection error disclosed a Secret-mounted password")
		}
	})

	t.Run("username and password ACL", func(t *testing.T) {
		serverTLS, ca := redisTLSFixture(t)
		mr, err := miniredis.RunTLS(serverTLS)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(mr.Close)
		password := randomFixturePassword(t)
		mr.RequireUserAuth("worker", password)
		dir := t.TempDir()
		st, err := redisstore.NewWithConfig(redisstore.Config{
			Addr: mr.Addr(), CAFile: writeFixtureFile(t, dir, "ca.pem", ca),
			UsernameFile: writeFixtureFile(t, dir, "username", "worker\n"),
			PasswordFile: writeFixtureFile(t, dir, "password", password+"\n"),
		})
		if err != nil {
			t.Fatalf("ACL TLS connection: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
	})

	t.Run("system trust store is verified, not skipped", func(t *testing.T) {
		serverTLS, _ := redisTLSFixture(t)
		mr, err := miniredis.RunTLS(serverTLS)
		if err != nil {
			t.Fatal(err)
		}
		defer mr.Close()
		// The fixture CA is not in the host trust store, so system-trust mode must
		// fail closed. A success here would mean verification was skipped.
		if _, err := redisstore.NewWithConfig(redisstore.Config{Addr: mr.Addr(), TLS: true}); err == nil {
			t.Fatal("system-trust TLS accepted a certificate from an untrusted CA")
		}
	})

	t.Run("system trust store satisfies the credential TLS requirement", func(t *testing.T) {
		// Validation must accept TLS-without-CAFile alongside a credential; the
		// connection then fails on trust, not on configuration.
		dir := t.TempDir()
		_, err := redisstore.NewWithConfig(redisstore.Config{
			Addr:         "127.0.0.1:1",
			TLS:          true,
			PasswordFile: writeFixtureFile(t, dir, "password", "unused"),
		})
		if err == nil {
			t.Fatal("NewWithConfig succeeded against a closed port")
		}
		if strings.Contains(err.Error(), "require verified TLS") {
			t.Fatalf("system-trust TLS was rejected as unverified: %v", err)
		}
	})
}

func TestSecureRedisConfigurationRejectsInvalidCombinations(t *testing.T) {
	dir := t.TempDir()
	_, ca := redisTLSFixture(t)
	caFile := writeFixtureFile(t, dir, "ca.pem", ca)
	passwordFile := writeFixtureFile(t, dir, "password", "password")
	usernameFile := writeFixtureFile(t, dir, "username", "user")
	emptyFile := writeFixtureFile(t, dir, "empty", "")

	cases := map[string]redisstore.Config{
		"password without any TLS":  {Addr: "127.0.0.1:1", PasswordFile: passwordFile},
		"username without password": {Addr: "127.0.0.1:1", CAFile: caFile, UsernameFile: usernameFile},
		"empty username":            {Addr: "127.0.0.1:1", CAFile: caFile, UsernameFile: emptyFile, PasswordFile: passwordFile},
		"empty password":            {Addr: "127.0.0.1:1", CAFile: caFile, PasswordFile: emptyFile},
		"unreadable CA bundle":      {Addr: "127.0.0.1:1", CAFile: filepath.Join(dir, "absent.pem"), PasswordFile: passwordFile},
		// host:port is enforced on every path, not just the secure one.
		"URL instead of host:port":        {Addr: "redis://127.0.0.1:6379", AllowPlaintext: true},
		"host without port":               {Addr: "127.0.0.1", AllowPlaintext: true},
		"port without host":               {Addr: ":6379", AllowPlaintext: true},
		"URL instead of host:port secure": {Addr: "rediss://127.0.0.1:6379", CAFile: caFile},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := redisstore.NewWithConfig(cfg); err == nil {
				t.Fatal("NewWithConfig succeeded, want rejection")
			}
		})
	}
}
