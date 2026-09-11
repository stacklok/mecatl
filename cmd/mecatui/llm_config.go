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

const nativeLLMConfigUsage = "Usage: mecatui llm config set ENDPOINT --gateway-url URL --issuer ISSUER --client-id ID --default-model MODEL --credential-home ABSOLUTE_PATH [--resource-audience AUDIENCE] [--scope SCOPE] [--issuer-ca-bundle PATH] [--gateway-ca-bundle PATH]"

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
	fs.StringVar(&opts.credentialHome, "credential-home", "", "absolute directory for protected native LLM credentials")
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
	if opts.gatewayURL == "" || opts.issuer == "" || opts.clientID == "" || opts.defaultModel == "" || opts.credentialHome == "" {
		return errors.New("llm config set: --gateway-url, --issuer, --client-id, --default-model, and --credential-home are required")
	}
	credentialHome, err := canonicalNativeCredentialHome(opts.credentialHome)
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
		return setNativeLLMEndpoint(doc, res.llmEndpoint, opts)
	}); err != nil {
		return fmt.Errorf("llm config set: %w", err)
	}
	_, err = fmt.Fprintf(stdout, "Configured native LLM endpoint %q in %s. Next run: mecatui llm login %s\n", res.llmEndpoint, settings.path, res.llmEndpoint)
	return err
}

func setNativeLLMEndpoint(doc *yamldiag.Document, endpointID string, opts nativeLLMConfigSetOptions) error {
	var section permconfig.LLMSection
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
		return fmt.Errorf("llm.credential_home is shared by all native endpoints; use the existing credential home %q or migrate credentials before changing it", section.CredentialHome)
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
