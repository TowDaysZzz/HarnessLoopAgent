package ragclient

import "time"

type MemoryLayer string
type MemoryKind string

type MemoryIndexRequest struct {
	MemoryID          string      `json:"memory_id"`
	CanonicalText     string      `json:"canonical_text"`
	ContentHash       string      `json:"content_hash"`
	Layer             MemoryLayer `json:"layer"`
	Kind              MemoryKind  `json:"kind"`
	CreatedAt         time.Time   `json:"created_at"`
	ProjectionVersion string      `json:"projection_version"`
}

type MemoryIndexResponse struct {
	MemoryID string `json:"memory_id"`
	Indexed  bool   `json:"indexed"`
	Reused   bool   `json:"reused"`
}

type MemorySearchRequest struct {
	Query  string        `json:"query"`
	Layers []MemoryLayer `json:"layers,omitempty"`
	Kinds  []MemoryKind  `json:"kinds,omitempty"`
	Limit  int           `json:"limit"`
	Cursor string        `json:"cursor,omitempty"`
}

type MemorySearchCandidate struct {
	MemoryID string  `json:"memory_id"`
	Score    float64 `json:"score"`
}

type MemorySearchResponse struct {
	RequestID  string                  `json:"request_id"`
	Candidates []MemorySearchCandidate `json:"candidates"`
	NextCursor string                  `json:"next_cursor,omitempty"`
}

type MemoryGenerationRequest struct {
	Generation        string `json:"generation"`
	EmbeddingModel    string `json:"embedding_model"`
	ProjectionVersion string `json:"projection_version"`
}

type MemoryGenerationResponse struct {
	Generation string `json:"generation"`
	Status     string `json:"status"`
}

type KnowledgeBase struct {
	ID          uint64    `json:"id"`
	TenantID    uint64    `json:"tenant_id"`
	UserID      uint64    `json:"user_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateKnowledgeBaseRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type KnowledgeBaseList struct {
	Items    []KnowledgeBase `json:"items"`
	Total    int64           `json:"total"`
	Page     int             `json:"page"`
	PageSize int             `json:"page_size"`
}

type RegisterRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	Name       string `json:"name"`
	TenantName string `json:"tenant_name,omitempty"`
}

type RegisterResponse struct {
	UserID   uint64 `json:"user_id"`
	Email    string `json:"email"`
	TenantID uint64 `json:"tenant_id"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	UserID       uint64 `json:"user_id"`
	Role         string `json:"role"`
	TenantID     uint64 `json:"tenant_id"`
}

type User struct {
	UserID     uint64 `json:"user_id"`
	Email      string `json:"email"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	TenantID   uint64 `json:"tenant_id"`
	TenantName string `json:"tenant_name"`
	CreatedAt  string `json:"created_at"`
}

type CreateNoteRequest struct {
	KBID           uint64   `json:"kb_id"`
	ExternalNoteID string   `json:"external_note_id"`
	Title          string   `json:"title"`
	Content        string   `json:"content"`
	Tags           []string `json:"tags,omitempty"`
	OccurredAt     string   `json:"occurred_at,omitempty"`
}

type CreateNoteResponse struct {
	DocumentID     uint64 `json:"document_id"`
	JobID          uint64 `json:"job_id"`
	ExternalNoteID string `json:"external_note_id"`
	Status         string `json:"status"`
	Reused         bool   `json:"reused"`
}

type NoteJobResponse struct {
	JobID          uint64 `json:"job_id"`
	DocumentID     uint64 `json:"document_id"`
	ExternalNoteID string `json:"external_note_id"`
	Status         string `json:"status"`
	ErrorCode      string `json:"error_code"`
	ErrorDetail    string `json:"error_detail"`
	ChunkCount     int    `json:"chunk_count"`
}

type DeleteNoteResponse struct {
	DocumentID          uint64 `json:"document_id"`
	ExternalNoteID      string `json:"external_note_id"`
	Deleted             bool   `json:"deleted"`
	VectorCleanupStatus string `json:"vector_cleanup_status"`
}

type RetrieveRequest struct {
	Query           string   `json:"query"`
	KBIDs           []uint64 `json:"kb_ids"`
	TopK            int      `json:"top_k"`
	StrategyProfile string   `json:"strategy_profile,omitempty"`
}

type RetrieveResponse struct {
	RequestID          string         `json:"request_id"`
	Items              []RetrieveItem `json:"items"`
	EvidenceGateResult string         `json:"evidence_gate_result,omitempty"`
	CitationCheck      *CitationCheck `json:"citation_check,omitempty"`
	Refusal            *Refusal       `json:"refusal,omitempty"`
}

type RetrieveItem struct {
	Content  string   `json:"content"`
	Score    float64  `json:"score"`
	Citation Citation `json:"citation"`
	Source   Source   `json:"source"`
}

type Citation struct {
	KBID          uint64 `json:"kb_id"`
	DocumentID    uint64 `json:"document_id"`
	ChunkID       string `json:"chunk_id"`
	FileName      string `json:"file_name"`
	ChunkIndex    int    `json:"chunk_index"`
	SnippetOffset int    `json:"snippet_offset,omitempty"`
}

type Source struct {
	Route            string `json:"route"`
	Collection       string `json:"collection"`
	RetrieverVersion string `json:"retriever_version"`
	SplitStrategy    string `json:"split_strategy,omitempty"`
	SplitVersion     string `json:"split_version,omitempty"`
	SourceFileType   string `json:"source_file_type,omitempty"`
	SectionTitle     string `json:"section_title,omitempty"`
	HierarchyPath    string `json:"hierarchy_path,omitempty"`
}

type CitationCheck struct {
	Supported             bool     `json:"supported"`
	SupportScore          float64  `json:"support_score"`
	UnsupportedClaims     []string `json:"unsupported_claims,omitempty"`
	UnsupportedClaimCount int      `json:"unsupported_claim_count"`
	Version               string   `json:"version,omitempty"`
	LatencyMS             int64    `json:"latency_ms,omitempty"`
	Error                 string   `json:"error,omitempty"`
}

type Refusal struct {
	Reason               string   `json:"reason"`
	Message              string   `json:"message"`
	Suggestions          []string `json:"suggestions,omitempty"`
	CitationSupportScore float64  `json:"citation_support_score,omitempty"`
}
