package app

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"google.golang.org/grpc/connectivity"

	"github.com/stacklok/mecatl/adapters/jsonlstore"
	"github.com/stacklok/mecatl/engine/adapter/memstore"
	"github.com/stacklok/mecatl/engine/adapter/mockllm"
	"github.com/stacklok/mecatl/engine/adapter/permpolicy"
	"github.com/stacklok/mecatl/engine/agent"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/engine/tool"
	"github.com/stacklok/mecatl/internal/adapter/mcpauthority"
	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/server"
	"github.com/stacklok/mecatl/internal/buildinfo"
	"github.com/stacklok/mecatl/internal/identitybroker"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/as1server/as1servertest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/brokerconfig/brokerconfigtest"
	"github.com/stacklok/mecatl/internal/identitybroker/adapter/definitionloader"
	"github.com/stacklok/mecatl/internal/syscaller"
	"github.com/stacklok/mecatl/internal/testutil/identityharness"
)

// closableCore wraps the real broker, recording the workloads it sees and
// whether it was closed.
type closableCore struct {
	*identitybroker.Broker
	mu        sync.Mutex
	workloads []identitybroker.Workload
	closed    int
}

func (c *closableCore) FindSession(ctx context.Context, workload identitybroker.Workload, req identitybroker.FindSessionRequest) (identitybroker.Session, bool, error) {
	c.mu.Lock()
	c.workloads = append(c.workloads, workload)
	c.mu.Unlock()
	return c.Broker.FindSession(ctx, workload, req)
}

func newClosableCore(t *testing.T) *closableCore {
	t.Helper()
	broker, err := newIdentityBroker(t.Context(), Config{}, testBrokerFile(t, brokerconfigtest.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	return &closableCore{Broker: broker.core}
}

func (c *closableCore) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func TestIdentityBrokerIsOffByDefault(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker != nil || built.IdentityBrokerHostedAS1 != nil {
		t.Fatal("an identity broker or AS-1 was constructed with the flag off")
	}
}

func TestIdentityBrokerCallsCarryTheUnverifiedWorkload(t *testing.T) {
	core := newClosableCore(t)
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker == nil {
		t.Fatal("no identity broker was constructed with the flag on")
	}
	// Service and Core cannot share an implementation, so Mecatl cannot hold
	// the core; calls go through the boundary, which supplies the workload.
	if _, _, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: "hostS42"}); err != nil {
		t.Fatalf("FindSession: %v", err)
	}
	want := identitybroker.Workload{Name: identityBrokerWorkload}
	core.mu.Lock()
	defer core.mu.Unlock()
	if len(core.workloads) != 1 || core.workloads[0] != want || core.workloads[0].Verified {
		t.Fatalf("core workloads = %+v, want one unverified %+v", core.workloads, want)
	}
}

func TestIdentityBrokerIsClosedOnShutdown(t *testing.T) {
	core := newClosableCore(t)
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core})
	if err != nil {
		t.Fatal(err)
	}
	core.mu.Lock()
	closedBeforeShutdown := core.closed
	core.mu.Unlock()
	if closedBeforeShutdown != 0 {
		t.Fatal("the identity broker was closed before shutdown")
	}
	built.Close()
	built.Close()
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.closed != 1 {
		t.Fatalf("identity broker closed %d times, want once", core.closed)
	}
}

func TestIdentityBrokerIsClosedWhenLaterStartupFails(t *testing.T) {
	core := newClosableCore(t)
	// A protected MCP route without a callback URL makes the bundled MCP broker,
	// built after the identity broker, fail to construct.
	cfg := Config{UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core,
		MCPAuthority: mcpauthority.NewBroker(mcpauthority.BrokerConfig{Routes: []permconfig.MCPServerProfile{protectedToolHiveRoute("calendar")}})}
	built, err := buildIsolated(t, t.Context(), cfg)
	if err == nil {
		built.Close()
		t.Fatal("Build succeeded; the test needs a startup failure after the identity broker is built")
	}
	if !strings.Contains(err.Error(), "MCP broker") {
		t.Fatalf("Build failed before the MCP broker, so the identity broker may not have been built: %v", err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.closed != 1 {
		t.Fatalf("identity broker closed %d times after a failed startup, want once", core.closed)
	}
}

func TestIdentityBrokerDefaultWiringOpensSessions(t *testing.T) {
	// No core override: this builds the real broker and the production
	// authenticator.
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	if built.IdentityBroker == nil {
		t.Fatal("no identity broker was constructed with the flag on")
	}
	if built.IdentityBrokerHostedAS1 != nil {
		t.Fatal("AS-1 was hosted without its configuration")
	}
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	opened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()), Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	found, ok, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: "hostS42"})
	if err != nil || !ok || found != opened {
		t.Fatalf("FindSession = %+v, %v, %v; want %+v, true, nil", found, ok, err, opened)
	}
	// The real broker's session IDs start with bs_.
	if !strings.HasPrefix(opened.ID, "bs_") {
		t.Fatalf("broker session ID %q is not the real broker's", opened.ID)
	}
}

