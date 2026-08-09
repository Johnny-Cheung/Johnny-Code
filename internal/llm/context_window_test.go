package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"johnnycode/internal/config"
)




func TestResolveContextWindow_FetchSuccess(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"claude-sonnet-4-6","type":"model","display_name":"x","max_input_tokens":555000,"max_tokens":8192}`))
	}))
	defer srv.Close()

	cfg := &config.ProviderConfig{
		Protocol: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4-6",
	}
	ResolveContextWindow(context.Background(), cfg)

	if !strings.Contains(gotPath, "/v1/models/claude-sonnet-4-6") {
		t.Errorf("expected fetch to hit /v1/models/{model}, got path %q", gotPath)
	}
	if got := cfg.GetContextWindow(); got != 555000 {
		t.Fatalf("fetched window should be used: got %d, want 555000", got)
	}
}




func TestResolveContextWindow_FetchErrorDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	cfg := &config.ProviderConfig{
		Protocol: "anthropic", BaseURL: srv.URL, APIKey: "k", Model: "claude-sonnet-4-6",
	}
	ResolveContextWindow(context.Background(), cfg) 

	
	if got := cfg.GetContextWindow(); got != 200000 {
		t.Fatalf("on fetch error should fall back to mapping table: got %d, want 200000", got)
	}
}




func TestResolveContextWindow_UnreachableDegrades(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() 

	cfg := &config.ProviderConfig{
		Protocol: "anthropic", BaseURL: url, APIKey: "k", Model: "gpt-4o",
	}
	ResolveContextWindow(context.Background(), cfg)

	if got := cfg.GetContextWindow(); got != 128000 {
		t.Fatalf("unreachable endpoint should fall back: got %d, want 128000", got)
	}
}




func TestResolveContextWindow_NonAnthropicSkipped(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"max_input_tokens":999999}`))
	}))
	defer srv.Close()

	cfg := &config.ProviderConfig{
		Protocol: "openai-compat", BaseURL: srv.URL, APIKey: "k", Model: "gpt-4o",
	}
	ResolveContextWindow(context.Background(), cfg)

	if called {
		t.Error("non-anthropic provider must not trigger a /v1/models fetch")
	}
	if got := cfg.GetContextWindow(); got != 128000 {
		t.Fatalf("non-anthropic should use mapping table: got %d, want 128000", got)
	}
}



func TestResolveContextWindow_ConfigOverrideSkipsFetch(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte(`{"max_input_tokens":999999}`))
	}))
	defer srv.Close()

	cfg := &config.ProviderConfig{
		Protocol: "anthropic", BaseURL: srv.URL, APIKey: "k",
		Model: "claude-sonnet-4-6", ContextWindow: 4096,
	}
	ResolveContextWindow(context.Background(), cfg)

	if called {
		t.Error("explicit config window must skip the fetch")
	}
	if got := cfg.GetContextWindow(); got != 4096 {
		t.Fatalf("config window should win: got %d, want 4096", got)
	}
}
