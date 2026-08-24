package reminder

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

const (
	MaxMemorySummaryChars = 160
	memoryRecallQuery     = "exact reminder memory association"
)

var sensitiveMemorySummary = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/-]{8,}|access[_-]?token|refresh[_-]?token|authorization\s*[:=]|cookie\s*[:=]|password\s*[:=]|api[_-]?key\s*[:=]|-----begin [a-z ]*private key-----)`)

// MemoryRecallPlan keeps trusted pinned references outside of the model-produced
// StructuredRecallPlan. This lets a UI supply a fixed reference without granting
// the LLM permission to invent arbitrary Memory IDs.
type MemoryRecallPlan struct {
	Plan   memory.StructuredRecallPlan
	Pinned []memory.MemoryRef
}

func BuildMemoryRecallPlan(selectors []MemorySelector, trusted []MemoryRef) (MemoryRecallPlan, error) {
	if len(selectors)+len(trusted) == 0 || len(selectors)+len(trusted) > MaxMemoryRefs {
		return MemoryRecallPlan{}, ErrInvalidInput
	}
	result := MemoryRecallPlan{Plan: memory.StructuredRecallPlan{Version: memory.StructuredRecallPlanVersion, Confidence: 1}}
	for _, selector := range selectors {
		copySelector := selector
		if err := copySelector.normalize(); err != nil {
			return MemoryRecallPlan{}, err
		}
		result.Plan.Selectors = append(result.Plan.Selectors, memory.RecallSelector{
			Type: copySelector.Type, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: copySelector.Namespace,
			SlotKey: copySelector.SlotKey, Entity: copySelector.Entity, ContentHash: copySelector.ContentHash,
		})
	}
	seen := make(map[string]struct{}, len(trusted))
	for _, ref := range trusted {
		if err := ref.Validate(); err != nil {
			return MemoryRecallPlan{}, err
		}
		if _, exists := seen[ref.ID]; exists {
			return MemoryRecallPlan{}, ErrInvalidInput
		}
		seen[ref.ID] = struct{}{}
		result.Pinned = append(result.Pinned, memory.MemoryRef{ID: ref.ID, LineageVersion: ref.LineageVersion, ContentHash: strings.ToLower(ref.ContentHash)})
	}
	if len(result.Plan.Selectors) > 0 {
		if err := result.Plan.Normalize(0); err != nil || !result.Plan.Executable() {
			return MemoryRecallPlan{}, ErrInvalidInput
		}
	}
	return result, nil
}

type MemoryRecallPort interface {
	Recall(context.Context, memory.RecallRequest, time.Time) (memory.RecallResult, error)
}

type MemoryAssociation struct {
	Refs          []MemoryRef
	Summaries     []MemorySummary
	Clarification *Clarification
}

// ResolveMemoryAssociation only accepts exact-only recall results and never
// silently selects one result from an ambiguous selector.
func ResolveMemoryAssociation(ctx context.Context, recall MemoryRecallPort, owner Owner, plan MemoryRecallPlan, now time.Time) (MemoryAssociation, error) {
	if recall == nil || !owner.Valid() || now.IsZero() || (len(plan.Plan.Selectors) == 0 && len(plan.Pinned) == 0) {
		return MemoryAssociation{}, ErrInvalidInput
	}
	request := memory.RecallRequest{
		Owner: memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID}, Query: memoryRecallQuery,
		Scope: memory.Scope{Type: memory.ScopeUser}, Pinned: append([]memory.MemoryRef(nil), plan.Pinned...),
		Target: MaxMemoryRefs, MaxContextChars: MaxMemoryRefs * (MaxMemorySummaryChars + 128),
	}
	if len(plan.Plan.Selectors) > 0 {
		copyPlan := plan.Plan
		request.Plan = &copyPlan
	}
	result, err := recall.Recall(ctx, request, now)
	if err != nil {
		return MemoryAssociation{}, err
	}
	if result.Mode != memory.RecallModeExactOnly {
		return MemoryAssociation{}, fmt.Errorf("%w: semantic memory recall is forbidden", ErrInvalidInput)
	}
	expected := len(plan.Plan.Selectors) + len(plan.Pinned)
	if len(result.Items) == 0 {
		return MemoryAssociation{Clarification: &Clarification{Needed: true, Reason: "memory_not_found", Question: "没有找到可精确关联的记忆，请确认选择条件。"}}, nil
	}
	if len(result.Items) != expected || result.ObsoleteFiltered > 0 || result.Truncated || result.Dropped > 0 {
		return MemoryAssociation{Clarification: &Clarification{Needed: true, Reason: "memory_ambiguous_or_unavailable", Question: "记忆关联不唯一或已不可用，请选择明确的记忆。"}}, nil
	}
	association := MemoryAssociation{Refs: make([]MemoryRef, 0, len(result.Items)), Summaries: make([]MemorySummary, 0, len(result.Items))}
	for _, item := range result.Items {
		ref := MemoryRef{ID: item.Memory.ID, LineageVersion: item.Memory.LineageVersion, ContentHash: item.Memory.ContentHash}
		if err := ref.Validate(); err != nil || !item.Memory.IsActiveAt(now) {
			return MemoryAssociation{Clarification: &Clarification{Needed: true, Reason: "memory_unavailable", Question: "关联记忆已不可用，请重新选择。"}}, nil
		}
		association.Refs = append(association.Refs, ref)
		association.Summaries = append(association.Summaries, BuildMemorySummary(item.Memory, MaxMemorySummaryChars))
	}
	sort.Slice(association.Refs, func(i, j int) bool { return association.Refs[i].ID < association.Refs[j].ID })
	sort.Slice(association.Summaries, func(i, j int) bool { return association.Summaries[i].ID < association.Summaries[j].ID })
	return association, nil
}

// ValidatePinnedMemoryRefs re-loads exact IDs under the authenticated owner
// immediately before commit and requires the pinned version/hash to remain active.
func ValidatePinnedMemoryRefs(ctx context.Context, repository memory.Repository, owner Owner, refs []MemoryRef, now time.Time) error {
	if repository == nil || !owner.Valid() || now.IsZero() || len(refs) > MaxMemoryRefs {
		return ErrInvalidInput
	}
	if len(refs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(refs))
	wanted := make(map[string]MemoryRef, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if _, duplicate := wanted[ref.ID]; duplicate {
			return ErrInvalidInput
		}
		wanted[ref.ID] = ref
		ids = append(ids, ref.ID)
	}
	values, err := repository.BatchGet(ctx, memory.Owner{TenantID: owner.TenantID, UserID: owner.UserID}, ids)
	if err != nil {
		return err
	}
	if len(values) != len(refs) {
		return ErrNotFound
	}
	for _, value := range values {
		ref, ok := wanted[value.ID]
		if !ok || value.LineageVersion != ref.LineageVersion || value.ContentHash != ref.ContentHash || !value.IsActiveAt(now) {
			return ErrStateConflict
		}
	}
	return nil
}

type MemorySummary struct {
	ID             string `json:"id"`
	LineageVersion uint64 `json:"lineage_version"`
	ContentHash    string `json:"content_hash"`
	UntrustedText  string `json:"untrusted_text"`
}

func BuildMemorySummary(value memory.Record, maxChars int) MemorySummary {
	if maxChars < 1 || maxChars > MaxMemorySummaryChars {
		maxChars = MaxMemorySummaryChars
	}
	text := strings.Join(strings.Fields(strings.TrimSpace(value.CanonicalText)), " ")
	if sensitiveMemorySummary.MatchString(text) {
		text = "[redacted_sensitive_memory]"
	} else if utf8.RuneCountInString(text) > maxChars {
		text = string([]rune(text)[:maxChars]) + "…"
	}
	return MemorySummary{ID: value.ID, LineageVersion: value.LineageVersion, ContentHash: value.ContentHash, UntrustedText: "[UNTRUSTED_MEMORY_SUMMARY] " + text + " [/UNTRUSTED_MEMORY_SUMMARY]"}
}
