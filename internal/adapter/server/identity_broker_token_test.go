package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memfs"
	"github.com/stacklok/mecatl/engine/adapter/memledger"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/sessnap"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/team"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/identitybroker"
)

// userBearer is the user's AS-1 access token: distinctive, so a leak anywhere
// is found by substring search.
const userBearer = "as1-user-bearer-7f3c9d2e41b8-must-not-leak"

// brokerTokenValidator vouches for userBearer as brokerAlice, the owner the
// broker tests expect.
func brokerTokenValidator() fakeValidator {
	return fakeValidator{ok: map[string]session.Principal{userBearer: *brokerAlice}}
}

// leakDiagnostics records every diagnostic record and bound argument as text.
type leakDiagnostics struct {
	mu    sync.Mutex
	lines []string
}

func (d *leakDiagnostics) Log(_ context.Context, level port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, fmt.Sprintf("%v %s %v %+v", level, msg, args, args))
}

func (d *leakDiagnostics) With(args ...any) port.Diagnostics {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lines = append(d.lines, fmt.Sprintf("with %v %+v", args, args))
	return d
}

func (d *leakDiagnostics) text() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.lines, "\n")
}

// wantNewOpenTokens checks that the broker opened at least one session since
// from opens and that every such open carried want (zero when want is "").
func wantNewOpenTokens(t *testing.T, broker *recordingBroker, from int, want string) {
	t.Helper()
	opens := broker.openRequests()
	if len(opens) <= from {
		t.Fatalf("OpenSession calls = %d, want more than %d", len(opens), from)
	}
	for i, open := range opens[from:] {
		if got := open.AccessToken.Value(); got != want {
			t.Fatalf("open %d for %s carried a token %t, want the request's token %t (matches = %t)",
				from+i, open.HostSessionID, got != "", want != "", got == want)
		}
	}
}

// withinRequest runs fn inside the handler of a request the authenticator
// admits with bearer on surface ("unary", "stream", or "http").
func withinRequest(t *testing.T, surface string, a *server.Authenticator, bearer string, fn func(context.Context)) {
	t.Helper()
	ctx := metadata.NewIncomingContext(t.Context(), metadata.Pairs("authorization", "Bearer "+bearer))
	ran := false
	var err error
	switch surface {
	case "unary":
		_, err = a.UnaryInterceptor()(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/mecatl.v1.HarnessService/CreateSession"},
			func(c context.Context, _ any) (any, error) { ran = true; fn(c); return nil, nil })
	case "stream":
		err = a.StreamInterceptor()(nil, fakeServerStream{ctx: ctx}, &grpc.StreamServerInfo{FullMethod: "/mecatl.v1.HarnessService/Converse"},
			func(_ any, ss grpc.ServerStream) error { ran = true; fn(ss.Context()); return nil })
	case "http":
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		a.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { ran = true; fn(r.Context()) })).ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			err = fmt.Errorf("HTTP status %d", rec.Code)
		}
	default:
		t.Fatalf("unknown surface %q", surface)
	}
	if err != nil || !ran {
		t.Fatalf("%s request: handler ran %t, err %v", surface, ran, err)
	}
}

// tokenTransport drives the user-request paths that open a broker session over
// one wire surface, with userBearer as the caller's bearer.
type tokenTransport struct {
	create func(t *testing.T) session.SessionID
	fork   func(t *testing.T, source session.SessionID) session.SessionID
	clear  func(t *testing.T, source session.SessionID) session.SessionID
	// prompt returns the failure the caller saw, or "" when the run succeeded.
	prompt func(t *testing.T, id session.SessionID) string
}

