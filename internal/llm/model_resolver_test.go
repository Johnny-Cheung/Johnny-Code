package llm

import (
	"strings"
	"testing"

	"johnnycode/internal/config"
)

func TestModelResolverAliasOfficialAnthropic(t *testing.T) {
	resolver := NewModelResolver(config.ProviderConfig{
		Name:     "official",
		Protocol: "anthropic",
		BaseURL:  "https://api.anthropic.com",
		Model:    "claude-sonnet-4-6",
		APIKey:   "sk-test",
	})
	for _, alias := range []string{"haiku", "sonnet", "opus"} {
		if _, err := resolver(alias); err != nil {
			t.Errorf("official anthropic: alias %q should resolve, got err: %v", alias, err)
		}
	}
}

func TestModelResolverAliasRejectedOnThirdParty(t *testing.T) {
	resolver := NewModelResolver(config.ProviderConfig{
		Name:     "thirdparty",
		Protocol: "anthropic",
		BaseURL:  "https://api.vectorcontrol.tech",
		Model:    "deepseek-chat",
		APIKey:   "sk-test",
	})
	if _, err := resolver("haiku"); err == nil {
		t.Fatal("third-party anthropic-compatible provider: alias should be rejected")
	} else if !strings.Contains(err.Error(), "official Anthropic") {
		t.Errorf("error should explain the alias limitation, got: %v", err)
	}
}

func TestModelResolverAliasRejectedOnOpenAI(t *testing.T) {
	resolver := NewModelResolver(config.ProviderConfig{
		Name:     "openai",
		Protocol: "openai",
		BaseURL:  "https://api.openai.com",
		Model:    "gpt-4o",
		APIKey:   "sk-test",
	})
	if _, err := resolver("opus"); err == nil {
		t.Fatal("openai provider: anthropic alias should be rejected")
	}
}

func TestModelResolverCustomModelNameAlwaysResolves(t *testing.T) {
	resolver := NewModelResolver(config.ProviderConfig{
		Name:     "thirdparty",
		Protocol: "anthropic",
		BaseURL:  "https://api.vectorcontrol.tech",
		Model:    "deepseek-chat",
		APIKey:   "sk-test",
	})
	if _, err := resolver("deepseek-chat"); err != nil {
		t.Fatalf("custom model name should resolve as-is, got err: %v", err)
	}
}
