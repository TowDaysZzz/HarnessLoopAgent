package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
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
	RAG             RAGConfig
	Agent           AgentConfig
	Resilience      ResilienceConfig
	Grounding       GroundingConfig
	Database        DatabaseConfig
	Context         ContextConfig
	Auth            AuthConfig
	Note            NoteConfig
	Memory          MemoryConfig
	Reminder        ReminderConfig
	Skills          SkillConfig
}

type ModelConfig struct {
	Provider string
	BaseURL  string
	Name     string
	APIKey   string
	Timeout  time.Duration
}

type RAGConfig struct {
	Enabled         bool
	BaseURL         string
	APIKey          string
	KBIDs           []uint64
	Timeout         time.Duration
	TopK            int
	StrategyProfile string
}

type AgentConfig struct {
	EnableMultiAgent            bool
	EnableIntentRouting         bool
	EnableLegacyRoutingFallback bool
	IntentComplexThreshold      int
	IntentMinWriteConfidence    float64
	NoteDraftTTL                time.Duration
	RunTimeout                  time.Duration
	ToolTimeout                 time.Duration
	MaxIterations               int
	MaxModelCalls               int
	MaxToolCalls                int
	MaxRepairAttempts           int
	MaxOutputTokens             int
}

type ResilienceConfig struct {
	ModelMaxAttempts        int
	RAGMaxAttempts          int
	RetryBaseDelay          time.Duration
	RetryMaxDelay           time.Duration
	ModelMaxConcurrency     int
	RAGMaxConcurrency       int
	CircuitFailureThreshold int
	CircuitOpenTimeout      time.Duration
}

type GroundingConfig struct {
	RequireRAGForNoteQuery  bool
	RequireEvidenceGate     bool
	RequireCitationCheck    bool
	MinResults              int
	MinTopScore             float64
	MinItemScore            float64
	RequireCompleteCitation bool
	MaxContextChars         int
	RejectPromptInjection   bool
}

type DatabaseConfig struct {
	Enabled         bool
	DSN             string
	AutoMigrate     bool
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
}

type ContextConfig struct {
	MaxInputTokens      int
	MinRecentMessages   int
	MessageHistoryLimit int
}

type AuthConfig struct {
	Enabled       bool
	SessionSecret string
	SessionTTL    time.Duration
	CookieName    string
	CookieSecure  bool
}

type NoteConfig struct {
	Enabled bool
	KBID    uint64
}

type MemoryConfig struct {
	Enabled                     bool
	RAGEnabled                  bool
	ProjectionEnabled           bool
	WorkflowPilotEnabled        bool
	RecallMode                  string
	StructuredPlanMinConfidence float64
	MaxRecallSelectors          int
	MaxExactCandidates          int
	MaxCandidateTextChars       int
	MaxLLMResponseBytes         int
	MaxLLMRepairAttempts        int
	DefaultSessionTTL           time.Duration
	RecallTarget                int
	RecallPageSize              int
	MaxScanned                  int
	MaxBatches                  int
	MaxContextChars             int
	ConflictThreshold           float64
	ProjectionBatchSize         int
	ProjectionBaseBackoff       time.Duration
	ProjectionMaxBackoff        time.Duration
	ProjectionMaxAttempts       int
	RAGEndpoint                 string
	RAGTimeout                  time.Duration
	RAGServiceToken             string
	OwnerClaimSecret            string
	ProjectionVersion           string
}

type ReminderConfig struct {
	Enabled, WorkflowPilotEnabled, DispatcherEnabled, WorkerEnabled        bool
	BatchSize, MaxBatches, MaxAttempts                                     int
	LeaseDuration, Interval, MaxHorizon, RetryBaseBackoff, RetryMaxBackoff time.Duration
	Timezone, ProductionDeliveryAdapter                                    string
}

type SkillConfig struct {
	Enabled, DailyReviewEnabled                                                     bool
	Timezone                                                                        string
	MaxLookbackDays, MaxChatMessages, PerSessionMessages, MaxNotes, MaxContextChars int
	MaxSteps, MaxModelCalls, MaxToolCalls, MaxRepairAttempts                        int
	CacheTTL, CacheLease, CacheWait                                                 time.Duration
	SkillVersion, SchemaVersion, PromptPolicyVersion                                string
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
	RAG             fileRAGConfig              `yaml:"RAG"`
	Agent           fileAgentConfig            `yaml:"AGENT"`
	Resilience      fileResilienceConfig       `yaml:"RESILIENCE"`
	Grounding       fileGroundingConfig        `yaml:"GROUNDING"`
	Database        fileDatabaseConfig         `yaml:"DATABASE"`
	Context         fileContextConfig          `yaml:"CONTEXT"`
	Auth            fileAuthConfig             `yaml:"AUTH"`
	Note            fileNoteConfig             `yaml:"NOTE"`
	Memory          fileMemoryConfig           `yaml:"MEMORY"`
	Reminder        fileReminderConfig         `yaml:"REMINDER"`
	Skills          fileSkillConfig            `yaml:"SKILLS"`

	// 保留原有单模型配置格式的兼容性。
	ModelProvider string `yaml:"MODEL_PROVIDER"`
	ModelBaseURL  string `yaml:"MODEL_BASE_URL"`
	ModelName     string `yaml:"MODEL_NAME"`
	ModelAPIKey   string `yaml:"MODEL_API_KEY"`
	ModelTimeout  string `yaml:"MODEL_TIMEOUT"`
}

type fileAgentConfig struct {
	EnableMultiAgent            bool    `yaml:"ENABLE_MULTI_AGENT"`
	EnableIntentRouting         bool    `yaml:"ENABLE_INTENT_ROUTING"`
	EnableLegacyRoutingFallback bool    `yaml:"ENABLE_LEGACY_ROUTING_FALLBACK"`
	IntentComplexThreshold      int     `yaml:"INTENT_COMPLEX_THRESHOLD"`
	IntentMinWriteConfidence    float64 `yaml:"INTENT_MIN_WRITE_CONFIDENCE"`
	NoteDraftTTL                string  `yaml:"NOTE_DRAFT_TTL"`
	RunTimeout                  string  `yaml:"RUN_TIMEOUT"`
	ToolTimeout                 string  `yaml:"TOOL_TIMEOUT"`
	MaxIterations               int     `yaml:"MAX_ITERATIONS"`
	MaxModelCalls               int     `yaml:"MAX_MODEL_CALLS"`
	MaxToolCalls                int     `yaml:"MAX_TOOL_CALLS"`
	MaxRepairAttempts           int     `yaml:"MAX_REPAIR_ATTEMPTS"`
	MaxOutputTokens             int     `yaml:"MAX_OUTPUT_TOKENS"`
}

