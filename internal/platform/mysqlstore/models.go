package mysqlstore

import "time"

// Persistence rows deliberately do not embed gorm.Model: the SQL migrations are
// the schema source of truth and none of these tables use GORM soft deletion.
type chatSessionRow struct {
	ID        string    `gorm:"column:id;primaryKey"`
	UserID    uint64    `gorm:"column:user_id"`
	TenantID  uint64    `gorm:"column:tenant_id"`
	Title     string    `gorm:"column:title"`
	Status    string    `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (chatSessionRow) TableName() string { return "chat_sessions" }

type agentRunRow struct {
	ID                string     `gorm:"column:id;primaryKey"`
	SessionID         string     `gorm:"column:session_id"`
	Status            string     `gorm:"column:status"`
	ModelName         string     `gorm:"column:model_name"`
	IdempotencyKey    string     `gorm:"column:idempotency_key"`
	ErrorCode         *string    `gorm:"column:error_code"`
	ErrorMessage      *string    `gorm:"column:error_message"`
	LastEventSequence int64      `gorm:"column:last_event_sequence"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	StartedAt         *time.Time `gorm:"column:started_at"`
	CompletedAt       *time.Time `gorm:"column:completed_at"`
}

func (agentRunRow) TableName() string { return "agent_runs" }

