package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/internal/adapter/mcpbroker"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokergrpc"
	"github.com/stacklok/mecatl/internal/adapter/mcpbrokerserver"
	"github.com/stacklok/mecatl/internal/adapter/redisstore"
	"github.com/stacklok/mecatl/internal/cliconfig"
)

const (
	brokerAPIVersion = "mecabroker.mecatl.dev/v1"
	maxConfigBytes   = 1 << 20
	maxJWKSStaleness = 24 * time.Hour
)

type duration time.Duration

func (d *duration) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return errors.New("duration must be a string")
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return errors.New("duration is invalid")
	}
	*d = duration(parsed)
	return nil
}
func (d duration) value() time.Duration { return time.Duration(d) }

type fileConfig struct {
	APIVersion string `json:"api_version"`
	Listener   struct {
		PublicAddress string `json:"public_address"`
		TLSCertFile   string `json:"tls_cert_file"`
		TLSKeyFile    string `json:"tls_key_file"`
	} `json:"listener"`
	WorkloadJWT struct {
		Issuer              string                   `json:"issuer"`
		JWKSURI             string                   `json:"jwks_uri"`
		Audience            string                   `json:"audience"`
		Subject             string                   `json:"subject"`
		TrustBundleFile     string                   `json:"trust_bundle_file"`
		MaxJWKSStaleness    duration                 `json:"max_jwks_staleness"`
		KubernetesBootstrap *fileKubernetesBootstrap `json:"kubernetes_bootstrap,omitempty"`
	} `json:"workload_jwt"`
	CallbackURL      string                `json:"callback_url"`
	Profiles         []fileProfile         `json:"profiles"`
	ProtectedStorage *fileProtectedStorage `json:"protected_storage,omitempty"`
	Drain            struct {
		PropagationDelay        duration `json:"propagation_delay"`
		Timeout                 duration `json:"timeout"`
		ListenerShutdownTimeout duration `json:"listener_shutdown_timeout"`
	} `json:"drain"`
	Transport struct {
		RPCDeadline        duration `json:"rpc_deadline"`
		ExecuteDeadline    duration `json:"execute_deadline"`
		HandleIdleTimeout  duration `json:"handle_idle_timeout"`
		SweepInterval      duration `json:"sweep_interval"`
		CleanupTimeout     duration `json:"cleanup_timeout"`
		MaxHandles         int      `json:"max_handles"`
		MaxOwners          int      `json:"max_owners"`
		MaxReceipts        int      `json:"max_receipts"`
		MaxReceiptBytes    int      `json:"max_receipt_bytes"`
		MaxPendingControls int      `json:"max_pending_controls"`
		MaxActiveExecutes  int      `json:"max_active_executes"`
	} `json:"transport"`
	Runtime struct {
		MaxLogicalSessions   int      `json:"max_logical_sessions"`
		LogicalRetention     duration `json:"logical_retention"`
		MaxPendingAuthStates int      `json:"max_pending_auth_states"`
	} `json:"runtime"`
}
type fileProtectedStorage struct {
	Redis      fileProtectedRedis      `json:"redis"`
	Encryption fileProtectedEncryption `json:"encryption"`
}
type fileProtectedRedis struct {
	Address          string    `json:"address"`
	UsernameFile     string    `json:"username_file,omitempty"`
	PasswordFile     string    `json:"password_file"`
	CAFile           string    `json:"ca_file,omitempty"`
	DialTimeout      *duration `json:"dial_timeout,omitempty"`
	OperationTimeout *duration `json:"operation_timeout,omitempty"`
	HealthTimeout    *duration `json:"health_timeout,omitempty"`
}
type fileProtectedEncryption struct {
	ActiveID string             `json:"active_id"`
	Keys     []fileProtectedKey `json:"keys"`
}
type fileProtectedKey struct {
	ID   string `json:"id"`
	File string `json:"file"`
}