type fileSkillConfig struct {
	Enabled             bool   `yaml:"ENABLED"`
	DailyReviewEnabled  bool   `yaml:"DAILY_REVIEW_ENABLED"`
	Timezone            string `yaml:"TIMEZONE"`
	MaxLookbackDays     int    `yaml:"MAX_LOOKBACK_DAYS"`
	MaxChatMessages     int    `yaml:"MAX_CHAT_MESSAGES"`
	PerSessionMessages  int    `yaml:"PER_SESSION_MESSAGES"`
	MaxNotes            int    `yaml:"MAX_NOTES"`
	MaxContextChars     int    `yaml:"MAX_CONTEXT_CHARS"`
	MaxSteps            int    `yaml:"MAX_STEPS"`
	MaxModelCalls       int    `yaml:"MAX_MODEL_CALLS"`
	MaxToolCalls        int    `yaml:"MAX_TOOL_CALLS"`
	MaxRepairAttempts   int    `yaml:"MAX_REPAIR_ATTEMPTS"`
	CacheTTL            string `yaml:"CACHE_TTL"`
	CacheLease          string `yaml:"CACHE_LEASE"`
	CacheWait           string `yaml:"CACHE_WAIT"`
	SkillVersion        string `yaml:"SKILL_VERSION"`
	SchemaVersion       string `yaml:"SCHEMA_VERSION"`
	PromptPolicyVersion string `yaml:"PROMPT_POLICY_VERSION"`
}

type fileResilienceConfig struct {
	ModelMaxAttempts        int    `yaml:"MODEL_MAX_ATTEMPTS"`
	RAGMaxAttempts          int    `yaml:"RAG_MAX_ATTEMPTS"`
	RetryBaseDelay          string `yaml:"RETRY_BASE_DELAY"`
	RetryMaxDelay           string `yaml:"RETRY_MAX_DELAY"`
	ModelMaxConcurrency     int    `yaml:"MODEL_MAX_CONCURRENCY"`
	RAGMaxConcurrency       int    `yaml:"RAG_MAX_CONCURRENCY"`
	CircuitFailureThreshold int    `yaml:"CIRCUIT_FAILURE_THRESHOLD"`
	CircuitOpenTimeout      string `yaml:"CIRCUIT_OPEN_TIMEOUT"`
}

type fileGroundingConfig struct {
	RequireRAGForNoteQuery  bool    `yaml:"REQUIRE_RAG_FOR_NOTE_QUERY"`
	RequireEvidenceGate     bool    `yaml:"REQUIRE_EVIDENCE_GATE"`
	RequireCitationCheck    bool    `yaml:"REQUIRE_CITATION_CHECK"`
	MinResults              int     `yaml:"MIN_RESULTS"`
	MinTopScore             float64 `yaml:"MIN_TOP_SCORE"`
	MinItemScore            float64 `yaml:"MIN_ITEM_SCORE"`
	RequireCompleteCitation bool    `yaml:"REQUIRE_COMPLETE_CITATION"`
	MaxContextChars         int     `yaml:"MAX_CONTEXT_CHARS"`
	RejectPromptInjection   bool    `yaml:"REJECT_PROMPT_INJECTION"`
}

type fileDatabaseConfig struct {
	Enabled         bool   `yaml:"ENABLED"`
	DSN             string `yaml:"DSN"`
	AutoMigrate     bool   `yaml:"AUTO_MIGRATE"`
	MaxOpenConns    int    `yaml:"MAX_OPEN_CONNS"`
	MaxIdleConns    int    `yaml:"MAX_IDLE_CONNS"`
	ConnMaxLifetime string `yaml:"CONN_MAX_LIFETIME"`
}

type fileContextConfig struct {
	MaxInputTokens      int `yaml:"MAX_INPUT_TOKENS"`
	MinRecentMessages   int `yaml:"MIN_RECENT_MESSAGES"`
	MessageHistoryLimit int `yaml:"MESSAGE_HISTORY_LIMIT"`
}

type fileAuthConfig struct {
	Enabled       bool   `yaml:"ENABLED"`
	SessionSecret string `yaml:"SESSION_SECRET"`
	SessionTTL    string `yaml:"SESSION_TTL"`
	CookieName    string `yaml:"COOKIE_NAME"`
	CookieSecure  bool   `yaml:"COOKIE_SECURE"`
}

type fileNoteConfig struct {
	Enabled bool   `yaml:"ENABLED"`
	KBID    uint64 `yaml:"KB_ID"`
}

type fileMemoryConfig struct {
	Enabled                     bool    `yaml:"ENABLED"`
	RAGEnabled                  bool    `yaml:"RAG_ENABLED"`
	ProjectionEnabled           bool    `yaml:"PROJECTION_ENABLED"`
	WorkflowPilotEnabled        bool    `yaml:"WORKFLOW_PILOT_ENABLED"`
	RecallMode                  string  `yaml:"RECALL_MODE"`
	StructuredPlanMinConfidence float64 `yaml:"STRUCTURED_PLAN_MIN_CONFIDENCE"`
	MaxRecallSelectors          int     `yaml:"MAX_RECALL_SELECTORS"`
	MaxExactCandidates          int     `yaml:"MAX_EXACT_CANDIDATES"`
	MaxCandidateTextChars       int     `yaml:"MAX_CANDIDATE_TEXT_CHARS"`
	MaxLLMResponseBytes         int     `yaml:"MAX_LLM_RESPONSE_BYTES"`
	MaxLLMRepairAttempts        int     `yaml:"MAX_LLM_REPAIR_ATTEMPTS"`
	DefaultSessionTTL           string  `yaml:"DEFAULT_SESSION_TTL"`
	RecallTarget                int     `yaml:"RECALL_TARGET"`
	RecallPageSize              int     `yaml:"RECALL_PAGE_SIZE"`
	MaxScanned                  int     `yaml:"MAX_SCANNED"`
	MaxBatches                  int     `yaml:"MAX_BATCHES"`
	MaxContextChars             int     `yaml:"MAX_CONTEXT_CHARS"`
	ConflictThreshold           float64 `yaml:"CONFLICT_THRESHOLD"`
	ProjectionBatchSize         int     `yaml:"PROJECTION_BATCH_SIZE"`
	ProjectionBaseBackoff       string  `yaml:"PROJECTION_BASE_BACKOFF"`
	ProjectionMaxBackoff        string  `yaml:"PROJECTION_MAX_BACKOFF"`
	ProjectionMaxAttempts       int     `yaml:"PROJECTION_MAX_ATTEMPTS"`
	RAGEndpoint                 string  `yaml:"RAG_ENDPOINT"`
	RAGTimeout                  string  `yaml:"RAG_TIMEOUT"`
	RAGServiceToken             string  `yaml:"RAG_SERVICE_TOKEN"`
	OwnerClaimSecret            string  `yaml:"OWNER_CLAIM_SECRET"`
	ProjectionVersion           string  `yaml:"PROJECTION_VERSION"`
}

