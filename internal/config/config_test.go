package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadLegacyYAML(t *testing.T) {
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

	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if cfg.Model.Name != "yaml-model" || cfg.Model.APIKey != "yaml-key" {
		t.Fatalf("model config = %#v", cfg.Model)
	}
	if cfg.Model.Timeout != 15*time.Second || cfg.HTTPAddr != ":9090" {
		t.Fatalf("config = %#v", cfg)
	}
}

func TestLoadSelectedModelProfile(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
ACTIVE_MODEL: deepseek
MODELS:
  deepseek:
    BASE_URL: https://api.deepseek.com/v1
    MODEL_NAME: deepseek-chat
    API_KEY: deepseek-key
  qwen:
    BASE_URL: https://dashscope.aliyuncs.com/compatible-mode/v1
    MODEL_NAME: qwen-plus
    API_KEY: qwen-key
`)

	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if cfg.ActiveModel != "deepseek" || cfg.Model.Name != "deepseek-chat" {
		t.Fatalf("selected config = %#v", cfg)
	}
	if cfg.Model.Provider != openAICompatible || cfg.Model.Timeout != 60*time.Second {
		t.Fatalf("profile defaults not applied: %#v", cfg.Model)
	}
}

func TestOptionOverridesActiveModel(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
ACTIVE_MODEL: first
MODELS:
  first: {MODEL_NAME: first-model, API_KEY: first-key}
  second: {MODEL_NAME: second-model, API_KEY: second-key}
`)

	cfg, err := LoadWithOptions(LoadOptions{Path: path, Model: "second"})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if cfg.ActiveModel != "second" || cfg.Model.Name != "second-model" {
		t.Fatalf("selected config = %#v", cfg)
	}
}

func TestEnvironmentOverridesSelectedProfile(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
ACTIVE_MODEL: yaml
MODELS:
  yaml: {MODEL_NAME: yaml-model, API_KEY: yaml-key}
`)
	t.Setenv("MODEL_NAME", "env-model")
	t.Setenv("MODEL_API_KEY", "env-key")

	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if cfg.Model.Name != "env-model" || cfg.Model.APIKey != "env-key" {
		t.Fatalf("environment did not override profile: %#v", cfg.Model)
	}
}

func TestLoadRejectsUnknownProfile(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "ACTIVE_MODEL: missing\nMODELS:\n  known: {MODEL_NAME: test, API_KEY: key}\n")

	_, err := LoadWithOptions(LoadOptions{Path: path})
	if err == nil || !strings.Contains(err.Error(), "available profiles: known") {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
}

func TestLoadRequiresModelCredentials(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: \"\"\nMODEL_API_KEY: \"\"\n")

	_, err := LoadWithOptions(LoadOptions{Path: path})
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

	_, err := LoadWithOptions(LoadOptions{Path: path})
	if err == nil || !strings.Contains(err.Error(), "MODEL_TIMEOUT") {
		t.Fatalf("LoadWithOptions() error = %v, want MODEL_TIMEOUT parse error", err)
	}
}

func TestLoadRejectsUnknownYAMLField(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\nMODEL_NMAE: typo\n")

	_, err := LoadWithOptions(LoadOptions{Path: path})
	if err == nil || !strings.Contains(err.Error(), "MODEL_NMAE") {
		t.Fatalf("LoadWithOptions() error = %v, want unknown field error", err)
	}
}

func clearOverrides(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONFIG_FILE", "ACTIVE_MODEL", "HTTP_ADDR", "SHUTDOWN_TIMEOUT",
		"MODEL_PROVIDER", "MODEL_BASE_URL", "MODEL_NAME", "MODEL_API_KEY", "MODEL_TIMEOUT",
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
