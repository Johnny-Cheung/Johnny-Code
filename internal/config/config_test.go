package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDotEnvAndEnvironmentOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing config.env")
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	data := "JOHNNY_API_KEY=file-key\nJOHNNY_BASE_URL=https://relay.example/gateway\nJOHNNY_MODEL=relay-deepseek\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JOHNNY_API_KEY", "environment-key")
	t.Chdir(t.TempDir())
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIKey != "environment-key" || cfg.BaseURL != "https://relay.example/gateway" || cfg.Model != "relay-deepseek" {
		t.Fatal("配置未正确读取已有文件或遵循环境变量优先级")
	}
	if after, err := os.Getwd(); err != nil || after != before {
		t.Fatal("读取配置不应切换工作目录")
	}
	t.Setenv("JOHNNY_API_KEY", "")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "JOHNNY_API_KEY") {
		t.Fatal("显式空环境变量应按已有规则覆盖配置并报告缺失")
	}
}

func TestLoadJEVConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jev.env")
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API", "JOHNNY_JEV_BASE_URL", "JOHNNY_JEV_API_KEY", "JOHNNY_JEV_MODEL"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	data := "JOHNNY_API_KEY=file-key\nJOHNNY_BASE_URL=https://relay.example/gateway\nJOHNNY_MODEL=relay-model\n" +
		"JOHNNY_JEV_BASE_URL=https://jev.example\nJOHNNY_JEV_API_KEY=jev-file-key\nJOHNNY_JEV_MODEL=jev-latest\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JEV.BaseURL != "https://jev.example" || cfg.JEV.APIKey != "jev-file-key" || cfg.JEV.Model != "jev-latest" {
		t.Fatalf("JEV 配置读取错误：%+v", cfg.JEV)
	}
	if !cfg.JEV.Configured() {
		t.Fatal("JEV 配置应视为已填写")
	}
	t.Setenv("JOHNNY_JEV_BASE_URL", "https://env-jev.example")
	t.Setenv("JOHNNY_JEV_API_KEY", "jev-env-key")
	cfg, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JEV.BaseURL != "https://env-jev.example" || cfg.JEV.APIKey != "jev-env-key" {
		t.Fatalf("JEV 环境变量未覆盖文件：%+v", cfg.JEV)
	}
}

func TestLoadJEVConfigOptional(t *testing.T) {
	path := filepath.Join(t.TempDir(), "no-jev.env")
	for _, name := range []string{"JOHNNY_API_KEY", "JOHNNY_BASE_URL", "JOHNNY_MODEL", "JOHNNY_API", "JOHNNY_JEV_BASE_URL", "JOHNNY_JEV_API_KEY", "JOHNNY_JEV_MODEL"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	data := "JOHNNY_API_KEY=file-key\nJOHNNY_BASE_URL=https://relay.example\nJOHNNY_MODEL=relay-model\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JEV.Configured() {
		t.Fatal("未填写 JEV 配置时不应视为已配置")
	}
}

func TestLoadRejectsMissingConfigAndMessageURL(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("JOHNNY_API_KEY", "")
	t.Setenv("JOHNNY_BASE_URL", "")
	t.Setenv("JOHNNY_MODEL", "")
	if _, err := Load(".env"); err == nil || !strings.Contains(err.Error(), "JOHNNY_API_KEY") {
		t.Fatalf("应指出缺失配置，实际为 %v", err)
	}

	t.Setenv("JOHNNY_API_KEY", "test-key")
	t.Setenv("JOHNNY_MODEL", "relay-deepseek")
	t.Setenv("JOHNNY_BASE_URL", "https://relay.example/v1/messages")
	if _, err := Load(".env"); err == nil || !strings.Contains(err.Error(), "去掉末尾") {
		t.Fatalf("应提示填写 API 根地址，实际为 %v", err)
	}
}
