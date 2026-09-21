package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	mecatlv1 "github.com/stacklok/mecatl/contracts/gen/go/mecatl/v1"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/server"
)

// compatibilityInfoService builds a Service with the given deployment label and
// default capabilities. It is deliberately separate from newService: these
// tests are about the descriptor, so they need to vary fields newService fixes.
func compatibilityInfoService(t *testing.T, deployment string, caps port.ProviderCapabilities) *server.Service {
	t.Helper()
	engine := agent.NewEngine(agent.Deps{
		LLM:     mockllm.New(mockllm.TextTurn("ok")),
		Catalog: tool.NewCatalog(),
		Policy:  permpolicy.NewPolicy(nil, nil),
		Model:   "test-model",
	})
	svc, err := newPlacementTestService(server.Config{
		Engine: engine,
		Store:  memstore.New(),

		Now:                 func() time.Time { return time.Unix(0, 0) },
		DefaultCapabilities: caps,
		DeploymentID:        deployment,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	return svc
}

// TestSDKServerEnablers_Scenario1_CompatibilityInfoMatchesCapabilities is AC1.1.
//
// GetCompatibilityInfo reports api_major 1 and the SAME ServerCapabilities projection a
// CreateSession echo carries for the same build — and does so WITHOUT creating a
// session. The no-session half is the point of the RPC: before it existed,
// discovering a server's capabilities cost a probe session that then had to be
// cleaned up.
func TestSDKServerEnablers_Scenario1_CompatibilityInfoMatchesCapabilities(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo: %v", err)
	}
	if got := info.GetApiMajor(); got != 1 {
		t.Fatalf("api_major = %d, want 1 (it bumps only on a genuine break; additions ride features)", got)
	}
	if info.GetCapabilities() == nil {
		t.Fatal("capabilities is nil: a client cannot distinguish 'nothing enabled' from 'server did not answer'")
	}

	// No session may have been created to answer the question.
	ls, err := client.ListSessions(ctx, &mecatlv1.ListSessionsRequest{})
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	if n := len(ls.GetSessions()); n != 0 {
		t.Fatalf("GetCompatibilityInfo created %d session(s); it must answer without a probe session", n)
	}

	// Session creation no longer echoes deployment-wide capabilities.
	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if cs.GetSessionId() == "" {
		t.Fatal("CreateSession returned no session id")
	}
}

// TestSDKServerEnablers_Scenario1_CompatibilityInfoTransportParity is AC1.2.
//
// The HTTP peer returns the identical descriptor as the gRPC handler. The SDK's
// premise is that Node (gRPC) and browser (HTTP) clients expose the same
// normalized surface; a divergence here makes that false at the first call.
func TestSDKServerEnablers_Scenario1_CompatibilityInfoTransportParity(t *testing.T) {
	svc := compatibilityInfoService(t, "edge-1", port.ProviderCapabilities{Image: true})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()
	srv := httptest.NewServer(server.NewHTTPHandler(svc))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	viaGRPC, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo (grpc): %v", err)
	}

	resp, err := http.Get(srv.URL + "/v1/compatibility") //nolint:noctx // httptest, bounded by the test deadline.
	if err != nil {
		t.Fatalf("GET /v1/compatibility: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/compatibility status = %d, want 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var viaHTTP mecatlv1.GetCompatibilityInfoResponse
	// The HTTP surface writes with encoding/json over the proto struct, so decode
	// the same way rather than through protojson: the assertion is about the
	// VALUES both transports report, not about a wire-format choice.
	if err := json.Unmarshal(raw, &viaHTTP); err != nil {
		// Fall back to protojson so the test still reports a value mismatch
		// (the useful failure) rather than a decode error, if the HTTP encoder
		// ever moves to protojson.
		if perr := protojson.Unmarshal(raw, &viaHTTP); perr != nil {
			t.Fatalf("decode HTTP body (json: %v, protojson: %v): %s", err, perr, raw)
		}
	}

	if viaHTTP.GetApiMajor() != viaGRPC.GetApiMajor() {
		t.Errorf("api_major differs: http=%d grpc=%d", viaHTTP.GetApiMajor(), viaGRPC.GetApiMajor())
	}
	if viaHTTP.GetDeployment() != viaGRPC.GetDeployment() {
		t.Errorf("deployment differs: http=%q grpc=%q", viaHTTP.GetDeployment(), viaGRPC.GetDeployment())
	}
	if !slices.Equal(viaHTTP.GetFeatures(), viaGRPC.GetFeatures()) {
		t.Errorf("features differ: http=%v grpc=%v", viaHTTP.GetFeatures(), viaGRPC.GetFeatures())
	}
	if viaHTTP.GetCapabilities().GetImage() != viaGRPC.GetCapabilities().GetImage() {
		t.Errorf("capabilities.image differs: http=%v grpc=%v",
			viaHTTP.GetCapabilities().GetImage(), viaGRPC.GetCapabilities().GetImage())
	}
}

// TestADR_0248_FeatureRegistryIsSingleSource is AC1.4.
//
// The feature set is non-empty, sorted, duplicate-free, and identical on both
// transports — it is one registry, not two lists that must be kept in step.
func TestADR_0248_FeatureRegistryIsSingleSource(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo: %v", err)
	}
	feats := info.GetFeatures()
	if len(feats) == 0 {
		t.Fatal("features is empty: the registry always advertises at least server_info, so empty means the projection broke")
	}
	if !slices.Contains(feats, "server_info") {
		t.Errorf("features %v is missing %q", feats, "server_info")
	}
	if !slices.IsSorted(feats) {
		t.Errorf("features %v is not sorted; keep the registry sorted so diffs stay readable", feats)
	}
	seen := make(map[string]bool, len(feats))
	for _, f := range feats {
		if f == "" {
			t.Error("features contains an empty identifier")
		}
		if seen[f] {
			t.Errorf("features contains duplicate %q", f)
		}
		seen[f] = true
	}

	// A caller must not be able to mutate the process-wide registry through a
	// returned slice: the response reaches the gRPC layer, which may retain it.
	feats[0] = "tampered"
	again, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo (second): %v", err)
	}
	if slices.Contains(again.GetFeatures(), "tampered") {
		t.Fatal("mutating a returned features slice corrupted the registry; serverFeatures must return a copy")
	}
}