func grpcTokenTransport(t *testing.T, svc *server.Service, auth *server.Authenticator) tokenTransport {
	client, cleanup := dialGRPCSecure(t, svc, auth)
	t.Cleanup(cleanup)
	ctx := func(t *testing.T) context.Context { return bearerCtx(t.Context(), userBearer) }
	return tokenTransport{
		create: func(t *testing.T) session.SessionID {
			resp, err := client.CreateSession(ctx(t), &mecatlv1.CreateSessionRequest{})
			if err != nil {
				t.Fatalf("CreateSession: %v", err)
			}
			return session.SessionID(resp.GetSessionId())
		},
		fork: func(t *testing.T, source session.SessionID) session.SessionID {
			resp, err := client.ForkSession(ctx(t), &mecatlv1.ForkSessionRequest{SourceSessionId: string(source)})
			if err != nil {
				t.Fatalf("ForkSession: %v", err)
			}
			return session.SessionID(resp.GetSessionId())
		},
		clear: func(t *testing.T, source session.SessionID) session.SessionID {
			resp, err := client.ClearSession(ctx(t), &mecatlv1.ClearSessionRequest{SourceSessionId: string(source)})
			if err != nil {
				t.Fatalf("ClearSession: %v", err)
			}
			return session.SessionID(resp.GetSessionId())
		},
		prompt: func(t *testing.T, id session.SessionID) string {
			stream, err := client.Converse(ctx(t))
			if err != nil {
				t.Fatalf("Converse: %v", err)
			}
			if err := stream.Send(&mecatlv1.ConverseRequest{Kind: &mecatlv1.ConverseRequest_Prompt{Prompt: &mecatlv1.Prompt{SessionId: string(id), Text: "hello"}}}); err != nil {
				t.Fatalf("send prompt: %v", err)
			}
			_ = stream.CloseSend()
			for {
				_, err := stream.Recv()
				if errors.Is(err, io.EOF) {
					return ""
				}
				if err != nil {
					return err.Error()
				}
			}
		},
	}
}

