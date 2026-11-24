package jev

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func responseJSON(choice, reason, model string) string {
	return `{"answers":{"decision":{"type":"choice","choice":"` + choice + `","confidence":0.9,"probabilities":{"allow":0.1,"ask":0.2,"deny":0.7}},"reason":{"type":"choice","choice":"` + reason + `","confidence":0.8,"probabilities":{` + `"read_only":0.8,"unknown":0.2` + `}}},"model":"` + model + `","usage":{"input_tokens":12,"output_tokens":3}}`
}

func TestClientClassify(t *testing.T) {
	var mu sync.Mutex
	var got request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		data, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(data, &got)
		mu.Unlock()
		_, _ = w.Write([]byte(responseJSON("allow", "read_only", "jev-1.14.0")))
	}))
	defer server.Close()
	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "test-key", Model: "jev-latest"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Classify(context.Background(), "git diff --stat HEAD", "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	if result.Decision != Allow || result.Reason != "read_only" || result.Model != "jev-1.14.0" {
		t.Fatalf("result = %+v", result)
	}
	if result.Probabilities["allow"] != 0.1 || result.InputTokens != 12 || result.OutputTokens != 3 {
		t.Fatalf("result = %+v", result)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.Model != "jev-latest" || got.State.Tool != "bash" || got.State.Command != "git diff --stat HEAD" || got.State.CWD != "/tmp/project" {
		t.Fatalf("request = %+v", got)
	}
	if got.Questions["decision"].Type != "choice" || len(got.Questions["decision"].Criteria) != 3 {
		t.Fatalf("decision question = %+v", got.Questions["decision"])
	}
}

func TestClientClassifyDecisions(t *testing.T) {
	for _, choice := range []string{"allow", "ask", "deny"} {
		t.Run(choice, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(responseJSON(choice, "unknown", "jev-1.14.0")))
			}))
			defer server.Close()
			client, err := NewClient(Options{BaseURL: server.URL, APIKey: "k"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := client.Classify(context.Background(), "ls", "/tmp")
			if err != nil {
				t.Fatal(err)
			}
			if string(result.Decision) != choice {
				t.Fatalf("decision = %q, want %q", result.Decision, choice)
			}
		})
	}
}

func TestClientRedactsSecrets(t *testing.T) {
	var mu sync.Mutex
	var command string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got request
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &got)
		mu.Lock()
		command = got.State.Command
		mu.Unlock()
		_, _ = w.Write([]byte(responseJSON("ask", "network", "jev-1.14.0")))
	}))
	defer server.Close()
	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	input := `curl -H "Authorization: Bearer supersecret123" --data token=abcd1234 https://example.com`
	if _, err := client.Classify(context.Background(), input, "/tmp"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Contains(command, "supersecret123") || strings.Contains(command, "abcd1234") {
		t.Fatalf("命令未脱敏：%s", command)
	}
	if !strings.Contains(command, "<redacted>") {
		t.Fatalf("命令缺少脱敏标记：%s", command)
	}
}

func TestClientSKRedaction(t *testing.T) {
	got, changed := redactCommand("echo sk-XNJu75F1qMBgulouDEv3yq2QDCXAtA4EFPbuBMBozvUq965m")
	if !changed || strings.Contains(got, "sk-XNJu") {
		t.Fatalf("redactCommand = %q, %v", got, changed)
	}
}

func TestClientHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad key", http.StatusUnauthorized)
	}))
	defer server.Close()
	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Classify(context.Background(), "ls", "/tmp"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientUnknownDecision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"answers":{"decision":{"type":"choice","choice":"maybe"}}}`))
	}))
	defer server.Close()
	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Classify(context.Background(), "ls", "/tmp"); err == nil || !strings.Contains(err.Error(), "未知") {
		t.Fatalf("err = %v", err)
	}
}

func TestClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = w.Write([]byte(responseJSON("allow", "read_only", "jev-1.14.0")))
	}))
	defer server.Close()
	client, err := NewClient(Options{BaseURL: server.URL, APIKey: "k", Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Classify(context.Background(), "ls", "/tmp"); err == nil {
		t.Fatal("超时未返回错误")
	}
}

func TestSystemOneEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com":              "https://api.example.com/v1/systemone",
		"https://api.example.com/":             "https://api.example.com/v1/systemone",
		"https://api.example.com/v1":           "https://api.example.com/v1/systemone",
		"https://api.example.com/v1/":          "https://api.example.com/v1/systemone",
		"https://api.example.com/v1/systemone": "https://api.example.com/v1/systemone",
		"https://api.example.com/gateway":      "https://api.example.com/gateway/v1/systemone",
		"http://127.0.0.1:8080/v1/systemone/":  "http://127.0.0.1:8080/v1/systemone",
	}
	for input, want := range cases {
		got, err := systemOneEndpoint(input)
		if err != nil {
			t.Fatalf("systemOneEndpoint(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("systemOneEndpoint(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"", "ftp://x", "https://", "https://x?y=1"} {
		if _, err := systemOneEndpoint(input); err == nil {
			t.Fatalf("systemOneEndpoint(%q) 应失败", input)
		}
	}
}

func TestNewClientRequiresConfig(t *testing.T) {
	if _, err := NewClient(Options{APIKey: "k"}); err == nil {
		t.Fatal("缺少地址应失败")
	}
	if _, err := NewClient(Options{BaseURL: "https://api.example.com"}); err == nil {
		t.Fatal("缺少 Key 应失败")
	}
}

func TestClientCommandTooLarge(t *testing.T) {
	client, err := NewClient(Options{BaseURL: "https://api.example.com", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	command := strings.Repeat("x", MaxCommandBytes+1)
	if _, err := client.Classify(context.Background(), command, "/tmp"); err == nil || !strings.Contains(err.Error(), "上限") {
		t.Fatalf("err = %v", err)
	}
}