func TestIdentityBrokerRefusesSessionsWithAShell(t *testing.T) {
	for name, cfg := range map[string]Config{
		"a shell":                   {Shell: "/bin/sh"},
		"a shell with Parallel on":  {Shell: "/bin/sh", EnableParallel: true},
		"a shell with a remote env": {Shell: "/bin/sh", RemoteExecution: true},
	} {
		t.Run(name, func(t *testing.T) {
			core := newClosableCore(t)
			cfg.UseMock, cfg.NoSoul, cfg.IdentityBroker, cfg.identityBrokerCore = true, true, true, core
			built, err := buildIsolated(t, t.Context(), cfg)
			if err == nil {
				built.Close()
				t.Fatal("Build started the identity broker while sessions have a shell")
			}
			if !errors.Is(err, errIdentityBrokerWithShell) {
				t.Fatalf("Build error = %v, want %v", err, errIdentityBrokerWithShell)
			}
		})
	}
	for name, cfg := range map[string]Config{
		"no shell":         {Shell: ""},
		"the shell is off": {Shell: "/bin/sh", NoShell: true},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.UseMock, cfg.NoSoul, cfg.IdentityBroker, cfg.IdentityBrokerConfigFile = true, true, true, testBrokerConfigFile(t)
			built, err := buildIsolated(t, t.Context(), cfg)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			t.Cleanup(built.Close)
			if built.IdentityBroker == nil {
				t.Fatal("no identity broker was constructed")
			}
		})
	}
	// The Redis filesystem turns the shell off before the broker's check runs.
	t.Run("the Redis filesystem", func(t *testing.T) {
		mr := miniredis.RunT(t)
		built, err := buildIsolated(t, t.Context(), Config{
			RedisURL: mr.Addr(), RedisAllowPlaintext: true, RedisFilesystem: true,
			Shell: "/bin/sh", UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t),
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		t.Cleanup(built.Close)
		if built.IdentityBroker == nil {
			t.Fatal("no identity broker was constructed")
		}
	})
}

// TestIdentityBrokerRefusesALocalWorkspace: sessions' file tools could read
// the broker's key files from a workspace on this process's filesystem.
func TestIdentityBrokerRefusesALocalWorkspace(t *testing.T) {
	for name, cfg := range map[string]Config{
		"a workspace": {IdentityBrokerConfigFile: testBrokerConfigFile(t)},
		"a workspace beside remote execution": {IdentityBrokerConfigFile: testBrokerConfigFile(t),
			RemoteExecution: true, PlacementScope: "remote", PlacementProvider: remoteFactoryPlacement{}},
		// The check is the deployment's, whatever the core.
		"an injected core": {identityBrokerCore: newClosableCore(t)},
	} {
		t.Run(name, func(t *testing.T) {
			cfg.Workspace, cfg.UseMock, cfg.NoSoul, cfg.IdentityBroker = t.TempDir(), true, true, true
			built, err := buildIsolated(t, t.Context(), cfg)
			if err == nil {
				built.Close()
				t.Fatal("Build started the identity broker with a local workspace")
			}
			if !errors.Is(err, errIdentityBrokerWithLocalWorkspace) {
				t.Fatalf("Build error = %v, want %v", err, errIdentityBrokerWithLocalWorkspace)
			}
		})
	}
}

func TestLocalWorkspace(t *testing.T) {
	remote := remoteFactoryPlacement{}
	for name, test := range map[string]struct {
		cfg  Config
		want bool
	}{
		"no workspace":                   {cfg: Config{}, want: false},
		"a workspace":                    {cfg: Config{Workspace: "/srv/work"}, want: true},
		"remote execution's fallback":    {cfg: Config{Workspace: "/srv/work", RemoteExecution: true, PlacementProvider: remote}, want: true},
		"another placement serves it":    {cfg: Config{Workspace: "/srv/work", PlacementProvider: remote}, want: false},
		"remote execution, no workspace": {cfg: Config{RemoteExecution: true, PlacementProvider: remote}, want: false},
	} {
		if got := localWorkspace(test.cfg); got != test.want {
			t.Errorf("%s: localWorkspace = %v, want %v", name, got, test.want)
		}
	}
}

func TestIdentityBrokerConfigFileRequiresTheBroker(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBrokerConfigFile: testBrokerConfigFile(t)})
	if err == nil {
		built.Close()
		t.Fatal("Build accepted the identity broker's configuration file without the broker")
	}
	if !errors.Is(err, errIdentityBrokerConfigFileWithoutBroker) {
		t.Fatalf("Build error = %v, want %v", err, errIdentityBrokerConfigFileWithoutBroker)
	}
}

func TestIdentityBrokerDefaultWiringRegistersAgents(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	incarnation := string(session.NewIncarnationID())
	opened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incarnation, Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: opened.ID, HostSessionID: "hostS42", Owner: owner, Incarnation: incarnation,
		Definition: builtinMainRef(),
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	if reg.Credential.IsZero() {
		t.Fatal("RegisterAgent returned no credential")
	}
	// The interim credential lifetime is an hour.
	if lifetime := time.Until(reg.ExpiresAt); lifetime < 59*time.Minute || lifetime > time.Hour {
		t.Fatalf("credential lifetime = %v, want an hour", lifetime)
	}
}