func httpTokenTransport(svc *server.Service, auth *server.Authenticator) tokenTransport {
	h := auth.Middleware(server.NewHTTPHandler(svc))
	do := func(t *testing.T, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+userBearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	created := func(t *testing.T, path string) session.SessionID {
		t.Helper()
		rec := do(t, path, `{}`)
		if rec.Code != http.StatusCreated {
			t.Fatalf("POST %s = %d %s", path, rec.Code, rec.Body.String())
		}
		var out struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.SessionID == "" {
			t.Fatalf("POST %s body %q: %v", path, rec.Body.String(), err)
		}
		return session.SessionID(out.SessionID)
	}
	return tokenTransport{
		create: func(t *testing.T) session.SessionID { return created(t, "/v1/sessions") },
		fork: func(t *testing.T, source session.SessionID) session.SessionID {
			return created(t, "/v1/sessions/"+string(source)+"/fork")
		},
		clear: func(t *testing.T, source session.SessionID) session.SessionID {
			return created(t, "/v1/sessions/"+string(source)+"/clear")
		},
		prompt: func(t *testing.T, id session.SessionID) string {
			rec := do(t, "/v1/sessions/"+string(id)+"/prompt", `{"text":"hello"}`)
			if rec.Code != http.StatusOK {
				return fmt.Sprintf("%d %s", rec.Code, rec.Body.String())
			}
			return ""
		},
	}
}

// TestIdentityBrokerOpenCarriesTheRequestAccessToken proves every user request
// that opens a broker session hands the broker the bearer that authenticated
// it, on both wire surfaces, and that the token reaches nothing else: not
// diagnostics, errors, the stored session, the event log, or the model.
func TestIdentityBrokerOpenCarriesTheRequestAccessToken(t *testing.T) {
	for _, transport := range []string{"grpc", "http"} {
		t.Run(transport, func(t *testing.T) {
			broker := newRecordingBroker()
			diag := &leakDiagnostics{}
			var mu sync.Mutex
			var requests []string
			observe := func(req port.LLMRequest) {
				body, err := json.Marshal(req)
				if err != nil {
					t.Errorf("marshal model request: %v", err)
				}
				mu.Lock()
				requests = append(requests, req.System.Render()+string(body))
				mu.Unlock()
			}
			llm := mockllm.NewWith([]mockllm.Option{mockllm.WithRequestObserver(observe)}, mockllm.TextTurn("done"))
			var store *memstore.Store
			events := memstore.NewEventLog()
			svc := newIdentityBrokerService(t, broker, llm, func(cfg *server.Config) {
				store = cfg.Store.(*memstore.Store)
				cfg.EventLog = events
				cfg.Diagnostics = diag
			})
			auth := server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator(), Diagnostics: diag})
			wire := httpTokenTransport(svc, auth)
			if transport == "grpc" {
				wire = grpcTokenTransport(t, svc, auth)
			}

			before := broker.openCount()
			source := wire.create(t)
			wantNewOpenTokens(t, broker, before, userBearer)

			before = broker.openCount()
			forked := wire.fork(t, source)
			wantNewOpenTokens(t, broker, before, userBearer)

			before = broker.openCount()
			cleared := wire.clear(t, source)
			wantNewOpenTokens(t, broker, before, userBearer)

			// Run entry reopens a lost broker session with the prompt's token.
			lost, ok := broker.find(t, source)
			if !ok {
				t.Fatal("source has no open broker session")
			}
			if err := broker.inner.CloseSession(t.Context(), identitybroker.CloseSessionRequest{SessionID: lost.ID, Reason: identitybroker.ReasonSessionClosed}); err != nil {
				t.Fatal(err)
			}
			before = broker.openCount()
			if failure := wire.prompt(t, source); failure != "" {
				t.Fatalf("prompt: %s", failure)
			}
			wantNewOpenTokens(t, broker, before, userBearer)

			// A failed broker call surfaces an error without the token.
			broker.setUnavailable(true)
			failure := wire.prompt(t, source)
			broker.setUnavailable(false)
			if failure == "" {
				t.Fatal("prompt with the identity broker down succeeded")
			}
			if strings.Contains(failure, userBearer) {
				t.Fatal("the caller's error contains the access token")
			}

			if strings.Contains(diag.text(), userBearer) {
				t.Fatal("diagnostics contain the access token")
			}
			if diag.text() == "" {
				t.Fatal("no diagnostics were recorded to search")
			}
			for _, id := range []session.SessionID{source, forked, cleared} {
				loaded, err := store.Load(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				persisted, err := sessnap.Marshal(loaded)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(persisted), userBearer) {
					t.Fatalf("stored session %s contains the access token", id)
				}
				var logged strings.Builder
				for ev, err := range events.Read(t.Context(), id) {
					if err != nil {
						t.Fatal(err)
					}
					body, err := json.Marshal(ev)
					if err != nil {
						t.Fatal(err)
					}
					logged.Write(body)
					fmt.Fprintf(&logged, "%+v", ev)
				}
				if strings.Contains(logged.String(), userBearer) {
					t.Fatalf("event log of %s contains the access token", id)
				}
				// Positive control: the source's run was recorded.
				if id == source && logged.Len() == 0 {
					t.Fatal("the source's event log is empty")
				}
			}
			mu.Lock()
			seen := append([]string(nil), requests...)
			mu.Unlock()
			if len(seen) == 0 {
				t.Fatal("the model saw no request")
			}
			for _, req := range seen {
				// Positive control: the observed text carries the conversation.
				if !strings.Contains(req, "hello") {
					t.Fatalf("model request does not carry the prompt: %s", req)
				}
				if strings.Contains(req, userBearer) {
					t.Fatal("the model's input contains the access token")
				}
			}
		})
	}
}

