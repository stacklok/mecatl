package identityharness

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
)

var (
	consentFormAction = regexp.MustCompile(`<form[^>]*\saction="([^"]*)"`)
	consentFormHandle = regexp.MustCompile(`<input type="hidden" name="handle" value="([^"]*)">`)
)

// AuthorizeWithConsent plays the browser for a real client's authorization
// request at a real AS-1 that shows a consent page, as ToolHive's embedded
// authorization server does for public clients. Unlike Authorize, it keeps
// cookies for the one sign-in (ToolHive binds the sign-in to the browser with
// a cookie), follows 302 and 303 redirects, and approves each consent page by
// posting its form with the page's Origin, as a browser would. It returns the
// query of the redirect to the request's redirect_uri (code, state, or error)
// without contacting the client's redirect URI.
func (h *Harness) AuthorizeWithConsent(ctx context.Context, authorizationURL string) (url.Values, error) {
	u, err := url.Parse(authorizationURL)
	if err != nil {
		return nil, err
	}
	redirectURI := u.Query().Get("redirect_uri")
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	client := h.HTTPClient()
	client.Jar = jar
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, authorizationURL, nil)
	if err != nil {
		return nil, err
	}
	for range 10 {
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if err != nil {
			return nil, err
		}
		switch resp.StatusCode {
		case http.StatusFound, http.StatusSeeOther:
			location, err := resp.Location()
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(location.String(), redirectURI+"?") {
				return location.Query(), nil
			}
			req, err = http.NewRequestWithContext(ctx, http.MethodGet, location.String(), nil)
			if err != nil {
				return nil, err
			}
		case http.StatusOK:
			req, err = approveConsent(ctx, req.URL, body)
			if err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("identityharness: %s %s: status %d", req.Method, req.URL.Path, resp.StatusCode)
		}
	}
	return nil, fmt.Errorf("identityharness: too many redirects")
}

// approveConsent builds the browser's approval of the consent page at page.
func approveConsent(ctx context.Context, page *url.URL, body []byte) (*http.Request, error) {
	action := consentFormAction.FindSubmatch(body)
	handle := consentFormHandle.FindSubmatch(body)
	if action == nil || handle == nil {
		return nil, fmt.Errorf("identityharness: GET %s: status 200 without a consent form", page.Path)
	}
	target, err := page.Parse(html.UnescapeString(string(action[1])))
	if err != nil {
		return nil, err
	}
	form := url.Values{"handle": {html.UnescapeString(string(handle[1]))}, "decision": {"approve"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", page.Scheme+"://"+page.Host)
	return req, nil
}
