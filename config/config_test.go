package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaults(t *testing.T) {
	cfg := defaults()

	if cfg.Server.Port != 8080 {
		t.Errorf("default port = %d, want 8080", cfg.Server.Port)
	}
	if cfg.Storage.Path != "./engram.db" {
		t.Errorf("default storage path = %q, want ./engram.db", cfg.Storage.Path)
	}
	// LLM is deprecated; check resolved per-function configs instead
	consolidationLLM := cfg.ResolvedConsolidationLLM()
	if consolidationLLM.Provider != "anthropic" {
		t.Errorf("default consolidation LLM provider = %q, want anthropic", consolidationLLM.Provider)
	}
	if consolidationLLM.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("default consolidation LLM model = %q, want claude-haiku-4-5-20251001", consolidationLLM.Model)
	}
	if cfg.Embedding.BaseURL != "http://localhost:11434" {
		t.Errorf("default embedding base URL = %q, want http://localhost:11434", cfg.Embedding.BaseURL)
	}
	if cfg.Embedding.Model != "nomic-embed-text" {
		t.Errorf("default embedding model = %q, want nomic-embed-text", cfg.Embedding.Model)
	}
	if cfg.NER.Provider != "ollama" {
		t.Errorf("default NER provider = %q, want ollama", cfg.NER.Provider)
	}
	if cfg.NER.Model != "qwen2.5:7b" {
		t.Errorf("default NER model = %q, want qwen2.5:7b", cfg.NER.Model)
	}
	if !cfg.Consolidation.Enabled {
		t.Error("default consolidation.enabled = false, want true")
	}
	if cfg.Consolidation.Interval != 15*time.Minute {
		t.Errorf("default consolidation interval = %v, want 15m", cfg.Consolidation.Interval)
	}
}

func TestLoadEmptyPath(t *testing.T) {
	// Clear env vars that could affect defaults
	t.Setenv("ENGRAM_SERVER_API_KEY", "")
	t.Setenv("ENGRAM_LLM_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error = %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("port = %d, want 8080", cfg.Server.Port)
	}
}

