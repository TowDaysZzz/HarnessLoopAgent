package ragclient

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

type responseEnvelope struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    RetrieveResponse `json:"data"`
}