type fileReminderConfig struct {
	Enabled                   bool   `yaml:"ENABLED"`
	WorkflowPilotEnabled      bool   `yaml:"WORKFLOW_PILOT_ENABLED"`
	DispatcherEnabled         bool   `yaml:"DISPATCHER_ENABLED"`
	WorkerEnabled             bool   `yaml:"WORKER_ENABLED"`
	BatchSize                 int    `yaml:"BATCH_SIZE"`
	MaxBatches                int    `yaml:"MAX_BATCHES"`
	MaxAttempts               int    `yaml:"MAX_ATTEMPTS"`
	LeaseDuration             string `yaml:"LEASE_DURATION"`
	Interval                  string `yaml:"INTERVAL"`
	MaxHorizon                string `yaml:"MAX_HORIZON"`
	RetryBaseBackoff          string `yaml:"RETRY_BASE_BACKOFF"`
	RetryMaxBackoff           string `yaml:"RETRY_MAX_BACKOFF"`
	Timezone                  string `yaml:"TIMEZONE"`
	ProductionDeliveryAdapter string `yaml:"PRODUCTION_DELIVERY_ADAPTER"`
}

type fileRAGConfig struct {
	Enabled         bool     `yaml:"ENABLED"`
	BaseURL         string   `yaml:"BASE_URL"`
	APIKey          string   `yaml:"API_KEY"`
	KBIDs           []uint64 `yaml:"KB_IDS"`
	Timeout         string   `yaml:"TIMEOUT"`
	TopK            int      `yaml:"TOP_K"`
	StrategyProfile string   `yaml:"STRATEGY_PROFILE"`
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

	if err := applyServiceEnvironment(&raw); err != nil {
		return Config{}, err
	}
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
	ragTimeout, err := parseDuration("RAG_TIMEOUT", raw.RAG.Timeout)
	if err != nil {
		return Config{}, err
	}
	runTimeout, err := parseDuration("AGENT_RUN_TIMEOUT", raw.Agent.RunTimeout)
	if err != nil {
		return Config{}, err
	}
	toolTimeout, err := parseDuration("AGENT_TOOL_TIMEOUT", raw.Agent.ToolTimeout)
	if err != nil {
		return Config{}, err
	}
	retryBaseDelay, err := parseDuration("RETRY_BASE_DELAY", raw.Resilience.RetryBaseDelay)
	if err != nil {
		return Config{}, err
	}
	retryMaxDelay, err := parseDuration("RETRY_MAX_DELAY", raw.Resilience.RetryMaxDelay)
	if err != nil {
		return Config{}, err
	}
	circuitOpenTimeout, err := parseDuration("CIRCUIT_OPEN_TIMEOUT", raw.Resilience.CircuitOpenTimeout)
	if err != nil {
		return Config{}, err
	}
	connMaxLifetime, err := parseDuration("DATABASE_CONN_MAX_LIFETIME", raw.Database.ConnMaxLifetime)
	if err != nil {
		return Config{}, err
	}
	sessionTTL, err := parseDuration("AUTH_SESSION_TTL", raw.Auth.SessionTTL)
	if err != nil {
		return Config{}, err
	}
	noteDraftTTL, err := parseDuration("NOTE_DRAFT_TTL", raw.Agent.NoteDraftTTL)
	if err != nil {
		return Config{}, err
	}
	memorySessionTTL, err := parseDuration("MEMORY_DEFAULT_SESSION_TTL", raw.Memory.DefaultSessionTTL)
	if err != nil {
		return Config{}, err
	}
	memoryRAGTimeout, err := parseDuration("MEMORY_RAG_TIMEOUT", raw.Memory.RAGTimeout)
	if err != nil {
		return Config{}, err
	}
	projectionBaseBackoff, err := parseDuration("MEMORY_PROJECTION_BASE_BACKOFF", raw.Memory.ProjectionBaseBackoff)
	if err != nil {
		return Config{}, err
	}
	projectionMaxBackoff, err := parseDuration("MEMORY_PROJECTION_MAX_BACKOFF", raw.Memory.ProjectionMaxBackoff)
	if err != nil {
		return Config{}, err
	}
	reminderLease, err := parseDuration("REMINDER_LEASE_DURATION", raw.Reminder.LeaseDuration)
	if err != nil {
		return Config{}, err
	}
	reminderInterval, err := parseDuration("REMINDER_INTERVAL", raw.Reminder.Interval)
	if err != nil {
		return Config{}, err
	}
	reminderHorizon, err := parseDuration("REMINDER_MAX_HORIZON", raw.Reminder.MaxHorizon)
	if err != nil {
		return Config{}, err
	}
	reminderRetryBase, err := parseDuration("REMINDER_RETRY_BASE_BACKOFF", raw.Reminder.RetryBaseBackoff)
	if err != nil {
		return Config{}, err
	}
	reminderRetryMax, err := parseDuration("REMINDER_RETRY_MAX_BACKOFF", raw.Reminder.RetryMaxBackoff)
	if err != nil {
		return Config{}, err
	}
	skillCacheTTL, err := parseDuration("SKILL_CACHE_TTL", raw.Skills.CacheTTL)
	if err != nil {
		return Config{}, err
	}
	skillCacheLease, err := parseDuration("SKILL_CACHE_LEASE", raw.Skills.CacheLease)
	if err != nil {
		return Config{}, err
	}
	skillCacheWait, err := parseDuration("SKILL_CACHE_WAIT", raw.Skills.CacheWait)
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
		RAG: RAGConfig{
			Enabled:         raw.RAG.Enabled,
			BaseURL:         strings.TrimRight(strings.TrimSpace(raw.RAG.BaseURL), "/"),
			APIKey:          strings.TrimSpace(raw.RAG.APIKey),
			KBIDs:           append([]uint64(nil), raw.RAG.KBIDs...),
			Timeout:         ragTimeout,
			TopK:            raw.RAG.TopK,
			StrategyProfile: strings.TrimSpace(raw.RAG.StrategyProfile),
		},
		Agent: AgentConfig{
			EnableMultiAgent:            raw.Agent.EnableMultiAgent,
			EnableIntentRouting:         raw.Agent.EnableIntentRouting,
			EnableLegacyRoutingFallback: raw.Agent.EnableLegacyRoutingFallback,
			IntentComplexThreshold:      raw.Agent.IntentComplexThreshold,
			IntentMinWriteConfidence:    raw.Agent.IntentMinWriteConfidence,
			NoteDraftTTL:                noteDraftTTL,
			RunTimeout:                  runTimeout, ToolTimeout: toolTimeout,
			MaxIterations: raw.Agent.MaxIterations, MaxModelCalls: raw.Agent.MaxModelCalls,
			MaxToolCalls: raw.Agent.MaxToolCalls, MaxRepairAttempts: raw.Agent.MaxRepairAttempts,
			MaxOutputTokens: raw.Agent.MaxOutputTokens,
		},
		Resilience: ResilienceConfig{
			ModelMaxAttempts: raw.Resilience.ModelMaxAttempts, RAGMaxAttempts: raw.Resilience.RAGMaxAttempts,
			RetryBaseDelay: retryBaseDelay, RetryMaxDelay: retryMaxDelay,
			ModelMaxConcurrency: raw.Resilience.ModelMaxConcurrency, RAGMaxConcurrency: raw.Resilience.RAGMaxConcurrency,
			CircuitFailureThreshold: raw.Resilience.CircuitFailureThreshold, CircuitOpenTimeout: circuitOpenTimeout,
		},
		Grounding: GroundingConfig{
			RequireRAGForNoteQuery: raw.Grounding.RequireRAGForNoteQuery,
			RequireEvidenceGate:    raw.Grounding.RequireEvidenceGate, RequireCitationCheck: raw.Grounding.RequireCitationCheck,
			MinResults: raw.Grounding.MinResults, MinTopScore: raw.Grounding.MinTopScore, MinItemScore: raw.Grounding.MinItemScore,
			RequireCompleteCitation: raw.Grounding.RequireCompleteCitation,
			MaxContextChars:         raw.Grounding.MaxContextChars, RejectPromptInjection: raw.Grounding.RejectPromptInjection,
		},
		Database: DatabaseConfig{
			Enabled: raw.Database.Enabled, DSN: strings.TrimSpace(raw.Database.DSN), AutoMigrate: raw.Database.AutoMigrate,
			MaxOpenConns: raw.Database.MaxOpenConns, MaxIdleConns: raw.Database.MaxIdleConns, ConnMaxLifetime: connMaxLifetime,
		},
		Context: ContextConfig{
			MaxInputTokens: raw.Context.MaxInputTokens, MinRecentMessages: raw.Context.MinRecentMessages,
			MessageHistoryLimit: raw.Context.MessageHistoryLimit,
		},
		Auth: AuthConfig{
			Enabled: raw.Auth.Enabled, SessionSecret: strings.TrimSpace(raw.Auth.SessionSecret), SessionTTL: sessionTTL,
			CookieName: strings.TrimSpace(raw.Auth.CookieName), CookieSecure: raw.Auth.CookieSecure,
		},
		Note:     NoteConfig{Enabled: raw.Note.Enabled, KBID: raw.Note.KBID},
		Memory:   MemoryConfig{Enabled: raw.Memory.Enabled, RAGEnabled: raw.Memory.RAGEnabled, ProjectionEnabled: raw.Memory.ProjectionEnabled, WorkflowPilotEnabled: raw.Memory.WorkflowPilotEnabled, RecallMode: strings.TrimSpace(raw.Memory.RecallMode), StructuredPlanMinConfidence: raw.Memory.StructuredPlanMinConfidence, MaxRecallSelectors: raw.Memory.MaxRecallSelectors, MaxExactCandidates: raw.Memory.MaxExactCandidates, MaxCandidateTextChars: raw.Memory.MaxCandidateTextChars, MaxLLMResponseBytes: raw.Memory.MaxLLMResponseBytes, MaxLLMRepairAttempts: raw.Memory.MaxLLMRepairAttempts, DefaultSessionTTL: memorySessionTTL, RecallTarget: raw.Memory.RecallTarget, RecallPageSize: raw.Memory.RecallPageSize, MaxScanned: raw.Memory.MaxScanned, MaxBatches: raw.Memory.MaxBatches, MaxContextChars: raw.Memory.MaxContextChars, ConflictThreshold: raw.Memory.ConflictThreshold, ProjectionBatchSize: raw.Memory.ProjectionBatchSize, ProjectionBaseBackoff: projectionBaseBackoff, ProjectionMaxBackoff: projectionMaxBackoff, ProjectionMaxAttempts: raw.Memory.ProjectionMaxAttempts, RAGEndpoint: strings.TrimRight(strings.TrimSpace(raw.Memory.RAGEndpoint), "/"), RAGTimeout: memoryRAGTimeout, RAGServiceToken: strings.TrimSpace(raw.Memory.RAGServiceToken), OwnerClaimSecret: strings.TrimSpace(raw.Memory.OwnerClaimSecret), ProjectionVersion: strings.TrimSpace(raw.Memory.ProjectionVersion)},
		Reminder: ReminderConfig{Enabled: raw.Reminder.Enabled, WorkflowPilotEnabled: raw.Reminder.WorkflowPilotEnabled, DispatcherEnabled: raw.Reminder.DispatcherEnabled, WorkerEnabled: raw.Reminder.WorkerEnabled, BatchSize: raw.Reminder.BatchSize, MaxBatches: raw.Reminder.MaxBatches, MaxAttempts: raw.Reminder.MaxAttempts, LeaseDuration: reminderLease, Interval: reminderInterval, MaxHorizon: reminderHorizon, RetryBaseBackoff: reminderRetryBase, RetryMaxBackoff: reminderRetryMax, Timezone: strings.TrimSpace(raw.Reminder.Timezone), ProductionDeliveryAdapter: strings.TrimSpace(raw.Reminder.ProductionDeliveryAdapter)},
		Skills:   SkillConfig{Enabled: raw.Skills.Enabled, DailyReviewEnabled: raw.Skills.DailyReviewEnabled, Timezone: strings.TrimSpace(raw.Skills.Timezone), MaxLookbackDays: raw.Skills.MaxLookbackDays, MaxChatMessages: raw.Skills.MaxChatMessages, PerSessionMessages: raw.Skills.PerSessionMessages, MaxNotes: raw.Skills.MaxNotes, MaxContextChars: raw.Skills.MaxContextChars, MaxSteps: raw.Skills.MaxSteps, MaxModelCalls: raw.Skills.MaxModelCalls, MaxToolCalls: raw.Skills.MaxToolCalls, MaxRepairAttempts: raw.Skills.MaxRepairAttempts, CacheTTL: skillCacheTTL, CacheLease: skillCacheLease, CacheWait: skillCacheWait, SkillVersion: strings.TrimSpace(raw.Skills.SkillVersion), SchemaVersion: strings.TrimSpace(raw.Skills.SchemaVersion), PromptPolicyVersion: strings.TrimSpace(raw.Skills.PromptPolicyVersion)},
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
	if err := c.RAG.Validate(); err != nil {
		return err
	}
	if err := c.Agent.Validate(); err != nil {
		return err
	}
	if err := c.Resilience.Validate(); err != nil {
		return err
	}
	if err := c.Grounding.Validate(); err != nil {
		return err
	}
	if err := c.Database.Validate(); err != nil {
		return err
	}
	if err := c.Context.Validate(); err != nil {
		return err
	}
	if err := c.Auth.Validate(c.Database.Enabled, c.RAG.Enabled); err != nil {
		return err
	}
	if err := c.Note.Validate(c.Auth.Enabled, c.Database.Enabled, c.RAG.Enabled); err != nil {
		return err
	}
	if err := c.Memory.Validate(c.Database.Enabled); err != nil {
		return err
	}
	if err := c.Reminder.Validate(c.Database.Enabled, c.Auth.Enabled, c.Memory.Enabled); err != nil {
		return err
	}
	if err := c.Skills.Validate(c.Agent.EnableIntentRouting, c.Database.Enabled, c.Auth.Enabled, c.Memory.Enabled); err != nil {
		return err
	}
	return nil
}