// TestADR_0248_CapabilitiesAreNotFeatures is AC1.5.
//
// The two vocabularies are independent. An operator toggle must never move the
// feature set: a client that reads a disabled capability as version skew would
// reject a correctly-configured deployment, and one that infers protocol
// support from an operator toggle would call an RPC the server never had.
func TestADR_0248_CapabilitiesAreNotFeatures(t *testing.T) {
	// Two services differing ONLY in an operator-controlled capability input.
	plain := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	withImage := compatibilityInfoService(t, "", port.ProviderCapabilities{Image: true})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	infoA := plain.CompatibilityInfo(ctx)
	infoB := withImage.CompatibilityInfo(ctx)

	if infoA.GetCapabilities().GetImage() == infoB.GetCapabilities().GetImage() {
		t.Fatal("test is vacuous: the two services must differ in an operator-enabled capability")
	}
	if !slices.Equal(infoA.GetFeatures(), infoB.GetFeatures()) {
		t.Errorf("an operator capability toggle changed the feature set: %v vs %v\n"+
			"capabilities answer 'what did the operator enable'; features answer 'what does the build implement'",
			infoA.GetFeatures(), infoB.GetFeatures())
	}
	if infoA.GetApiMajor() != infoB.GetApiMajor() {
		t.Errorf("an operator capability toggle changed api_major: %d vs %d", infoA.GetApiMajor(), infoB.GetApiMajor())
	}
}

// TestADR_0248_DeploymentIdentityIsOperatorSetOnly is AC1.6.
//
// The label is empty unless the operator set it, and is passed through verbatim
// when they did. The empty-by-default half is the security-relevant one: mecatl
// must never infer a label from hostname, pod name, or environment, because
// that leaks infrastructure topology to every authenticated caller.
func TestADR_0248_DeploymentIdentityIsOperatorSetOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	unset := compatibilityInfoService(t, "", port.ProviderCapabilities{}).CompatibilityInfo(ctx)
	if got := unset.GetDeployment(); got != "" {
		t.Fatalf("deployment = %q with no --deployment-id; it must be empty and never inferred from the host", got)
	}

	const label = "eu-west-1 staging"
	set := compatibilityInfoService(t, label, port.ProviderCapabilities{}).CompatibilityInfo(ctx)
	if got := set.GetDeployment(); got != label {
		t.Errorf("deployment = %q, want the operator's value %q verbatim", got, label)
	}
}