type chatMessageRow struct {
	ID        string    `gorm:"column:id;primaryKey"`
	SessionID string    `gorm:"column:session_id"`
	RunID     *string   `gorm:"column:run_id"`
	Sequence  int64     `gorm:"column:sequence"`
	Role      string    `gorm:"column:role"`
	Content   string    `gorm:"column:content"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (chatMessageRow) TableName() string { return "chat_messages" }

type agentRunEventRow struct {
	RunID     string    `gorm:"column:run_id;primaryKey"`
	Sequence  int64     `gorm:"column:sequence;primaryKey"`
	EventType string    `gorm:"column:event_type"`
	EventData []byte    `gorm:"column:event_data"`
	CreatedAt time.Time `gorm:"column:created_at"`
}

func (agentRunEventRow) TableName() string { return "agent_run_events" }

type authSessionRow struct {
	ID                     string    `gorm:"column:id;primaryKey"`
	UserID                 uint64    `gorm:"column:user_id"`
	TenantID               uint64    `gorm:"column:tenant_id"`
	Role                   string    `gorm:"column:role"`
	Email                  string    `gorm:"column:email"`
	Name                   string    `gorm:"column:name"`
	AccessTokenCiphertext  string    `gorm:"column:access_token_ciphertext"`
	RefreshTokenCiphertext string    `gorm:"column:refresh_token_ciphertext"`
	AccessExpiresAt        time.Time `gorm:"column:access_expires_at"`
	ExpiresAt              time.Time `gorm:"column:expires_at"`
	CreatedAt              time.Time `gorm:"column:created_at"`
	UpdatedAt              time.Time `gorm:"column:updated_at"`
}

func (authSessionRow) TableName() string { return "agent_user_sessions" }

type noteRow struct {
	ID                   string     `gorm:"column:id;primaryKey"`
	UserID               uint64     `gorm:"column:user_id"`
	TenantID             uint64     `gorm:"column:tenant_id"`
	ExternalNoteID       string     `gorm:"column:external_note_id"`
	CreateIdempotencyKey string     `gorm:"column:create_idempotency_key"`
	Title                string     `gorm:"column:title"`
	Content              string     `gorm:"column:content"`
	Tags                 []byte     `gorm:"column:tags"`
	OccurredAt           *time.Time `gorm:"column:occurred_at"`
	Status               string     `gorm:"column:status"`
	RAGKBID              uint64     `gorm:"column:rag_kb_id"`
	RAGDocumentID        *uint64    `gorm:"column:rag_document_id"`
	RAGJobID             *uint64    `gorm:"column:rag_job_id"`
	RAGStatus            string     `gorm:"column:rag_status"`
	LastError            string     `gorm:"column:last_error"`
	ContentHash          string     `gorm:"column:content_hash"`
	DeletedAt            *time.Time `gorm:"column:deleted_at"`
	CreatedAt            time.Time  `gorm:"column:created_at"`
	UpdatedAt            time.Time  `gorm:"column:updated_at"`
}

func (noteRow) TableName() string { return "notes" }

type noteOutboxRow struct {
	ID             string     `gorm:"column:id;primaryKey"`
	NoteID         string     `gorm:"column:note_id"`
	UserID         uint64     `gorm:"column:user_id"`
	TenantID       uint64     `gorm:"column:tenant_id"`
	EventType      string     `gorm:"column:event_type"`
	IdempotencyKey string     `gorm:"column:idempotency_key"`
	Status         string     `gorm:"column:status"`
	Attempt        int        `gorm:"column:attempt"`
	LastError      string     `gorm:"column:last_error"`
	AvailableAt    time.Time  `gorm:"column:available_at"`
	CreatedAt      time.Time  `gorm:"column:created_at"`
	ProcessedAt    *time.Time `gorm:"column:processed_at"`
}

func (noteOutboxRow) TableName() string { return "note_outbox_events" }

type knowledgeBaseRow struct {
	UserID    uint64    `gorm:"column:user_id;primaryKey"`
	TenantID  uint64    `gorm:"column:tenant_id;primaryKey"`
	RAGKBID   uint64    `gorm:"column:rag_kb_id"`
	Name      string    `gorm:"column:name"`
	Status    string    `gorm:"column:status"`
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (knowledgeBaseRow) TableName() string { return "agent_user_knowledge_bases" }

type noteDraftRow struct {
	ID          string    `gorm:"column:id;primaryKey"`
	UserID      uint64    `gorm:"column:user_id"`
	TenantID    uint64    `gorm:"column:tenant_id"`
	SessionID   string    `gorm:"column:session_id"`
	Title       string    `gorm:"column:title"`
	Content     string    `gorm:"column:content"`
	Status      string    `gorm:"column:status"`
	ContentHash string    `gorm:"column:content_hash"`
	ExpiresAt   time.Time `gorm:"column:expires_at"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (noteDraftRow) TableName() string { return "note_drafts" }

type workflowRunRow struct {
	ID                      string     `gorm:"column:id;primaryKey"`
	TenantID                uint64     `gorm:"column:tenant_id"`
	OwnerID                 uint64     `gorm:"column:owner_id"`
	WorkflowID              string     `gorm:"column:workflow_id"`
	DefinitionVersion       string     `gorm:"column:definition_version"`
	SourceType              *string    `gorm:"column:source_type"`
	SourceID                *string    `gorm:"column:source_id"`
	IdempotencyKey          string     `gorm:"column:idempotency_key"`
	Status                  string     `gorm:"column:status"`
	StateVersion            uint64     `gorm:"column:state_version"`
	CheckpointSchemaID      string     `gorm:"column:checkpoint_schema_id"`
	CheckpointSchemaVersion uint64     `gorm:"column:checkpoint_schema_version"`
	Checkpoint              []byte     `gorm:"column:checkpoint"`
	StepsExecuted           int        `gorm:"column:steps_executed"`
	ResumeCount             int        `gorm:"column:resume_count"`
	EventSequence           int64      `gorm:"column:event_sequence"`
	MaxSteps                int        `gorm:"column:max_steps"`
	MaxResumes              int        `gorm:"column:max_resumes"`
	Deadline                *time.Time `gorm:"column:deadline"`
	ClaimToken              *string    `gorm:"column:claim_token"`
	LeaseUntil              *time.Time `gorm:"column:lease_until"`
	CreatedAt               time.Time  `gorm:"column:created_at"`
	UpdatedAt               time.Time  `gorm:"column:updated_at"`
}

func (workflowRunRow) TableName() string { return "workflow_runs" }

type workflowWaitRow struct {
	WaitID            string     `gorm:"column:wait_id;primaryKey"`
	RunID             string     `gorm:"column:run_id"`
	NodeID            string     `gorm:"column:node_id"`
	Kind              string     `gorm:"column:kind"`
	WaitVersion       uint64     `gorm:"column:wait_version"`
	RecordVersion     uint64     `gorm:"column:record_version"`
	ContentHash       string     `gorm:"column:content_hash"`
	AllowedActions    []byte     `gorm:"column:allowed_actions"`
	PayloadRef        *string    `gorm:"column:payload_ref"`
	Status            string     `gorm:"column:status"`
	ExpiresAt         time.Time  `gorm:"column:expires_at"`
	ClaimToken        *string    `gorm:"column:claim_token"`
	LeaseUntil        *time.Time `gorm:"column:lease_until"`
	ResolvedAction    *string    `gorm:"column:resolved_action"`
	ResolvedActorType *string    `gorm:"column:resolved_actor_type"`
	ResolvedActorID   *string    `gorm:"column:resolved_actor_id"`
	ResolvedAt        *time.Time `gorm:"column:resolved_at"`
	CreatedAt         time.Time  `gorm:"column:created_at"`
	UpdatedAt         time.Time  `gorm:"column:updated_at"`
}

func (workflowWaitRow) TableName() string { return "workflow_waits" }

type workflowNodeEventRow struct {
	RunID       string    `gorm:"column:run_id;primaryKey"`
	Sequence    int64     `gorm:"column:sequence;primaryKey"`
	WorkflowID  string    `gorm:"column:workflow_id"`
	NodeID      string    `gorm:"column:node_id"`
	EventType   string    `gorm:"column:event_type"`
	RunStatus   string    `gorm:"column:run_status"`
	Attempt     int       `gorm:"column:attempt"`
	ResumeCount int       `gorm:"column:resume_count"`
	WaitID      *string   `gorm:"column:wait_id"`
	ErrorCode   *string   `gorm:"column:error_code"`
	DurationNS  int64     `gorm:"column:duration_ns"`
	OccurredAt  time.Time `gorm:"column:occurred_at"`
}

func (workflowNodeEventRow) TableName() string { return "workflow_node_events" }

type memoryRecordRow struct {
	ID              string     `gorm:"column:id;primaryKey"`
	TenantID        uint64     `gorm:"column:tenant_id"`
	UserID          uint64     `gorm:"column:user_id"`
	Layer           string     `gorm:"column:layer"`
	Kind            string     `gorm:"column:kind"`
	ScopeType       string     `gorm:"column:scope_type"`
	ScopeID         string     `gorm:"column:scope_id"`
	Namespace       string     `gorm:"column:namespace"`
	SlotKey         string     `gorm:"column:slot_key"`
	EntityType      string     `gorm:"column:entity_type"`
	EntityID        string     `gorm:"column:entity_id"`
	LineageID       string     `gorm:"column:lineage_id"`
	LineageVersion  uint64     `gorm:"column:lineage_version"`
	RowVersion      uint64     `gorm:"column:row_version"`
	CanonicalText   string     `gorm:"column:canonical_text"`
	StructuredValue []byte     `gorm:"column:structured_value"`
	ContentHash     string     `gorm:"column:content_hash"`
	Authority       string     `gorm:"column:authority"`
	Confidence      float64    `gorm:"column:confidence"`
	Salience        float64    `gorm:"column:salience"`
	SourceType      string     `gorm:"column:source_type"`
	SourceID        string     `gorm:"column:source_id"`
	EvidenceID      string     `gorm:"column:evidence_id"`
	Status          string     `gorm:"column:status"`
	SupersedesID    *string    `gorm:"column:supersedes_id"`
	SupersededBy    *string    `gorm:"column:superseded_by"`
	ExpiresAt       *time.Time `gorm:"column:expires_at"`
	CreatedAt       time.Time  `gorm:"column:created_at"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (memoryRecordRow) TableName() string { return "memory_records" }

type memoryEventRow struct {
	ID             string    `gorm:"column:id;primaryKey"`
	TenantID       uint64    `gorm:"column:tenant_id"`
	UserID         uint64    `gorm:"column:user_id"`
	MemoryID       string    `gorm:"column:memory_id"`
	EventType      string    `gorm:"column:event_type"`
	OldStatus      *string   `gorm:"column:old_status"`
	NewStatus      string    `gorm:"column:new_status"`
	Actor          string    `gorm:"column:actor"`
	ReasonCode     string    `gorm:"column:reason_code"`
	ExecutionID    string    `gorm:"column:execution_id"`
	InputHash      string    `gorm:"column:input_hash"`
	ResultMemoryID string    `gorm:"column:result_memory_id"`
	OccurredAt     time.Time `gorm:"column:occurred_at"`
}

func (memoryEventRow) TableName() string { return "memory_events" }

type memoryRelationRow struct {
	ID           string    `gorm:"column:id;primaryKey"`
	TenantID     uint64    `gorm:"column:tenant_id"`
	UserID       uint64    `gorm:"column:user_id"`
	FromMemoryID string    `gorm:"column:from_memory_id"`
	ToMemoryID   string    `gorm:"column:to_memory_id"`
	RelationType string    `gorm:"column:relation_type"`
	ReasonCode   string    `gorm:"column:reason_code"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (memoryRelationRow) TableName() string { return "memory_relations" }

type memoryProjectionRow struct {
	ID            string     `gorm:"column:id;primaryKey"`
	TenantID      uint64     `gorm:"column:tenant_id"`
	UserID        uint64     `gorm:"column:user_id"`
	MemoryID      string     `gorm:"column:memory_id"`
	ContentHash   string     `gorm:"column:content_hash"`
	ModelVersion  string     `gorm:"column:model_version"`
	Status        string     `gorm:"column:status"`
	Attempt       int        `gorm:"column:attempt"`
	AvailableAt   time.Time  `gorm:"column:available_at"`
	ClaimedAt     *time.Time `gorm:"column:claimed_at"`
	ProcessedAt   *time.Time `gorm:"column:processed_at"`
	LastErrorCode string     `gorm:"column:last_error_code"`
	CreatedAt     time.Time  `gorm:"column:created_at"`
}

func (memoryProjectionRow) TableName() string { return "memory_projection_outbox" }

type memoryEditPayloadRow struct {
	ID          string     `gorm:"column:id;primaryKey"`
	TenantID    uint64     `gorm:"column:tenant_id"`
	UserID      uint64     `gorm:"column:user_id"`
	DraftJSON   []byte     `gorm:"column:draft_json"`
	ContentHash string     `gorm:"column:content_hash"`
	Status      string     `gorm:"column:status"`
	ExpiresAt   time.Time  `gorm:"column:expires_at"`
	ConsumedAt  *time.Time `gorm:"column:consumed_at"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
}

func (memoryEditPayloadRow) TableName() string { return "memory_edit_payloads" }
