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

func TestLoadRAGFromYAML(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
MODEL_NAME: test
MODEL_API_KEY: key
RAG:
  ENABLED: true
  BASE_URL: http://127.0.0.1:8899
  API_KEY: rag_test
  KB_IDS: [2]
  TIMEOUT: 12s
  TOP_K: 6
  STRATEGY_PROFILE: hybrid
`)

	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if !cfg.RAG.Enabled || cfg.RAG.BaseURL != "http://127.0.0.1:8899" || cfg.RAG.APIKey != "rag_test" {
		t.Fatalf("RAG config = %#v", cfg.RAG)
	}
	if len(cfg.RAG.KBIDs) != 1 || cfg.RAG.KBIDs[0] != 2 || cfg.RAG.TopK != 6 || cfg.RAG.Timeout != 12*time.Second {
		t.Fatalf("RAG config = %#v", cfg.RAG)
	}
}

func TestRAGEnvironmentOverridesYAML(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\n")
	t.Setenv("RAG_ENABLED", "true")
	t.Setenv("RAG_BASE_URL", "http://rag.example")
	t.Setenv("RAG_API_KEY", "rag_env")
	t.Setenv("RAG_KB_IDS", "2,3")
	t.Setenv("RAG_TOP_K", "7")

	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if !cfg.RAG.Enabled || cfg.RAG.TopK != 7 || len(cfg.RAG.KBIDs) != 2 || cfg.RAG.KBIDs[1] != 3 {
		t.Fatalf("RAG environment overrides = %#v", cfg.RAG)
	}
}

func TestEnabledRAGRequiresConnectionSettings(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\nRAG:\n  ENABLED: true\n")

	_, err := LoadWithOptions(LoadOptions{Path: path})
	if err == nil || !strings.Contains(err.Error(), "RAG_BASE_URL") || !strings.Contains(err.Error(), "RAG_API_KEY") {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
}

func TestRAGRejectsInvalidEnvironment(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\n")
	t.Setenv("RAG_ENABLED", "maybe")

	_, err := LoadWithOptions(LoadOptions{Path: path})
	if err == nil || !strings.Contains(err.Error(), "RAG_ENABLED") {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
}

func TestLoadHarnessRuntimeAndGroundingConfig(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
MODEL_NAME: test
MODEL_API_KEY: key
AGENT:
  RUN_TIMEOUT: 45s
  TOOL_TIMEOUT: 8s
  MAX_ITERATIONS: 4
  MAX_MODEL_CALLS: 5
  MAX_TOOL_CALLS: 6
  MAX_REPAIR_ATTEMPTS: 1
  MAX_OUTPUT_TOKENS: 1200
RESILIENCE:
  MODEL_MAX_ATTEMPTS: 2
  RAG_MAX_ATTEMPTS: 4
  RETRY_BASE_DELAY: 100ms
  RETRY_MAX_DELAY: 1s
  MODEL_MAX_CONCURRENCY: 3
  RAG_MAX_CONCURRENCY: 7
  CIRCUIT_FAILURE_THRESHOLD: 4
  CIRCUIT_OPEN_TIMEOUT: 20s
GROUNDING:
  REQUIRE_RAG_FOR_NOTE_QUERY: true
  REQUIRE_EVIDENCE_GATE: true
  REQUIRE_CITATION_CHECK: true
  MIN_RESULTS: 2
  MIN_TOP_SCORE: 0.7
  MIN_ITEM_SCORE: 0.5
  REQUIRE_COMPLETE_CITATION: true
  MAX_CONTEXT_CHARS: 12000
  REJECT_PROMPT_INJECTION: true
`)
	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.RunTimeout != 45*time.Second || cfg.Agent.MaxModelCalls != 5 || cfg.Resilience.RAGMaxAttempts != 4 {
		t.Fatalf("runtime config = %#v %#v", cfg.Agent, cfg.Resilience)
	}
	if !cfg.Grounding.RequireEvidenceGate || cfg.Grounding.MinResults != 2 || cfg.Grounding.MinTopScore != 0.7 {
		t.Fatalf("grounding config = %#v", cfg.Grounding)
	}
}

