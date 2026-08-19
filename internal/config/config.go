package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigFile = "config.yaml"
	openAICompatible  = "openai-compatible"
)

type Config struct {
	HTTPAddr        string
	ShutdownTimeout time.Duration
	ActiveModel     string
	Model           ModelConfig
}

type ModelConfig struct {
	Provider string
	BaseURL  string
	Name     string
	APIKey   string
	Timeout  time.Duration
}

type LoadOptions struct {
	Path  string
	Model string
}

type fileConfig struct {
	HTTPAddr        string                     `yaml:"HTTP_ADDR"`
	ShutdownTimeout string                     `yaml:"SHUTDOWN_TIMEOUT"`
	ActiveModel     string                     `yaml:"ACTIVE_MODEL"`
	Models          map[string]fileModelConfig `yaml:"MODELS"`

	// 保留原有单模型配置格式的兼容性。
	ModelProvider string `yaml:"MODEL_PROVIDER"`
	ModelBaseURL  string `yaml:"MODEL_BASE_URL"`
	ModelName     string `yaml:"MODEL_NAME"`
	ModelAPIKey   string `yaml:"MODEL_API_KEY"`
	ModelTimeout  string `yaml:"MODEL_TIMEOUT"`
}

type fileModelConfig struct {
	Provider string `yaml:"PROVIDER"`
	BaseURL  string `yaml:"BASE_URL"`
	Name     string `yaml:"MODEL_NAME"`
	APIKey   string `yaml:"API_KEY"`
	Timeout  string `yaml:"TIMEOUT"`
}

func Load() (Config, error) {
	return LoadWithOptions(LoadOptions{})
}

func LoadWithOptions(options LoadOptions) (Config, error) {
	path, explicitlyConfigured := configPath(options.Path)
	raw := defaultFileConfig()
	if err := readYAML(path, &raw); err != nil {
		if explicitlyConfigured || !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
	}

	applyServiceEnvironment(&raw)
	selectedName := firstNonEmpty(
		strings.TrimSpace(options.Model),
		strings.TrimSpace(os.Getenv("ACTIVE_MODEL")),
		strings.TrimSpace(raw.ActiveModel),
	)
	selected, err := selectModel(raw, selectedName)
	if err != nil {
		return Config{}, err
	}
	applyModelEnvironment(&selected)

	modelTimeout, err := parseDuration("MODEL_TIMEOUT", selected.Timeout)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := parseDuration("SHUTDOWN_TIMEOUT", raw.ShutdownTimeout)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:        strings.TrimSpace(raw.HTTPAddr),
		ShutdownTimeout: shutdownTimeout,
		ActiveModel:     selectedName,
		Model: ModelConfig{
			Provider: strings.TrimSpace(selected.Provider),
			BaseURL:  strings.TrimSpace(selected.BaseURL),
			Name:     strings.TrimSpace(selected.Name),
			APIKey:   strings.TrimSpace(selected.APIKey),
			Timeout:  modelTimeout,
		},
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	var missing []string
	if c.HTTPAddr == "" {
		missing = append(missing, "HTTP_ADDR")
	}
	if c.Model.Name == "" {
		missing = append(missing, "MODEL_NAME")
	}
	if c.Model.APIKey == "" {
		missing = append(missing, "MODEL_API_KEY/API_KEY")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if c.Model.Provider != openAICompatible {
		return fmt.Errorf("unsupported model provider %q", c.Model.Provider)
	}
	if c.Model.Timeout <= 0 {
		return errors.New("MODEL_TIMEOUT/TIMEOUT must be greater than zero")
	}
	if c.ShutdownTimeout <= 0 {
		return errors.New("SHUTDOWN_TIMEOUT must be greater than zero")
	}
	return nil
}

func defaultFileConfig() fileConfig {
	return fileConfig{
		HTTPAddr:        ":8080",
		ShutdownTimeout: "10s",
		ModelProvider:   openAICompatible,
		ModelBaseURL:    "https://api.openai.com/v1",
		ModelTimeout:    "60s",
	}
}

func configPath(optionPath string) (string, bool) {
	if path := strings.TrimSpace(optionPath); path != "" {
		return path, true
	}
	if path := strings.TrimSpace(os.Getenv("CONFIG_FILE")); path != "" {
		return path, true
	}
	return defaultConfigFile, false
}

func selectModel(raw fileConfig, selectedName string) (fileModelConfig, error) {
	if len(raw.Models) == 0 {
		if selectedName != "" {
			return fileModelConfig{}, fmt.Errorf("model profile %q requested, but MODELS is empty", selectedName)
		}
		return fileModelConfig{
			Provider: raw.ModelProvider,
			BaseURL:  raw.ModelBaseURL,
			Name:     raw.ModelName,
			APIKey:   raw.ModelAPIKey,
			Timeout:  raw.ModelTimeout,
		}, nil
	}
	if selectedName == "" {
		return fileModelConfig{}, errors.New("ACTIVE_MODEL is required when MODELS contains profiles")
	}
	selected, ok := raw.Models[selectedName]
	if !ok {
		names := make([]string, 0, len(raw.Models))
		for name := range raw.Models {
			names = append(names, name)
		}
		sort.Strings(names)
		return fileModelConfig{}, fmt.Errorf("unknown model profile %q; available profiles: %s", selectedName, strings.Join(names, ", "))
	}
	if strings.TrimSpace(selected.Provider) == "" {
		selected.Provider = openAICompatible
	}
	if strings.TrimSpace(selected.BaseURL) == "" {
		selected.BaseURL = "https://api.openai.com/v1"
	}
	if strings.TrimSpace(selected.Timeout) == "" {
		selected.Timeout = "60s"
	}
	return selected, nil
}

func readYAML(path string, target *fileConfig) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %q: %w", path, err)
	}
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode config file %q: %w", path, err)
	}
	return nil
}

func applyServiceEnvironment(raw *fileConfig) {
	applyEnvironment(map[string]*string{
		"HTTP_ADDR":        &raw.HTTPAddr,
		"SHUTDOWN_TIMEOUT": &raw.ShutdownTimeout,
	})
}

func applyModelEnvironment(raw *fileModelConfig) {
	applyEnvironment(map[string]*string{
		"MODEL_PROVIDER": &raw.Provider,
		"MODEL_BASE_URL": &raw.BaseURL,
		"MODEL_NAME":     &raw.Name,
		"MODEL_API_KEY":  &raw.APIKey,
		"MODEL_TIMEOUT":  &raw.Timeout,
	})
}

func applyEnvironment(overrides map[string]*string) {
	for key, target := range overrides {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			*target = value
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func parseDuration(key, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return d, nil
}