func protectedTimeouts(r fileProtectedRedis) (dial, operation, health time.Duration) {
	dial, operation, health = 5*time.Second, 5*time.Second, 2*time.Second
	if r.DialTimeout != nil {
		dial = r.DialTimeout.value()
	}
	if r.OperationTimeout != nil {
		operation = r.OperationTimeout.value()
	}
	if r.HealthTimeout != nil {
		health = r.HealthTimeout.value()
	}
	return dial, operation, health
}

type fileKubernetesBootstrap struct {
	DiscoveryURL string `json:"discovery_url"`
	JWKSURI      string `json:"jwks_uri"`
	TokenFile    string `json:"token_file"`
}

type fileProfile struct {
	Name   string       `json:"name"`
	URL    string       `json:"url"`
	Auth   string       `json:"auth"`
	OAuth  *fileOAuth   `json:"oauth,omitempty"`
	Static []fileStatic `json:"tools,omitempty"`
}
type fileOAuth struct {
	Issuer                string            `json:"issuer,omitempty"`
	AuthorizationEndpoint string            `json:"authorization_endpoint,omitempty"`
	TokenEndpoint         string            `json:"token_endpoint,omitempty"`
	ClientMode            string            `json:"client_mode,omitempty"`
	CIMDDocumentURL       string            `json:"cimd_document_url,omitempty"`
	DCRDiscoveryURL       string            `json:"dcr_discovery_url,omitempty"`
	ClientID              string            `json:"client_id,omitempty"`
	ClientSecretFile      string            `json:"client_secret_file,omitempty"`
	Scopes                []string          `json:"scopes"`
	RequestRefreshToken   bool              `json:"request_refresh_token,omitempty"`
	Network               *fileOAuthNetwork `json:"network,omitempty"`
}
type fileOAuthNetwork struct {
	AdditionalOrigins []string `json:"additional_origins"`
	PrivateOrigins    []string `json:"private_origins"`
	MaxRedirects      int      `json:"max_redirects"`
}
type fileStatic struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
	ReadOnly    bool            `json:"read_only,omitempty"`
}

