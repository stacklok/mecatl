package client

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/contracts/sessionaffinity"
	"github.com/stacklok/mecatl/internal/adapter/clientauth"
	"github.com/stacklok/mecatl/internal/adapter/credentialstore"
)

type compatibilityTestService struct {
	mecatlv1.UnimplementedHarnessServiceServer

	response *mecatlv1.GetCompatibilityInfoResponse
	err      error
	mu       sync.Mutex
	metadata metadata.MD
}

func (s *compatibilityTestService) GetCompatibilityInfo(ctx context.Context, _ *mecatlv1.GetCompatibilityInfoRequest) (*mecatlv1.GetCompatibilityInfoResponse, error) {
	s.mu.Lock()
	s.metadata, _ = metadata.FromIncomingContext(ctx)
	s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return s.response, nil
}

func (s *compatibilityTestService) receivedMetadata() metadata.MD {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.metadata.Copy()
}

type compatibilityFixture struct {
	client   *Client
	service  *compatibilityTestService
	server   *grpc.Server
	conn     *grpc.ClientConn
	listener *bufconn.Listener
}

func newCompatibilityFixture(t *testing.T, response *mecatlv1.GetCompatibilityInfoResponse, serviceErr error, options ...grpc.DialOption) *compatibilityFixture {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	service := &compatibilityTestService{response: response, err: serviceErr}
	server := grpc.NewServer()
	mecatlv1.RegisterHarnessServiceServer(server, service)
	go func() { _ = server.Serve(listener) }()
	dialOptions := append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(bearerCreds{token: "fixture-token", allowInsecure: true}),
	}, options...)
	conn, err := grpc.NewClient("passthrough:///compatibility-test", dialOptions...)
	if err != nil {
		t.Fatalf("dial compatibility fixture: %v", err)
	}
	return &compatibilityFixture{
		client:   &Client{conn: conn, svc: mecatlv1.NewHarnessServiceClient(conn)},
		service:  service,
		server:   server,
		conn:     conn,
		listener: listener,
	}
}

func (f *compatibilityFixture) cleanup() {
	_ = f.conn.Close()
	f.server.Stop()
	_ = f.listener.Close()
}

