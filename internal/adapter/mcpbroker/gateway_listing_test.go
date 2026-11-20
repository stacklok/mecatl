package mcpbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	mcpadapter "github.com/stacklok/mecatl/internal/adapter/mcp"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/inprocesshttp"
)

// anonymousMCPProfile serves an anonymous MCP backend, offline, with tools,
// each named and read-only as given, and returns its profile.
func anonymousMCPProfile(t *testing.T, name string, tools map[string]bool) ToolHiveProfile {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: name, Version: "v1"}, nil)
	for tool, readOnly := range tools {
		var outputSchema any
		if tool == "echo" {
			outputSchema = echoOutputSchema()
		}
		mcpsdk.AddTool(server, &mcpsdk.Tool{Name: tool, Description: name + " " + tool, OutputSchema: outputSchema, Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: readOnly}},
			func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
				return &mcpsdk.CallToolResult{}, nil, nil
			})
	}
	backend := httptest.NewServer(mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, nil))
	t.Cleanup(backend.Close)
	return ToolHiveProfile{Name: name, URL: backend.URL, Auth: authNone}
}

// echoOutputSchema is the output schema every test backend's echo tool
// declares.
func echoOutputSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}
}

// embeddedGatewayProcess is a ToolHive process with the embedded gateway in
// front of its real vMCP, reached in process, and two anonymous backends. Its
// gateway lists within limits.
func embeddedGatewayProcess(t *testing.T, limits mcpadapter.GatewayListLimits) *Process {
	t.Helper()
	t.Setenv("MECATL_TEST_CLIENT_SECRET", "construction-only-secret")
	t.Setenv("TMPDIR", t.TempDir())
	calls := GatewayCalls{Authorizer: &fakeAuthorizer{}, Credentials: &fakeCredentials{}, Servers: []string{"notes", "other"}, ListLimits: limits}
	process, err := NewToolHiveProcess(t.Context(), ToolHiveConfig{
		CallbackURL: as2TestCallbackURL, BrokerClient: newAS2BrokerClient(t).config, GatewayCalls: &calls,
		Profiles: []ToolHiveProfile{
			staticProtectedProfile("private"),
			anonymousMCPProfile(t, "notes", map[string]bool{"echo": true, "ping": false}),
			anonymousMCPProfile(t, "other", map[string]bool{"ping": false}),
		},
		EmbeddedGatewayClient: func(vmcp http.Handler, endpoint string) (*http.Client, error) {
			target, err := url.Parse(endpoint)
			if err != nil {
				return nil, err
			}
			return inprocesshttp.NewStreamingClient(vmcp, func(u *url.URL) error {
				if u.Host != target.Host || u.Path != target.Path {
					return fmt.Errorf("not the gateway endpoint: %s", u)
				}
				return nil
			}), nil
		},
	})
	if err != nil {
		t.Fatalf("NewToolHiveProcess: %v", err)
	}
	t.Cleanup(func() { _ = process.Close() })
	return process
}

