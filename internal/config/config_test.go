package config

import (
	"strings"
	"testing"
)

func cleanLLMEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://test")
	t.Setenv("LLM_PROVIDER", "")
	t.Setenv("LLM_MODEL", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("LLM_MAX_ATTEMPTS", "")
}

func TestLoadOpenRouterConfiguration(t *testing.T) {
	cleanLLMEnv(t)
	t.Setenv("LLM_PROVIDER", ProviderOpenRouter)
	t.Setenv("LLM_MODEL", "google/test-model")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("OPENROUTER_SITE_URL", "https://example.test")
	t.Setenv("OPENROUTER_APP_NAME", "Harness Test")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLMProvider != ProviderOpenRouter || cfg.Model != "google/test-model" {
		t.Fatalf("unexpected provider/model: %q %q", cfg.LLMProvider, cfg.Model)
	}
	if cfg.OpenRouterAPIKey != "or-key" || cfg.OpenRouterSiteURL != "https://example.test" || cfg.OpenRouterAppName != "Harness Test" {
		t.Fatalf("unexpected OpenRouter config: %+v", cfg)
	}
}

func TestLoadInfersOpenRouterFromKey(t *testing.T) {
	cleanLLMEnv(t)
	t.Setenv("LLM_MODEL", "openai/test-model")
	t.Setenv("OPENROUTER_API_KEY", "or-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLMProvider != ProviderOpenRouter {
		t.Fatalf("provider = %q, want openrouter", cfg.LLMProvider)
	}
}

func TestLoadAnthropicConfigurationDoesNotRequireOpenRouterKey(t *testing.T) {
	cleanLLMEnv(t)
	t.Setenv("LLM_PROVIDER", ProviderAnthropic)
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Model == "" || cfg.LLMProvider != ProviderAnthropic {
		t.Fatalf("unexpected Anthropic config: %+v", cfg)
	}
}

func TestLoadRequiresSelectedProviderKey(t *testing.T) {
	cleanLLMEnv(t)
	t.Setenv("LLM_PROVIDER", ProviderOpenRouter)
	t.Setenv("LLM_MODEL", "google/test-model")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsUnsupportedProvider(t *testing.T) {
	cleanLLMEnv(t)
	t.Setenv("LLM_PROVIDER", "other")
	t.Setenv("LLM_MODEL", "some-model")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "unsupported LLM_PROVIDER") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadCapsConfiguredAttemptsAtThree(t *testing.T) {
	cleanLLMEnv(t)
	t.Setenv("LLM_PROVIDER", ProviderOpenRouter)
	t.Setenv("LLM_MODEL", "google/test-model")
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	t.Setenv("LLM_MAX_ATTEMPTS", "4")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "between 1 and 3") {
		t.Fatalf("error = %v", err)
	}
}
