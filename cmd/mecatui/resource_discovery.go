package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/stacklok/toolhive/pkg/oauthproto"

	"github.com/stacklok/mecatl/engine/session"
	"github.com/stacklok/mecatl/internal/adapter/resourceurl"
)

// This is a narrow adaptation of ToolHive's RFC 9728 discovery flow
// (Apache-2.0, ToolHive v0.40.0), using the public-address dial discipline
// from ToolHive-Core v0.0.41. It deliberately has no credential or browser
// dependency: discovery must finish before either can be initialized.
const (
	maxDiscoveryBodyBytes = 1 << 20
	bootstrapTimeout      = 15 * time.Second
)

var errDiscoveryRejected = errors.New("protected-resource discovery rejected")

type protectedResource struct {
	Resource    string
	GRPCTarget  string
	MetadataURL string
}

type discoveredResource struct {
	protectedResource
	Issuer   string
	Audience string
	ClientID string
	Scopes   []string
}

func parseProtectedResource(raw string) (protectedResource, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || !safeDisplayValue(raw) {
		return protectedResource{}, errDiscoveryRejected
	}
	if !strings.Contains(raw, "://") {
		if !validDNSName(raw) {
			return protectedResource{}, errDiscoveryRejected
		}
		canonical, err := resourceurl.Canonical("https://" + strings.ToLower(raw))
		if err != nil {
			return protectedResource{}, errDiscoveryRejected
		}
		u, err := url.Parse(canonical)
		if err != nil {
			return protectedResource{}, errDiscoveryRejected
		}
		return newProtectedResource(u), nil
	}
	canonical, err := resourceurl.Canonical(raw)
	if err != nil {
		return protectedResource{}, errDiscoveryRejected
	}
	u, err := url.Parse(canonical)
	if err != nil {
		return protectedResource{}, errDiscoveryRejected
	}
	return newProtectedResource(u), nil
}

func newProtectedResource(u *url.URL) protectedResource {
	resource := u.String()
	return protectedResource{Resource: resource, GRPCTarget: net.JoinHostPort(u.Hostname(), portOr443(u)), MetadataURL: resourceurl.MetadataURL(resource)}
}

func resourceMatches(expected protectedResource, actual string) bool {
	canonical, err := resourceurl.Canonical(actual)
	return err == nil && canonical == expected.Resource
}

func parseIssuer(raw string) (string, error) {
	if !safeDisplayValue(raw) {
		return "", errDiscoveryRejected
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery || !validAuthority(u) {
		return "", errDiscoveryRejected
	}
	// Issuer identifiers are compared exactly (RFC 8414); validation must not
	// rewrite their semantic spelling.
	return raw, nil
}

func oidcMetadataURL(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil {
		return ""
	}
	path := strings.TrimSuffix(u.EscapedPath(), "/") + oauthproto.WellKnownOIDCPath
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return ""
	}
	u.Path, u.RawPath = decoded, path
	return u.String()
}

func validAuthority(u *url.URL) bool {
	host := u.Hostname()
	if host == "" || strings.ContainsAny(u.Host, "@/\\?#") || net.ParseIP(host) != nil {
		return false
	}
	if u.Port() != "" {
		for _, r := range u.Port() {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return validDNSName(host)
}

func canonicalAuthority(u *url.URL) string {
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" {
		return net.JoinHostPort(host, port)
	}
	return host
}

func portOr443(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	return "443"
}

func validDNSName(host string) bool {
	if len(host) == 0 || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !isDNSLabelRune(r) {
				return false
			}
		}
	}
	return true
}

func isDNSLabelRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-'
}

// ToolHive-Core's networking package is intentionally not used for this client:
// its generic host-scoped client permits policies (notably redirects) that are
// too broad for anonymous issuer/resource bootstrap. This transport validates
// every DNS answer, pins the selected address, and refuses every redirect.
func newPublicBootstrapClient() *http.Client {
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            publicBootstrapDial,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		MaxIdleConns:           2,
		MaxIdleConnsPerHost:    1,
		IdleConnTimeout:        15 * time.Second,
	}
	return &http.Client{Transport: anonymousTransport{next: transport}, Timeout: bootstrapTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errDiscoveryRejected }}
}

func publicBootstrapDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errDiscoveryRejected
	}
	answers, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(answers) == 0 {
		return nil, errDiscoveryRejected
	}
	for _, answer := range answers {
		if err := validatePublicAddress(answer.String()); err != nil {
			return nil, errDiscoveryRejected
		}
	}
	// Dial the validated answer rather than the hostname, pinning this connection
	// to the DNS result that passed admission. TLS still verifies the hostname.
	return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(answers[0].String(), port))
}

func validatePublicAddress(raw string) error {
	addr, err := netip.ParseAddr(raw)
	if err != nil || !addr.IsValid() || addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() || addr.IsMulticast() || addr.IsUnspecified() {
		return errDiscoveryRejected
	}
	if err := session.ValidateResolvedIP(net.IP(addr.AsSlice())); err != nil {
		return errDiscoveryRejected
	}
	return nil
}

type anonymousTransport struct{ next http.RoundTripper }

func (t anonymousTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := validateAnonymousRequest(req); err != nil {
		return nil, err
	}
	return t.next.RoundTrip(req)
}

