// Package config loads and validates the Engram configuration.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for Engram.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Storage       StorageConfig       `yaml:"storage"`
	// Deprecated: use CompressionLLM, ConsolidationLLM, and InferenceLLM instead.
	// If set, it acts as a fallback for any unset specific config.
	// A deprecation warning will be logged at startup.
	LLM             LLMConfig           `yaml:"llm"`
	CompressionLLM  LLMConfig           `yaml:"compression_llm"`
	ConsolidationLLM LLMConfig          `yaml:"consolidation_llm"`
	InferenceLLM    LLMConfig           `yaml:"inference_llm"`
	Embedding     EmbeddingConfig     `yaml:"embedding"`
	NER           NERConfig           `yaml:"ner"`
	Consolidation ConsolidationConfig `yaml:"consolidation"`
	Decay         DecayConfig         `yaml:"decay"`
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
	Enabled     bool          `yaml:"enabled"`
	Interval    time.Duration `yaml:"interval"`
	MinEpisodes int           `yaml:"min_episodes"` // N — minimum unconsolidated episodes to be eligible
	IdleTime    time.Duration `yaml:"idle_time"`    // T — time since last episode in a channel
	MaxBuffer   int           `yaml:"max_buffer"`   // M — unconsolidated count that forces a run immediately
}

type DecayConfig struct {
	Interval time.Duration `yaml:"interval"` // how often to run background decay (0 = disabled)
	Lambda   float64       `yaml:"lambda"`   // exponential decay coefficient
	Floor    float64       `yaml:"floor"`    // minimum activation level
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
		// LLM intentionally left empty — it is deprecated.
		// CompressionLLM/ConsolidationLLM/InferenceLLM are resolved via ResolvedXxxLLM().
		Embedding: EmbeddingConfig{
			BaseURL: "http://localhost:11434",
			Model:   "nomic-embed-text",
		},
		NER: NERConfig{
			Provider: "ollama",
			Model:    "qwen2.5:7b",
		},
		Consolidation: ConsolidationConfig{
			Enabled:     true,
			Interval:    15 * time.Minute,
			MinEpisodes: 10,
			IdleTime:    30 * time.Minute,
			MaxBuffer:   100,
		},
		Decay: DecayConfig{
			Interval: 1 * time.Hour,
			Lambda:   0.005,
			Floor:    0.01,
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
	// Deprecated llm.* env vars
	if v := env("ENGRAM_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := env("ENGRAM_LLM_MODEL"); v != "" {
		cfg.LLM.Model = v
	}
	if v := env("ENGRAM_LLM_API_KEY"); v != "" {
		cfg.LLM.APIKey = v
	}
	if v := env("ENGRAM_LLM_BASE_URL"); v != "" {
		cfg.LLM.BaseURL = v
	}
	if v := env("ENGRAM_LLM_BINARY_PATH"); v != "" {
		cfg.LLM.BinaryPath = v
	}

	// compression_llm env vars
	if v := env("ENGRAM_COMPRESSION_LLM_PROVIDER"); v != "" {
		cfg.CompressionLLM.Provider = v
	}
	if v := env("ENGRAM_COMPRESSION_LLM_MODEL"); v != "" {
		cfg.CompressionLLM.Model = v
	}
	if v := env("ENGRAM_COMPRESSION_LLM_API_KEY"); v != "" {
		cfg.CompressionLLM.APIKey = v
	}
	if v := env("ENGRAM_COMPRESSION_LLM_BASE_URL"); v != "" {
		cfg.CompressionLLM.BaseURL = v
	}
	if v := env("ENGRAM_COMPRESSION_LLM_BINARY_PATH"); v != "" {
		cfg.CompressionLLM.BinaryPath = v
	}

	// consolidation_llm env vars
	if v := env("ENGRAM_CONSOLIDATION_LLM_PROVIDER"); v != "" {
		cfg.ConsolidationLLM.Provider = v
	}
	if v := env("ENGRAM_CONSOLIDATION_LLM_MODEL"); v != "" {
		cfg.ConsolidationLLM.Model = v
	}
	if v := env("ENGRAM_CONSOLIDATION_LLM_API_KEY"); v != "" {
		cfg.ConsolidationLLM.APIKey = v
	}
	if v := env("ENGRAM_CONSOLIDATION_LLM_BASE_URL"); v != "" {
		cfg.ConsolidationLLM.BaseURL = v
	}
	if v := env("ENGRAM_CONSOLIDATION_LLM_BINARY_PATH"); v != "" {
		cfg.ConsolidationLLM.BinaryPath = v
	}

	// inference_llm env vars
	if v := env("ENGRAM_INFERENCE_LLM_PROVIDER"); v != "" {
		cfg.InferenceLLM.Provider = v
	}
	if v := env("ENGRAM_INFERENCE_LLM_MODEL"); v != "" {
		cfg.InferenceLLM.Model = v
	}
	if v := env("ENGRAM_INFERENCE_LLM_API_KEY"); v != "" {
		cfg.InferenceLLM.APIKey = v
	}
	if v := env("ENGRAM_INFERENCE_LLM_BASE_URL"); v != "" {
		cfg.InferenceLLM.BaseURL = v
	}
	if v := env("ENGRAM_INFERENCE_LLM_BINARY_PATH"); v != "" {
		cfg.InferenceLLM.BinaryPath = v
	}

	// ANTHROPIC_API_KEY is a global fallback for any anthropic-using config
	if v := env("ANTHROPIC_API_KEY"); v != "" {
		if cfg.LLM.APIKey == "" {
			cfg.LLM.APIKey = v
		}
		if cfg.CompressionLLM.APIKey == "" {
			cfg.CompressionLLM.APIKey = v
		}
		if cfg.ConsolidationLLM.APIKey == "" {
			cfg.ConsolidationLLM.APIKey = v
		}
		if cfg.InferenceLLM.APIKey == "" {
			cfg.InferenceLLM.APIKey = v
		}
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
	if v := env("ENGRAM_CONSOLIDATION_MIN_EPISODES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Consolidation.MinEpisodes = n
		}
	}
	if v := env("ENGRAM_CONSOLIDATION_IDLE_TIME"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			cfg.Consolidation.IdleTime = d
		}
	}
	if v := env("ENGRAM_CONSOLIDATION_MAX_BUFFER"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			cfg.Consolidation.MaxBuffer = n
		}
	}
}

