package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type RecallMode string

const (
	RecallModeExactOnly         RecallMode = "exact-only"
	RecallModeExactPlusSemantic RecallMode = "exact-plus-semantic"
)

type RecallConfig struct {
	Mode               RecallMode
	DefaultTarget      int
	MaxTarget          int
	PageSize           int
	MaxScanned         int
	MaxBatches         int
	MaxDuration        time.Duration
	MaxContextChars    int
	PlanMinConfidence  float64
	MaxExactCandidates int
}

type RecallRequest struct {
	Owner           Owner
	Query           string
	Scope           Scope
	Layers          []Layer
	Kinds           []Kind
	Namespace       string
	SlotKey         string
	Entity          EntityRef
	ContentHash     string
	Pinned          []MemoryRef
	Plan            *StructuredRecallPlan
	Target          int
	MaxContextChars int
}

type RecallItem struct {
	Memory        Record
	MatchSource   MatchSource
	SemanticScore float64
	RankScore     float64
	Exact         bool
	PromptData    string
}

type RecallResult struct {
	Mode              RecallMode
	HadSelector       bool
	Items             []RecallItem
	Context           string
	Clarification     *RecallClarification
	Scanned           int
	ObsoleteFiltered  int
	UnknownFiltered   int
	Truncated         bool
	Dropped           int
	DegradationReason string
}

type memorySearcher interface {
	SearchMemory(context.Context, ragclient.MemorySearchRequest) (*ragclient.MemorySearchResponse, error)
}

type RecallService struct {
	repository Repository
	searcher   memorySearcher
	config     RecallConfig
	telemetry  Telemetry
}

func (s *RecallService) SetTelemetry(telemetry Telemetry) {
	if s != nil {
		s.telemetry = telemetry
	}
}

func NewRecallService(repository Repository, searcher memorySearcher, config RecallConfig) (*RecallService, error) {
	if repository == nil || (config.Mode != RecallModeExactOnly && config.Mode != RecallModeExactPlusSemantic) || (config.Mode == RecallModeExactPlusSemantic && searcher == nil) || config.DefaultTarget < 1 || config.MaxTarget < config.DefaultTarget || config.PageSize < 1 || config.PageSize > 200 || config.MaxScanned < config.PageSize || config.MaxBatches < 1 || config.MaxDuration <= 0 || config.MaxContextChars < 1 || config.PlanMinConfidence < 0 || config.PlanMinConfidence > 1 {
		return nil, ErrInvalidInput
	}
	if config.MaxExactCandidates == 0 {
		config.MaxExactCandidates = config.MaxTarget * 4
	}
	if config.MaxExactCandidates < config.MaxTarget || config.MaxExactCandidates > 200 {
		return nil, ErrInvalidInput
	}
	return &RecallService{repository: repository, searcher: searcher, config: config}, nil
}

