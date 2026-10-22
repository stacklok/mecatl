// Package oauthbrowser drives explicit browser consent in offline OAuth fixtures.
package oauthbrowser

import (
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Approve follows an authorization request, then submits its consent form with
// the same browser cookies. The caller owns the returned response body.
//
//nolint:gosec // G704: only test-owned httptest URLs are passed by the offline fixtures.
func Approve(client *http.Client, request *http.Request) (*http.Response, error) {
	browser := *client
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	browser.Jar = jar
	response, err := browser.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("consent page status: %d", response.StatusCode)
	}
	var handle string
	tokens := html.NewTokenizer(response.Body)
	for {
		kind := tokens.Next()
		if kind == html.ErrorToken {
			if err := tokens.Err(); err != io.EOF {
				return nil, err
			}
			break
		}
		if kind != html.StartTagToken && kind != html.SelfClosingTagToken {
			continue
		}
		token := tokens.Token()
		if token.Data != "input" {
			continue
		}
		var name, value string
		for _, attr := range token.Attr {
			switch attr.Key {
			case "name":
				name = attr.Val
			case "value":
				value = attr.Val
			}
		}
		if name == "handle" {
			handle = value
		}
	}
	if handle == "" {
		return nil, fmt.Errorf("consent page has no authorization handle")
	}
	consentURL := *request.URL
	consentURL.Path = strings.TrimSuffix(consentURL.Path, "/authorize") + "/consent"
	consentURL.RawQuery = ""
	form := url.Values{"handle": {handle}, "decision": {"approve"}}
	approve, err := http.NewRequestWithContext(request.Context(), http.MethodPost, consentURL.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	approve.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	approve.Header.Set("Origin", consentURL.Scheme+"://"+consentURL.Host)
	return browser.Do(approve)
}
