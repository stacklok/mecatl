package guestagent

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/stacklok/mecatl/environment/microvm/control"
)

func TestRepositoryRegisterClassifiesOnlyAssignedRootUnavailable(t *testing.T) {
	const secret = "token=guest-secret /private/repository errno=13"
	authorityKey := []byte("01234567890123456789012345678901")
	binding := control.Binding{
		Owner: "owner", SessionID: "session", EnvironmentID: "logical-id",
		Ref: "logical-id@1", Generation: 1, AssignedRoot: "/assigned/worktree",
	}
	issuer, err := control.NewCapabilityIssuer(authorityKey)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := issuer.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("resolved root failure is closed", func(t *testing.T) {
		server, err := NewRepositoryServer(RepositoryServerConfig{
			Owner: binding.Owner, Generation: binding.Generation, AuthorityKey: authorityKey,
			ResolveRoot: func(string) (string, error) { return "", errors.New(secret) },
		})
		if err != nil {
			t.Fatal(err)
		}
		response, serveErr := exchangeRepositoryControl(t, server, RepositoryControlRequest{
			Operation: RepositoryRegister, Binding: binding, Capability: capability,
		})
		if response.ErrorCode != "logical_root_unavailable" || !errors.Is(serveErr, ErrLogicalRootUnavailable) {
			t.Fatalf("response=%q err=%v, want closed logical-root classification", response.ErrorCode, serveErr)
		}
		if strings.Contains(response.ErrorCode, secret) || strings.Contains(serveErr.Error(), secret) {
			t.Fatalf("guest response leaked backend detail: response=%q err=%v", response.ErrorCode, serveErr)
		}
	})

	t.Run("open failure is closed", func(t *testing.T) {
		server, err := NewRepositoryServer(RepositoryServerConfig{
			Owner: binding.Owner, Generation: binding.Generation, AuthorityKey: authorityKey,
			ResolveRoot: func(string) (string, error) { return "/missing/secret/repository", nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		response, _ := exchangeRepositoryControl(t, server, RepositoryControlRequest{
			Operation: RepositoryRegister, Binding: binding, Capability: capability,
		})
		if response.ErrorCode != "logical_root_unavailable" {
			t.Fatalf("response=%q, want logical_root_unavailable", response.ErrorCode)
		}
	})

	t.Run("capability failure remains unauthenticated", func(t *testing.T) {
		resolved := false
		server, err := NewRepositoryServer(RepositoryServerConfig{
			Owner: binding.Owner, Generation: binding.Generation, AuthorityKey: authorityKey,
			ResolveRoot: func(string) (string, error) { resolved = true; return "", errors.New(secret) },
		})
		if err != nil {
			t.Fatal(err)
		}
		response, _ := exchangeRepositoryControl(t, server, RepositoryControlRequest{
			Operation: RepositoryRegister, Binding: binding, Capability: "invalid",
		})
		if response.ErrorCode != "unauthenticated" || resolved {
			t.Fatalf("response=%q resolved=%v, want unauthenticated before root resolution", response.ErrorCode, resolved)
		}
	})

	t.Run("binding failure remains unauthenticated", func(t *testing.T) {
		resolved := false
		server, err := NewRepositoryServer(RepositoryServerConfig{
			Owner: binding.Owner, Generation: binding.Generation, AuthorityKey: authorityKey,
			ResolveRoot: func(string) (string, error) { resolved = true; return "", errors.New(secret) },
		})
		if err != nil {
			t.Fatal(err)
		}
		wrong := binding
		wrong.Owner = "other"
		response, _ := exchangeRepositoryControl(t, server, RepositoryControlRequest{
			Operation: RepositoryRegister, Binding: wrong, Capability: capability,
		})
		if response.ErrorCode != "unauthenticated" || resolved {
			t.Fatalf("response=%q resolved=%v, want unauthenticated before root resolution", response.ErrorCode, resolved)
		}
	})

	t.Run("replay failure remains unauthenticated", func(t *testing.T) {
		root := t.TempDir()
		server, err := NewRepositoryServer(RepositoryServerConfig{
			Owner: binding.Owner, Generation: binding.Generation, AuthorityKey: authorityKey,
			ResolveRoot: func(string) (string, error) { return root, nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		request := RepositoryControlRequest{Operation: RepositoryRegister, Binding: binding, Capability: capability}
		first, firstErr := exchangeRepositoryControl(t, server, request)
		if firstErr != nil || first.ErrorCode != "" {
			t.Fatalf("first registration response=%q err=%v", first.ErrorCode, firstErr)
		}
		replayed, _ := exchangeRepositoryControl(t, server, request)
		if replayed.ErrorCode != "unauthenticated" {
			t.Fatalf("replayed response=%q, want unauthenticated", replayed.ErrorCode)
		}
	})
}

func exchangeRepositoryControl(t *testing.T, server *RepositoryServer, request RepositoryControlRequest) (RepositoryControlResponse, error) {
	t.Helper()
	host, guest := net.Pipe()
	errCh := make(chan error, 1)
	go func() { errCh <- server.ServeControl(context.Background(), guest) }()
	codec := control.NewCodec(control.DefaultMaxMessageBytes)
	if err := codec.Write(host, request); err != nil {
		t.Fatal(err)
	}
	var response RepositoryControlResponse
	if err := codec.Read(host, &response); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	return response, <-errCh
}