func TestLoadIntentRoutingConfigFromYAMLAndEnvironment(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
MODEL_NAME: test
MODEL_API_KEY: key
AGENT:
  ENABLE_INTENT_ROUTING: false
  ENABLE_LEGACY_ROUTING_FALLBACK: false
  INTENT_COMPLEX_THRESHOLD: 180
  INTENT_MIN_WRITE_CONFIDENCE: 0.8
  NOTE_DRAFT_TTL: 12h
`)
	t.Setenv("ENABLE_INTENT_ROUTING", "true")
	t.Setenv("INTENT_COMPLEX_THRESHOLD", "150")
	t.Setenv("NOTE_DRAFT_TTL", "36h")

	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if !cfg.Agent.EnableIntentRouting || cfg.Agent.EnableLegacyRoutingFallback {
		t.Fatalf("routing switches = %#v", cfg.Agent)
	}
	if cfg.Agent.IntentComplexThreshold != 150 || cfg.Agent.IntentMinWriteConfidence != 0.8 || cfg.Agent.NoteDraftTTL != 36*time.Hour {
		t.Fatalf("routing settings = %#v", cfg.Agent)
	}
}

func TestIntentRoutingConfigDefaults(t *testing.T) {
	clearOverrides(t)
	cfg, err := LoadWithOptions(LoadOptions{Path: writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Agent.EnableIntentRouting || !cfg.Agent.EnableLegacyRoutingFallback || cfg.Agent.IntentComplexThreshold != 120 || cfg.Agent.IntentMinWriteConfidence != 0.95 || cfg.Agent.NoteDraftTTL != 24*time.Hour {
		t.Fatalf("routing defaults = %#v", cfg.Agent)
	}
}

func TestMemoryConfigDefaultsAreSafeAndDisabled(t *testing.T) {
	clearOverrides(t)
	cfg, err := LoadWithOptions(LoadOptions{Path: writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\n")})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Memory.Enabled || cfg.Memory.RAGEnabled || cfg.Memory.ProjectionEnabled || cfg.Memory.WorkflowPilotEnabled {
		t.Fatalf("memory switches must default off: %+v", cfg.Memory)
	}
	if cfg.Memory.RecallPageSize != 20 || cfg.Memory.MaxScanned != 200 || cfg.Memory.DefaultSessionTTL != 24*time.Hour || cfg.Memory.ProjectionMaxBackoff != 5*time.Minute {
		t.Fatalf("memory defaults=%+v", cfg.Memory)
	}
}

func TestLoadMemoryConfigAndEnvironment(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `MODEL_NAME: test
MODEL_API_KEY: key
DATABASE:
  ENABLED: true
  DSN: user:pass@tcp(localhost:3306)/test
MEMORY:
  ENABLED: true
  RAG_ENABLED: true
  PROJECTION_ENABLED: true
  WORKFLOW_PILOT_ENABLED: true
  DEFAULT_SESSION_TTL: 12h
  RECALL_TARGET: 5
  RECALL_PAGE_SIZE: 20
  MAX_SCANNED: 100
  MAX_BATCHES: 5
  MAX_CONTEXT_CHARS: 8000
  CONFLICT_THRESHOLD: 0.9
  PROJECTION_BATCH_SIZE: 25
  PROJECTION_BASE_BACKOFF: 2s
  PROJECTION_MAX_BACKOFF: 1m
  PROJECTION_MAX_ATTEMPTS: 4
  RAG_ENDPOINT: http://memory-rag.test
  RAG_TIMEOUT: 5s
  RAG_SERVICE_TOKEN: service-token
  OWNER_CLAIM_SECRET: 12345678901234567890123456789012
  PROJECTION_VERSION: v2
`)
	t.Setenv("MEMORY_RECALL_TARGET", "6")
	t.Setenv("MEMORY_WORKFLOW_PILOT_ENABLED", "false")
	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Memory.Enabled || !cfg.Memory.RAGEnabled || !cfg.Memory.ProjectionEnabled || cfg.Memory.WorkflowPilotEnabled || cfg.Memory.RecallTarget != 6 || cfg.Memory.RAGTimeout != 5*time.Second || cfg.Memory.ProjectionVersion != "v2" {
		t.Fatalf("memory config=%+v", cfg.Memory)
	}
}

func TestMemoryConfigRejectsInvalidFeatureCombinations(t *testing.T) {
	base := "MODEL_NAME: test\nMODEL_API_KEY: key\n"
	tests := map[string]string{
		"subfeature without memory": `MEMORY:
  RAG_ENABLED: true
`,
		"memory without database": `MEMORY:
  ENABLED: true
`,
		"projection without rag": `DATABASE:
  ENABLED: true
  DSN: mysql
MEMORY:
  ENABLED: true
  PROJECTION_ENABLED: true
`,
		"rag missing credentials": `DATABASE:
  ENABLED: true
  DSN: mysql
MEMORY:
  ENABLED: true
  RAG_ENABLED: true
`,
		"invalid scan": `MEMORY:
  MAX_SCANNED: 1
`,
	}
	for name, fragment := range tests {
		t.Run(name, func(t *testing.T) {
			clearOverrides(t)
			_, err := LoadWithOptions(LoadOptions{Path: writeConfig(t, base+fragment)})
			if err == nil {
				t.Fatal("expected invalid memory config")
			}
		})
	}
}

func TestLoadRejectsInvalidIntentRoutingConfig(t *testing.T) {
	for name, yaml := range map[string]string{
		"threshold":  "INTENT_COMPLEX_THRESHOLD: 0",
		"confidence": "INTENT_MIN_WRITE_CONFIDENCE: 1.1",
		"ttl":        "NOTE_DRAFT_TTL: 0s",
	} {
		t.Run(name, func(t *testing.T) {
			clearOverrides(t)
			path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\nAGENT:\n  "+yaml+"\n")
			if _, err := LoadWithOptions(LoadOptions{Path: path}); err == nil {
				t.Fatal("expected invalid routing configuration error")
			}
		})
	}
}

func TestLoadDatabaseAndContextConfig(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, `
MODEL_NAME: test
MODEL_API_KEY: key
DATABASE:
  ENABLED: true
  DSN: user:pass@tcp(127.0.0.1:3306)/note_agent?parseTime=true
  AUTO_MIGRATE: true
  MAX_OPEN_CONNS: 12
  MAX_IDLE_CONNS: 4
  CONN_MAX_LIFETIME: 3m
CONTEXT:
  MAX_INPUT_TOKENS: 16000
  MIN_RECENT_MESSAGES: 4
  MESSAGE_HISTORY_LIMIT: 80
`)
	cfg, err := LoadWithOptions(LoadOptions{Path: path})
	if err != nil {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
	if !cfg.Database.Enabled || !cfg.Database.AutoMigrate || cfg.Database.MaxOpenConns != 12 || cfg.Database.ConnMaxLifetime != 3*time.Minute {
		t.Fatalf("database config = %#v", cfg.Database)
	}
	if cfg.Context.MaxInputTokens != 16000 || cfg.Context.MinRecentMessages != 4 || cfg.Context.MessageHistoryLimit != 80 {
		t.Fatalf("context config = %#v", cfg.Context)
	}
}

func TestEnabledDatabaseRequiresDSN(t *testing.T) {
	clearOverrides(t)
	path := writeConfig(t, "MODEL_NAME: test\nMODEL_API_KEY: key\nDATABASE:\n  ENABLED: true\n")
	_, err := LoadWithOptions(LoadOptions{Path: path})
	if err == nil || !strings.Contains(err.Error(), "DATABASE_DSN") {
		t.Fatalf("LoadWithOptions() error = %v", err)
	}
}

func clearOverrides(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"CONFIG_FILE", "ACTIVE_MODEL", "HTTP_ADDR", "SHUTDOWN_TIMEOUT",
		"MODEL_PROVIDER", "MODEL_BASE_URL", "MODEL_NAME", "MODEL_API_KEY", "MODEL_TIMEOUT",
		"RAG_ENABLED", "RAG_BASE_URL", "RAG_API_KEY", "RAG_KB_IDS", "RAG_TIMEOUT", "RAG_TOP_K", "RAG_STRATEGY_PROFILE",
		"AGENT_RUN_TIMEOUT", "AGENT_TOOL_TIMEOUT", "AGENT_MAX_ITERATIONS", "AGENT_MAX_MODEL_CALLS", "AGENT_MAX_TOOL_CALLS", "AGENT_MAX_REPAIR_ATTEMPTS", "AGENT_MAX_OUTPUT_TOKENS",
		"ENABLE_MULTI_AGENT", "ENABLE_INTENT_ROUTING", "ENABLE_LEGACY_ROUTING_FALLBACK", "INTENT_COMPLEX_THRESHOLD", "INTENT_MIN_WRITE_CONFIDENCE", "NOTE_DRAFT_TTL",
		"MODEL_MAX_ATTEMPTS", "RAG_MAX_ATTEMPTS", "RETRY_BASE_DELAY", "RETRY_MAX_DELAY", "MODEL_MAX_CONCURRENCY", "RAG_MAX_CONCURRENCY", "CIRCUIT_FAILURE_THRESHOLD", "CIRCUIT_OPEN_TIMEOUT",
		"GROUNDING_REQUIRE_RAG_FOR_NOTE_QUERY", "GROUNDING_REQUIRE_EVIDENCE_GATE", "GROUNDING_REQUIRE_CITATION_CHECK", "GROUNDING_MIN_RESULTS", "GROUNDING_MIN_TOP_SCORE", "GROUNDING_MIN_ITEM_SCORE", "GROUNDING_REQUIRE_COMPLETE_CITATION", "GROUNDING_MAX_CONTEXT_CHARS", "GROUNDING_REJECT_PROMPT_INJECTION",
		"DATABASE_ENABLED", "DATABASE_DSN", "DATABASE_AUTO_MIGRATE", "DATABASE_MAX_OPEN_CONNS", "DATABASE_MAX_IDLE_CONNS", "DATABASE_CONN_MAX_LIFETIME",
		"CONTEXT_MAX_INPUT_TOKENS", "CONTEXT_MIN_RECENT_MESSAGES", "CONTEXT_MESSAGE_HISTORY_LIMIT",
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