func TestCheckCompatibility(t *testing.T) {
	t.Run("equal preserves ordinary metadata and removes session affinity", func(t *testing.T) {
		fixture := newCompatibilityFixture(t, &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: int32(SupportedAPIMajor)}, nil)
		defer fixture.cleanup()
		ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs(sessionaffinity.HeaderName, "session-1", "x-fixture", "retained"))
		if err := fixture.client.CheckCompatibility(ctx); err != nil {
			t.Fatalf("CheckCompatibility: %v", err)
		}
		got := fixture.service.receivedMetadata()
		if values := got.Get(sessionaffinity.HeaderName); len(values) != 0 {
			t.Fatalf("session affinity = %q, want absent", values)
		}
		if got.Get("authorization")[0] != "Bearer fixture-token" || got.Get("x-fixture")[0] != "retained" {
			t.Fatalf("metadata = %v, want auth and ordinary metadata retained", got)
		}
	})

	t.Run("equal ignores unknown features and disabled capabilities", func(t *testing.T) {
		fixture := newCompatibilityFixture(t, &mecatlv1.GetCompatibilityInfoResponse{
			ApiMajor:     int32(SupportedAPIMajor),
			Features:     []string{"future-unknown-feature"},
			Capabilities: &mecatlv1.ServerCapabilities{},
		}, nil)
		defer fixture.cleanup()
		if err := fixture.client.CheckCompatibility(t.Context()); err != nil {
			t.Fatalf("CheckCompatibility: %v", err)
		}
	})

	for _, tc := range []struct {
		name     string
		response *mecatlv1.GetCompatibilityInfoResponse
		want     string
	}{
		{name: "server newer", response: &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: 3}, want: "server API is newer than this mecatui (expected major 2, observed 3); update mecatui"},
		{name: "server older", response: &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: 1}, want: "server API is older than this mecatui (expected major 2, observed 1); update server"},
		{name: "zero is invalid", response: &mecatlv1.GetCompatibilityInfoResponse{}, want: "server returned invalid compatibility information"},
		{name: "negative is invalid", response: &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: -1}, want: "server returned invalid compatibility information"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCompatibilityFixture(t, tc.response, nil)
			defer fixture.cleanup()
			err := fixture.client.CheckCompatibility(t.Context())
			if message, ok := SafeCompatibilityError(err); !ok || message != tc.want {
				t.Fatalf("SafeCompatibilityError(%v) = %q, %v; want %q, true", err, message, ok, tc.want)
			}
			if tc.response.GetApiMajor() > 0 {
				var mismatch *CompatibilityError
				if !errors.As(err, &mismatch) || mismatch.Expected != SupportedAPIMajor || mismatch.Observed != APIMajor(tc.response.GetApiMajor()) {
					t.Fatalf("error = %v, want typed expected and observed majors", err)
				}
			}
		})
	}

	t.Run("missing compatibility RPC preserves unimplemented", func(t *testing.T) {
		fixture := newCompatibilityFixture(t, nil, status.Error(codes.Unimplemented, "remote detail must not be displayed"))
		defer fixture.cleanup()
		err := fixture.client.CheckCompatibility(t.Context())
		if status.Code(err) != codes.Unimplemented {
			t.Fatalf("status = %v, want Unimplemented", status.Code(err))
		}
		if cause := errors.Unwrap(err); status.Code(cause) != codes.Unimplemented {
			t.Fatalf("unwrapped status = %v, want Unimplemented", status.Code(cause))
		}
		if strings.Contains(err.Error(), "remote detail must not be displayed") {
			t.Fatalf("compatibility error exposed remote detail: %q", err)
		}
		if message, ok := SafeCompatibilityError(err); !ok || message != "server does not support compatibility checking; update server" {
			t.Fatalf("SafeCompatibilityError(%v) = %q, %v", err, message, ok)
		}
	})

	for _, code := range []codes.Code{codes.Unauthenticated, codes.PermissionDenied, codes.Unavailable, codes.DeadlineExceeded, codes.Canceled} {
		t.Run(code.String()+" is preserved", func(t *testing.T) {
			fixture := newCompatibilityFixture(t, nil, status.Error(code, "remote detail must not be displayed"))
			defer fixture.cleanup()
			err := fixture.client.CheckCompatibility(t.Context())
			if status.Code(err) != code || status.Convert(err).Message() != "remote detail must not be displayed" {
				t.Fatalf("error = %v, want unchanged status %v and message", err, code)
			}
			if code == codes.DeadlineExceeded {
				if t.Context().Err() != nil {
					t.Fatalf("caller context = %v, want unexpired", t.Context().Err())
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("remote deadline = %v, want DeadlineExceeded cause", err)
				}
			}
			if code == codes.Canceled && (t.Context().Err() != nil || errors.Is(err, context.Canceled)) {
				t.Fatalf("remote cancellation = %v, caller context = %v; want a remote failure, not operator interruption", err, t.Context().Err())
			}
			if _, ok := SafeCompatibilityError(err); ok {
				t.Fatalf("SafeCompatibilityError(%v) unexpectedly accepted remote error", err)
			}
		})
	}

	t.Run("transport unavailable is preserved", func(t *testing.T) {
		fixture := newCompatibilityFixture(t, &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: int32(SupportedAPIMajor)}, nil)
		defer fixture.cleanup()
		fixture.server.Stop()
		_ = fixture.listener.Close()
		if err := fixture.client.CheckCompatibility(t.Context()); status.Code(err) != codes.Unavailable {
			t.Fatalf("status = %v, want Unavailable", status.Code(err))
		}
	})
}

func TestCheckCompatibilityCallerCancellationPreserved(t *testing.T) {
	fixture := newCompatibilityFixture(t, &mecatlv1.GetCompatibilityInfoResponse{ApiMajor: int32(SupportedAPIMajor)}, nil)
	defer fixture.cleanup()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := fixture.client.CheckCompatibility(ctx); status.Code(err) != codes.Canceled || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want Canceled status and cause", err)
	}
}

func TestCompareAPIMajor(t *testing.T) {
	if err := compareAPIMajor(&mecatlv1.GetCompatibilityInfoResponse{ApiMajor: 1}, 2); err == nil {
		t.Fatal("lower positive server major was accepted")
	} else if message, _ := SafeCompatibilityError(err); message != "server API is older than this mecatui (expected major 2, observed 1); update server" {
		t.Fatalf("lower major message = %q", message)
	}
	if err := compareAPIMajor(nil, SupportedAPIMajor); err == nil {
		t.Fatal("nil descriptor was accepted")
	}
}

