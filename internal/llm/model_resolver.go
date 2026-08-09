package llm

import (
	"fmt"
	"net/url"
	"strings"

	"johnnycode/internal/config"
)

var modelAliases = map[string]string{
	"haiku":  "claude-haiku-4-5-20251001",
	"sonnet": "claude-sonnet-4-6-20250514",
	"opus":   "claude-opus-4-6-20250514",
}

// isOfficialAnthropic 判断 provider 是否直连 Anthropic 官方 API。
// 模型别名（haiku/sonnet/opus）只对官方 Anthropic 有意义——第三方代理或
// 兼容层通常没有这些模型的渠道，把别名解析成官方模型名只会导致调用失败
// （如 model_not_found）。非官方 provider 下别名解析返回错误，由调用方
// （selectClient 等）回退到父模型。
func isOfficialAnthropic(cfg config.ProviderConfig) bool {
	if cfg.Protocol != "anthropic" {
		return false
	}
	if cfg.BaseURL == "" {
		return true
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "api.anthropic.com" || strings.HasSuffix(host, ".api.anthropic.com")
}

func NewModelResolver(baseCfg config.ProviderConfig) func(string) (Client, error) {
	return func(shortName string) (Client, error) {
		modelID, ok := modelAliases[shortName]
		if !ok {
			modelID = shortName
		} else if !isOfficialAnthropic(baseCfg) {
			return nil, fmt.Errorf("model alias %q is only available on the official Anthropic API; provider %q uses protocol=%s base_url=%q",
				shortName, baseCfg.Name, baseCfg.Protocol, baseCfg.BaseURL)
		}

		cfg := baseCfg
		cfg.Model = modelID
		return NewClient(&cfg, "")
	}
}

