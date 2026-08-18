package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadFromYAML(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
HTTP_ADDR: ":9090"
MODEL_PROVIDER: "openai-compatible"
MODEL_BASE_URL: "https://model.example/v1"
MODEL_NAME: "yaml-model"
MODEL_API_KEY: "yaml-key"
MODEL_TIMEOUT: "15s"
SHUTDOWN_TIMEOUT: "5s"
`)
	t.Setenv("CONFIG_FILE", path)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Model.Name != "yaml-model" || cfg.Model.APIKey != "yaml-key" {
		t.Fatalf("model config = %#v", cfg.Model)
	}
	if cfg.Model.Timeout != 15*time.Second || cfg.HTTPAddr != ":9090" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestEnvironmentOverridesYAML(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: yaml-model\nMODEL_API_KEY: yaml-key\n")
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("MODEL_NAME", "env-model")
	t.Setenv("MODEL_API_KEY", "env-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Model.Name != "env-model" || cfg.Model.APIKey != "env-key" {
		t.Fatalf("environment did not override YAML: %#v", cfg.Model)
	}
}

func TestLoadRequiresModelCredentials(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: \"\"\nMODEL_API_KEY: \"\"\n")
	t.Setenv("CONFIG_FILE", path)

	_, err := Load()
	if err == nil {
		t.Fatal("expected missing credentials error")
	}
	for _, key := range []string{"MODEL_NAME", "MODEL_API_KEY"} {
		if !strings.Contains(err.Error(), key) {
			t.Fatalf("error %q does not name %s", err, key)
		}
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\nMODEL_TIMEOUT: later\n")
	t.Setenv("CONFIG_FILE", path)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MODEL_TIMEOUT") {
		t.Fatalf("Load() error = %v, want MODEL_TIMEOUT parse error", err)
	}
}

func TestLoadRejectsUnknownYAMLField(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\nMODEL_NMAE: typo\n")
	t.Setenv("CONFIG_FILE", path)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MODEL_NMAE") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func clearOverrides(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"HTTP_ADDR", "SHUTDOWN_TIMEOUT", "MODEL_PROVIDER", "MODEL_BASE_URL",
		"MODEL_NAME", "MODEL_API_KEY", "MODEL_TIMEOUT",
	} {
		t.Setenv(key, "")
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}
