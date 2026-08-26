package mcp

import (
	"context"
	"errors"
	"testing"

	"github.com/stacklok/mecatl/mcp/oauthlogin"
)

func TestOAuthLoginPresenterConvertsOnlyCallbackResult(t *testing.T) {
	presenter := OAuthLoginPresenter(func(context.Context, string) (oauthlogin.Result, error) {
		return oauthlogin.Result{Code: "code", State: "state", Iss: "https://issuer.example"}, nil
	})
	result, err := presenter.PresentAuthorization(context.Background(), "https://issuer.example/authorize?state=state")
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != "code" || result.State != "state" || result.Iss != "https://issuer.example" {
		t.Fatalf("result = %#v", result)
	}
}

func TestOAuthLoginPresenterPreservesEmptyIssuer(t *testing.T) {
	presenter := OAuthLoginPresenter(func(context.Context, string) (oauthlogin.Result, error) {
		return oauthlogin.Result{Code: "code", State: "state"}, nil
	})
	result, err := presenter.PresentAuthorization(context.Background(), "https://issuer.example/authorize?state=state")
	if err != nil {
		t.Fatal(err)
	}
	if result.Code != "code" || result.State != "state" || result.Iss != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestOAuthLoginPresenterPreservesCancellation(t *testing.T) {
	presenter := OAuthLoginPresenter(func(context.Context, string) (oauthlogin.Result, error) {
		return oauthlogin.Result{}, context.Canceled
	})
	if _, err := presenter.PresentAuthorization(context.Background(), "https://issuer.example/authorize?state=state"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}