// TestIdentityBrokerTeamRunCarriesTheRequestAccessToken proves a team run
// opens its root session's broker session with the run request's token.
func TestIdentityBrokerTeamRunCarriesTheRequestAccessToken(t *testing.T) {
	for _, transport := range []string{"grpc", "http"} {
		t.Run(transport, func(t *testing.T) {
			store := memstore.New()
			ref := session.EnvironmentRef{Kind: session.EnvKindLocal, ID: "/ws", Revision: "in-tree-v1"}
			env := tool.MustEnvironment(ref, memfs.NewWorkspace("/ws"), memledger.New(), nil)
			provider := &teamRootCaptureProvider{}
			memberEngine := func(*team.Team, agent.MemberSpec, string) agent.MemberBuild {
				return agent.MemberBuild{Engine: agent.NewEngine(agent.Deps{LLM: provider, Catalog: tool.NewCatalog(), Model: "test"})}
			}
			broker := newRecordingBroker()
			svc, err := server.NewService(server.Config{
				Engine: agent.NewEngine(agent.Deps{Catalog: tool.NewCatalog()}), Store: store,
				PlacementProvider: delegationPlacementProvider{binding: server.PlacementBinding{Environment: env, Ref: ref}},
				PlacementScope:    "test-scope", MemberEngine: memberEngine, IdentityBroker: broker,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Close)
			// The root session as a restart leaves it: saved, with no broker session.
			source := session.New("team-root", session.ModeDefault, ref, session.Limits{}, time.Unix(1, 0))
			if err := source.RestoreLabels(brokerAlice, session.Authority{}); err != nil {
				t.Fatal(err)
			}
			if err := store.Create(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			teamID, _, err := svc.CreateTeamForSession(t.Context(), source.ID, "reviewers", "goal", 0, []agent.MemberSpec{{Name: "lead", Lead: true}})
			if err != nil {
				t.Fatal(err)
			}
			auth := server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator()})
			before := broker.openCount()
			if transport == "grpc" {
				client, cleanup := dialGRPCSecure(t, svc, auth)
				t.Cleanup(cleanup)
				stream, err := client.RunTeam(bearerCtx(t.Context(), userBearer), &mecatlv1.RunTeamRequest{TeamId: teamID})
				if err != nil {
					t.Fatal(err)
				}
				for {
					_, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					if err != nil {
						t.Fatalf("RunTeam: %v", err)
					}
				}
			} else {
				req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/teams/"+teamID+"/run", nil)
				req.Header.Set("Authorization", "Bearer "+userBearer)
				rec := httptest.NewRecorder()
				auth.Middleware(server.NewHTTPHandler(svc)).ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("POST run team = %d %s", rec.Code, rec.Body.String())
				}
			}
			wantNewOpenTokens(t, broker, before, userBearer)
			wantBrokerSession(t, broker, source)
		})
	}
}

// TestIdentityBrokerOpenWithoutAUserRequestHasNoToken proves a broker session
// opened outside a live user request carries no access token.
func TestIdentityBrokerOpenWithoutAUserRequestHasNoToken(t *testing.T) {
	auth := oidcOnly(t, brokerTokenValidator(), 0, 0)
	// retained runs the authenticator for a request and returns its context
	// after the request has returned, as a context kept by later work would be.
	retained := map[string]func(*testing.T) context.Context{
		"retained unary context": func(t *testing.T) context.Context {
			ctx, err := callUnary(auth, userBearer)
			if err != nil {
				t.Fatal(err)
			}
			return ctx
		},
		"retained stream context": func(t *testing.T) context.Context {
			ctx, err := callStream(auth, userBearer)
			if err != nil {
				t.Fatal(err)
			}
			return ctx
		},
		"retained http context": func(t *testing.T) context.Context {
			ctx, rec := callHTTP(auth, userBearer)
			if ctx == nil {
				t.Fatalf("handler did not run: %d", rec.Code)
			}
			return ctx
		},
	}
	cases := map[string]func(*testing.T, *server.Service) error{
		"scheduled create": func(t *testing.T, svc *server.Service) error {
			_, err := svc.CreateSessionWithProfile(t.Context(), session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault,
				server.WithSessionID("fire-1"), server.WithOwner(brokerAlice), server.WithScheduledRelationship("nightly", ""))
			return err
		},
		"principal without a request": func(t *testing.T, svc *server.Service) error {
			_, err := svc.CreateSession(aliceContext(t), session.ModeDefault, session.Limits{})
			return err
		},
	}
	for name, ctxFor := range retained {
		cases[name] = func(t *testing.T, svc *server.Service) error {
			ctx := ctxFor(t)
			if p := session.PrincipalFromContext(ctx); p == nil || p.Subject != brokerAlice.Subject {
				t.Fatalf("retained context principal = %+v, want alice", p)
			}
			_, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{})
			return err
		}
	}
	for name, create := range cases {
		t.Run(name, func(t *testing.T) {
			broker := newRecordingBroker()
			svc := newIdentityBrokerService(t, broker, mockllm.New())
			if err := create(t, svc); err != nil {
				t.Fatal(err)
			}
			wantNewOpenTokens(t, broker, 0, "")
		})
	}
}

