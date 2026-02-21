// Package config loads and validates the Engram configuration.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for Engram.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Storage       StorageConfig       `yaml:"storage"`
	LLM           LLMConfig           `yaml:"llm"`
	Embedding     EmbeddingConfig     `yaml:"embedding"`
	NER           NERConfig           `yaml:"ner"`
	Consolidation ConsolidationConfig `yaml:"consolidation"`
	Identity      IdentityConfig      `yaml:"identity"`
}

type ServerConfig struct {
	Port   int    `yaml:"port"`
	APIKey string `yaml:"api_key"`
}

type StorageConfig struct {
	Path string `yaml:"path"`
}

type LLMConfig struct {
	Provider   string `yaml:"provider"`    // "anthropic" | "ollama" | "claude-code"
	Model      string `yaml:"model"`
	APIKey     string `yaml:"api_key"`
	BaseURL    string `yaml:"base_url"`    // for Ollama
	BinaryPath string `yaml:"binary_path"` // for claude-code: path to claude CLI (default: "claude")
}

type EmbeddingConfig struct {
	BaseURL string `yaml:"base_url"`
	Model   string `yaml:"model"`
	APIKey  string `yaml:"api_key"`
}

type NERConfig struct {
	Provider string `yaml:"provider"` // "spacy" | "ollama"
	Model    string `yaml:"model"`
	SpacyURL string `yaml:"spacy_url"`
}

type ConsolidationConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
}

type IdentityConfig struct {
	Name     string   `yaml:"name"`      // Bot's display name, e.g. "Bud"
	AuthorID string   `yaml:"author_id"` // Matches episode.author_id
	OwnerIDs []string `yaml:"owner_ids"` // Optional; if empty, no owner distinction
}

// Load reads config from a YAML file and applies environment variable overrides.
// Env vars use the ENGRAM_ prefix (e.g., ENGRAM_SERVER_API_KEY).
func Load(path string) (*Config, error) {
	cfg := defaults()

	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading config file %q: %w", path, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parsing config file: %w", err)
		}
	}

	applyEnv(cfg)

	if err := validate(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Port: 8080,
		},
		Storage: StorageConfig{
			Path: "./engram.db",
		},
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-6",
		},
		Embedding: EmbeddingConfig{
			BaseURL: "http://localhost:11434",
			Model:   "nomic-embed-text",
		},
		NER: NERConfig{
			Provider: "ollama",
			Model:    "qwen2.5:7b",
		},
		Consolidation: ConsolidationConfig{
			Enabled:  true,
			Interval: 15 * time.Minute,
		},
	}
}

// applyEnv overrides specific fields from ENGRAM_* environment variables.
func applyEnv(cfg *Config) {
	if v := env("ENGRAM_SERVER_API_KEY"); v != "" {
		cfg.Server.APIKey = v
	}
	if v := env("ENGRAM_STORAGE_PATH"); v != "" {
		cfg.Storage.Path = v
	}
	if v := env("ENGRAM_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := env("ENGRAM_LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := env("ENGRAM_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := env("ANTHROPIC_API_KEY"); v != "" && cfg.LLM.APIKey == "" {
		cfg.LLM.APIKey = v
	}
	if v := env("ENGRAM_LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := env("ENGRAM_LLM_BINARY_PATH"); v != "" {
		cfg.LLM.BinaryPath = v
	}
	if v := env("ENGRAM_EMBEDDING_BASE_URL"); v != "" {
		cfg.Embedding.BaseURL = v
	}
	if v := env("ENGRAM_EMBEDDING_MODEL"); v != "" {
		cfg.Embedding.Model = v
	}
	if v := env("ENGRAM_EMBEDDING_API_KEY"); v != "" {
		cfg.Embedding.APIKey = v
	}
	if v := env("ENGRAM_NER_PROVIDER"); v != "" {
		cfg.NER.Provider = v
	}
	if v := env("ENGRAM_NER_MODEL"); v != "" {
		cfg.NER.Model = v
	}
	if v := env("ENGRAM_NER_SPACY_URL"); v != "" {
		cfg.NER.SpacyURL = v
	}
	if v := env("ENGRAM_IDENTITY_NAME"); v != "" {
		cfg.Identity.Name = v
	}
	if v := env("ENGRAM_IDENTITY_AUTHOR_ID"); v != "" {
		cfg.Identity.AuthorID = v
	}
}

func validate(cfg *Config) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid server.port: %d", cfg.Server.Port)
	}
	provider := strings.ToLower(cfg.LLM.Provider)
	if provider != "anthropic" && provider != "ollama" && provider != "claude-code" {
		return fmt.Errorf("invalid llm.provider %q: must be \"anthropic\", \"ollama\", or \"claude-code\"", cfg.LLM.Provider)
	}
	nerProvider := strings.ToLower(cfg.NER.Provider)
	if nerProvider != "spacy" && nerProvider != "ollama" {
		return fmt.Errorf("invalid ner.provider %q: must be \"spacy\" or \"ollama\"", cfg.NER.Provider)
	}
	return nil
}

func env(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}
