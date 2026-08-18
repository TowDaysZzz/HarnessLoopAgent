package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const openAICompatible = "openai-compatible"

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

func Load() (Config, error) {
	modelTimeout, err := durationEnv("MODEL_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	shutdownTimeout, err := durationEnv("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		HTTPAddr:        valueOrDefault("HTTP_ADDR", ":8080"),
		ShutdownTimeout: shutdownTimeout,
		Model: ModelConfig{
			Provider: valueOrDefault("MODEL_PROVIDER", openAICompatible),
			BaseURL:  valueOrDefault("MODEL_BASE_URL", "https://api.openai.com/v1"),
			Name:     strings.TrimSpace(os.Getenv("MODEL_NAME")),
			APIKey:   strings.TrimSpace(os.Getenv("MODEL_API_KEY")),
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
	if strings.TrimSpace(c.HTTPAddr) == "" {
		missing = append(missing, "HTTP_ADDR")
	}
	if strings.TrimSpace(c.Model.Name) == "" {
		missing = append(missing, "MODEL_NAME")
	}
	if strings.TrimSpace(c.Model.APIKey) == "" {
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

func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return d, nil
}

func valueOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