func (s *RecallService) Recall(ctx context.Context, request RecallRequest, now time.Time) (RecallResult, error) {
	result := RecallResult{Mode: s.config.Mode}
	if !request.Owner.Valid() || strings.TrimSpace(request.Query) == "" {
		return result, ErrInvalidInput
	}
	if _, err := NormalizeScope(request.Scope); err != nil {
		return result, err
	}
	target := request.Target
	if target == 0 {
		target = s.config.DefaultTarget
	}
	if target < 1 || target > s.config.MaxTarget {
		return result, ErrInvalidInput
	}
	budget := request.MaxContextChars
	if budget == 0 {
		budget = s.config.MaxContextChars
	}
	if budget < 1 || budget > s.config.MaxContextChars || len(request.Pinned) > MaxExactQuerySelectors {
		return result, ErrInvalidInput
	}

	callCtx, cancel := context.WithTimeout(ctx, s.config.MaxDuration)
	defer cancel()
	candidates := map[string]*RecallItem{}
	if request.Plan != nil {
		plan := cloneRecallPlan(*request.Plan)
		if err := plan.Normalize(s.config.PlanMinConfidence); err != nil {
			return result, err
		}
		result.HadSelector = len(request.Pinned) > 0 || len(plan.Selectors) > 0
		if !plan.Executable() {
			result.Clarification = plan.Clarification
			result.DegradationReason = "clarification_required"
			s.observe(result)
			return result, nil
		}
		if err := s.loadPinned(callCtx, request, now, candidates, &result); err != nil {
			return result, err
		}
		for _, selector := range plan.Selectors {
			query := ExactQuery{Owner: request.Owner, Scope: selector.Scope, Layers: plan.Layers, Kinds: plan.Kinds, ActiveAt: &now, Limit: s.config.MaxExactCandidates}
			source := MatchHash
			switch selector.Type {
			case SelectorEntity:
				query.Entities = []EntityRef{selector.Entity}
				source = MatchEntity
			case SelectorSlot:
				query.Slots = []SlotSelector{{Namespace: selector.Namespace, SlotKey: selector.SlotKey}}
				source = MatchSlot
			case SelectorContentHash:
				query.ContentHashes = []string{selector.ContentHash}
			case SelectorLocalScope:
				query.ScopeOnly = true
			default:
				return result, ErrInvalidInput
			}
			values, err := s.repository.FindExact(callCtx, query)
			if err != nil {
				return result, err
			}
			for _, value := range values {
				addRecallCandidate(candidates, value, source, 1)
			}
			if len(candidates) >= s.config.MaxExactCandidates {
				break
			}
		}
	} else {
		result.HadSelector = len(request.Pinned) > 0
		if err := s.loadPinned(callCtx, request, now, candidates, &result); err != nil {
			return result, err
		}
		query := ExactQuery{Owner: request.Owner, Scope: request.Scope, Layers: request.Layers, Kinds: request.Kinds, ActiveAt: &now, Namespace: request.Namespace, SlotKey: request.SlotKey, Entity: request.Entity, ContentHash: request.ContentHash, Limit: s.config.MaxExactCandidates}
		if query.HasSelector() {
			result.HadSelector = true
			values, err := s.repository.FindExact(callCtx, query)
			if err != nil {
				return result, err
			}
			for _, value := range values {
				addRecallCandidate(candidates, value, directMatchSource(value, request), 1)
			}
		}
	}

	if s.config.Mode == RecallModeExactPlusSemantic {
		s.loadSemantic(callCtx, request, now, target, candidates, &result)
	}
	if len(candidates) < target && result.DegradationReason == "" {
		result.DegradationReason = "results_exhausted"
	}
	items := make([]RecallItem, 0, len(candidates))
	for _, item := range candidates {
		item.RankScore = rankMemory(*item, now)
		items = append(items, *item)
	}
	sortRecallItems(items)
	if len(items) > target {
		result.Dropped += len(items) - target
		items = items[:target]
		result.Truncated = true
	}
	var builder strings.Builder
	usedChars := 0
	selected := make([]RecallItem, 0, len(items))
	for _, item := range items {
		prompt := fmt.Sprintf("[UNTRUSTED_MEMORY id=%s lineage_version=%d]\n%s\n[/UNTRUSTED_MEMORY]\n", item.Memory.ID, item.Memory.LineageVersion, item.Memory.CanonicalText)
		chars := utf8.RuneCountInString(prompt)
		if usedChars+chars > budget {
			result.Dropped++
			result.Truncated = true
			continue
		}
		item.PromptData = prompt
		builder.WriteString(prompt)
		usedChars += chars
		selected = append(selected, item)
	}
	result.Items, result.Context = selected, builder.String()
	s.observe(result)
	return result, nil
}

func (s *RecallService) loadPinned(ctx context.Context, request RecallRequest, now time.Time, candidates map[string]*RecallItem, result *RecallResult) error {
	if len(request.Pinned) == 0 {
		return nil
	}
	ids := make([]string, 0, len(request.Pinned))
	refs := map[string]MemoryRef{}
	for _, ref := range request.Pinned {
		if ref.ID == "" || ref.LineageVersion == 0 || !validSHA256(ref.ContentHash) {
			return ErrInvalidInput
		}
		ids = append(ids, ref.ID)
		refs[ref.ID] = ref
	}
	values, err := s.repository.BatchGet(ctx, request.Owner, ids)
	if err != nil {
		return err
	}
	for _, value := range values {
		ref, ok := refs[value.ID]
		if !ok || value.LineageVersion != ref.LineageVersion || value.ContentHash != ref.ContentHash || !value.IsActiveAt(now) || !visibleInScope(value, request.Scope) {
			result.ObsoleteFiltered++
			continue
		}
		addRecallCandidate(candidates, value, MatchPinned, 1)
	}
	return nil
}

