// Copyright 2026 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/stacklok/mecatl/internal/adapter/resourceurl"
)

// ProtectedResourceProfile is the validated, public subset of the server's
// OIDC configuration that RFC 9728 makes discoverable. This is a minimal
// adaptation of Apache-2.0 ToolHive v0.40.0 pkg/auth/well_known.go; it keeps
// the profile type open for mecatl's namespaced fields and routes only the
// configured resource path.
type ProtectedResourceProfile struct {
	Resource string
	Issuer   string
	Audience string
	ClientID string
	Scopes   []string
}

// WellKnownProtectedResourceURL derives the RFC 9728 metadata URL for resource.
// The same helper is used for public routing and bearer challenges so path
// resources cannot advertise one endpoint and serve another.
func WellKnownProtectedResourceURL(resource string) string {
	return resourceurl.MetadataURL(resource)
}

// NewProtectedResourceHandler returns the anonymous RFC 9728 endpoint for a
// complete profile. A nil result means discovery is disabled.
func NewProtectedResourceHandler(profile ProtectedResourceProfile) http.Handler {
	if !validProtectedResourceScopes(profile.Scopes) {
		return nil
	}
	canonicalResource, err := resourceurl.Canonical(profile.Resource)
	if err != nil || canonicalResource == "" {
		return nil
	}
	profile.Resource = canonicalResource
	metadataURL := WellKnownProtectedResourceURL(profile.Resource)
	if metadataURL == "" || profile.Issuer == "" || profile.Audience == "" || profile.ClientID == "" {
		return nil
	}
	metadata, err := url.Parse(metadataURL)
	if err != nil {
		return nil
	}
	return protectedResourceHandler{path: metadata.EscapedPath(), profile: profile}
}

// WithProtectedResourceMetadata mounts the anonymous metadata endpoint in front
// of next. Non-metadata routes retain next's existing behavior.
func WithProtectedResourceMetadata(profile ProtectedResourceProfile, next http.Handler) http.Handler {
	h := NewProtectedResourceHandler(profile)
	if h == nil {
		return next
	}
	metadataURL := WellKnownProtectedResourceURL(profile.Resource)
	metadata, _ := url.Parse(metadataURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() == metadata.EscapedPath() {
			h.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type protectedResourceHandler struct {
	path    string
	profile ProtectedResourceProfile
}

func (h protectedResourceHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.EscapedPath() != h.path || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	metadata := protectedResourceMetadata{
		Resource:               h.profile.Resource,
		AuthorizationServers:   []string{h.profile.Issuer},
		BearerMethodsSupported: []string{"header"},
		Audience:               h.profile.Audience,
		ClientID:               h.profile.ClientID,
	}
	if len(h.profile.Scopes) != 0 {
		metadata.ScopesSupported = append([]string(nil), h.profile.Scopes...)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(metadata)
}

type protectedResourceMetadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	Audience               string   `json:"com.stacklok.mecatl.audience"`
	ClientID               string   `json:"com.stacklok.mecatl.client_id"`
}

func validProtectedResourceScopes(scopes []string) bool {
	for _, scope := range scopes {
		if scope == "" {
			return false
		}
		for _, r := range scope {
			if r < 0x21 || r > 0x7e || r == ',' || r == '"' || r == '\\' {
				return false
			}
		}
	}
	return true
}

func protectedResourceChallenge() string {
	return "Bearer"
}