func (c SkillConfig) Validate(intentRouting, databaseEnabled, authEnabled, memoryEnabled bool) error {
	if c.Timezone != "Asia/Shanghai" || c.MaxLookbackDays < 1 || c.MaxLookbackDays > 366 || c.MaxChatMessages < 1 || c.MaxChatMessages > 1000 || c.PerSessionMessages < 1 || c.PerSessionMessages > c.MaxChatMessages || c.MaxNotes < 1 || c.MaxNotes > 500 || c.MaxContextChars < 1 || c.MaxSteps < 9 || c.MaxModelCalls < 1 || c.MaxToolCalls < 2 || c.MaxRepairAttempts < 0 || c.MaxRepairAttempts > 2 || c.CacheTTL <= 0 || c.CacheLease <= 0 || c.CacheWait <= 0 || c.CacheWait > c.CacheLease || c.SkillVersion == "" || c.SchemaVersion == "" || c.PromptPolicyVersion == "" {
		return errors.New("SKILLS limits, versions, durations or timezone are invalid")
	}
	if c.DailyReviewEnabled && !c.Enabled {
		return errors.New("DAILY_REVIEW_ENABLED requires SKILLS_ENABLED")
	}
	if c.Enabled && (!intentRouting || !databaseEnabled || !authEnabled) {
		return errors.New("SKILLS require intent routing, DATABASE and AUTH")
	}
	if c.DailyReviewEnabled && !memoryEnabled {
		return errors.New("daily review requires MEMORY")
	}
	return nil
}

