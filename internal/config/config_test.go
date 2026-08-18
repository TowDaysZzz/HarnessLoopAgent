package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadRequiresModelCredentials(t *testing.T) {
	t.Setenv("MODEL_NAME", "")
	t.Setenv("MODEL_API_KEY", "")

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

func TestLoadValidConfiguration(t *testing.T) {
	t.Setenv("MODEL_NAME", "test-model")
	t.Setenv("MODEL_API_KEY", "test-key")
	t.Setenv("MODEL_TIMEOUT", "15s")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Model.Timeout != 15*time.Second {
		t.Fatalf("model timeout = %s, want 15s", cfg.Model.Timeout)
	}
	if cfg.Model.Provider != openAICompatible {
		t.Fatalf("provider = %q", cfg.Model.Provider)
	}
}

func TestLoadRejectsInvalidDuration(t *testing.T) {
	t.Setenv("MODEL_TIMEOUT", "later")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "MODEL_TIMEOUT") {
		t.Fatalf("Load() error = %v, want MODEL_TIMEOUT parse error", err)
	}
}
