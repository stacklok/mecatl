package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/goccy/go-yaml"

	"github.com/stacklok/mecatl/internal/adapter/permconfig"
	"github.com/stacklok/mecatl/internal/adapter/xdgconfig"
	"github.com/stacklok/mecatl/internal/adapter/yamldiag"
)

const nativeLLMConfigUsage = "Usage: mecatui llm config set ENDPOINT --gateway-url URL --issuer ISSUER --client-id ID --default-model MODEL [--credential-home ABSOLUTE_PATH] [--resource-audience AUDIENCE] [--scope SCOPE] [--issuer-ca-bundle PATH] [--gateway-ca-bundle PATH]"

type repeatedScopes []string

func (s *repeatedScopes) String() string { return strings.Join(*s, ",") }
func (s *repeatedScopes) Set(value string) error {
	*s = append(*s, value)
	return nil
}

type nativeLLMConfigSetOptions struct {
	gatewayURL, issuer, clientID, defaultModel, credentialHome string
	resourceAudience, issuerCABundle, gatewayCABundle          string
	scopes                                                     repeatedScopes
}

func runLLMConfigCommand(res invocationResolution, stdout, stderr io.Writer) error {
	if len(res.remaining) == 1 && isHelpMetaFlag(res.remaining[0]) {
		_, _ = fmt.Fprintln(stderr, nativeLLMConfigUsage)
		return flag.ErrHelp
	}
	fs := flag.NewFlagSet("llm config set", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts nativeLLMConfigSetOptions
	fs.StringVar(&opts.gatewayURL, "gateway-url", "", "HTTPS URL of the OpenAI Responses gateway")
	fs.StringVar(&opts.issuer, "issuer", "", "HTTPS OIDC issuer URL")
	fs.StringVar(&opts.clientID, "client-id", "", "OIDC public client ID")
	fs.StringVar(&opts.defaultModel, "default-model", "", "default model for this endpoint")
	fs.StringVar(&opts.credentialHome, "credential-home", "", "existing owner-only credential directory (default: $XDG_STATE_HOME/mecatl/provider-oidc)")
	fs.StringVar(&opts.resourceAudience, "resource-audience", "", "optional OAuth resource audience")
	fs.Var(&opts.scopes, "scope", "OIDC scope (repeatable; defaults to openid and offline_access)")
	fs.StringVar(&opts.issuerCABundle, "issuer-ca-bundle", "", "PEM CA bundle for a private-CA OIDC issuer")
	fs.StringVar(&opts.gatewayCABundle, "gateway-ca-bundle", "", "PEM CA bundle for a private-CA gateway")
	if err := fs.Parse(res.remaining); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("llm config set: unexpected positional arguments; run `mecatui llm config --help`")
	}
	if opts.gatewayURL == "" || opts.issuer == "" || opts.clientID == "" || opts.defaultModel == "" {
		return errors.New("llm config set: --gateway-url, --issuer, --client-id, and --default-model are required")
	}
	credentialHome, defaultCredentialHome, err := resolveNativeCredentialHome(opts.credentialHome)
	if err != nil {
		return err
	}
	opts.credentialHome = credentialHome
	if len(opts.scopes) == 0 {
		opts.scopes = repeatedScopes{"openid", "offline_access"}
	}
	base := xdgconfig.UserConfigDir(xdgconfig.OSEnv)
	if base == "" {
		return errors.New("llm config set: operator settings path is unavailable")
	}
	settings := &operatorLearningSettings{path: filepath.Join(base, "mecatl", "settings.yaml")}
	if err := settings.withLockedDocument(func(doc *yamldiag.Document) error {
		credentialHome, err := prepareNativeCredentialHome(doc, opts.credentialHome, defaultCredentialHome)
		if err != nil {
			return err
		}
		opts.credentialHome = credentialHome
		return setNativeLLMEndpoint(doc, res.llmEndpoint, opts)
	}); err != nil {
		return fmt.Errorf("llm config set: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "Configured native LLM endpoint %q in %s. Next run: mecatui llm login %s\n", res.llmEndpoint, settings.path, res.llmEndpoint)
	return err
}

func setNativeLLMEndpoint(doc *yamldiag.Document, endpointID string, opts nativeLLMConfigSetOptions) error {
	section := permconfig.LLMSection{CredentialKey: permconfig.NativeCredentialKey{Source: "keyring"}}
	llmNode, err := uniqueMappingValue(doc.Mapping(), "llm", false)
	if err != nil {
		return err
	}
	if llmNode != nil {
		if err := doc.Decode(llmNode, &section); err != nil {
			return errors.New("existing llm configuration is invalid")
		}
	}
	if section.Endpoints == nil {
		section.Endpoints = make(permconfig.NativeEndpointDefinitions)
	}
	if section.CredentialHome != "" && section.CredentialHome != opts.credentialHome {
		return sharedNativeCredentialHomeError(section.CredentialHome)
	}
	section.CredentialHome = opts.credentialHome
	section.Endpoints[endpointID] = permconfig.NativeEndpointDefinition{
		Protocol: "openai-responses", URL: opts.gatewayURL, DefaultModel: opts.defaultModel,
		OIDC:        permconfig.NativeOIDC{Issuer: opts.issuer, ClientID: opts.clientID, ResourceAudience: opts.resourceAudience, Scopes: []string(opts.scopes)},
		IssuerTrust: nativeTrustForBundle(opts.issuerCABundle), GatewayTrust: nativeTrustForBundle(opts.gatewayCABundle),
	}
	encoded, err := yaml.Marshal(struct {
		LLM permconfig.LLMSection `yaml:"llm"`
	}{LLM: section})
	if err != nil {
		return errors.New("encode native LLM configuration")
	}
	replacementDoc, err := yamldiag.ParseSettingsDocument(encoded)
	if err != nil {
		return errors.New("encode native LLM configuration")
	}
	replacement := replacementDoc.Mapping().Values[0]
	if len(doc.Mapping().Values) == 0 {
		*doc.Mapping() = *replacementDoc.Mapping()
	} else if llmNode == nil {
		doc.Mapping().Values = append(doc.Mapping().Values, replacement)
	} else {
		for _, entry := range doc.Mapping().Values {
			key, _ := scalarValue(entry.Key)
			if key == "llm" {
				if err := entry.Replace(replacement.Value); err != nil {
					return errors.New("update native LLM configuration")
				}
				break
			}
		}
	}
	if err := permconfig.ValidateYAML([]byte(doc.String())); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}
	return nil
}

func resolveNativeCredentialHome(path string) (string, bool, error) {
	if path != "" {
		canonical, err := canonicalNativeCredentialHome(path)
		return canonical, false, err
	}
	base := xdgconfig.UserStateDir(xdgconfig.OSEnv)
	if !filepath.IsAbs(base) {
		home, err := xdgconfig.OSEnv.UserHomeDir()
		if err != nil || !filepath.IsAbs(home) {
			return "", true, errors.New("llm config set: native credential state path is unavailable; pass --credential-home")
		}
		base = filepath.Join(home, ".local", "state")
	}
	credentialHome := filepath.Join(base, "mecatl", "provider-oidc")
	return filepath.Clean(credentialHome), true, nil
}

func prepareNativeCredentialHome(doc *yamldiag.Document, requested string, useDefault bool) (string, error) {
	configured, err := configuredNativeCredentialHome(doc)
	if err != nil {
		return "", err
	}
	if configured != "" && configured != requested {
		canonical, resolveErr := filepath.EvalSymlinks(requested)
		if !useDefault || resolveErr != nil || filepath.Clean(canonical) != configured {
			return "", sharedNativeCredentialHomeError(configured)
		}
	}
	if useDefault {
		return ensureDefaultNativeCredentialHome(requested)
	}
	return requested, nil
}

func configuredNativeCredentialHome(doc *yamldiag.Document) (string, error) {
	llmNode, err := uniqueMappingValue(doc.Mapping(), "llm", false)
	if err != nil || llmNode == nil {
		return "", err
	}
	var section permconfig.LLMSection
	if err := doc.Decode(llmNode, &section); err != nil {
		return "", errors.New("existing llm configuration is invalid")
	}
	return section.CredentialHome, nil
}

func sharedNativeCredentialHomeError(home string) error {
	return fmt.Errorf("llm.credential_home is shared by all native endpoints; use the existing credential home %q or migrate credentials before changing it", home)
}

func ensureDefaultNativeCredentialHome(path string) (string, error) {
	var missing []string
	ancestor := path
	for {
		info, err := os.Lstat(ancestor)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 && ancestor == path {
				return "", errors.New("default credential home must not be a symbolic link")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("default credential home is unavailable")
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("default credential home has no existing ancestor")
		}
		missing = append(missing, filepath.Base(ancestor))
		ancestor = parent
	}
	physical, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", errors.New("default credential home is unavailable")
	}
	for i := len(missing) - 1; i >= 0; i-- {
		physical = filepath.Join(physical, missing[i])
		if err := os.Mkdir(physical, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", errors.New("create default credential home")
		}
		info, err := os.Lstat(physical)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", errors.New("default credential home contains an unsafe path component")
		}
	}
	return canonicalNativeCredentialHome(physical)
}

func canonicalNativeCredentialHome(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("llm config set: --credential-home must be an absolute path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("llm config set: --credential-home must be an existing directory")
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", errors.New("llm config set: --credential-home must be an existing directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 || int64(stat.Uid) != int64(os.Geteuid()) {
		return "", errors.New("llm config set: --credential-home must be an owner-only directory")
	}
	return filepath.Clean(canonical), nil
}

func nativeTrustForBundle(bundle string) permconfig.NativeTrust {
	if bundle == "" {
		return permconfig.NativeTrust{Policy: "public"}
	}
	return permconfig.NativeTrust{Policy: "private-ca", CABundle: bundle}
}
