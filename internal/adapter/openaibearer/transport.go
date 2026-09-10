// Package openaibearer injects rotating file-backed credentials into OpenAI requests.
package openaibearer

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/stacklok/mecatl/internal/adapter/llmresilience"
)

const (
	maxBearerTokenFileBytes = 64 << 10
	bearerTokenUnavailable  = "OpenAI bearer token is unavailable; check that the projected token file is mounted and readable"
)

// NewHTTPClient returns an HTTP client that reads path before
// every request and replaces Authorization with the current bearer token.
// Bearers are sent only to the configured base URL's origin. Redirects are
// refused so neither the bearer nor a request body can reach another origin.
func NewHTTPClient(path, baseURL string, base *http.Client) (*http.Client, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("OpenAI bearer token file path is empty")
	}
	origin, err := bearerOrigin(baseURL)
	if err != nil {
		return nil, err
	}

	client := &http.Client{}
	if base != nil {
		*client = *base
	}
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = &bearerTokenFileTransport{base: transport, path: path, origin: origin}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}

type bearerTokenFileTransport struct {
	base   http.RoundTripper
	path   string
	origin url.URL
}

func (t *bearerTokenFileTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !sameBearerOrigin(req.URL, &t.origin) {
		return nil, errors.New("refusing OpenAI bearer token outside configured origin")
	}
	token, err := readBearerTokenFile(t.path)
	if err != nil {
		return nil, errors.Join(llmresilience.ErrCredentials, err)
	}
	clone := req.Clone(req.Context())
	clone.Header.Del("Authorization")
	clone.Header.Set("Authorization", "Bearer "+token)
	return t.base.RoundTrip(clone)
}

func readBearerTokenFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New(bearerTokenUnavailable)
	}
	defer func() { _ = file.Close() }()

	contents, err := io.ReadAll(io.LimitReader(file, maxBearerTokenFileBytes+1))
	if err != nil {
		return "", errors.New(bearerTokenUnavailable)
	}
	if len(contents) > maxBearerTokenFileBytes {
		return "", errors.New("OpenAI bearer token file exceeds 64 KiB")
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "", errors.New("OpenAI bearer token file is empty")
	}
	if !validBearerToken(token) {
		return "", errors.New("OpenAI bearer token file contains an invalid bearer token")
	}
	return token, nil
}

func validBearerToken(token string) bool {
	for i := range len(token) {
		c := token[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		switch c {
		case '-', '.', '_', '~', '+', '/', '=':
			continue
		default:
			return false
		}
	}
	return true
}

func bearerOrigin(raw string) (url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return url.URL{}, errors.New("OpenAI bearer token base URL must be an absolute URL without userinfo, query, or fragment")
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && (scheme != "http" || !isBearerLoopback(u.Hostname())) {
		return url.URL{}, errors.New("OpenAI bearer token base URL must use HTTPS (HTTP is allowed only for loopback)")
	}
	u.Scheme = scheme
	return *u, nil
}

func sameBearerOrigin(got, want *url.URL) bool {
	if got == nil || want == nil {
		return false
	}
	return strings.EqualFold(got.Scheme, want.Scheme) && strings.EqualFold(got.Hostname(), want.Hostname()) && effectivePort(got) == effectivePort(want)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	return ""
}

func isBearerLoopback(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	default:
		return false
	}
}
