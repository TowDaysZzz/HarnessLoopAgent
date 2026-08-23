package memory

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type RecallConfig struct {
	DefaultTarget   int
	MaxTarget       int
	PageSize        int
	MaxScanned      int
	MaxBatches      int
	MaxDuration     time.Duration
	MaxContextChars int
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
	Target          int
	MaxContextChars int
}

type RecallItem struct {
	Memory        Record
	SemanticScore float64
	RankScore     float64
	Exact         bool
	PromptData    string
}

type RecallResult struct {
	Items             []RecallItem
	Context           string
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
	if repository == nil || searcher == nil || config.DefaultTarget < 1 || config.MaxTarget < config.DefaultTarget || config.PageSize < 1 || config.PageSize > 200 || config.MaxScanned < config.PageSize || config.MaxBatches < 1 || config.MaxDuration <= 0 || config.MaxContextChars < 1 {
		return nil, ErrInvalidInput
	}
	return &RecallService{repository: repository, searcher: searcher, config: config}, nil
}

func (s *RecallService) Recall(ctx context.Context, request RecallRequest, now time.Time) (RecallResult, error) {
	if !request.Owner.Valid() || strings.TrimSpace(request.Query) == "" {
		return RecallResult{}, ErrInvalidInput
	}
	target := request.Target
	if target == 0 {
		target = s.config.DefaultTarget
	}
	if target < 1 || target > s.config.MaxTarget {
		return RecallResult{}, ErrInvalidInput
	}
	budget := request.MaxContextChars
	if budget == 0 {
		budget = s.config.MaxContextChars
	}
	if budget < 1 || budget > s.config.MaxContextChars {
		return RecallResult{}, ErrInvalidInput
	}
	callCtx, cancel := context.WithTimeout(ctx, s.config.MaxDuration)
	defer cancel()
	exactByID := map[string]Record{}
	if len(request.Pinned) > 0 {
		ids := make([]string, 0, len(request.Pinned))
		for _, ref := range request.Pinned {
			ids = append(ids, ref.ID)
		}
		values, err := s.repository.BatchGet(callCtx, request.Owner, ids)
		if err != nil {
			return RecallResult{}, err
		}
		for _, v := range values {
			for _, ref := range request.Pinned {
				if v.ID == ref.ID && v.LineageVersion == ref.LineageVersion && v.ContentHash == ref.ContentHash {
					exactByID[v.ID] = v
				}
			}
		}
	}
	exact, err := s.repository.FindExact(callCtx, ExactQuery{Owner: request.Owner, Scope: request.Scope, Namespace: request.Namespace, SlotKey: request.SlotKey, Entity: request.Entity, ContentHash: request.ContentHash, Limit: s.config.MaxTarget * 4})
	if err != nil {
		return RecallResult{}, err
	}
	for _, v := range exact {
		exactByID[v.ID] = v
	}
	candidates := map[string]*RecallItem{}
	result := RecallResult{}
	for id, v := range exactByID {
		if v.IsActiveAt(now) && visibleInScope(v, request.Scope) {
			candidates[id] = &RecallItem{Memory: v, Exact: true, SemanticScore: 1}
		} else {
			result.ObsoleteFiltered++
		}
	}
	cursor := ""
	seenVector := map[string]struct{}{}
	for batch := 0; batch < s.config.MaxBatches && len(candidates) < target && result.Scanned < s.config.MaxScanned; batch++ {
		limit := s.config.PageSize
		if remaining := s.config.MaxScanned - result.Scanned; limit > remaining {
			limit = remaining
		}
		layers := make([]ragclient.MemoryLayer, len(request.Layers))
		for i, v := range request.Layers {
			layers[i] = ragclient.MemoryLayer(v)
		}
		kinds := make([]ragclient.MemoryKind, len(request.Kinds))
		for i, v := range request.Kinds {
			kinds[i] = ragclient.MemoryKind(v)
		}
		page, searchErr := s.searcher.SearchMemory(ragclient.WithTrustedMemoryOwner(callCtx, request.Owner.TenantID, request.Owner.UserID), ragclient.MemorySearchRequest{Query: request.Query, Layers: layers, Kinds: kinds, Limit: limit, Cursor: cursor})
		if searchErr != nil {
			if callCtx.Err() != nil {
				result.DegradationReason = "time_budget_exceeded"
			} else {
				result.DegradationReason = "rag_unavailable"
			}
			break
		}
		ids := make([]string, 0, len(page.Candidates))
		scores := map[string]float64{}
		for _, candidate := range page.Candidates {
			result.Scanned++
			if _, ok := seenVector[candidate.MemoryID]; ok {
				continue
			}
			seenVector[candidate.MemoryID] = struct{}{}
			ids = append(ids, candidate.MemoryID)
			scores[candidate.MemoryID] = candidate.Score
		}
		values, err := s.repository.BatchGet(callCtx, request.Owner, ids)
		if err != nil {
			return RecallResult{}, err
		}
		found := map[string]Record{}
		for _, v := range values {
			found[v.ID] = v
		}
		for _, id := range ids {
			v, ok := found[id]
			if !ok {
				result.UnknownFiltered++
				continue
			}
			if !v.IsActiveAt(now) || !visibleInScope(v, request.Scope) {
				result.ObsoleteFiltered++
				continue
			}
			if existing, ok := candidates[id]; ok {
				if scores[id] > existing.SemanticScore {
					existing.SemanticScore = scores[id]
				}
				continue
			}
			candidates[id] = &RecallItem{Memory: v, SemanticScore: scores[id]}
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	if len(candidates) < target && result.DegradationReason == "" {
		switch {
		case result.Scanned >= s.config.MaxScanned:
			result.DegradationReason = "max_scan_reached"
		case callCtx.Err() != nil:
			result.DegradationReason = "time_budget_exceeded"
		case cursor != "":
			result.DegradationReason = "max_batches_reached"
		default:
			result.DegradationReason = "results_exhausted"
		}
	}
	items := make([]RecallItem, 0, len(candidates))
	for _, item := range candidates {
		item.RankScore = rankMemory(*item, now)
		items = append(items, *item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].RankScore == items[j].RankScore {
			return items[i].Memory.ID < items[j].Memory.ID
		}
		return items[i].RankScore > items[j].RankScore
	})
	if len(items) > target {
		result.Dropped += len(items) - target
		items = items[:target]
		result.Truncated = true
	}
	var builder strings.Builder
	selected := make([]RecallItem, 0, len(items))
	for _, item := range items {
		prompt := fmt.Sprintf("[UNTRUSTED_MEMORY id=%s lineage_version=%d]\n%s\n[/UNTRUSTED_MEMORY]\n", item.Memory.ID, item.Memory.LineageVersion, item.Memory.CanonicalText)
		remaining := budget - builder.Len()
		if remaining <= 0 {
			result.Dropped++
			result.Truncated = true
			continue
		}
		if len(prompt) > remaining {
			result.Dropped++
			result.Truncated = true
			continue
		}
		item.PromptData = prompt
		builder.WriteString(prompt)
		selected = append(selected, item)
	}
	result.Items, result.Context = selected, builder.String()
	if s.telemetry != nil {
		s.telemetry.ObserveRecall(result)
	}
	return result, nil
}

func visibleInScope(value Record, requested Scope) bool {
	if value.Layer == LayerLongTerm {
		return value.Scope.Type == ScopeUser
	}
	return value.Scope == requested
}

func rankMemory(item RecallItem, now time.Time) float64 {
	exact := 0.0
	if item.Exact {
		exact = 2
	}
	ageHours := now.Sub(item.Memory.CreatedAt).Hours()
	if ageHours < 0 {
		ageHours = 0
	}
	recency := math.Exp(-ageHours / (24 * 90))
	return exact + item.SemanticScore*.55 + float64(item.Memory.Authority.Rank())*.12 + item.Memory.Salience*.12 + recency*.05
}