func (t anonymousTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func validateAnonymousRequest(req *http.Request) error {
	if req == nil || req.URL == nil || req.URL.Scheme != "https" || req.URL.User != nil || req.Header.Get("Authorization") != "" || req.Header.Get("Cookie") != "" || req.Header.Get("Proxy-Authorization") != "" {
		return errDiscoveryRejected
	}
	return nil
}

func discoverProtectedResource(ctx context.Context, resource protectedResource, transport http.RoundTripper) (discoveredResource, error) {
	var client *http.Client
	if transport == nil {
		client = newPublicBootstrapClient()
		defer client.CloseIdleConnections()
	} else {
		client = &http.Client{Transport: anonymousTransport{next: transport}, Timeout: bootstrapTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return errDiscoveryRejected }}
	}
	body, err := fetchDiscoveryJSON(ctx, client, resource.MetadataURL)
	if err != nil {
		return discoveredResource{}, errDiscoveryRejected
	}
	profile, err := parseProfileDocument(resource, body)
	if err != nil {
		return discoveredResource{}, errDiscoveryRejected
	}
	issuerBody, err := fetchDiscoveryJSON(ctx, client, oidcMetadataURL(profile.Issuer))
	if err != nil || validateIssuerDocument(profile.Issuer, issuerBody) != nil {
		return discoveredResource{}, errDiscoveryRejected
	}
	return discoveredResource{protectedResource: resource, Issuer: profile.Issuer, Audience: profile.Audience, ClientID: profile.ClientID, Scopes: profile.Scopes}, nil
}

func fetchDiscoveryJSON(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	if client == nil {
		return nil, errDiscoveryRejected
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errDiscoveryRejected
	}
	res, err := client.Do(req)
	if err != nil || res == nil || res.Body == nil {
		return nil, errDiscoveryRejected
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK || validateJSONMediaType(res.Header.Get("Content-Type")) != nil {
		return nil, errDiscoveryRejected
	}
	return readJSONBody(res.Body)
}

func validateJSONMediaType(contentType string) error {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || (mediaType != "application/json" && !strings.HasSuffix(mediaType, "+json")) {
		return errDiscoveryRejected
	}
	return nil
}

func readJSONBody(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxDiscoveryBodyBytes+1))
	if err != nil || len(data) > maxDiscoveryBodyBytes || !json.Valid(data) {
		return nil, errDiscoveryRejected
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	var value any
	if dec.Decode(&value) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return nil, errDiscoveryRejected
	}
	return data, nil
}

type profileDocument struct {
	Issuer   string
	Audience string
	ClientID string
	Scopes   []string
}

func parseProfileDocument(resource protectedResource, body []byte) (profileDocument, error) {
	if hasDuplicateSecurityFields(body, map[string]bool{"resource": true, "authorization_servers": true, "com.stacklok.mecatl.audience": true, "com.stacklok.mecatl.client_id": true, "scopes_supported": true}) {
		return profileDocument{}, errDiscoveryRejected
	}
	var doc struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		Audience             string   `json:"com.stacklok.mecatl.audience"`
		ClientID             string   `json:"com.stacklok.mecatl.client_id"`
		Scopes               []string `json:"scopes_supported"`
	}
	if json.Unmarshal(body, &doc) != nil || !resourceMatches(resource, doc.Resource) || len(doc.AuthorizationServers) != 1 || !safeProfileValue(doc.Audience) || !safeProfileValue(doc.ClientID) {
		return profileDocument{}, errDiscoveryRejected
	}
	issuer, err := parseIssuer(doc.AuthorizationServers[0])
	if err != nil {
		return profileDocument{}, errDiscoveryRejected
	}
	scopes := make(map[string]bool, len(doc.Scopes))
	for _, scope := range doc.Scopes {
		if !validScope(scope) || scopes[scope] {
			return profileDocument{}, errDiscoveryRejected
		}
		scopes[scope] = true
	}
	return profileDocument{Issuer: issuer, Audience: doc.Audience, ClientID: doc.ClientID, Scopes: append([]string(nil), doc.Scopes...)}, nil
}

func validateIssuerDocument(expected string, body []byte) error {
	if hasDuplicateSecurityFields(body, map[string]bool{"issuer": true}) {
		return errDiscoveryRejected
	}
	var doc struct {
		Issuer string `json:"issuer"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return errDiscoveryRejected
	}
	issuer, err := parseIssuer(doc.Issuer)
	if err != nil || issuer != expected {
		return errDiscoveryRejected
	}
	return nil
}

func hasDuplicateSecurityFields(body []byte, fields map[string]bool) bool {
	dec := json.NewDecoder(strings.NewReader(string(body)))
	token, err := dec.Token()
	if err != nil {
		return true
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return false
	}
	seen := make(map[string]bool)
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return true
		}
		name, ok := token.(string)
		if !ok {
			return true
		}
		if fields[name] && seen[name] {
			return true
		}
		seen[name] = true
		var discard json.RawMessage
		if dec.Decode(&discard) != nil {
			return true
		}
	}
	_, err = dec.Token()
	return err != nil
}

func safeProfileValue(value string) bool {
	return safeDisplayValue(value)
}

func safeDisplayValue(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return false
		}
	}
	return true
}

func validScope(scope string) bool {
	if scope == "" {
		return false
	}
	for _, r := range scope {
		if r < 0x21 || r == '"' || r == '\\' || r > 0x7e {
			return false
		}
	}
	return true
}