func (c ReminderConfig) Validate(databaseEnabled, authEnabled, memoryEnabled bool) error {
	if c.BatchSize < 1 || c.BatchSize > 100 || c.MaxBatches < 1 || c.MaxAttempts < 1 || c.LeaseDuration < time.Second || c.Interval <= 0 || c.MaxHorizon <= 0 || c.RetryBaseBackoff <= 0 || c.RetryMaxBackoff < c.RetryBaseBackoff || c.Timezone != "Asia/Shanghai" {
		return errors.New("REMINDER limits, durations, retry policy or timezone are invalid")
	}
	if !c.Enabled {
		if c.WorkflowPilotEnabled || c.DispatcherEnabled || c.WorkerEnabled {
			return errors.New("REMINDER subfeatures require REMINDER_ENABLED")
		}
		return nil
	}
	if !databaseEnabled || !authEnabled {
		return errors.New("REMINDER requires DATABASE and AUTH")
	}
	if c.WorkflowPilotEnabled && !memoryEnabled {
		return errors.New("REMINDER workflow pilot requires MEMORY exact recall")
	}
	if c.WorkerEnabled && !c.DispatcherEnabled {
		return errors.New("REMINDER_WORKER_ENABLED requires REMINDER_DISPATCHER_ENABLED")
	}
	if c.WorkerEnabled && c.ProductionDeliveryAdapter == "" {
		return errors.New("REMINDER worker requires a production delivery adapter")
	}
	return nil
}

func (c MemoryConfig) Validate(databaseEnabled bool) error {
	if c.DefaultSessionTTL <= 0 || c.RecallTarget < 1 || c.RecallTarget > c.RecallPageSize || c.RecallPageSize < 1 || c.RecallPageSize > 200 || c.MaxScanned < c.RecallPageSize || c.MaxBatches < 1 || c.MaxContextChars < 1 || c.ConflictThreshold < 0 || c.ConflictThreshold > 1 || c.ProjectionBatchSize < 1 || c.ProjectionBatchSize > 200 || c.ProjectionBaseBackoff <= 0 || c.ProjectionMaxBackoff < c.ProjectionBaseBackoff || c.ProjectionMaxAttempts < 1 || c.RAGTimeout <= 0 || c.ProjectionVersion == "" || c.StructuredPlanMinConfidence < 0 || c.StructuredPlanMinConfidence > 1 || c.MaxRecallSelectors < 1 || c.MaxRecallSelectors > 32 || c.MaxExactCandidates < 1 || c.MaxExactCandidates > 200 || c.MaxCandidateTextChars < 1 || c.MaxLLMResponseBytes < 256 || c.MaxLLMRepairAttempts < 0 || c.MaxLLMRepairAttempts > 3 {
		return errors.New("MEMORY limits, durations, thresholds or projection version are invalid")
	}
	if c.RecallMode != "exact-only" && c.RecallMode != "exact-plus-semantic" {
		return errors.New("MEMORY_RECALL_MODE must be exact-only or exact-plus-semantic")
	}
	if !c.Enabled {
		if c.RAGEnabled || c.ProjectionEnabled || c.WorkflowPilotEnabled {
			return errors.New("MEMORY subfeatures require MEMORY_ENABLED")
		}
		return nil
	}
	if !databaseEnabled {
		return errors.New("MEMORY requires DATABASE to be enabled")
	}
	if c.ProjectionEnabled && !c.RAGEnabled {
		return errors.New("MEMORY_PROJECTION_ENABLED requires MEMORY_RAG_ENABLED")
	}
	if c.RecallMode == "exact-plus-semantic" && !c.RAGEnabled {
		return errors.New("MEMORY_RECALL_MODE exact-plus-semantic requires MEMORY_RAG_ENABLED")
	}
	if c.RAGEnabled {
		if c.RAGEndpoint == "" || c.RAGServiceToken == "" || len(c.OwnerClaimSecret) < 32 {
			return errors.New("MEMORY RAG requires endpoint, service token and OWNER_CLAIM_SECRET of at least 32 characters")
		}
		parsed, err := url.ParseRequestURI(c.RAGEndpoint)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return errors.New("MEMORY_RAG_ENDPOINT must be an absolute HTTP(S) URL")
		}
	}
	return nil
}

func (c AuthConfig) Validate(databaseEnabled, ragEnabled bool) error {
	if !c.Enabled {
		return nil
	}
	if !databaseEnabled || !ragEnabled {
		return errors.New("AUTH requires DATABASE and RAG to be enabled")
	}
	if len(c.SessionSecret) < 32 || c.SessionTTL <= 0 || c.CookieName == "" {
		return errors.New("AUTH requires SESSION_SECRET of at least 32 characters, positive SESSION_TTL and COOKIE_NAME")
	}
	return nil
}

func (c NoteConfig) Validate(authEnabled, databaseEnabled, ragEnabled bool) error {
	if !c.Enabled {
		return nil
	}
	if !authEnabled || !databaseEnabled || !ragEnabled || c.KBID == 0 {
		return errors.New("NOTE requires AUTH, DATABASE, RAG and a positive KB_ID")
	}
	return nil
}