func TestCheckCompatibilityTokenAcquisitionCancellation(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "blocking source"
		if refresh {
			name = "offline refresh exchange"
		}
		t.Run(name, func(t *testing.T) {
			for _, deadline := range []bool{false, true} {
				name := "cancellation"
				if deadline {
					name = "earlier deadline"
				}
				t.Run(name, func(t *testing.T) {
					entered := make(chan struct{})
					var source TokenSource
					var wantSourceErr error = &AuthError{Reason: AuthStorageUnavailable}
					if refresh {
						source = newCompatibilityRefreshSource(t, entered)
						wantSourceErr = clientauth.ErrTokenExchange
					} else {
						source = tokenSourceFunc(func(ctx context.Context) (string, error) {
							close(entered)
							<-ctx.Done()
							return "", wantSourceErr
						})
					}
					ctx, cancel := context.WithCancel(t.Context())
					wantCause, wantCode := context.Canceled, codes.Canceled
					if deadline {
						cancel()
						ctx, cancel = context.WithTimeout(t.Context(), 2*time.Second)
						wantCause, wantCode = context.DeadlineExceeded, codes.DeadlineExceeded
					}
					defer cancel()
					var sourceErr error
					var probeDeadline time.Time
					fixture := newCompatibilityFixture(t, nil, nil,
						grpc.WithChainUnaryInterceptor(tokenSourceUnary(tokenSourceFunc(func(ctx context.Context) (string, error) {
							probeDeadline, _ = ctx.Deadline()
							token, err := source.Token(ctx)
							sourceErr = err
							return token, err
						}))))
					defer fixture.cleanup()
					result := make(chan error, 1)
					started := time.Now()
					go func() { result <- fixture.client.CheckCompatibility(ctx) }()
					select {
					case <-entered:
					case err := <-result:
						t.Fatalf("probe finished before token acquisition: %v", err)
					case <-t.Context().Done():
						t.Fatal("test canceled before token acquisition")
					}
					if !deadline {
						cancel()
					}
					err := <-result
					if sourceErr != wantSourceErr || errors.Is(sourceErr, wantCause) {
						t.Fatalf("source error = %v, want closed sentinel %v without context cause", sourceErr, wantSourceErr)
					}
					if !errors.Is(err, wantCause) || status.Code(err) != wantCode {
						t.Fatalf("probe error = %v, want cause %v and status %v", err, wantCause, wantCode)
					}
					if _, ok := SafeCompatibilityError(err); ok {
						t.Fatal("context error classified as compatibility mismatch")
					}
					if deadline {
						callerDeadline, _ := ctx.Deadline()
						if !probeDeadline.Equal(callerDeadline) {
							t.Fatalf("probe deadline = %v, want caller deadline %v", probeDeadline, callerDeadline)
						}
					} else if probeDeadline.Before(started.Add(compatibilityCheckTimeout)) || probeDeadline.After(time.Now().Add(compatibilityCheckTimeout)) {
						t.Fatalf("probe deadline %v is not bounded by the ten-second timeout", probeDeadline)
					}
				})
			}
		})
	}
}

func newCompatibilityRefreshSource(t *testing.T, entered chan struct{}) *clientauth.RefreshSource {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize",
				"token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/keys",
				"code_challenge_methods_supported": []string{"S256"},
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
				"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "compatibility-test",
				"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": "AQAB",
			}}})
		case "/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "refresh_token" {
				t.Error("expected a refresh_token exchange")
			}
			close(entered)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	store, err := credentialstore.NewMemoryBackend().Open("compatibility-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	creds, err := clientauth.NewCredentials(store)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := clientauth.OpenRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := clientauth.Identity{
		Target: "remote.example:443", Issuer: server.URL, ClientID: "mecatui",
		Audience: "vmcp", RedirectURI: "http://127.0.0.1:8080/callback", Scopes: []string{"openid"},
	}
	original := clientauth.Token{
		AccessToken: "fixture-access", RefreshToken: "fixture-refresh", TokenType: "Bearer",
		Expiry: time.Now().Add(-time.Minute).UTC().Format(time.RFC3339),
	}
	if _, err := creds.Save(t.Context(), id, original, nil); err != nil {
		t.Fatal(err)
	}
	source, err := clientauth.NewRefreshSource(t.Context(), creds, clientauth.LoginConfig{
		Identity: id, Registry: registry, HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	t.Cleanup(func() {
		stored, err := creds.Load(context.Background(), id)
		if err != nil || stored.Token != original {
			t.Errorf("canceled exchange changed stored credentials (load error: %v)", err)
		}
	})
	return source
}

func TestCheckCompatibilityTokenErrorUnchanged(t *testing.T) {
	want := &AuthError{Reason: AuthStorageUnavailable}
	fixture := newCompatibilityFixture(t, nil, nil,
		grpc.WithChainUnaryInterceptor(tokenSourceUnary(tokenSourceFunc(func(context.Context) (string, error) {
			return "", want
		}))))
	defer fixture.cleanup()
	if err := fixture.client.CheckCompatibility(t.Context()); err != want {
		t.Fatalf("error = %v, want unchanged token error", err)
	}
}
