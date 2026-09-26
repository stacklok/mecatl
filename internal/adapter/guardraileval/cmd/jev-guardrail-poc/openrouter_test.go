package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenRouterPoC_SyntheticRequestAndBoundedAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Error("missing synthetic authorization")
		}
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role, Content string
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model != openRouterModel || len(req.Messages) != 2 || !strings.Contains(req.Messages[1].Content, exampleCase().Content) {
			t.Error("missing bounded synthetic comparison payload")
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"assessment\":\"review\"}"}}]}`))
	}))
	defer srv.Close()
	client := &openRouterClient{key: "fixture-key", url: srv.URL, client: srv.Client()}
	assessment, err := client.assess(context.Background(), exampleCase())
	if err != nil || assessment != "review" {
		t.Fatalf("assessment=%q err=%v", assessment, err)
	}
}

func TestOpenRouterPoC_ProviderFailureDoesNotDisclose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"secret":"fixture-key","content":"private"}`))
	}))
	defer srv.Close()
	client := &openRouterClient{key: "fixture-key", url: srv.URL, client: srv.Client()}
	_, err := client.assess(context.Background(), exampleCase())
	if err == nil || strings.Contains(err.Error(), "fixture-key") || strings.Contains(err.Error(), "private") {
		t.Fatalf("unsafe failure handling: %v", err)
	}
}
