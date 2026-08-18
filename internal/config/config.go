package config

import (
	"errors"
	"fmt"
	"os"
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
	Model           ModelConfig
}

type ModelConfig struct {
	Provider string
	BaseURL  string
	Name     string
	APIKey   string
	Timeout  time.Duration
}

type fileConfig struct {
	HTTPAddr        string `yaml:"HTTP_ADDR"`
	ShutdownTimeout string `yaml:"SHUTDOWN_TIMEOUT"`
	ModelProvider   string `yaml:"MODEL_PROVIDER"`
	ModelBaseURL    string `yaml:"MODEL_BASE_URL"`
	ModelName       string `yaml:"MODEL_NAME"`
	ModelAPIKey     string `yaml:"MODEL_API_KEY"`
	ModelTimeout    string `yaml:"MODEL_TIMEOUT"`
}

func Load() (Config, error) {
	path, explicitlyConfigured := os.LookupEnv("CONFIG_FILE")
	path = strings.TrimSpace(path)
	if path == "" {
		path = defaultConfigFile
		explicitlyConfigured = false
	}

	raw := defaultFileConfig()
	if err := readYAML(path, &raw); err != nil {
		if explicitlyConfigured || !errors.Is(err, os.ErrNotExist) {
			return Config{}, err
		}
	}
	applyEnvironment(&raw)

	modelTimeout, err := parseDuration("MODEL_TIMEOUT", raw.ModelTimeout)
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
		Model: ModelConfig{
			Provider: strings.TrimSpace(raw.ModelProvider),
			BaseURL:  strings.TrimSpace(raw.ModelBaseURL),
			Name:     strings.TrimSpace(raw.ModelName),
			APIKey:   strings.TrimSpace(raw.ModelAPIKey),
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
		missing = append(missing, "MODEL_API_KEY")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if c.Model.Provider != openAICompatible {
		return fmt.Errorf("unsupported MODEL_PROVIDER %q", c.Model.Provider)
	}
	if c.Model.Timeout <= 0 {
		return errors.New("MODEL_TIMEOUT must be greater than zero")
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

func applyEnvironment(raw *fileConfig) {
	overrides := map[string]*string{
		"HTTP_ADDR":        &raw.HTTPAddr,
		"SHUTDOWN_TIMEOUT": &raw.ShutdownTimeout,
		"MODEL_PROVIDER":   &raw.ModelProvider,
		"MODEL_BASE_URL":   &raw.ModelBaseURL,
		"MODEL_NAME":       &raw.ModelName,
		"MODEL_API_KEY":    &raw.ModelAPIKey,
		"MODEL_TIMEOUT":    &raw.ModelTimeout,
	}
	for key, target := range overrides {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			*target = value
		}
	}
}

func parseDuration(key, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return d, nil
}