func (c DatabaseConfig) Validate() error {
	if c.MaxOpenConns < 1 || c.MaxIdleConns < 0 || c.MaxIdleConns > c.MaxOpenConns || c.ConnMaxLifetime <= 0 {
		return errors.New("DATABASE connection pool settings are invalid")
	}
	if c.Enabled && c.DSN == "" {
		return errors.New("DATABASE_DSN is required when DATABASE is enabled")
	}
	return nil
}

func (c ContextConfig) Validate() error {
	if c.MaxInputTokens < 1 || c.MinRecentMessages < 1 || c.MessageHistoryLimit < c.MinRecentMessages {
		return errors.New("CONTEXT limits are invalid")
	}
	return nil
}

func (c AgentConfig) Validate() error {
	if c.RunTimeout <= 0 || c.ToolTimeout <= 0 {
		return errors.New("AGENT_RUN_TIMEOUT and AGENT_TOOL_TIMEOUT must be greater than zero")
	}
	if c.MaxIterations < 1 || c.MaxModelCalls < 1 || c.MaxToolCalls < 1 || c.MaxOutputTokens < 1 || c.MaxRepairAttempts < 0 {
		return errors.New("AGENT limits must be positive and MAX_REPAIR_ATTEMPTS must not be negative")
	}
	if c.IntentComplexThreshold < 1 {
		return errors.New("INTENT_COMPLEX_THRESHOLD must be positive")
	}
	if c.IntentMinWriteConfidence < 0 || c.IntentMinWriteConfidence > 1 {
		return errors.New("INTENT_MIN_WRITE_CONFIDENCE must be within [0,1]")
	}
	if c.NoteDraftTTL <= 0 {
		return errors.New("NOTE_DRAFT_TTL must be greater than zero")
	}
	return nil
}

func (c ResilienceConfig) Validate() error {
	if c.ModelMaxAttempts < 1 || c.RAGMaxAttempts < 1 || c.ModelMaxConcurrency < 1 || c.RAGMaxConcurrency < 1 || c.CircuitFailureThreshold < 1 {
		return errors.New("RESILIENCE attempt, concurrency, and circuit limits must be positive")
	}
	if c.RetryBaseDelay <= 0 || c.RetryMaxDelay < c.RetryBaseDelay || c.CircuitOpenTimeout <= 0 {
		return errors.New("RESILIENCE durations are invalid")
	}
	return nil
}

func (c GroundingConfig) Validate() error {
	if c.MinResults < 1 || c.MaxContextChars < 1 {
		return errors.New("GROUNDING_MIN_RESULTS and GROUNDING_MAX_CONTEXT_CHARS must be positive")
	}
	if c.MinTopScore < 0 || c.MinTopScore > 1 || c.MinItemScore < 0 || c.MinItemScore > 1 {
		return errors.New("GROUNDING score thresholds must be within [0,1]")
	}
	return nil
}

