package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	APIKey  string
	BaseURL string
	Model   string
}

func Load(path string) (Config, error) {
	values, err := godotenv.Read(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("读取配置文件 %q 失败，请检查文件权限和格式", path)
	}

	get := func(name string) string {
		if value, ok := os.LookupEnv(name); ok {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(values[name])
	}

	cfg := Config{
		APIKey:  get("JOHNNY_API_KEY"),
		BaseURL: get("JOHNNY_BASE_URL"),
		Model:   get("JOHNNY_MODEL"),
	}

	var missing []string
	for _, item := range []struct{ name, value string }{
		{"JOHNNY_API_KEY", cfg.APIKey},
		{"JOHNNY_BASE_URL", cfg.BaseURL},
		{"JOHNNY_MODEL", cfg.Model},
	} {
		if item.value == "" {
			missing = append(missing, item.name)
		}
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("请在配置文件 %q 或环境变量中填写：%s", path, strings.Join(missing, "、"))
	}

	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return Config{}, errors.New("JOHNNY_BASE_URL 必须是有效的 http 或 https API 根地址")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Config{}, errors.New("JOHNNY_BASE_URL 应仅包含 API 根地址，凭据请填写在 JOHNNY_API_KEY 中")
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1/messages") {
		return Config{}, errors.New("JOHNNY_BASE_URL 请去掉末尾的 /v1/messages，保留中转站根路径")
	}

	return cfg, nil
}