func parseFlagsWithLogging() (fileConfig, slog.Level, string, error) {
	flags := flag.NewFlagSet("mecabroker", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	logFlags := cliconfig.RegisterLogLevelFlag(flags)
	cfg, err := parseConfigFlag(flags)
	level, warning := logFlags.Resolve()
	if err != nil {
		return fileConfig{}, level, warning, err
	}
	return cfg, level, warning, nil
}

func parseConfigFlag(flags *flag.FlagSet) (fileConfig, error) {
	var path string
	flags.StringVar(&path, "config", "", "strict JSON broker configuration (required)")
	if err := flags.Parse(os.Args[1:]); err != nil {
		return fileConfig{}, err
	}
	if path == "" {
		return fileConfig{}, errors.New("--config is required")
	}
	if flags.NArg() != 0 {
		return fileConfig{}, errors.New("serving accepts only --config")
	}
	return readConfig(path)
}

func readConfig(path string) (fileConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileConfig{}, errors.New("open broker configuration")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(data) > maxConfigBytes {
		return fileConfig{}, errors.New("broker configuration exceeds size limit")
	}
	if err := rejectDuplicateJSON(data); err != nil {
		return fileConfig{}, errors.New("decode broker configuration")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var cfg fileConfig
	if err := decoder.Decode(&cfg); err != nil {
		return fileConfig{}, errors.New("decode broker configuration")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fileConfig{}, errors.New("broker configuration has trailing data")
	}
	if err := cfg.validate(); err != nil {
		return fileConfig{}, err
	}
	return cfg, nil
}

const maxClientSecretBytes = 64 << 10

// validateConfiguredSecretFile bounds secret handling at configuration admission.
// ToolHive reads the path only while constructing its upstream client.
func validateConfiguredSecretFile(path string) error {
	if path == "" {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	secret, err := io.ReadAll(io.LimitReader(file, maxClientSecretBytes+1))
	if err != nil || len(secret) > maxClientSecretBytes || len(bytes.TrimSpace(secret)) == 0 {
		return errors.New("invalid")
	}
	return nil
}

//nolint:gocyclo // configuration admission keeps cross-field policy together.
func (cfg fileConfig) validate() error {
	if cfg.APIVersion != brokerAPIVersion {
		return errors.New("broker configuration API version is required and unsupported versions are rejected")
	}
	if cfg.Listener.PublicAddress == "" || cfg.Listener.TLSCertFile == "" || cfg.Listener.TLSKeyFile == "" {
		return errors.New("complete public listener configuration is required")
	}
	if _, _, err := net.SplitHostPort(cfg.Listener.PublicAddress); err != nil {
		return errors.New("public listener address is invalid")
	}
	bootstrap := cfg.WorkloadJWT.KubernetesBootstrap
	if cfg.WorkloadJWT.Audience == "" || cfg.WorkloadJWT.Subject == "" || cfg.WorkloadJWT.TrustBundleFile == "" {
		return errors.New("complete workload-JWT configuration is required")
	}
	if bootstrap == nil && (cfg.WorkloadJWT.Issuer == "" || cfg.WorkloadJWT.JWKSURI == "") {
		return errors.New("complete workload-JWT configuration is required")
	}
	if bootstrap != nil && (cfg.WorkloadJWT.Issuer != "" || cfg.WorkloadJWT.JWKSURI != "" || bootstrap.DiscoveryURL == "" || bootstrap.JWKSURI == "" || bootstrap.TokenFile == "") {
		return errors.New("kubernetes workload-JWT bootstrap configuration is incomplete or conflicts with explicit issuer/JWKS")
	}
	if bootstrap == nil {
		if err := mcpbroker.ValidateProtectedURL(cfg.WorkloadJWT.Issuer, "workload-JWT issuer"); err != nil {
			return err
		}
		if err := mcpbroker.ValidateProtectedURL(cfg.WorkloadJWT.JWKSURI, "workload-JWT JWKS URI"); err != nil {
			return err
		}
	} else {
		if err := mcpbroker.ValidateProtectedURL(bootstrap.DiscoveryURL, "Kubernetes discovery URL"); err != nil {
			return err
		}
		if err := mcpbroker.ValidateProtectedURL(bootstrap.JWKSURI, "Kubernetes JWKS URI"); err != nil {
			return err
		}
	}
	if cfg.WorkloadJWT.MaxJWKSStaleness.value() <= 0 || cfg.WorkloadJWT.MaxJWKSStaleness.value() > maxJWKSStaleness {
		return errors.New("workload-JWT JWKS staleness is invalid")
	}
	if cfg.CallbackURL != "" {
		if err := mcpbroker.ValidateProtectedURL(cfg.CallbackURL, "broker callback"); err != nil {
			return err
		}
	}
	for _, d := range []duration{cfg.Drain.PropagationDelay, cfg.Drain.Timeout, cfg.Drain.ListenerShutdownTimeout, cfg.Transport.RPCDeadline, cfg.Transport.ExecuteDeadline, cfg.Transport.HandleIdleTimeout, cfg.Transport.SweepInterval, cfg.Transport.CleanupTimeout, cfg.Runtime.LogicalRetention} {
		if d.value() <= 0 {
			return errors.New("broker duration bounds must be positive")
		}
	}
	for _, n := range []int{cfg.Transport.MaxHandles, cfg.Transport.MaxOwners, cfg.Transport.MaxReceipts, cfg.Transport.MaxReceiptBytes, cfg.Transport.MaxPendingControls, cfg.Transport.MaxActiveExecutes, cfg.Runtime.MaxLogicalSessions, cfg.Runtime.MaxPendingAuthStates} {
		if n <= 0 {
			return errors.New("broker capacity bounds must be positive")
		}
	}
	seenProfiles := make(map[string]struct{}, len(cfg.Profiles))
	hasOAuthProfile := false
	for _, profile := range cfg.Profiles {
		profileKey := strings.ToLower(profile.Name)
		if _, exists := seenProfiles[profileKey]; exists {
			return fmt.Errorf("duplicate broker profile name %q", profile.Name)
		}
		seenProfiles[profileKey] = struct{}{}
		if profile.Name == "" || profile.URL == "" {
			return errors.New("profile name and URL are required")
		}
		switch profile.Auth {
		case "none":
			if profile.OAuth != nil || len(profile.Static) != 0 {
				return errors.New("anonymous profile contains protected configuration")
			}
		case "oauth":
			hasOAuthProfile = true
			if profile.OAuth == nil {
				return errors.New("protected profile OAuth configuration is required")
			}
			if profile.OAuth.Network != nil && (len(profile.OAuth.Network.AdditionalOrigins) != 0 || len(profile.OAuth.Network.PrivateOrigins) != 0 || profile.OAuth.Network.MaxRedirects != 0) {
				return errors.New("OAuth network settings are unsupported by mecabroker; use mecak8s or remove oauth.network before starting the broker")
			}
			if profile.OAuth.ClientMode != "preregistered" && profile.OAuth.ClientMode != "cimd" && profile.OAuth.ClientMode != "dcr" {
				return errors.New("protected profile OAuth client_mode must be preregistered, cimd, or dcr")
			}
			mode := profile.OAuth.ClientMode
			switch mode {
			case "preregistered":
				if profile.OAuth.ClientID == "" || profile.OAuth.ClientSecretFile == "" {
					return errors.New("preregistered OAuth client_mode requires client_id and client_secret_file")
				}
				if profile.OAuth.CIMDDocumentURL != "" || profile.OAuth.DCRDiscoveryURL != "" {
					return errors.New("preregistered OAuth client_mode cannot include CIMD or DCR configuration")
				}
			case "cimd":
				if profile.OAuth.CIMDDocumentURL == "" {
					return errors.New("cimd OAuth client_mode requires cimd_document_url")
				}
				if profile.OAuth.ClientID != "" || profile.OAuth.ClientSecretFile != "" || profile.OAuth.DCRDiscoveryURL != "" {
					return errors.New("cimd OAuth client_mode cannot include client credentials or DCR configuration")
				}
			case "dcr":
				if profile.OAuth.DCRDiscoveryURL == "" {
					return errors.New("dcr OAuth client_mode requires dcr_discovery_url")
				}
				if profile.OAuth.AuthorizationEndpoint == "" || profile.OAuth.TokenEndpoint == "" {
					return errors.New("dcr OAuth client_mode requires explicit authorization_endpoint and token_endpoint")
				}
				if profile.OAuth.ClientID != "" || profile.OAuth.ClientSecretFile != "" || profile.OAuth.CIMDDocumentURL != "" {
					return errors.New("dcr OAuth client_mode cannot include client credentials or CIMD configuration")
				}
			default:
				return errors.New("protected profile OAuth client_mode must be preregistered, cimd, or dcr")
			}
			if profile.OAuth.CIMDDocumentURL != "" {
				if err := mcpbroker.ValidateProtectedURL(profile.OAuth.CIMDDocumentURL, "CIMD document URL"); err != nil {
					return err
				}
			}
			if profile.OAuth.DCRDiscoveryURL != "" {
				if err := mcpbroker.ValidateProtectedURL(profile.OAuth.DCRDiscoveryURL, "DCR discovery URL"); err != nil {
					return err
				}
			}
			if mode == "preregistered" {
				if err := validateConfiguredSecretFile(profile.OAuth.ClientSecretFile); err != nil {
					return errors.New("protected profile OAuth client secret file is invalid")
				}
			}
			if err := mcpbroker.ValidateProtectedURL(profile.URL, "protected upstream URL"); err != nil {
				return err
			}
			for label, endpoint := range map[string]string{"issuer": profile.OAuth.Issuer, "authorization endpoint": profile.OAuth.AuthorizationEndpoint, "token endpoint": profile.OAuth.TokenEndpoint} {
				if endpoint != "" {
					if err := mcpbroker.ValidateProtectedURL(endpoint, label); err != nil {
						return err
					}
				}
			}
			if (profile.OAuth.AuthorizationEndpoint == "") != (profile.OAuth.TokenEndpoint == "") {
				return errors.New("protected profile has partial OAuth endpoints")
			}
			if profile.OAuth.Issuer == "" && profile.OAuth.AuthorizationEndpoint == "" {
				return errors.New("protected profile OAuth issuer or endpoints are required")
			}
			for _, static := range profile.Static {
				if static.Name == "" || len(static.Schema) == 0 || !json.Valid(static.Schema) || static.Schema[0] != '{' {
					return errors.New("protected profile static tool is invalid")
				}
			}
		default:
			return errors.New("profile auth mode is invalid")
		}
	}
	if hasOAuthProfile && cfg.CallbackURL == "" {
		return errors.New("broker callback is required when OAuth profiles are configured")
	}
	// OAuth credentials live only in encrypted durable custody; there is no
	// in-memory fallback for a protected profile.
	if hasOAuthProfile && cfg.ProtectedStorage == nil {
		return errors.New("protected storage is required for OAuth profiles")
	}
	if cfg.ProtectedStorage != nil {
		if !hasOAuthProfile {
			return errors.New("protected storage requires an OAuth profile")
		}
		if err := cfg.validateProtectedStorage(); err != nil {
			return err
		}
	}
	return nil
}

var protectedKeyID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

//nolint:gocyclo // strict cross-field configuration policy is intentionally centralized.
func (cfg fileConfig) validateProtectedStorage() error {
	r := cfg.ProtectedStorage.Redis
	host, portNumber, err := net.SplitHostPort(r.Address)
	portValue, portErr := strconv.Atoi(portNumber)
	if err != nil || portErr != nil || host == "" || portValue < 1 || portValue > 65535 || strings.ContainsAny(r.Address, "/@") {
		return errors.New("protected Redis address is invalid")
	}
	if r.PasswordFile == "" {
		return errors.New("protected Redis password file is required")
	}
	dial, op, health := protectedTimeouts(r)
	for _, d := range []time.Duration{dial, op, health} {
		if d <= 0 || d > 30*time.Second {
			return errors.New("protected Redis timeout is invalid")
		}
	}
	if health > op {
		return errors.New("protected Redis health timeout exceeds operation timeout")
	}
	e := cfg.ProtectedStorage.Encryption
	if !protectedKeyID.MatchString(e.ActiveID) || len(e.Keys) == 0 || len(e.Keys) > 16 {
		return errors.New("protected encryption key ring is invalid")
	}
	seen := map[string]struct{}{}
	active := false
	for _, k := range e.Keys {
		if !protectedKeyID.MatchString(k.ID) || k.File == "" {
			return errors.New("protected encryption key is invalid")
		}
		if _, ok := seen[k.ID]; ok {
			return errors.New("duplicate protected encryption key")
		}
		seen[k.ID] = struct{}{}
		active = active || k.ID == e.ActiveID
	}
	if !active {
		return errors.New("active protected encryption key is missing")
	}
	return nil
}

func rejectDuplicateJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		d, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		if d == '{' {
			seen := map[string]struct{}{}
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				if !ok {
					return errors.New("invalid object")
				}
				if _, ok := seen[strings.ToLower(key)]; ok {
					return errors.New("duplicate object member")
				}
				seen[strings.ToLower(key)] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		if d == '[' {
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = dec.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func (cfg fileConfig) loadProductionConfig(diagnostics port.Diagnostics) (mcpbrokerserver.ProductionConfig, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.Listener.TLSCertFile, cfg.Listener.TLSKeyFile)
	if err != nil {
		return mcpbrokerserver.ProductionConfig{}, errors.New("load server identity")
	}
	caPEM, err := os.ReadFile(cfg.WorkloadJWT.TrustBundleFile)
	if err != nil {
		return mcpbrokerserver.ProductionConfig{}, errors.New("read workload-JWT trust bundle")
	}
	return cfg.productionConfig(certificate, caPEM, diagnostics), nil
}

func (cfg fileConfig) productionConfig(certificate tls.Certificate, caPEM []byte, diagnostics port.Diagnostics) mcpbrokerserver.ProductionConfig {
	return mcpbrokerserver.ProductionConfig{
		PublicAddress: cfg.Listener.PublicAddress,
		AdminAddress:  defaultAdminAddress,
		TLSConfig:     &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12},
		WorkloadJWT:   cfg.workloadJWTConfig(caPEM),
		ToolHive:      cfg.toolHive(), Diagnostics: diagnostics,
		PropagationWait: cfg.Drain.PropagationDelay.value(), DrainTimeout: cfg.Drain.Timeout.value(),
		ShutdownTimeout: cfg.Drain.ListenerShutdownTimeout.value(),
		PublicBounds:    mcpbrokerserver.DefaultPublicListenerConfig(),
		Transport:       cfg.transport(), RuntimeLimits: cfg.runtimeLimits(),
	}
}

func (cfg fileConfig) workloadJWTConfig(caPEM []byte) mcpbrokerserver.WorkloadJWTConfig {
	out := mcpbrokerserver.WorkloadJWTConfig{
		Issuer: cfg.WorkloadJWT.Issuer, JWKSURI: cfg.WorkloadJWT.JWKSURI,
		Audience: cfg.WorkloadJWT.Audience, AllowedSubjects: []string{cfg.WorkloadJWT.Subject},
		TrustedCAPEM: caPEM, MaxJWKSStaleness: cfg.WorkloadJWT.MaxJWKSStaleness.value(),
	}
	if bootstrap := cfg.WorkloadJWT.KubernetesBootstrap; bootstrap != nil {
		out.KubernetesBootstrap = &mcpbrokerserver.KubernetesBootstrapConfig{DiscoveryURL: bootstrap.DiscoveryURL, JWKSURI: bootstrap.JWKSURI, TokenSource: projectedTokenSource(bootstrap.TokenFile)}
	}
	return out
}

func projectedTokenSource(path string) func() ([]byte, error) {
	return func() ([]byte, error) {
		file, err := os.Open(path)
		if err != nil {
			return nil, errors.New("open projected Kubernetes token")
		}
		defer func() { _ = file.Close() }()
		data, err := io.ReadAll(io.LimitReader(file, maxClientSecretBytes+1))
		if err != nil || len(data) > maxClientSecretBytes {
			return nil, errors.New("read projected Kubernetes token")
		}
		return data, nil
	}
}

func (cfg fileConfig) drainRequestTimeout() time.Duration {
	return cfg.Drain.PropagationDelay.value() + cfg.Drain.Timeout.value() + cfg.Drain.ListenerShutdownTimeout.value()
}
func (cfg fileConfig) transport() mcpbrokergrpc.Config {
	// DialTimeout is a remote-client concern. Keep the adapter's finite default;
	// serving configuration owns only the broker's server-side bounds.
	out := mcpbrokergrpc.DefaultConfig()
	out.RPCDeadline = cfg.Transport.RPCDeadline.value()
	out.ExecuteDeadline = cfg.Transport.ExecuteDeadline.value()
	out.HandleIdleTimeout = cfg.Transport.HandleIdleTimeout.value()
	out.SweepInterval = cfg.Transport.SweepInterval.value()
	out.CleanupTimeout = cfg.Transport.CleanupTimeout.value()
	out.MaxHandles = cfg.Transport.MaxHandles
	out.MaxOwners = cfg.Transport.MaxOwners
	out.MaxReceipts = cfg.Transport.MaxReceipts
	out.MaxReceiptBytes = cfg.Transport.MaxReceiptBytes
	out.MaxPendingControls = cfg.Transport.MaxPendingControls
	out.MaxActiveExecutes = cfg.Transport.MaxActiveExecutes
	return out
}
func (cfg fileConfig) runtimeLimits() mcpbroker.Limits {
	return mcpbroker.Limits{MaxLogicalSessions: cfg.Runtime.MaxLogicalSessions, LogicalRetention: cfg.Runtime.LogicalRetention.value(), SweepInterval: cfg.Transport.SweepInterval.value(), MaxPendingStates: cfg.Runtime.MaxPendingAuthStates}
}
func (cfg fileConfig) toolHive() mcpbroker.ToolHiveConfig {
	profiles := make([]mcpbroker.ToolHiveProfile, len(cfg.Profiles))
	for i, profile := range cfg.Profiles {
		converted := mcpbroker.ToolHiveProfile{Name: profile.Name, URL: profile.URL, Auth: profile.Auth}
		if profile.OAuth != nil {
			converted.OAuth = &mcpbroker.ToolHiveOAuth{Issuer: profile.OAuth.Issuer, AuthorizationEndpoint: profile.OAuth.AuthorizationEndpoint, TokenEndpoint: profile.OAuth.TokenEndpoint, ClientID: profile.OAuth.ClientID, ClientSecretFile: profile.OAuth.ClientSecretFile, DCRDiscoveryURL: profile.OAuth.DCRDiscoveryURL, Scopes: append([]string(nil), profile.OAuth.Scopes...), RequestRefreshToken: profile.OAuth.RequestRefreshToken}
			if profile.OAuth.CIMDDocumentURL != "" {
				converted.OAuth.ClientID = profile.OAuth.CIMDDocumentURL
			}
		}
		converted.Static = make([]mcpbroker.StaticTool, len(profile.Static))
		for j, spec := range profile.Static {
			converted.Static[j] = mcpbroker.StaticTool{Name: spec.Name, Description: spec.Description, Schema: append(json.RawMessage(nil), spec.Schema...), ReadOnly: spec.ReadOnly}
		}
		profiles[i] = converted
	}
	callbackURL := ""
	if len(profiles) != 0 {
		callbackURL = cfg.CallbackURL
	}
	out := mcpbroker.ToolHiveConfig{CallbackURL: callbackURL, Profiles: profiles}
	if cfg.ProtectedStorage != nil {
		r := cfg.ProtectedStorage.Redis
		dial, op, health := protectedTimeouts(r)
		keys := make([]mcpbroker.ProtectedEncryptionKey, len(cfg.ProtectedStorage.Encryption.Keys))
		for i, k := range cfg.ProtectedStorage.Encryption.Keys {
			keys[i] = mcpbroker.ProtectedEncryptionKey{ID: k.ID, File: k.File}
		}
		clientConfig := mcpbroker.ProtectedRedisClientConfig{Addr: r.Address, UsernameFile: r.UsernameFile, PasswordFile: r.PasswordFile, CAFile: r.CAFile, TLS: true, DialTimeout: dial, OperationTimeout: op}
		factory := func(config mcpbroker.ProtectedRedisClientConfig) (redis.UniversalClient, error) {
			return redisstore.NewClient(redisstore.Config{Addr: config.Addr, UsernameFile: config.UsernameFile, PasswordFile: config.PasswordFile, CAFile: config.CAFile, TLS: config.TLS, AllowPlaintext: config.AllowPlaintext, DialTimeout: config.DialTimeout, OperationTimeout: config.OperationTimeout})
		}
		out.ProtectedStorage = &mcpbroker.ProtectedStorageConfig{Redis: mcpbroker.ProtectedRedisConfig{Client: factory, ClientConfig: clientConfig, HealthTimeout: health}, Encryption: mcpbroker.ProtectedEncryptionConfig{ActiveID: cfg.ProtectedStorage.Encryption.ActiveID, Keys: keys}}
	}
	return out
}
