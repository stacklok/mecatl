package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"strings"
	"testing"
)

func TestMicroVMEnvironments_Scenario2_CapabilityNegotiationFailsClosed(t *testing.T) {
	binding := Binding{
		Owner:         "caller:alice",
		SessionID:     "session-1",
		EnvironmentID: "environment-1",
		Ref:           "microvm:environment-1",
		Generation:    7,
	}
	key := []byte("0123456789abcdef0123456789abcdef")

	for _, missing := range RequiredCapabilities() {
		t.Run("missing "+string(missing), func(t *testing.T) {
			hello := validMultiplexHello(t, binding, key)
			hello.Capabilities = hello.Capabilities.Without(missing)
			reply, serverErr := exchangeProductionHandshake(t, binding, key, hello)
			if !errors.Is(serverErr, ErrCapabilityMismatch) {
				t.Fatalf("production multiplex without %q: got %v, want ErrCapabilityMismatch", missing, serverErr)
			}
			if reply.ErrorCode != "capability_mismatch" {
				t.Fatalf("production multiplex reply without %q: got %q, want capability_mismatch", missing, reply.ErrorCode)
			}
		})
	}

	t.Run("missing message bound", func(t *testing.T) {
		hello := validMultiplexHello(t, binding, key)
		hello.MaxMessageBytes = 0
		reply, serverErr := exchangeProductionHandshake(t, binding, key, hello)
		if !errors.Is(serverErr, ErrMessageBound) || reply.ErrorCode != "message_bound" {
			t.Fatalf("zero message bound: server=%v reply=%q, want ErrMessageBound", serverErr, reply.ErrorCode)
		}
	})

	t.Run("wrong generation", func(t *testing.T) {
		hello := validMultiplexHello(t, binding, key)
		hello.Binding.Generation++
		reply, serverErr := exchangeProductionHandshake(t, binding, key, hello)
		if !errors.Is(serverErr, ErrUnauthenticatedCapability) || reply.ErrorCode != "unauthenticated" {
			t.Fatalf("wrong generation: server=%v reply=%q, want unauthenticated", serverErr, reply.ErrorCode)
		}
	})

	t.Run("wrong protocol version", func(t *testing.T) {
		hello := validMultiplexHello(t, binding, key)
		hello.Version++
		reply, serverErr := exchangeProductionHandshake(t, binding, key, hello)
		if !errors.Is(serverErr, ErrProtocolVersion) || reply.ErrorCode != "protocol_version" {
			t.Fatalf("wrong version: server=%v reply=%q, want ErrProtocolVersion", serverErr, reply.ErrorCode)
		}
	})

	t.Run("production client receives negotiated capabilities", func(t *testing.T) {
		issuer, err := NewCapabilityIssuer(key)
		if err != nil {
			t.Fatal(err)
		}
		capability, err := issuer.Issue(binding)
		if err != nil {
			t.Fatal(err)
		}
		verifier, err := NewCapabilityVerifier(key)
		if err != nil {
			t.Fatal(err)
		}
		host, guest := net.Pipe()
		done := make(chan error, 1)
		go func() {
			done <- ServeMultiplex(context.Background(), guest, binding, verifier, map[ServiceName]Handler{
				ServiceWorkspace: func(context.Context, string, json.RawMessage, func(any) error) (any, string, error) {
					return struct{}{}, "", nil
				},
				ServiceExec: func(context.Context, string, json.RawMessage, func(any) error) (any, string, error) {
					return struct{}{}, "", nil
				},
			}, DefaultMaxMessageBytes)
		}()
		client, err := OpenClient(context.Background(), host, binding, capability, []ServiceName{ServiceWorkspace, ServiceExec}, DefaultMaxMessageBytes)
		if err != nil {
			t.Fatalf("OpenClient: %v", err)
		}
		agreement := client.Agreement()
		if err := validateAgreement(agreement, ProtocolVersion, RequiredCapabilities(), DefaultMaxMessageBytes); err != nil {
			t.Fatalf("negotiated production agreement: %v", err)
		}
		_ = client.Close()
		_ = guest.Close()
		if err := <-done; err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, os.ErrClosed) {
			t.Fatalf("ServeMultiplex: %v", err)
		}
	})

	t.Run("oversized frame", func(t *testing.T) {
		var frame bytes.Buffer
		if err := NewCodec(64).Write(&frame, map[string]string{"value": "too large"}); err != nil {
			t.Fatalf("write oversized test frame: %v", err)
		}
		var value map[string]string
		if err := NewCodec(8).Read(&frame, &value); !errors.Is(err, ErrFrameTooLarge) {
			t.Fatalf("read oversized frame: got %v, want ErrFrameTooLarge", err)
		}
	})

	if _, err := GuestControlOption("/run/mecatl/guest-control.sock"); err != nil {
		t.Fatalf("wire go-microvm vsock-to-UDS control transport: %v", err)
	}
	if _, err := GuestControlOption("relative-control.sock"); err == nil {
		t.Fatal("relative guest control socket unexpectedly accepted")
	}
}

func TestGuestProtocolHasSingleProductionHandshake(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, entry.Name(), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && (fn.Name.Name == "AcceptGuest" || fn.Name.Name == "OpenGuest") {
				t.Fatalf("dead independent handshake %s remains in %s", fn.Name.Name, entry.Name())
			}
		}
	}
}

func validMultiplexHello(t *testing.T, binding Binding, key []byte) multiplexHello {
	t.Helper()
	issuer, err := NewCapabilityIssuer(key)
	if err != nil {
		t.Fatal(err)
	}
	capability, err := issuer.Issue(binding)
	if err != nil {
		t.Fatal(err)
	}
	return multiplexHello{
		Version: ProtocolVersion, Binding: binding, Capability: capability,
		Services: []ServiceName{ServiceWorkspace, ServiceExec}, Capabilities: RequiredCapabilities(),
		MaxMessageBytes: DefaultMaxMessageBytes,
	}
}

func exchangeProductionHandshake(t *testing.T, expected Binding, key []byte, hello multiplexHello) (multiplexReply, error) {
	t.Helper()
	verifier, err := NewCapabilityVerifier(key)
	if err != nil {
		t.Fatal(err)
	}
	host, guest := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeMultiplex(context.Background(), guest, expected, verifier, map[ServiceName]Handler{
			ServiceWorkspace: func(context.Context, string, json.RawMessage, func(any) error) (any, string, error) {
				return struct{}{}, "", nil
			},
			ServiceExec: func(context.Context, string, json.RawMessage, func(any) error) (any, string, error) {
				return struct{}{}, "", nil
			},
		}, DefaultMaxMessageBytes)
	}()
	codec := NewCodec(DefaultMaxMessageBytes)
	if err := codec.Write(host, hello); err != nil {
		t.Fatal(err)
	}
	var reply multiplexReply
	if err := codec.Read(host, &reply); err != nil {
		t.Fatal(err)
	}
	_ = host.Close()
	_ = guest.Close()
	return reply, <-serverErr
}