// TestInvariant_capability_truth_single_intersection is AC1.7.
//
// It pins the AGENTS.md invariant that capability truth is ONE
// composition-computed intersection, applied to the new descriptor: the
// server-wide image/audio bits on GetCompatibilityInfo come from the SAME
// DefaultCapabilities the CreateSession echo uses, and GetCompatibilityInfo introduces
// no second, recomputed source that could disagree with it.
//
// The server-wide value is a UI-chrome hint. The per-session
// CreateSessionResponse echo remains authoritative for whether a given session
// may send media, which is what an SDK must validate against.
func TestInvariant_capability_truth_single_intersection(t *testing.T) {
	caps := port.ProviderCapabilities{Image: true, Audio: false}
	svc := compatibilityInfoService(t, "", caps)
	client, cleanup := dialGRPC(t, svc)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo: %v", err)
	}
	if got := info.GetCapabilities().GetImage(); got != caps.Image {
		t.Errorf("server-wide image = %v, want the composition-computed %v", got, caps.Image)
	}
	if got := info.GetCapabilities().GetAudio(); got != caps.Audio {
		t.Errorf("server-wide audio = %v, want the composition-computed %v", got, caps.Audio)
	}

	// The session echo is the authority and must agree with the one source.
	cs, err := client.CreateSession(ctx, &mecatlv1.CreateSessionRequest{})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if cs.GetSessionCapabilities().GetImage() != info.GetCapabilities().GetImage() {
		t.Errorf("session image capability %v disagrees with the server-wide hint %v; both must read the ONE intersection",
			cs.GetSessionCapabilities().GetImage(), info.GetCapabilities().GetImage())
	}
}

// TestSDKServerEnablers_Scenario1_CompatibilityInfoRequiresAuth is AC1.3.
//
// GetCompatibilityInfo is authenticated like every other RPC. The distinction that
// matters to the SDK is UNAUTHENTICATED vs UNIMPLEMENTED: the SDK treats
// UNIMPLEMENTED as "below the compatibility floor" and fails loudly without
// probing further, so a bad or missing token must NOT be able to present as a
// too-old server — that would send an operator hunting a version problem when
// they have a credentials problem.
func TestSDKServerEnablers_Scenario1_CompatibilityInfoRequiresAuth(t *testing.T) {
	svc := compatibilityInfoService(t, "", port.ProviderCapabilities{})
	auth := server.NewAuthenticator(server.SecurityConfig{AuthToken: "secret"})
	client, cleanup := dialGRPCSecure(t, svc, auth)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// No credentials at all.
	if _, err := client.GetCompatibilityInfo(ctx, &mecatlv1.GetCompatibilityInfoRequest{}); err == nil {
		t.Fatal("GetCompatibilityInfo succeeded with no credentials; it must be authenticated like every other RPC")
	} else if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("no-credentials code = %v, want %v (never %v, which the SDK reads as 'server too old')",
			got, codes.Unauthenticated, codes.Unimplemented)
	}

	// Wrong credentials.
	bad := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer wrong")
	if _, err := client.GetCompatibilityInfo(bad, &mecatlv1.GetCompatibilityInfoRequest{}); err == nil {
		t.Fatal("GetCompatibilityInfo succeeded with a wrong token")
	} else if got := status.Code(err); got != codes.Unauthenticated {
		t.Fatalf("wrong-token code = %v, want %v", got, codes.Unauthenticated)
	}

	// Correct credentials.
	good := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer secret")
	info, err := client.GetCompatibilityInfo(good, &mecatlv1.GetCompatibilityInfoRequest{})
	if err != nil {
		t.Fatalf("GetCompatibilityInfo with a valid token: %v", err)
	}
	if info.GetApiMajor() != 1 {
		t.Errorf("api_major = %d, want 1", info.GetApiMajor())
	}
}
