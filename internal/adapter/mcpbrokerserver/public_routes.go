package mcpbrokerserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

func publicHandler(grpcHandler, callbackHandler http.Handler, cfg PublicListenerConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			grpcHandler.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), cfg.CallbackTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		if status, message := validateBoundedPublicRoute(w, r, cfg.MaxCallbackBytes); status != 0 {
			http.Error(w, message, status)
			return
		}
		callbackHandler.ServeHTTP(w, r)
	})
}
func validateBoundedPublicRoute(w http.ResponseWriter, r *http.Request, maximum int64) (int, string) {
	if status, message := validatePublicRouteMethod(r); status != 0 {
		return status, message
	}
	if r.ContentLength > maximum {
		return http.StatusRequestEntityTooLarge, "public route body is too large"
	}
	if r.Body == nil {
		return 0, ""
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximum)
	stopClose := context.AfterFunc(r.Context(), func() { _ = r.Body.Close() })
	body, err := io.ReadAll(r.Body)
	stopClose()
	_ = r.Body.Close()
	if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
		return http.StatusRequestTimeout, "public request deadline exceeded"
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return http.StatusRequestEntityTooLarge, "public route body is too large"
		}
		return http.StatusBadRequest, "public route body is unreadable"
	}
	if r.ContentLength >= 0 && int64(len(body)) != r.ContentLength {
		return http.StatusBadRequest, "public route body is incomplete"
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return 0, ""
}
func mediaType(r *http.Request) string {
	value := r.Header.Get("Content-Type")
	typ, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	return strings.ToLower(typ)
}
func validatePublicRouteMethod(r *http.Request) (int, string) {
	contentType := mediaType(r)
	if status, message, matched := validateFixedToolHiveRoute(r, contentType); matched {
		return status, message
	}
	switch {
	case strings.HasPrefix(r.URL.Path, "/v1/mcp/broker/"):
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "ToolHive route method is not supported"
		}
		if r.Method == http.MethodPost && contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
			return http.StatusUnsupportedMediaType, "ToolHive route content type is not supported"
		}
	case strings.Contains(r.URL.Path, "/authorize"):
		if r.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, "OAuth authorize route requires GET"
		}
	case strings.Contains(r.URL.Path, "/token"):
		if r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "OAuth token route requires POST"
		}
		if contentType != "application/x-www-form-urlencoded" {
			return http.StatusUnsupportedMediaType, "OAuth token route requires form content"
		}
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "public route method is not supported"
		}
		if r.Method == http.MethodPost && contentType != "application/x-www-form-urlencoded" && contentType != "multipart/form-data" {
			return http.StatusUnsupportedMediaType, "public route content type is not supported"
		}
	}
	return 0, ""
}
func validateFixedToolHiveRoute(r *http.Request, contentType string) (int, string, bool) {
	switch r.URL.Path {
	case "/v1/mcp/broker/oauth/authorize":
		if r.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, "OAuth authorize route requires GET", true
		}
	case "/v1/mcp/broker/oauth/token":
		if r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "OAuth token route requires POST", true
		}
		if contentType != "application/x-www-form-urlencoded" {
			return http.StatusUnsupportedMediaType, "OAuth token route requires form content", true
		}
	case "/v1/mcp/broker/oauth/callback", "/v1/mcp/broker/.well-known/openid-configuration", "/v1/mcp/broker/.well-known/jwks.json", "/v1/mcp/broker/.well-known/oauth-protected-resource":
		if r.Method != http.MethodGet {
			return http.StatusMethodNotAllowed, "OAuth metadata and callback routes require GET", true
		}
	case "/v1/mcp/broker/mcp":
		if r.Method != http.MethodGet && r.Method != http.MethodPost {
			return http.StatusMethodNotAllowed, "MCP route method is not supported", true
		}
		if r.Method == http.MethodPost && contentType != "application/json" && !strings.HasSuffix(contentType, "+json") {
			return http.StatusUnsupportedMediaType, "MCP route content type is not supported", true
		}
	default:
		return 0, "", false
	}
	return 0, "", true
}