func TestLoadFromFile(t *testing.T) {
	yaml := `
server:
  port: 9090
  api_key: test-key
storage:
  path: /tmp/test.db
llm:
  provider: ollama
  model: llama3
  base_url: http://localhost:11434
embedding:
  base_url: http://localhost:11434
  model: nomic-embed-text
ner:
  provider: spacy
  spacy_url: http://localhost:8765
consolidation:
  enabled: false
  interval: 30m
identity:
  name: TestBot
  author_id: bot-123
`
	f := writeTempConfig(t, yaml)

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("port = %d, want 9090", cfg.Server.Port)
	}
	if cfg.Server.APIKey != "test-key" {
		t.Errorf("api_key = %q, want test-key", cfg.Server.APIKey)
	}
	if cfg.Storage.Path != "/tmp/test.db" {
		t.Errorf("storage path = %q, want /tmp/test.db", cfg.Storage.Path)
	}
	if cfg.LLM.Provider != "ollama" {
		t.Errorf("llm.provider = %q, want ollama", cfg.LLM.Provider)
	}
	if cfg.LLM.Model != "llama3" {
		t.Errorf("llm.model = %q, want llama3", cfg.LLM.Model)
	}
	if cfg.NER.Provider != "spacy" {
		t.Errorf("ner.provider = %q, want spacy", cfg.NER.Provider)
	}
	if cfg.NER.SpacyURL != "http://localhost:8765" {
		t.Errorf("ner.spacy_url = %q, want http://localhost:8765", cfg.NER.SpacyURL)
	}
	if cfg.Consolidation.Enabled {
		t.Error("consolidation.enabled = true, want false")
	}
	if cfg.Consolidation.Interval != 30*time.Minute {
		t.Errorf("consolidation.interval = %v, want 30m", cfg.Consolidation.Interval)
	}
	if cfg.Identity.Name != "TestBot" {
		t.Errorf("identity.name = %q, want TestBot", cfg.Identity.Name)
	}
	if cfg.Identity.AuthorID != "bot-123" {
		t.Errorf("identity.author_id = %q, want bot-123", cfg.Identity.AuthorID)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load("/nonexistent/path/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	f := writeTempConfig(t, "not: valid: yaml: [[[")
	_, err := Load(f)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestEnvOverrides(t *testing.T) {
	yaml := `
server:
  port: 8080
llm:
  provider: anthropic
  model: claude-sonnet-4-6
ner:
  provider: ollama
`
	f := writeTempConfig(t, yaml)

	t.Setenv("ENGRAM_SERVER_API_KEY", "env-api-key")
	t.Setenv("ENGRAM_STORAGE_PATH", "/env/path.db")
	t.Setenv("ENGRAM_LLM_PROVIDER", "anthropic")
	t.Setenv("ENGRAM_LLM_MODEL", "claude-opus-4-6")
	t.Setenv("ENGRAM_LLM_API_KEY", "env-llm-key")
	t.Setenv("ENGRAM_LLM_BASE_URL", "http://env-ollama:11434")
	t.Setenv("ENGRAM_EMBEDDING_BASE_URL", "http://env-embed:11434")
	t.Setenv("ENGRAM_EMBEDDING_MODEL", "env-embed-model")
	t.Setenv("ENGRAM_NER_PROVIDER", "ollama")
	t.Setenv("ENGRAM_NER_MODEL", "env-ner-model")
	t.Setenv("ENGRAM_NER_SPACY_URL", "http://env-spacy:8765")
	t.Setenv("ENGRAM_IDENTITY_NAME", "EnvBot")
	t.Setenv("ENGRAM_IDENTITY_AUTHOR_ID", "env-bot-456")

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Server.APIKey != "env-api-key" {
		t.Errorf("Server.APIKey = %q, want env-api-key", cfg.Server.APIKey)
	}
	if cfg.Storage.Path != "/env/path.db" {
		t.Errorf("Storage.Path = %q, want /env/path.db", cfg.Storage.Path)
	}
	if cfg.LLM.Model != "claude-opus-4-6" {
		t.Errorf("LLM.Model = %q, want claude-opus-4-6", cfg.LLM.Model)
	}
	if cfg.LLM.APIKey != "env-llm-key" {
		t.Errorf("LLM.APIKey = %q, want env-llm-key", cfg.LLM.APIKey)
	}
	if cfg.LLM.BaseURL != "http://env-ollama:11434" {
		t.Errorf("LLM.BaseURL = %q, want http://env-ollama:11434", cfg.LLM.BaseURL)
	}
	if cfg.Embedding.BaseURL != "http://env-embed:11434" {
		t.Errorf("Embedding.BaseURL = %q, want http://env-embed:11434", cfg.Embedding.BaseURL)
	}
	if cfg.Embedding.Model != "env-embed-model" {
		t.Errorf("Embedding.Model = %q, want env-embed-model", cfg.Embedding.Model)
	}
	if cfg.NER.Model != "env-ner-model" {
		t.Errorf("NER.Model = %q, want env-ner-model", cfg.NER.Model)
	}
	if cfg.NER.SpacyURL != "http://env-spacy:8765" {
		t.Errorf("NER.SpacyURL = %q, want http://env-spacy:8765", cfg.NER.SpacyURL)
	}
	if cfg.Identity.Name != "EnvBot" {
		t.Errorf("Identity.Name = %q, want EnvBot", cfg.Identity.Name)
	}
	if cfg.Identity.AuthorID != "env-bot-456" {
		t.Errorf("Identity.AuthorID = %q, want env-bot-456", cfg.Identity.AuthorID)
	}
}

func TestAnthropicAPIKeyFallback(t *testing.T) {
	yaml := `
server:
  port: 8080
llm:
  provider: anthropic
  model: claude-sonnet-4-6
ner:
  provider: ollama
`
	f := writeTempConfig(t, yaml)

	t.Setenv("ENGRAM_LLM_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "fallback-key")

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.LLM.APIKey != "fallback-key" {
		t.Errorf("LLM.APIKey = %q, want fallback-key (from ANTHROPIC_API_KEY)", cfg.LLM.APIKey)
	}
}

func TestAnthropicAPIKeyNoFallbackWhenSet(t *testing.T) {
	// ENGRAM_LLM_API_KEY takes precedence over ANTHROPIC_API_KEY
	yaml := `
server:
  port: 8080
llm:
  provider: anthropic
  model: claude-sonnet-4-6
ner:
  provider: ollama
`
	f := writeTempConfig(t, yaml)

	t.Setenv("ENGRAM_LLM_API_KEY", "explicit-key")
	t.Setenv("ANTHROPIC_API_KEY", "fallback-key")

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.LLM.APIKey != "explicit-key" {
		t.Errorf("LLM.APIKey = %q, want explicit-key (ENGRAM_LLM_API_KEY should take precedence)", cfg.LLM.APIKey)
	}
}

func TestValidateInvalidPort(t *testing.T) {
	tests := []struct {
		name string
		port int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too large", 65536},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaults()
			cfg.Server.Port = tc.port
			if err := validate(cfg); err == nil {
				t.Errorf("validate() = nil, want error for port %d", tc.port)
			}
		})
	}
}

func TestValidateValidPort(t *testing.T) {
	tests := []int{1, 80, 443, 8080, 65535}
	for _, port := range tests {
		cfg := defaults()
		cfg.Server.Port = port
		if err := validate(cfg); err != nil {
			t.Errorf("validate() error = %v for valid port %d", err, port)
		}
	}
}

func TestValidateInvalidLLMProvider(t *testing.T) {
	cfg := defaults()
	cfg.LLM.Provider = "openai"
	if err := validate(cfg); err == nil {
		t.Error("validate() = nil, want error for invalid LLM provider 'openai'")
	}
}

func TestValidateValidLLMProviders(t *testing.T) {
	providers := []string{"anthropic", "ollama", "claude-code", "Anthropic", "OLLAMA"}
	for _, provider := range providers {
		cfg := defaults()
		cfg.LLM.Provider = provider
		if err := validate(cfg); err != nil {
			t.Errorf("validate() error = %v for valid LLM provider %q", err, provider)
		}
	}
}

func TestValidateInvalidNERProvider(t *testing.T) {
	cfg := defaults()
	cfg.NER.Provider = "bert"
	if err := validate(cfg); err == nil {
		t.Error("validate() = nil, want error for invalid NER provider 'bert'")
	}
}

func TestValidateValidNERProviders(t *testing.T) {
	providers := []string{"spacy", "ollama", "SpaCy", "OLLAMA"}
	for _, provider := range providers {
		cfg := defaults()
		cfg.NER.Provider = provider
		if err := validate(cfg); err != nil {
			t.Errorf("validate() error = %v for valid NER provider %q", err, provider)
		}
	}
}

func TestEnvTrimSpace(t *testing.T) {
	// env() should trim whitespace from environment variables
	t.Setenv("ENGRAM_IDENTITY_NAME", "  SpacedBot  ")

	yaml := `
server:
  port: 8080
llm:
  provider: anthropic
ner:
  provider: ollama
`
	f := writeTempConfig(t, yaml)

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Identity.Name != "SpacedBot" {
		t.Errorf("Identity.Name = %q, want SpacedBot (trimmed)", cfg.Identity.Name)
	}
}

func TestFileMergesWithDefaults(t *testing.T) {
	// Only override port; other defaults should remain
	yaml := `
server:
  port: 9999
llm:
  provider: anthropic
ner:
  provider: ollama
`
	f := writeTempConfig(t, yaml)

	t.Setenv("ENGRAM_LLM_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")

	cfg, err := Load(f)
	if err != nil {
		t.Fatalf("Load error = %v", err)
	}

	if cfg.Server.Port != 9999 {
		t.Errorf("port = %d, want 9999", cfg.Server.Port)
	}
	if cfg.Embedding.Model != "nomic-embed-text" {
		t.Errorf("embedding.model = %q, want default nomic-embed-text", cfg.Embedding.Model)
	}
}

// writeTempConfig writes YAML content to a temp file and returns its path.
func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeTempConfig: %v", err)
	}
	return path
}