func validateLLMProvider(provider, key string) error {
	p := strings.ToLower(provider)
	if p != "anthropic" && p != "ollama" && p != "claude-code" {
		return fmt.Errorf("invalid %s.provider %q: must be \"anthropic\", \"ollama\", or \"claude-code\"", key, provider)
	}
	return nil
}

func validate(cfg *Config) error {
	if cfg.Server.Port <= 0 || cfg.Server.Port > 65535 {
		return fmt.Errorf("invalid server.port: %d", cfg.Server.Port)
	}
	if cfg.LLM.Provider != "" {
		if err := validateLLMProvider(cfg.LLM.Provider, "llm"); err != nil {
			return err
		}
	}
	if cfg.CompressionLLM.Provider != "" {
		if err := validateLLMProvider(cfg.CompressionLLM.Provider, "compression_llm"); err != nil {
			return err
		}
	}
	if cfg.ConsolidationLLM.Provider != "" {
		if err := validateLLMProvider(cfg.ConsolidationLLM.Provider, "consolidation_llm"); err != nil {
			return err
		}
	}
	if cfg.InferenceLLM.Provider != "" {
		if err := validateLLMProvider(cfg.InferenceLLM.Provider, "inference_llm"); err != nil {
			return err
		}
	}
	nerProvider := strings.ToLower(cfg.NER.Provider)
	if nerProvider != "spacy" && nerProvider != "ollama" {
		return fmt.Errorf("invalid ner.provider %q: must be \"spacy\" or \"ollama\"", cfg.NER.Provider)
	}
	return nil
}

// LLMDeprecatedUsed reports whether the deprecated top-level llm key was set.
// If true, main should log a deprecation warning.
func (c *Config) LLMDeprecatedUsed() bool {
	return c.LLM.Provider != ""
}

// ResolvedCompressionLLM returns the effective LLM config for pyramid compression.
// Resolution order: compression_llm → llm (deprecated) → default (ollama/qwen2.5:7b).
func (c *Config) ResolvedCompressionLLM() LLMConfig {
	if c.CompressionLLM.Provider != "" {
		return c.CompressionLLM
	}
	if c.LLM.Provider != "" {
		return c.LLM
	}
	baseURL := c.Embedding.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	return LLMConfig{Provider: "ollama", Model: "qwen2.5:7b", BaseURL: baseURL}
}

// ResolvedConsolidationLLM returns the effective LLM config for engram/trace summarization.
// Resolution order: consolidation_llm → llm (deprecated) → default (anthropic/haiku).
func (c *Config) ResolvedConsolidationLLM() LLMConfig {
	if c.ConsolidationLLM.Provider != "" {
		return c.ConsolidationLLM
	}
	if c.LLM.Provider != "" {
		return c.LLM
	}
	return LLMConfig{Provider: "anthropic", Model: "claude-haiku-4-5-20251001", APIKey: c.LLM.APIKey}
}

// ResolvedInferenceLLM returns the effective LLM config for relationship/edge detection.
// Resolution order: inference_llm → llm (deprecated) → default (anthropic/haiku).
func (c *Config) ResolvedInferenceLLM() LLMConfig {
	if c.InferenceLLM.Provider != "" {
		return c.InferenceLLM
	}
	if c.LLM.Provider != "" {
		return c.LLM
	}
	return LLMConfig{Provider: "anthropic", Model: "claude-haiku-4-5-20251001", APIKey: c.LLM.APIKey}
}

func env(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}