// TestIdentityBrokerUsesTheOperatorKeysPerRole: the wiring signs agent
// credentials with the file's agent issuer key and client assertions with its
// broker client key, and a second build from the same file, as after a
// restart, has the same keys.
func TestIdentityBrokerUsesTheOperatorKeysPerRole(t *testing.T) {
	files := brokerconfigtest.Write(t, brokerconfigtest.Options{})
	file, err := brokerconfig.Load(files.Path, brokerconfig.Host{})
	if err != nil {
		t.Fatal(err)
	}
	broker, err := newIdentityBroker(t.Context(), Config{}, file)
	if err != nil {
		t.Fatal(err)
	}
	if broker.core == nil || broker.client == nil || broker.clientKeySetFile != files.BrokerClientKeySetFile {
		t.Fatalf("newIdentityBroker = %+v, want a core, a client assertion signer, and the file's key set", broker)
	}
	workload := identitybroker.Workload{Name: identityBrokerWorkload}
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	incarnation := string(session.NewIncarnationID())
	opened, err := broker.core.OpenSession(t.Context(), workload, identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incarnation, Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := broker.core.RegisterAgent(t.Context(), workload, identitybroker.RegisterAgentRequest{
		SessionID: opened.ID, HostSessionID: "hostS42", Owner: owner, Incarnation: incarnation,
		Definition: builtinMainRef(),
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	header, err := base64.RawURLEncoding.DecodeString(strings.Split(reg.Credential.Value(), ".")[0])
	if err != nil {
		t.Fatal(err)
	}
	var agentKey struct {
		KID string `json:"kid"`
	}
	if err := json.Unmarshal(header, &agentKey); err != nil || agentKey.KID == "" {
		t.Fatalf("credential header %s: %v", header, err)
	}
	if want := kidOf(t, files.AgentIssuerKey); agentKey.KID != want {
		t.Fatalf("credential kid %q, want the file's agent issuer key %q", agentKey.KID, want)
	}
	keys := broker.client.JWKS().Keys
	if want := kidOf(t, files.BrokerClientKey); len(keys) != 1 || keys[0].KeyID != want {
		t.Fatalf("broker client JWKS %+v; want the file's broker client key %q", keys, want)
	}
	restarted, err := newIdentityBroker(t.Context(), Config{}, file)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.client.JWKS().Keys[0] != keys[0] {
		t.Fatal("a second build from the same file has another broker client key")
	}
}

// kidOf is key's RFC 7638 thumbprint, the kid the broker signs with.
func kidOf(t *testing.T, key *ecdsa.PrivateKey) string {
	t.Helper()
	signer, err := identitybroker.NewClientAssertionSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	return signer.JWKS().Keys[0].KeyID
}

// testBrokerConfigFile writes an identity broker configuration file for t
// with a key for every role but AS-1's signing key, which only a hosted AS-1
// takes, and returns its path.
func testBrokerConfigFile(t *testing.T) string {
	t.Helper()
	return brokerconfigtest.Write(t, brokerconfigtest.Options{}).Path
}

// testHostedAS1BrokerConfigFile is testBrokerConfigFile with AS-1's signing
// key, for a hosted AS-1.
func testHostedAS1BrokerConfigFile(t *testing.T) string {
	t.Helper()
	return brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true}).Path
}

// testBrokerConfigFileFor is the configuration file cfg takes: with AS-1's
// signing key exactly when cfg hosts AS-1.
func testBrokerConfigFileFor(t *testing.T, cfg Config) string {
	t.Helper()
	return brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: cfg.IdentityBrokerHostedAS1 != nil}).Path
}

// testBrokerFile writes an identity broker configuration file for t with
// opts and loads it.
func testBrokerFile(t *testing.T, opts brokerconfigtest.Options) brokerconfig.File {
	t.Helper()
	file, err := brokerconfig.Load(brokerconfigtest.Write(t, opts).Path, brokerconfig.Host{})
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestIdentityBrokerRequiresItsConfigurationFile(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true})
	if err == nil {
		built.Close()
		t.Fatal("Build started the identity broker without its configuration file")
	}
	if !errors.Is(err, errNoIdentityBrokerConfigFile) {
		t.Fatalf("Build error = %v, want %v", err, errNoIdentityBrokerConfigFile)
	}
}

// TestIdentityBrokerRefusesAnInvalidConfigurationFile: startup with a hosted
// AS-1 fails on the file's checks, naming the file and the reason;
// brokerconfig's tests cover each check.
func TestIdentityBrokerRefusesAnInvalidConfigurationFile(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	sharedKey := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
	brokerconfigtest.WriteKey(t, sharedKey.AS1SigningKeyFile, sharedKey.BrokerClientKey)
	noKeySet := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true})
	brokerconfigtest.WriteKeySet(t, noKeySet.BrokerClientKeySetFile, brokerconfigtest.NewKey(t))
	for name, test := range map[string]struct{ path, want string }{
		"absent":                              {path: filepath.Join(t.TempDir(), "absent.json"), want: "no such file"},
		"one key for two roles":               {path: sharedKey.Path, want: "the broker client key is also the AS-1 signing key"},
		"no broker client key in its key set": {path: noKeySet.Path, want: "does not hold the broker client key's public key"},
		"no AS-1 signing key":                 {path: brokerConfigFileWithout(t, brokerconfigtest.Options{HostedAS1: true}, "as1_signing"), want: "AS-1's signing key is required with a hosted AS-1"},
		"no broker client key":                {path: brokerConfigFileWithout(t, brokerconfigtest.Options{HostedAS1: true}, "broker_client"), want: "the broker client key is required with a hosted AS-1"},
	} {
		t.Run(name, func(t *testing.T) {
			built, err := buildIsolated(t, t.Context(), Config{
				UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: test.path,
				IdentityBrokerHostedAS1: hostedAS1Config(t, h, "https://mecatl.test/as1"),
			})
			if err == nil {
				built.Close()
				t.Fatal("Build started the identity broker from an invalid configuration file")
			}
			if !strings.Contains(err.Error(), "identity broker configuration") || !strings.Contains(err.Error(), test.path) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Build error = %v, want the file's, containing %q", err, test.want)
			}
		})
	}
}