func (s *RecallService) loadSemantic(ctx context.Context, request RecallRequest, now time.Time, target int, candidates map[string]*RecallItem, result *RecallResult) {
	cursor := ""
	seen := map[string]struct{}{}
	for batch := 0; batch < s.config.MaxBatches && len(candidates) < target && result.Scanned < s.config.MaxScanned; batch++ {
		limit := s.config.PageSize
		if remaining := s.config.MaxScanned - result.Scanned; limit > remaining {
			limit = remaining
		}
		layers := make([]ragclient.MemoryLayer, len(request.Layers))
		for i, value := range request.Layers {
			layers[i] = ragclient.MemoryLayer(value)
		}
		kinds := make([]ragclient.MemoryKind, len(request.Kinds))
		for i, value := range request.Kinds {
			kinds[i] = ragclient.MemoryKind(value)
		}
		page, err := s.searcher.SearchMemory(ragclient.WithTrustedMemoryOwner(ctx, request.Owner.TenantID, request.Owner.UserID), ragclient.MemorySearchRequest{Query: request.Query, Layers: layers, Kinds: kinds, Limit: limit, Cursor: cursor})
		if err != nil {
			if ctx.Err() != nil {
				result.DegradationReason = "time_budget_exceeded"
			} else {
				result.DegradationReason = "rag_unavailable"
			}
			return
		}
		ids := make([]string, 0, len(page.Candidates))
		scores := map[string]float64{}
		for _, candidate := range page.Candidates {
			result.Scanned++
			if _, ok := seen[candidate.MemoryID]; ok {
				continue
			}
			seen[candidate.MemoryID] = struct{}{}
			ids = append(ids, candidate.MemoryID)
			scores[candidate.MemoryID] = candidate.Score
		}
		values, err := s.repository.BatchGet(ctx, request.Owner, ids)
		if err != nil {
			result.DegradationReason = "repository_unavailable"
			return
		}
		found := map[string]Record{}
		for _, value := range values {
			found[value.ID] = value
		}
		for _, id := range ids {
			value, ok := found[id]
			if !ok {
				result.UnknownFiltered++
				continue
			}
			if !value.IsActiveAt(now) || !visibleInScope(value, request.Scope) {
				result.ObsoleteFiltered++
				continue
			}
			addRecallCandidate(candidates, value, MatchSemantic, scores[id])
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if result.DegradationReason == "" {
		switch {
		case result.Scanned >= s.config.MaxScanned:
			result.DegradationReason = "max_scan_reached"
		case ctx.Err() != nil:
			result.DegradationReason = "time_budget_exceeded"
		case cursor != "":
			result.DegradationReason = "max_batches_reached"
		}
	}
}

func addRecallCandidate(candidates map[string]*RecallItem, value Record, source MatchSource, semanticScore float64) {
	if existing, ok := candidates[value.ID]; ok {
		if source.Priority() > existing.MatchSource.Priority() {
			existing.MatchSource = source
			existing.Exact = source != MatchSemantic
		}
		if semanticScore > existing.SemanticScore {
			existing.SemanticScore = semanticScore
		}
		return
	}
	candidates[value.ID] = &RecallItem{Memory: value, MatchSource: source, SemanticScore: semanticScore, Exact: source != MatchSemantic}
}

func directMatchSource(value Record, request RecallRequest) MatchSource {
	if !request.Entity.Empty() && value.Entity == request.Entity {
		return MatchEntity
	}
	if request.Namespace != "" && request.SlotKey != "" && value.Namespace == request.Namespace && value.SlotKey == request.SlotKey {
		return MatchSlot
	}
	return MatchHash
}

func cloneRecallPlan(plan StructuredRecallPlan) StructuredRecallPlan {
	plan.Layers = append([]Layer(nil), plan.Layers...)
	plan.Kinds = append([]Kind(nil), plan.Kinds...)
	plan.Selectors = append([]RecallSelector(nil), plan.Selectors...)
	if plan.Clarification != nil {
		copy := *plan.Clarification
		plan.Clarification = &copy
	}
	return plan
}

func (s *RecallService) observe(result RecallResult) {
	if s.telemetry != nil {
		s.telemetry.ObserveRecall(result)
	}
}

func visibleInScope(value Record, requested Scope) bool {
	if value.Layer == LayerLongTerm {
		return value.Scope.Type == ScopeUser
	}
	return value.Scope == requested
}

func rankMemory(item RecallItem, now time.Time) float64 {
	age := now.Sub(item.Memory.CreatedAt).Hours()
	if age < 0 {
		age = 0
	}
	recency := 1 / (1 + age/(24*90))
	return float64(item.MatchSource.Priority()*100+item.Memory.Authority.Rank()*10) + item.Memory.Salience + recency/10
}

func sortRecallItems(items []RecallItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.MatchSource.Priority() != b.MatchSource.Priority() {
			return a.MatchSource.Priority() > b.MatchSource.Priority()
		}
		if a.Memory.Authority.Rank() != b.Memory.Authority.Rank() {
			return a.Memory.Authority.Rank() > b.Memory.Authority.Rank()
		}
		if a.Memory.Salience != b.Memory.Salience {
			return a.Memory.Salience > b.Memory.Salience
		}
		if !a.Memory.CreatedAt.Equal(b.Memory.CreatedAt) {
			return a.Memory.CreatedAt.After(b.Memory.CreatedAt)
		}
		return a.Memory.ID < b.Memory.ID
	})
}
