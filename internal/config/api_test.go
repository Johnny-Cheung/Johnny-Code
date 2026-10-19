package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAPIAndModelSelection(t *testing.T) {
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("JOHNNY_API_KEY=key\nJOHNNY_BASE_URL=https://relay.example/v1\nJOHNNY_MODEL=file-model\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || cfg.API != "anthropic-messages" {
		t.Fatalf("旧配置应默认使用 Anthropic：%+v %v", cfg, err)
	}
	for _, api := range []string{"anthropic-messages", "openai-responses", "openai-chat-completions"} {
		t.Setenv("JOHNNY_API", api)
		t.Setenv("JOHNNY_MODEL", "environment-model")
		cfg, err := Load(path)
		if err != nil || cfg.API != api || cfg.Model != "environment-model" {
			t.Fatalf("环境变量选择失败：%+v %v", cfg, err)
		}
	}
	t.Setenv("JOHNNY_API", "invalid")
	api, model := "openai-responses", "flag-model"
	cfg, err = LoadWithOverrides(path, Overrides{API: &api, Model: &model})
	if err != nil || cfg.API != api || cfg.Model != model {
		t.Fatalf("命令行配置优先级错误：%+v %v", cfg, err)
	}
	for _, invalid := range []string{"", "openai", "typo"} {
		t.Setenv("JOHNNY_API", invalid)
		if _, err := Load(path); err == nil {
			t.Fatal("显式空值和未知 API 应报错")
		}
	}
	for _, suffix := range []string{"/v1/messages", "/v1/responses", "/v1/chat/completions"} {
		t.Setenv("JOHNNY_API", "openai-responses")
		t.Setenv("JOHNNY_BASE_URL", "https://relay.example"+suffix)
		if _, err := Load(path); err == nil {
			t.Fatal("完整接口路径不应被当作根地址")
		}
	}
}