// TestIdentityBrokerStaticTokenAuthCarriesNoAccessToken proves the static
// server token is never handed to the broker as a user's access token, and a
// rejected bearer never reaches a handler that could open a broker session.
func TestIdentityBrokerStaticTokenAuthCarriesNoAccessToken(t *testing.T) {
	const staticToken = "static-server-token-3a9e"
	for _, surface := range []string{"unary", "stream", "http"} {
		for _, tc := range []struct {
			name   string
			auth   *server.Authenticator
			bearer string
			want   string
		}{
			// Positive control: the same in-request create carries a verified
			// identity bearer.
			{name: "identity", auth: server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator()}), bearer: userBearer, want: userBearer},
			{name: "static", auth: server.NewAuthenticator(server.SecurityConfig{AuthToken: staticToken}), bearer: staticToken},
		} {
			t.Run(tc.name+" "+surface, func(t *testing.T) {
				broker := newRecordingBroker()
				svc := newIdentityBrokerService(t, broker, mockllm.New())
				withinRequest(t, surface, tc.auth, tc.bearer, func(ctx context.Context) {
					// The static token yields no principal, so the session gets
					// its owner here; the open still has no access token.
					if session.PrincipalFromContext(ctx) == nil {
						ctx = session.WithPrincipal(ctx, brokerAlice)
					}
					if _, err := svc.CreateSession(ctx, session.ModeDefault, session.Limits{}); err != nil {
						t.Fatal(err)
					}
				})
				wantNewOpenTokens(t, broker, 0, tc.want)
			})
		}
	}
	// A fire-now request creates the scheduled session inside the user's
	// request; the scheduled session still opens without the user's token.
	for _, surface := range []string{"unary", "stream", "http"} {
		t.Run("scheduled create in a request "+surface, func(t *testing.T) {
			broker := newRecordingBroker()
			svc := newIdentityBrokerService(t, broker, mockllm.New())
			auth := server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator()})
			withinRequest(t, surface, auth, userBearer, func(ctx context.Context) {
				if _, err := svc.CreateSessionWithProfile(ctx, session.ModeDefault, session.Limits{}, server.ProviderSelector{}, server.ProfileDefault,
					server.WithSessionID("fire-now-1"), server.WithOwner(brokerAlice), server.WithScheduledRelationship("nightly", "")); err != nil {
					t.Fatal(err)
				}
			})
			wantNewOpenTokens(t, broker, 0, "")
		})
	}
	t.Run("rejected bearer", func(t *testing.T) {
		broker := newRecordingBroker()
		svc := newIdentityBrokerService(t, broker, mockllm.New())
		auth := server.NewAuthenticator(server.SecurityConfig{Validator: brokerTokenValidator()})
		client, cleanup := dialGRPCSecure(t, svc, auth)
		t.Cleanup(cleanup)
		if _, err := client.CreateSession(bearerCtx(t.Context(), "forged-"+userBearer), &mecatlv1.CreateSessionRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("gRPC create with a rejected bearer: %v, want Unauthenticated", err)
		}
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/v1/sessions", strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer forged-"+userBearer)
		rec := httptest.NewRecorder()
		auth.Middleware(server.NewHTTPHandler(svc)).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("HTTP create with a rejected bearer = %d, want 401", rec.Code)
		}
		if n := broker.openCount(); n != 0 {
			t.Fatalf("a rejected bearer opened %d broker sessions", n)
		}
		sessions, err := svc.ListSessions(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if len(sessions) != 0 {
			t.Fatalf("a rejected bearer created %d sessions", len(sessions))
		}
	})
}