// TestIdentityBrokerRefusesAnAS1WithoutTheBrokerClientKey: an AS-1 the broker
// converts tokens at needs the broker client key, which a file loaded without
// a hosted AS-1 may leave out.
func TestIdentityBrokerRefusesAnAS1WithoutTheBrokerClientKey(t *testing.T) {
	file, err := brokerconfig.Load(brokerConfigFileWithout(t, brokerconfigtest.Options{}, "broker_client"), brokerconfig.Host{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newIdentityBroker(t.Context(), Config{IdentityBrokerAS1: unusedAS1("https://as1.example")}, file); !errors.Is(err, errNoBrokerClientKey) {
		t.Fatalf("newIdentityBroker = %v, want %v", err, errNoBrokerClientKey)
	}
}

// brokerConfigFileWithout writes a configuration file with opts' keys but
// role's and returns its path.
func brokerConfigFileWithout(t *testing.T, opts brokerconfigtest.Options, role string) string {
	t.Helper()
	path := brokerconfigtest.Write(t, opts).Path
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc["keys"].(map[string]any), role)
	if raw, err = json.Marshal(doc); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestIdentityBrokerVerifiesCredentialsOfTheRotatedAgentIssuerKey: after the
// agent issuer key rotates, with the old key as a fallback in the file, a
// startup verifies a credential the old key signed; without the fallback, it
// does not.
func TestIdentityBrokerVerifiesCredentialsOfTheRotatedAgentIssuerKey(t *testing.T) {
	oldKey := brokerconfigtest.NewKey(t)
	build := func(opts brokerconfigtest.Options) *Built {
		t.Helper()
		built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: brokerconfigtest.Write(t, opts).Path})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(built.Close)
		return built
	}
	before := build(brokerconfigtest.Options{AgentIssuerKey: oldKey})
	owner := identitybroker.Owner{Issuer: "https://login.example.com", Subject: "alice-204"}
	incarnation := string(session.NewIncarnationID())
	opened, err := before.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{HostSessionID: "hostS42", Incarnation: incarnation, Owner: owner})
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	reg, err := before.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: opened.ID, HostSessionID: "hostS42", Owner: owner, Incarnation: incarnation,
		Definition: builtinMainRef(),
	})
	if err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}
	req := identitybroker.VerifyCredentialRequest{SessionID: opened.ID, Credential: reg.Credential}
	// The rebuilt broker has none of the old one's sessions, so a credential
	// that passes the signature checks is refused only as unregistered.
	const unregistered, invalid = "not a registered agent", "the credential is not valid"
	rotated := build(brokerconfigtest.Options{AgentIssuerFallbackKeys: []*ecdsa.PrivateKey{oldKey}})
	if _, err := rotated.identityBrokerCore.VerifyCredential(t.Context(), req); err == nil || !strings.Contains(err.Error(), unregistered) {
		t.Fatalf("with the old key as a fallback, VerifyCredential = %v, want the credential verified and its agent %s", err, unregistered)
	}
	withoutFallback := build(brokerconfigtest.Options{})
	if _, err := withoutFallback.identityBrokerCore.VerifyCredential(t.Context(), req); err == nil || !strings.Contains(err.Error(), invalid) {
		t.Fatalf("without the fallback, VerifyCredential = %v, want %q", err, invalid)
	}
}