// vmcpToken is a token the vMCP accepts: signed by the embedded authorization
// server for the vMCP's resource.
func vmcpToken(t *testing.T, process *Process) string {
	t.Helper()
	signing, err := process.auth.KeyProvider().SigningKey(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	token := jwt.NewWithClaims(jwt.GetSigningMethod(signing.Algorithm), jwt.MapClaims{
		"iss": as2TestIssuer, "aud": []string{process.Gateway.Resource()}, "sub": "alice",
		"iat": now.Unix(), "nbf": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	token.Header["kid"] = signing.KeyID
	signed, err := token.SignedString(signing.Key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestEmbeddedGatewayListsOnlyTheServersTools(t *testing.T) {
	process := embeddedGatewayProcess(t, mcpadapter.GatewayListLimits{})
	credential := staticTokenSource(vmcpToken(t, process))
	for server, want := range map[string][]GatewayTool{
		"notes": {
			{Name: "echo", Description: "notes echo", ReadOnly: true},
			{Name: "ping", Description: "notes ping"},
		},
		"other": {{Name: "ping", Description: "other ping"}},
	} {
		listed, err := process.Gateway.ListTools(t.Context(), server, credential)
		if err != nil {
			t.Fatalf("ListTools(%s): %v", server, err)
		}
		for i := range listed {
			if listed[i].InputSchema["type"] != "object" {
				t.Fatalf("ListTools(%s) tool %q schema = %v, want an object schema", server, listed[i].Name, listed[i].InputSchema)
			}
			listed[i].InputSchema = nil
			// The vMCP passes a declared output schema on, normalised (it adds
			// an empty "required"), and invents none for a tool without one.
			if listed[i].Name == "echo" {
				if listed[i].OutputSchema["type"] != "object" ||
					!reflect.DeepEqual(listed[i].OutputSchema["properties"], echoOutputSchema()["properties"]) {
					t.Fatalf("ListTools(%s) tool echo output schema = %v, want %v", server, listed[i].OutputSchema, echoOutputSchema())
				}
			} else if len(listed[i].OutputSchema) != 0 {
				t.Fatalf("ListTools(%s) tool %q output schema = %v, want none", server, listed[i].Name, listed[i].OutputSchema)
			}
			listed[i].OutputSchema = nil
		}
		if !reflect.DeepEqual(listed, want) {
			t.Fatalf("ListTools(%s) = %+v, want %+v", server, listed, want)
		}
	}
	if listed, err := process.Gateway.ListTools(t.Context(), "private", credential); err != nil || len(listed) != 0 {
		t.Fatalf("ListTools(private) without an upstream credential = %+v, %v; want no tools", listed, err)
	}
	if _, err := process.Gateway.ListTools(t.Context(), "notes", staticTokenSource("not-a-vmcp-token")); !errors.Is(err, mcpadapter.ErrGatewayList) {
		t.Fatalf("ListTools with a token the vMCP refuses = %v, want ErrGatewayList", err)
	}
}

// TestEmbeddedGatewayListsWithinGatewayCallsLimits lists within the limits
// GatewayCalls sets: the vMCP lists three tools, over a one-tool limit.
func TestEmbeddedGatewayListsWithinGatewayCallsLimits(t *testing.T) {
	process := embeddedGatewayProcess(t, mcpadapter.GatewayListLimits{MaxTools: 1})
	if listed, err := process.Gateway.ListTools(t.Context(), "other", staticTokenSource(vmcpToken(t, process))); !errors.Is(err, mcpadapter.ErrGatewayList) || listed != nil {
		t.Fatalf("ListTools over the tool limit = %+v, %v; want nil and ErrGatewayList", listed, err)
	}
}

// gatewayCatalogue is a runtime whose "first" backend is discovered in
// process and whose "second" is a gateway route listed by list.
func gatewayCatalogue(t *testing.T, list func(string, oauth2.TokenSource) ([]GatewayTool, error)) (*Attachment, *orderedCapabilityQueries, *recordingBrokerDiagnostics) {
	t.Helper()
	runtime := testAnonymousRuntime(t)
	diag := &recordingBrokerDiagnostics{}
	runtime.diag = diag
	queries := &orderedCapabilityQueries{responses: map[string]AuthenticatedCapabilities{
		"first": {Backend: "first", Tools: []ToolDefinition{{Backend: "first", Name: "mcp__first__one", Description: "one", Schema: json.RawMessage(`{"type":"object"}`)}}},
	}}
	testCatalogueProcess(runtime, queries, nil, "first", "second")
	WithGatewayCalls(testGateway{endpoint: "https://gateway.example/mcp", resource: "https://gateway.example/mcp", client: &http.Client{}, list: list},
		GatewayCalls{Authorizer: &fakeAuthorizer{}, Credentials: &fakeCredentials{}, Servers: []string{"second"}})(runtime)
	return testAttachment(t, runtime), queries, diag
}

func objectSchema() map[string]any { return map[string]any{"type": "object"} }

func TestFreezeListsGatewayRoutesThroughTheGateway(t *testing.T) {
	var mu sync.Mutex
	var listedServers, bearers []string
	attachment, queries, _ := gatewayCatalogue(t, func(server string, credential oauth2.TokenSource) ([]GatewayTool, error) {
		token, err := credential.Token()
		if err != nil {
			return nil, err
		}
		mu.Lock()
		listedServers, bearers = append(listedServers, server), append(bearers, token.AccessToken)
		mu.Unlock()
		return []GatewayTool{{Name: "two", Description: "two", InputSchema: objectSchema(), ReadOnly: true}}, nil
	})
	if _, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), staticTokenSource("enrollment-token")); err != nil {
		t.Fatalf("FreezeAuthenticatedCatalogue: %v", err)
	}
	if !reflect.DeepEqual(queries.order, []string{"first"}) {
		t.Fatalf("in-process queries = %v, want only the non-gateway backend", queries.order)
	}
	if !reflect.DeepEqual(listedServers, []string{"second"}) || !reflect.DeepEqual(bearers, []string{"enrollment-token"}) {
		t.Fatalf("gateway listings = %v with %v, want second with the enrollment credential", listedServers, bearers)
	}
	if got, want := toolNames(attachment.Tools()), []string{"mcp__anonymous__status", "mcp__first__one", "mcp__second__two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
	r, ok := attachment.lookupRoute("mcp__second__two")
	if !ok || r.backend != "second" || !r.broker || !r.readOnly || string(r.spec.Schema) != `{"type":"object"}` {
		t.Fatalf("gateway route = %+v, %v", r, ok)
	}
	if server, routed, err := attachment.runtime.gateway.Route(r.backend, r.spec.Name); server != "second" || routed != "second.two" || err != nil {
		t.Fatalf("Route = %q, %q, %v", server, routed, err)
	}
}

func TestFreezeFailsClosedOnAGatewayListing(t *testing.T) {
	for name, tc := range map[string]struct {
		listed []GatewayTool
		err    error
		want   error
		reason string
	}{
		"listing error":  {err: mcpadapter.ErrGatewayList, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
		"no tools":       {want: errGatewayListedNoTools, reason: diagnosticReasonNoTools},
		"duplicate tool": {listed: []GatewayTool{{Name: "two", InputSchema: objectSchema()}, {Name: "two", InputSchema: objectSchema()}}, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
		"private string": {listed: []GatewayTool{{Name: "two", Description: "see enrollment-token", InputSchema: objectSchema()}}, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
		"private schema": {listed: []GatewayTool{{Name: "two", InputSchema: map[string]any{"type": "object", "default": "enrollment-token"}}}, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
		"invalid name":   {listed: []GatewayTool{{Name: "two words", InputSchema: objectSchema()}}, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
		"invalid UTF-8":  {listed: []GatewayTool{{Name: "two", Description: "\xff", InputSchema: objectSchema()}}, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
		"no schema":      {listed: []GatewayTool{{Name: "two"}}, want: errGatewayListingFailed, reason: diagnosticReasonDiscoveryFailed},
	} {
		t.Run(name, func(t *testing.T) {
			attachment, _, diag := gatewayCatalogue(t, func(string, oauth2.TokenSource) ([]GatewayTool, error) { return tc.listed, tc.err })
			_, err := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), staticTokenSource("enrollment-token"))
			if !errors.Is(err, tc.want) || !errors.Is(err, ErrAuthenticatedDiscovery) {
				t.Fatalf("FreezeAuthenticatedCatalogue = %v, want %v", err, tc.want)
			}
			if got := toolNames(attachment.Tools()); !reflect.DeepEqual(got, []string{"mcp__anonymous__status"}) {
				t.Fatalf("a failed freeze published %v", got)
			}
			// The recorder joins arguments as fmt.Sprint does, without spaces.
			if !strings.Contains(diag.String(), "reason"+tc.reason+"backend_index1") {
				t.Fatalf("diagnostics %s, want reason %s for backend 1", diag.String(), tc.reason)
			}
		})
	}
}

// TestGatewayListingNeverLeaksTheBearer lists through the embedded gateway
// against a gateway that echoes the bearer in its refusals and in a tool, and
// checks no error or diagnostic carries it.
func TestGatewayListingNeverLeaksTheBearer(t *testing.T) {
	const canary = "LEAKCANARY-enrollment"
	for name, respond := range map[string]func(http.ResponseWriter, *http.Request){
		"refused": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "refused "+r.Header.Get("Authorization"), http.StatusUnauthorized)
		},
		"server error": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "failed "+r.Header.Get("Authorization"), http.StatusInternalServerError)
		},
		"tool echoes it": nil,
	} {
		t.Run(name, func(t *testing.T) {
			var mu sync.Mutex
			var authorizations []string
			echo := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "gateway", Version: "v1"}, nil)
			mcpsdk.AddTool(echo, &mcpsdk.Tool{Name: "second.two", Description: "token " + canary},
				func(context.Context, *mcpsdk.CallToolRequest, struct{}) (*mcpsdk.CallToolResult, any, error) {
					return &mcpsdk.CallToolResult{}, nil, nil
				})
			mcp := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return echo }, nil)
			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				authorizations = append(authorizations, r.Header.Get("Authorization"))
				mu.Unlock()
				if respond != nil {
					respond(w, r)
					return
				}
				mcp.ServeHTTP(w, r)
			}))
			t.Cleanup(gateway.Close)
			embedded := embeddedGateway{endpoint: gateway.URL, resource: gateway.URL, client: gateway.Client()}
			attachment, _, diag := gatewayCatalogue(t, func(server string, credential oauth2.TokenSource) ([]GatewayTool, error) {
				return embedded.ListTools(t.Context(), server, credential)
			})
			_, freezeErr := attachment.FreezeAuthenticatedCatalogue(t.Context(), testEnrollmentRef(), staticTokenSource(canary))
			_, listErr := embedded.ListTools(t.Context(), "second", staticTokenSource(canary))
			if freezeErr == nil {
				t.Fatal("the freeze succeeded")
			}
			if respond != nil && listErr == nil {
				t.Fatal("a refused listing succeeded")
			}
			for _, s := range []string{fmt.Sprintf("%v %+v", freezeErr, freezeErr), fmt.Sprintf("%v %+v", listErr, listErr), diag.String()} {
				if strings.Contains(s, canary) {
					t.Fatalf("the bearer leaked: %q", s)
				}
			}
			// Positive control: the gateway got the bearer, in the
			// Authorization header.
			mu.Lock()
			defer mu.Unlock()
			if len(authorizations) == 0 || authorizations[0] != "Bearer "+canary {
				t.Fatalf("Authorization headers = %q, want the bearer", authorizations)
			}
		})
	}
}