func (c RAGConfig) Validate() error {
	if c.Timeout <= 0 {
		return errors.New("RAG_TIMEOUT must be greater than zero")
	}
	if c.TopK < 1 || c.TopK > 20 {
		return errors.New("RAG_TOP_K must be between 1 and 20")
	}
	if !c.Enabled {
		return nil
	}
	var missing []string
	if c.BaseURL == "" {
		missing = append(missing, "RAG_BASE_URL")
	}
	if c.APIKey == "" {
		missing = append(missing, "RAG_API_KEY")
	}
	if len(c.KBIDs) == 0 {
		missing = append(missing, "RAG_KB_IDS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required RAG configuration: %s", strings.Join(missing, ", "))
	}
	parsed, err := url.ParseRequestURI(c.BaseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("RAG_BASE_URL must be an absolute HTTP(S) URL")
	}
	for _, kbID := range c.KBIDs {
		if kbID == 0 {
			return errors.New("RAG_KB_IDS must contain only positive IDs")
		}
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
		RAG: fileRAGConfig{
			Timeout:         "10s",
			TopK:            8,
			StrategyProfile: "default",
		},
		Agent: fileAgentConfig{
			EnableMultiAgent: false, EnableIntentRouting: true, EnableLegacyRoutingFallback: true,
			IntentComplexThreshold: 120, IntentMinWriteConfidence: 0.95, NoteDraftTTL: "24h",
			RunTimeout: "90s", ToolTimeout: "15s", MaxIterations: 6,
			MaxModelCalls: 3, MaxToolCalls: 3, MaxRepairAttempts: 1, MaxOutputTokens: 2000,
		},
		Resilience: fileResilienceConfig{
			ModelMaxAttempts: 2, RAGMaxAttempts: 3, RetryBaseDelay: "200ms", RetryMaxDelay: "2s",
			ModelMaxConcurrency: 8, RAGMaxConcurrency: 16, CircuitFailureThreshold: 5, CircuitOpenTimeout: "30s",
		},
		Grounding: fileGroundingConfig{
			RequireRAGForNoteQuery: true, RequireEvidenceGate: true, RequireCitationCheck: true,
			MinResults: 1, MinTopScore: 0.60, MinItemScore: 0.45,
			RequireCompleteCitation: true, MaxContextChars: 24000, RejectPromptInjection: true,
		},
		Database: fileDatabaseConfig{
			ConnMaxLifetime: "5m", MaxOpenConns: 20, MaxIdleConns: 10,
		},
		Context: fileContextConfig{
			MaxInputTokens: 24000, MinRecentMessages: 6, MessageHistoryLimit: 100,
		},
		Auth:     fileAuthConfig{SessionTTL: "168h", CookieName: "note_agent_session"},
		Memory:   fileMemoryConfig{RecallMode: "exact-only", StructuredPlanMinConfidence: .75, MaxRecallSelectors: 8, MaxExactCandidates: 40, MaxCandidateTextChars: 16000, MaxLLMResponseBytes: 16384, MaxLLMRepairAttempts: 1, DefaultSessionTTL: "24h", RecallTarget: 10, RecallPageSize: 20, MaxScanned: 200, MaxBatches: 10, MaxContextChars: 12000, ConflictThreshold: .8, ProjectionBatchSize: 50, ProjectionBaseBackoff: "1s", ProjectionMaxBackoff: "5m", ProjectionMaxAttempts: 8, RAGTimeout: "10s", ProjectionVersion: "v1"},
		Reminder: fileReminderConfig{BatchSize: 20, MaxBatches: 5, MaxAttempts: 8, LeaseDuration: "30s", Interval: "1s", MaxHorizon: "8760h", RetryBaseBackoff: "1s", RetryMaxBackoff: "5m", Timezone: "Asia/Shanghai"},
		Skills:   fileSkillConfig{Timezone: "Asia/Shanghai", MaxLookbackDays: 31, MaxChatMessages: 200, PerSessionMessages: 50, MaxNotes: 100, MaxContextChars: 24000, MaxSteps: 9, MaxModelCalls: 1, MaxToolCalls: 2, MaxRepairAttempts: 1, CacheTTL: "24h", CacheLease: "2m", CacheWait: "30s", SkillVersion: "v1", SchemaVersion: "v1", PromptPolicyVersion: "v1"},
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

func applyServiceEnvironment(raw *fileConfig) error {
	applyEnvironment(map[string]*string{
		"HTTP_ADDR":        &raw.HTTPAddr,
		"SHUTDOWN_TIMEOUT": &raw.ShutdownTimeout,
	})
	applyEnvironment(map[string]*string{
		"RAG_BASE_URL":                         &raw.RAG.BaseURL,
		"RAG_API_KEY":                          &raw.RAG.APIKey,
		"RAG_TIMEOUT":                          &raw.RAG.Timeout,
		"RAG_STRATEGY_PROFILE":                 &raw.RAG.StrategyProfile,
		"AGENT_RUN_TIMEOUT":                    &raw.Agent.RunTimeout,
		"AGENT_TOOL_TIMEOUT":                   &raw.Agent.ToolTimeout,
		"NOTE_DRAFT_TTL":                       &raw.Agent.NoteDraftTTL,
		"RETRY_BASE_DELAY":                     &raw.Resilience.RetryBaseDelay,
		"RETRY_MAX_DELAY":                      &raw.Resilience.RetryMaxDelay,
		"CIRCUIT_OPEN_TIMEOUT":                 &raw.Resilience.CircuitOpenTimeout,
		"DATABASE_DSN":                         &raw.Database.DSN,
		"DATABASE_CONN_MAX_LIFETIME":           &raw.Database.ConnMaxLifetime,
		"AUTH_SESSION_SECRET":                  &raw.Auth.SessionSecret,
		"AUTH_SESSION_TTL":                     &raw.Auth.SessionTTL,
		"AUTH_COOKIE_NAME":                     &raw.Auth.CookieName,
		"MEMORY_DEFAULT_SESSION_TTL":           &raw.Memory.DefaultSessionTTL,
		"MEMORY_PROJECTION_BASE_BACKOFF":       &raw.Memory.ProjectionBaseBackoff,
		"MEMORY_PROJECTION_MAX_BACKOFF":        &raw.Memory.ProjectionMaxBackoff,
		"MEMORY_RAG_ENDPOINT":                  &raw.Memory.RAGEndpoint,
		"MEMORY_RAG_TIMEOUT":                   &raw.Memory.RAGTimeout,
		"MEMORY_RAG_SERVICE_TOKEN":             &raw.Memory.RAGServiceToken,
		"MEMORY_OWNER_CLAIM_SECRET":            &raw.Memory.OwnerClaimSecret,
		"MEMORY_PROJECTION_VERSION":            &raw.Memory.ProjectionVersion,
		"MEMORY_RECALL_MODE":                   &raw.Memory.RecallMode,
		"REMINDER_LEASE_DURATION":              &raw.Reminder.LeaseDuration,
		"REMINDER_INTERVAL":                    &raw.Reminder.Interval,
		"REMINDER_MAX_HORIZON":                 &raw.Reminder.MaxHorizon,
		"REMINDER_RETRY_BASE_BACKOFF":          &raw.Reminder.RetryBaseBackoff,
		"REMINDER_RETRY_MAX_BACKOFF":           &raw.Reminder.RetryMaxBackoff,
		"REMINDER_TIMEZONE":                    &raw.Reminder.Timezone,
		"REMINDER_PRODUCTION_DELIVERY_ADAPTER": &raw.Reminder.ProductionDeliveryAdapter,
		"SKILL_TIMEZONE":                       &raw.Skills.Timezone,
		"SKILL_CACHE_TTL":                      &raw.Skills.CacheTTL,
		"SKILL_CACHE_LEASE":                    &raw.Skills.CacheLease,
		"SKILL_CACHE_WAIT":                     &raw.Skills.CacheWait,
		"DAILY_REVIEW_SKILL_VERSION":           &raw.Skills.SkillVersion,
		"DAILY_REVIEW_SCHEMA_VERSION":          &raw.Skills.SchemaVersion,
		"DAILY_REVIEW_PROMPT_POLICY_VERSION":   &raw.Skills.PromptPolicyVersion,
	})
	for key, target := range map[string]*bool{
		"DATABASE_ENABLED":                &raw.Database.Enabled,
		"DATABASE_AUTO_MIGRATE":           &raw.Database.AutoMigrate,
		"AUTH_ENABLED":                    &raw.Auth.Enabled,
		"AUTH_COOKIE_SECURE":              &raw.Auth.CookieSecure,
		"NOTE_ENABLED":                    &raw.Note.Enabled,
		"ENABLE_MULTI_AGENT":              &raw.Agent.EnableMultiAgent,
		"ENABLE_INTENT_ROUTING":           &raw.Agent.EnableIntentRouting,
		"ENABLE_LEGACY_ROUTING_FALLBACK":  &raw.Agent.EnableLegacyRoutingFallback,
		"MEMORY_ENABLED":                  &raw.Memory.Enabled,
		"MEMORY_RAG_ENABLED":              &raw.Memory.RAGEnabled,
		"MEMORY_PROJECTION_ENABLED":       &raw.Memory.ProjectionEnabled,
		"MEMORY_WORKFLOW_PILOT_ENABLED":   &raw.Memory.WorkflowPilotEnabled,
		"REMINDER_ENABLED":                &raw.Reminder.Enabled,
		"REMINDER_WORKFLOW_PILOT_ENABLED": &raw.Reminder.WorkflowPilotEnabled,
		"REMINDER_DISPATCHER_ENABLED":     &raw.Reminder.DispatcherEnabled,
		"REMINDER_WORKER_ENABLED":         &raw.Reminder.WorkerEnabled,
		"SKILLS_ENABLED":                  &raw.Skills.Enabled,
		"DAILY_REVIEW_ENABLED":            &raw.Skills.DailyReviewEnabled,
	} {
		if err := applyBoolEnvironment(key, target); err != nil {
			return err
		}
	}
	if value := strings.TrimSpace(os.Getenv("NOTE_KB_ID")); value != "" {
		kbID, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return fmt.Errorf("parse NOTE_KB_ID: %w", err)
		}
		raw.Note.KBID = kbID
	}
	if value := strings.TrimSpace(os.Getenv("RAG_ENABLED")); value != "" {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Errorf("parse RAG_ENABLED: %w", err)
		}
		raw.RAG.Enabled = enabled
	}
	if value := strings.TrimSpace(os.Getenv("RAG_TOP_K")); value != "" {
		topK, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("parse RAG_TOP_K: %w", err)
		}
		raw.RAG.TopK = topK
	}
	if value := strings.TrimSpace(os.Getenv("RAG_KB_IDS")); value != "" {
		ids, err := parseUint64List(value)
		if err != nil {
			return fmt.Errorf("parse RAG_KB_IDS: %w", err)
		}
		raw.RAG.KBIDs = ids
	}
	for key, target := range map[string]*int{
		"AGENT_MAX_ITERATIONS":              &raw.Agent.MaxIterations,
		"AGENT_MAX_MODEL_CALLS":             &raw.Agent.MaxModelCalls,
		"AGENT_MAX_TOOL_CALLS":              &raw.Agent.MaxToolCalls,
		"AGENT_MAX_REPAIR_ATTEMPTS":         &raw.Agent.MaxRepairAttempts,
		"AGENT_MAX_OUTPUT_TOKENS":           &raw.Agent.MaxOutputTokens,
		"INTENT_COMPLEX_THRESHOLD":          &raw.Agent.IntentComplexThreshold,
		"MODEL_MAX_ATTEMPTS":                &raw.Resilience.ModelMaxAttempts,
		"RAG_MAX_ATTEMPTS":                  &raw.Resilience.RAGMaxAttempts,
		"MODEL_MAX_CONCURRENCY":             &raw.Resilience.ModelMaxConcurrency,
		"RAG_MAX_CONCURRENCY":               &raw.Resilience.RAGMaxConcurrency,
		"CIRCUIT_FAILURE_THRESHOLD":         &raw.Resilience.CircuitFailureThreshold,
		"GROUNDING_MIN_RESULTS":             &raw.Grounding.MinResults,
		"GROUNDING_MAX_CONTEXT_CHARS":       &raw.Grounding.MaxContextChars,
		"DATABASE_MAX_OPEN_CONNS":           &raw.Database.MaxOpenConns,
		"DATABASE_MAX_IDLE_CONNS":           &raw.Database.MaxIdleConns,
		"CONTEXT_MAX_INPUT_TOKENS":          &raw.Context.MaxInputTokens,
		"CONTEXT_MIN_RECENT_MESSAGES":       &raw.Context.MinRecentMessages,
		"CONTEXT_MESSAGE_HISTORY_LIMIT":     &raw.Context.MessageHistoryLimit,
		"MEMORY_RECALL_TARGET":              &raw.Memory.RecallTarget,
		"MEMORY_RECALL_PAGE_SIZE":           &raw.Memory.RecallPageSize,
		"MEMORY_MAX_SCANNED":                &raw.Memory.MaxScanned,
		"MEMORY_MAX_BATCHES":                &raw.Memory.MaxBatches,
		"MEMORY_MAX_CONTEXT_CHARS":          &raw.Memory.MaxContextChars,
		"MEMORY_PROJECTION_BATCH_SIZE":      &raw.Memory.ProjectionBatchSize,
		"MEMORY_PROJECTION_MAX_ATTEMPTS":    &raw.Memory.ProjectionMaxAttempts,
		"MEMORY_MAX_RECALL_SELECTORS":       &raw.Memory.MaxRecallSelectors,
		"MEMORY_MAX_EXACT_CANDIDATES":       &raw.Memory.MaxExactCandidates,
		"MEMORY_MAX_CANDIDATE_TEXT_CHARS":   &raw.Memory.MaxCandidateTextChars,
		"MEMORY_MAX_LLM_RESPONSE_BYTES":     &raw.Memory.MaxLLMResponseBytes,
		"MEMORY_MAX_LLM_REPAIR_ATTEMPTS":    &raw.Memory.MaxLLMRepairAttempts,
		"REMINDER_BATCH_SIZE":               &raw.Reminder.BatchSize,
		"REMINDER_MAX_BATCHES":              &raw.Reminder.MaxBatches,
		"REMINDER_MAX_ATTEMPTS":             &raw.Reminder.MaxAttempts,
		"SKILL_MAX_LOOKBACK_DAYS":           &raw.Skills.MaxLookbackDays,
		"DAILY_REVIEW_MAX_CHAT_MESSAGES":    &raw.Skills.MaxChatMessages,
		"DAILY_REVIEW_PER_SESSION_MESSAGES": &raw.Skills.PerSessionMessages,
		"DAILY_REVIEW_MAX_NOTES":            &raw.Skills.MaxNotes,
		"DAILY_REVIEW_MAX_CONTEXT_CHARS":    &raw.Skills.MaxContextChars,
		"DAILY_REVIEW_MAX_STEPS":            &raw.Skills.MaxSteps,
		"DAILY_REVIEW_MAX_MODEL_CALLS":      &raw.Skills.MaxModelCalls,
		"DAILY_REVIEW_MAX_TOOL_CALLS":       &raw.Skills.MaxToolCalls,
		"DAILY_REVIEW_MAX_REPAIR_ATTEMPTS":  &raw.Skills.MaxRepairAttempts,
	} {
		if err := applyIntEnvironment(key, target); err != nil {
			return err
		}
	}
	for key, target := range map[string]*float64{
		"GROUNDING_MIN_TOP_SCORE":               &raw.Grounding.MinTopScore,
		"GROUNDING_MIN_ITEM_SCORE":              &raw.Grounding.MinItemScore,
		"INTENT_MIN_WRITE_CONFIDENCE":           &raw.Agent.IntentMinWriteConfidence,
		"MEMORY_CONFLICT_THRESHOLD":             &raw.Memory.ConflictThreshold,
		"MEMORY_STRUCTURED_PLAN_MIN_CONFIDENCE": &raw.Memory.StructuredPlanMinConfidence,
	} {
		if err := applyFloatEnvironment(key, target); err != nil {
			return err
		}
	}
	for key, target := range map[string]*bool{
		"GROUNDING_REQUIRE_RAG_FOR_NOTE_QUERY": &raw.Grounding.RequireRAGForNoteQuery,
		"GROUNDING_REQUIRE_EVIDENCE_GATE":      &raw.Grounding.RequireEvidenceGate,
		"GROUNDING_REQUIRE_CITATION_CHECK":     &raw.Grounding.RequireCitationCheck,
		"GROUNDING_REQUIRE_COMPLETE_CITATION":  &raw.Grounding.RequireCompleteCitation,
		"GROUNDING_REJECT_PROMPT_INJECTION":    &raw.Grounding.RejectPromptInjection,
	} {
		if err := applyBoolEnvironment(key, target); err != nil {
			return err
		}
	}
	return nil
}

func applyIntEnvironment(key string, target *int) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*target = parsed
	return nil
}

func applyFloatEnvironment(key string, target *float64) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*target = parsed
	return nil
}

func applyBoolEnvironment(key string, target *bool) error {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fmt.Errorf("parse %s: %w", key, err)
	}
	*target = parsed
	return nil
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

func parseUint64List(raw string) ([]uint64, error) {
	parts := strings.Split(raw, ",")
	ids := make([]uint64, 0, len(parts))
	for _, part := range parts {
		id, err := strconv.ParseUint(strings.TrimSpace(part), 10, 64)
		if err != nil || id == 0 {
			return nil, fmt.Errorf("invalid knowledge base ID %q", strings.TrimSpace(part))
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseDuration(key, raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse %s: %w", key, err)
	}
	return d, nil
}