// TestHostedAS1PublishesTheOperatorKeySet: the hosted AS-1 publishes the
// file's AS-1 signing key and its fallback, the same after a restart.
func TestHostedAS1PublishesTheOperatorKeySet(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	const issuer = "https://mecatl.test/as1"
	path := brokerconfigtest.Write(t, brokerconfigtest.Options{HostedAS1: true, AS1Fallbacks: 1}).Path
	as1 := hostedAS1Config(t, h, issuer)
	published := func() string {
		built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: path, IdentityBrokerHostedAS1: as1})
		if err != nil {
			t.Fatal(err)
		}
		defer built.Close()
		resp, err := built.IdentityBrokerHostedAS1.HTTPClient.Get(issuer + "/.well-known/jwks.json")
		if err != nil {
			t.Fatalf("AS-1 key set: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var set struct {
			Keys []map[string]any `json:"keys"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&set); err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("AS-1 key set: status %d, %v", resp.StatusCode, err)
		}
		if len(set.Keys) != 2 {
			t.Fatalf("AS-1 publishes %d keys, want the signing key and its fallback", len(set.Keys))
		}
		kids := make([]string, 0, len(set.Keys))
		for _, key := range set.Keys {
			kid, _ := key["kid"].(string)
			kids = append(kids, kid)
		}
		slices.Sort(kids)
		return strings.Join(kids, ",")
	}
	if first, restarted := published(), published(); first != restarted {
		t.Fatalf("AS-1 key set %s, after a restart %s; want the same", first, restarted)
	}
}

// TestIdentityBrokerWiringConvertsAtAS1: with an AS-1 configured, the real
// broker's session open converts the access token there, authenticating with
// the broker client key.
func TestIdentityBrokerWiringConvertsAtAS1(t *testing.T) {
	a := as1servertest.Start(t, identityharness.New(t, identityharness.Options{}), nil)
	broker, err := newIdentityBroker(t.Context(), Config{IdentityBrokerAS1: &IdentityBrokerAS1{
		Issuer: a.Issuer(), TokenEndpoint: a.Server.TokenEndpoint(), HTTPClient: a.HTTPClient(),
	}}, testBrokerFile(t, brokerconfigtest.Options{BrokerClientKey: a.Key}))
	if err != nil {
		t.Fatal(err)
	}
	workload := identitybroker.Workload{Name: identityBrokerWorkload}
	alice := a.Harness.Owner(identityharness.Alice)
	req := identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()),
		Owner:       identitybroker.Owner{Issuer: alice.Issuer, Subject: alice.Subject},
		AccessToken: a.SignIn(t, as1servertest.SignIn{User: identityharness.Alice}),
	}
	opened, err := broker.core.OpenSession(t.Context(), workload, req)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	if !opened.HoldsRefreshToken || a.Faults.Conversions() != 1 {
		t.Fatalf("session %+v after %d conversions; want it holding the one grant AS-1 issued", opened, a.Faults.Conversions())
	}
	// The broker authenticated with the client key registered at AS-1.
	if reqs := a.Faults.Requests(); len(reqs) != 1 || reqs[0].ClientAssertion.IsZero() {
		t.Fatalf("AS-1 saw %d requests, want the one exchange with a client assertion", len(reqs))
	}
	// A reopen without a token, as mecak8s sends to a session holding a
	// refresh token, spends nothing.
	req.AccessToken = identitybroker.Token{}
	again, err := broker.core.OpenSession(t.Context(), workload, req)
	if err != nil || again != opened || len(a.Faults.Requests()) != 1 {
		t.Fatalf("reopen = %+v, %v after %d requests to AS-1; want %+v and no new request", again, err, len(a.Faults.Requests()), opened)
	}
}

// TestIdentityBrokerSessionsRegisterTheirAgent is the slice test: with the
// real broker and no shell, a signed-in user's session registers its main agent
// when created, and runs.
func TestIdentityBrokerSessionsRegisterTheirAgent(t *testing.T) {
	llm := mockllm.New(mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), Config{MockProvider: llm, NoSoul: true, NoShell: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(built.Close)
	ctx := session.WithPrincipal(t.Context(), &session.Principal{Issuer: "https://login.example.com", Subject: "alice-204", GrantType: session.GrantTypeUser})
	sess, err := built.Service.CreateSession(ctx, session.ModeDefault, session.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	open, found, err := built.IdentityBroker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(sess.ID)})
	if err != nil || !found {
		t.Fatalf("FindSession = %v, %v; want the session's broker session", found, err)
	}
	// The main agent is registered with the loaded built-in main: the same
	// registration again returns the agent, and a child naming it as parent
	// passes the parent check (an unregistered parent is PERMISSION_DENIED),
	// then is refused as a takeover of the main agent's incarnation.
	main, err := built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: open.ID, HostSessionID: string(sess.ID), Owner: open.Owner, Incarnation: string(sess.Incarnation()),
		Definition: builtinMainRef(),
	})
	if err != nil {
		t.Fatalf("registering the main agent again: %v", err)
	}
	_, err = built.IdentityBroker.RegisterAgent(t.Context(), identitybroker.RegisterAgentRequest{
		SessionID: open.ID, HostSessionID: string(sess.ID) + "-child", Owner: open.Owner, Incarnation: string(sess.Incarnation()),
		Definition: builtinMainRef(), Parent: main.Agent, ParentHostSessionID: string(sess.ID),
	})
	if !errors.Is(err, identitybroker.NewError(identitybroker.CodeAlreadyExists, "", "")) {
		t.Fatalf("a child taking the main agent's incarnation = %v, want ALREADY_EXISTS", err)
	}
	run, err := built.Service.StartRun(ctx, sess.ID, "hello")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	for range run.Events() {
	}
	built.Service.FinishRun(sess.ID, run)
	if llm.Calls() == 0 {
		t.Fatal("the agent did not run")
	}
}

// hostedAS1Config configures a hosted AS-1 for issuer with h's IdP as its
// upstream, registering it there.
func hostedAS1Config(t *testing.T, h *identityharness.Harness, issuer string) *IdentityBrokerAS1Config {
	t.Helper()
	dir := t.TempDir()
	secretFile := h.RegisterAS1Upstream(t, dir, "hosted-as1", issuer)
	return &IdentityBrokerAS1Config{
		Issuer: issuer, Audience: identityharness.DefaultAudience,
		PublicClients:  []IdentityBrokerAS1PublicClient{{ID: identityharness.DefaultClientID, RedirectURIs: []string{identityharness.DefaultRedirectURI}}},
		UpstreamIssuer: h.IdP.Issuer(), UpstreamClientID: "hosted-as1", UpstreamClientSecretFile: secretFile,
		UpstreamCAFile: h.WriteCACert(t, dir), UpstreamAllowPrivateAddresses: true,
	}
}

// as1StorageRunning reports whether a hosted AS-1's in-memory storage is
// still running its cleanup loop, the one goroutine Close stops.
func as1StorageRunning() bool {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return strings.Contains(string(buf[:n]), "storage.(*MemoryStorage).cleanupLoop")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// TestIdentityBrokerHostsAS1 builds a hosted AS-1 beside the broker, serving
// its routes under the issuer path and reachable in process, points session
// open at it, and releases it with the broker. A tokenless open pairs a session
// holding no refresh token; TestHostedAS1SignsTheTUIIn proves the conversion of
// a real sign-in.
func TestIdentityBrokerHostsAS1(t *testing.T) {
	h := identityharness.New(t, identityharness.Options{})
	const issuer = "https://mecatl.test/as1"
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, IdentityBrokerConfigFile: testHostedAS1BrokerConfigFile(t), IdentityBrokerHostedAS1: hostedAS1Config(t, h, issuer)})
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			built.Close()
		}
	})
	as1 := built.IdentityBrokerHostedAS1
	if as1 == nil || as1.Issuer != issuer || !slices.Contains(as1.Routes, "/as1/oauth/token") || as1.Handler == nil {
		t.Fatalf("hosted AS-1 = %+v, want one for %s serving under /as1", as1, issuer)
	}
	resp, err := as1.HTTPClient.Get(issuer + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatalf("in-process discovery: %v", err)
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	err = json.NewDecoder(resp.Body).Decode(&doc)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || doc.Issuer != issuer {
		t.Fatalf("in-process discovery: status %d, issuer %q, err %v", resp.StatusCode, doc.Issuer, err)
	}
	alice := h.Owner(identityharness.Alice)
	opened, err := built.IdentityBroker.OpenSession(t.Context(), identitybroker.OpenSessionRequest{
		HostSessionID: "hostS42", Incarnation: string(session.NewIncarnationID()),
		Owner: identitybroker.Owner{Issuer: alice.Issuer, Subject: alice.Subject},
	})
	if err != nil || opened.HoldsRefreshToken {
		t.Fatalf("tokenless OpenSession = %+v, %v; want a session holding no refresh token", opened, err)
	}
	if !as1StorageRunning() {
		t.Fatal("the hosted AS-1's storage is not running")
	}
	built.Close()
	closed = true
	if as1StorageRunning() {
		t.Fatal("the hosted AS-1 was not released on shutdown")
	}
}

func TestSameIssuer(t *testing.T) {
	const issuer = "https://broker.example/v1/mcp/broker"
	for other, want := range map[string]bool{
		issuer:                                 true,
		"HTTPS://Broker.Example/v1/mcp/broker": true,
		"https://broker.example:443/v1/mcp/broker":  true,
		"https://broker.example/v1/mcp/broker/":     true,
		"http://broker.example/v1/mcp/broker":       false,
		"https://broker.example:8443/v1/mcp/broker": false,
		"https://other.example/v1/mcp/broker":       false,
		"https://broker.example/v1/mcp/BROKER":      false,
		"https://broker.example/as1":                false,
	} {
		if got := sameIssuer(issuer, other); got != want {
			t.Errorf("sameIssuer(%q, %q) = %v, want %v", issuer, other, got, want)
		}
		if got := sameIssuer(other, issuer); got != want {
			t.Errorf("sameIssuer(%q, %q) = %v, want %v", other, issuer, got, want)
		}
	}
}

func TestIdentityBrokerAS1RequiresTheBroker(t *testing.T) {
	built, err := buildIsolated(t, t.Context(), Config{Workspace: t.TempDir(), UseMock: true, NoSoul: true, IdentityBrokerHostedAS1: &IdentityBrokerAS1Config{Issuer: "https://mecatl.test/as1"}})
	if err == nil {
		built.Close()
		t.Fatal("Build hosted AS-1 without the identity broker")
	}
	if !errors.Is(err, errIdentityBrokerAS1WithoutBroker) {
		t.Fatalf("Build error = %v, want %v", err, errIdentityBrokerAS1WithoutBroker)
	}
}

func TestIdentityBrokerIsReleasedWhenAS1ConstructionFails(t *testing.T) {
	core := newClosableCore(t)
	invalid := &IdentityBrokerAS1Config{Issuer: "https://mecatl.test/as1"}
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true, IdentityBroker: true, identityBrokerCore: core, IdentityBrokerHostedAS1: invalid})
	if err == nil {
		built.Close()
		t.Fatal("Build succeeded with an invalid AS-1 configuration")
	}
	if !errors.Is(err, as1server.ErrInvalidConfig) {
		t.Fatalf("Build error = %v, want AS-1's invalid configuration", err)
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.closed != 1 {
		t.Fatalf("identity broker closed %d times after AS-1 failed to build, want once", core.closed)
	}
}

func TestIdentityBrokerConstructionFailureStopsStartup(t *testing.T) {
	// The session store's driver connection is closed on this failure only by
	// the identity broker's own error branch: no deferred cleanup covers the
	// store at this point in Build. The profile lifecycle is closed by Build's
	// earlier deferred cleanup.
	closer := new(countingMCPProfileCloser)
	addr := startSessionStoreDriver(t, memstore.New())
	conns := newDriverConns()
	invalid := identitybroker.Config{}
	built, err := buildIsolated(t, t.Context(), Config{UseMock: true, NoSoul: true,
		SessionStoreURL: addr, driverConns: conns,
		IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t), identityBrokerConfig: &invalid, MCPProfileLifecycle: closer})
	if err == nil {
		built.Close()
		t.Fatal("Build succeeded with an invalid identity broker configuration")
	}
	if !errors.Is(err, identitybroker.ErrInvalidConfig) {
		t.Fatalf("Build error = %v, want the identity broker's invalid configuration", err)
	}
	if got := closer.calls.Load(); got != 1 {
		t.Fatalf("profile lifecycle Close calls = %d, want 1", got)
	}
	conns.mu.Lock()
	dc := conns.conns[addr]
	conns.mu.Unlock()
	if dc == nil {
		t.Fatal("Build did not dial the session store driver")
	}
	if state := dc.conn.GetState(); state != connectivity.Shutdown {
		t.Fatalf("session store driver connection = %v after the failed startup, want %v", state, connectivity.Shutdown)
	}
}

// countingIdentityBroker counts the broker sessions opened and closed through
// the boundary it wraps.
type countingIdentityBroker struct {
	identitybroker.Service
	mu            sync.Mutex
	opens, closes int
}

func (b *countingIdentityBroker) OpenSession(ctx context.Context, req identitybroker.OpenSessionRequest) (identitybroker.Session, error) {
	b.mu.Lock()
	b.opens++
	b.mu.Unlock()
	return b.Service.OpenSession(ctx, req)
}

func (b *countingIdentityBroker) CloseSession(ctx context.Context, req identitybroker.CloseSessionRequest) error {
	b.mu.Lock()
	b.closes++
	b.mu.Unlock()
	return b.Service.CloseSession(ctx, req)
}

func TestScheduledFireClosesItsBrokerSessionWhenTheRunEnds(t *testing.T) {
	store, err := jsonlstore.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ib, err := newIdentityBroker(t.Context(), Config{}, testBrokerFile(t, brokerconfigtest.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	broker := &countingIdentityBroker{Service: identitybroker.NewInProcess(identitybroker.UnverifiedAuthenticator{Name: identityBrokerWorkload}, ib.core)}
	llm := mockllm.New(mockllm.TextTurn("fire done"))
	engine := agent.NewEngine(agent.Deps{
		LLM: llm, Catalog: tool.NewCatalog(), Model: "test-model", Store: store,
		Policy: permpolicy.NewPolicy(permpolicy.AllowAllFloorRules(), nil),
	})
	svc, err := newTestServerService(server.Config{
		Engine: engine, Store: store, Now: time.Now, DefaultCapabilities: llm.Capabilities(),
		EventLog: store, Diagnostics: port.NopDiagnostics{}, IdentityBroker: broker, IdentityDefinitions: definitionRefs(ib.definitions),
	})
	if err != nil {
		t.Fatal(err)
	}
	owner := alicePrincipal
	sched := port.Schedule{Spec: port.ScheduleSpec{
		Name: "owned-fire", Prompt: "say hi", Mode: session.ModePlan, Owner: &owner,
		EnvironmentRef: session.EnvironmentRef{Kind: session.EnvKindLocal, ID: t.TempDir(), Revision: "in-tree-v1"},
		PlacementScope: "legacy-local",
	}}
	fire := makeFireFunc(svc, store.ScheduleStore(), defaultFireTimeout, nil)
	rec, err := fire(syscaller.Context(context.Background(), syscaller.RootScheduler), sched, time.Now())
	if err != nil {
		t.Fatalf("fire: %v", err)
	}
	if rec.Stop != session.StopEndTurn {
		t.Fatalf("fire stop = %q, want %q", rec.Stop, session.StopEndTurn)
	}
	broker.mu.Lock()
	opens, closes := broker.opens, broker.closes
	broker.mu.Unlock()
	if opens != 1 || closes != 1 {
		t.Fatalf("broker sessions opened %d and closed %d, want one of each", opens, closes)
	}
	if _, open, err := broker.FindSession(t.Context(), identitybroker.FindSessionRequest{RootHostSessionID: string(rec.SessionID)}); err != nil || open {
		t.Fatalf("after the fire, FindSession = %v, %v; want none open", open, err)
	}
}

// builtinMainRef is the reference of the built-in main definition the broker
// loads in this build.
func builtinMainRef() identitybroker.DefinitionRef {
	return identitybroker.DefinitionRef{Source: identitybroker.SourceSystem, Name: identitybroker.BuiltinMain, Revision: definitionloader.BuiltinRevision(buildinfo.BuildID)}
}

func TestIdentityBrokerLoadsTheOperatorsDefinitions(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	write("reviewer.md", "---\nname: reviewer\ndescription: Reviews code.\n---\nReview the change.\n")
	const secret = "sk-test-do-not-log"
	bad := write("broken.md", "---\nname: broken\n---\n"+secret+"\n") // no description
	diag := &captureDiag{}
	ib, err := newIdentityBroker(t.Context(), Config{AgentsDirs: []string{dir}, Diagnostics: diag}, testBrokerFile(t, brokerconfigtest.Options{}))
	if err != nil {
		t.Fatalf("newIdentityBroker: %v", err)
	}
	var refs []identitybroker.DefinitionRef
	for _, rec := range ib.definitions {
		refs = append(refs, rec.Ref)
		if _, ok := ib.core.Definition(rec.Ref); !ok {
			t.Errorf("the broker did not load %+v", rec.Ref)
		}
	}
	want := map[string]bool{"system/main": false, "system/explorer": false, "system/branch": false, "system/member": false, "global/reviewer": false}
	for _, ref := range refs {
		if _, ok := want[ref.Source+"/"+ref.Name]; ok {
			want[ref.Source+"/"+ref.Name] = true
		}
	}
	for name, loaded := range want {
		if !loaded || len(refs) != len(want) {
			t.Fatalf("loaded %+v, want the four built-ins and global/reviewer (missing %s)", refs, name)
		}
	}
	if n := diag.warnCount("identity broker definition problem"); n != 1 {
		t.Fatalf("logged %d definition problems, want 1 for %s", n, bad)
	}
	for _, e := range diag.entries {
		line := fmt.Sprint(e.msg, e.args)
		if strings.Contains(line, secret) {
			t.Fatalf("a definition problem logged the file's contents: %s", line)
		}
		if e.msg == "identity broker definition problem" && !strings.Contains(line, bad) {
			t.Fatalf("the definition problem %s does not name %s", line, bad)
		}
	}
}

func TestIdentityBrokerMissingAgentsDirFailsStartup(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := newIdentityBroker(t.Context(), Config{AgentsDirs: []string{missing}}, testBrokerFile(t, brokerconfigtest.Options{})); err == nil || !strings.Contains(err.Error(), "load identity broker definitions") {
		t.Fatalf("newIdentityBroker with a missing agents directory = %v, want a load failure", err)
	}
	llm := mockllm.New(mockllm.TextTurn("done"))
	built, err := buildIsolated(t, t.Context(), Config{MockProvider: llm, NoSoul: true, NoShell: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t), AgentsDirs: []string{missing}})
	if err == nil {
		built.Close()
		t.Fatal("startup with the identity broker and a missing agents directory succeeded")
	}
	// Positive control: the same startup with the directory present succeeds.
	built, err = buildIsolated(t, t.Context(), Config{MockProvider: llm, NoSoul: true, NoShell: true, IdentityBroker: true, IdentityBrokerConfigFile: testBrokerConfigFile(t), AgentsDirs: []string{t.TempDir()}})
	if err != nil {
		t.Fatalf("startup with the identity broker and an empty agents directory: %v", err)
	}
	built.Close()
}

func TestIdentityDefinitionRefsUnderAHarnessPolicy(t *testing.T) {
	records := []identitybroker.DefinitionRecord{
		{Ref: builtinMainRef()},
		{Ref: identitybroker.DefinitionRef{Source: identitybroker.SourceGlobal, Name: "reviewer", Revision: "r1"}},
	}
	// Without a harness policy the operator's agent directories win every
	// name, so the global references are passed.
	if got := identityDefinitionRefs(Config{}, records); len(got) != 2 {
		t.Fatalf("references without a harness policy = %+v, want both", got)
	}
	// A harness policy can let another source win a name the broker loaded,
	// so only the built-in references are passed.
	got := identityDefinitionRefs(Config{harnessResolver: &harnessCommandResolver{}}, records)
	if len(got) != 1 || got[0] != builtinMainRef() {
		t.Fatalf("references under a harness policy = %+v, want only the built-in main", got)
	}
}

// TestIdentityBrokerWithAnInjectedCoreSendsTheConfiguredDefinitions pins that
// the server gets the references of the definitions this configuration loads
// even when a test injects the broker core, so it never starts without them
// (the server refuses an identity broker with none).
func TestIdentityBrokerWithAnInjectedCoreSendsTheConfiguredDefinitions(t *testing.T) {
	parts, err := buildIdentityBroker(t.Context(), Config{IdentityBroker: true, NoShell: true, identityBrokerCore: newClosableCore(t)}, nil)
	if err != nil {
		t.Fatalf("buildIdentityBroker: %v", err)
	}
	t.Cleanup(parts.release)
	refs := definitionRefs(parts.definitions)
	if !slices.Contains(refs, builtinMainRef()) {
		t.Fatalf("an injected core's definitions = %+v, want the built-in main among them", refs)
	}
}
