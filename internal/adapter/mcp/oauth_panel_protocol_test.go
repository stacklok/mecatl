package mcp

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
)

func TestDirectMCPOnboarding_Scenario1_RFC9207Matrix(t *testing.T) {
	resource := "https://mcp.example/mcp"
	issuer := "https://issuer.example"
	store := newOAuthMemoryStore(t)
	controller := &OAuthController{
		state: &oauthCredentialState{
			writer:       store,
			identity:     oauthCredentialIdentity{Resource: resource},
			registration: oauthRegistration{kind: oauthDCRClientKind},
		},
		transport:                &oauthHTTPTransport{issuerOrigin: issuer},
		dcrIssuer:                issuer,
		dcrIssParameterSupported: true,
		presenter: OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) {
			return &auth.AuthorizationResult{Code: "code", State: "state"}, nil
		}),
	}

	url := issuer + "/authorize?scope=openid&resource=https%3A%2F%2Fmcp.example%2Fmcp"
	if _, err := controller.presentAuthorization(context.Background(), &auth.AuthorizationArgs{URL: url}); err == nil {
		t.Fatal("callback without RFC 9207 iss was accepted")
	}

	controller.presenter = OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) {
		return &auth.AuthorizationResult{Code: "code", State: "state", Iss: "https://wrong.example"}, nil
	})
	if _, err := controller.presentAuthorization(context.Background(), &auth.AuthorizationArgs{URL: url}); err == nil {
		t.Fatal("callback with a mismatched RFC 9207 iss was accepted")
	}

	controller.dcrIssParameterSupported = false
	if _, err := controller.presentAuthorization(context.Background(), &auth.AuthorizationArgs{URL: url}); err == nil {
		t.Fatal("present mismatched iss was accepted when RFC 9207 omission was allowed")
	}
	controller.presenter = OAuthPresenterFunc(func(context.Context, string) (*auth.AuthorizationResult, error) {
		return &auth.AuthorizationResult{Code: "code", State: "state", Iss: issuer}, nil
	})
	if _, err := controller.presentAuthorization(context.Background(), &auth.AuthorizationArgs{URL: url}); err != nil {
		t.Fatalf("callback with the discovered issuer was rejected: %v", err)
	}
}

func TestDirectMCPOnboarding_ScenarioDCRMetadataPersistsRFC9207Capability(t *testing.T) {
	meta := oauthDCRMetadata{
		Issuer:                  issuerForPanelTest,
		Resource:                "https://mcp.example/mcp",
		RedirectPolicy:          oauthDCRRedirectPolicy,
		RedirectPath:            "/oauth/callback/test",
		TokenEndpointAuthMethod: "none",
		GrantTypes:              []string{"authorization_code"},
		ResponseTypes:           []string{"code"},
		Scopes:                  []string{oauthDCRScope},
		AuthorizationResponseIssParameterSupported: true,
	}
	if !equalDCRMetadata(meta, meta) || fingerprintDCRMetadata(meta) == fingerprintDCRMetadata(oauthDCRMetadata{
		Issuer: meta.Issuer, Resource: meta.Resource, RedirectPolicy: meta.RedirectPolicy,
		RedirectPath: meta.RedirectPath, TokenEndpointAuthMethod: meta.TokenEndpointAuthMethod,
		GrantTypes: meta.GrantTypes, ResponseTypes: meta.ResponseTypes, Scopes: meta.Scopes,
	}) {
		t.Fatal("RFC 9207 capability is not part of the DCR transaction binding")
	}
}

const issuerForPanelTest = "https://issuer.example"
