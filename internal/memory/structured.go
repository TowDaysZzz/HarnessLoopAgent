package memory

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	StructuredRecallPlanVersion = "v1"
	MaxRecallPlanBytes          = 16 * 1024
	MaxRecallSelectors          = 8
	MaxRecallFilterValues       = 8
	MaxTaxonomyValueLength      = 64
	MaxScopeIDLength            = 128
	MaxClarificationLength      = 256
)

var taxonomyValuePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

type SelectorType string

const (
	SelectorEntity      SelectorType = "entity"
	SelectorSlot        SelectorType = "slot"
	SelectorContentHash SelectorType = "content_hash"
	SelectorLocalScope  SelectorType = "local_scope"
)

type MatchSource string

const (
	MatchPinned   MatchSource = "pinned"
	MatchEntity   MatchSource = "entity"
	MatchSlot     MatchSource = "slot"
	MatchHash     MatchSource = "content_hash"
	MatchSemantic MatchSource = "semantic"
)

func (s MatchSource) Priority() int {
	switch s {
	case MatchPinned:
		return 4
	case MatchEntity:
		return 3
	case MatchSlot:
		return 2
	case MatchHash:
		return 1
	default:
		return 0
	}
}

type RecallSelector struct {
	Type        SelectorType `json:"type"`
	Scope       Scope        `json:"scope"`
	Namespace   string       `json:"namespace,omitempty"`
	SlotKey     string       `json:"slot_key,omitempty"`
	Entity      EntityRef    `json:"entity,omitempty"`
	ContentHash string       `json:"content_hash,omitempty"`
}

type RecallClarification struct {
	Needed   bool   `json:"needed"`
	Reason   string `json:"reason,omitempty"`
	Question string `json:"question,omitempty"`
}

type StructuredRecallPlan struct {
	Version       string               `json:"version"`
	Confidence    float64              `json:"confidence"`
	Layers        []Layer              `json:"layers,omitempty"`
	Kinds         []Kind               `json:"kinds,omitempty"`
	Selectors     []RecallSelector     `json:"selectors,omitempty"`
	Clarification *RecallClarification `json:"clarification,omitempty"`
}

func DecodeStructuredRecallPlan(raw []byte, minConfidence float64) (StructuredRecallPlan, error) {
	if len(raw) == 0 || len(raw) > MaxRecallPlanBytes {
		return StructuredRecallPlan{}, fmt.Errorf("%w: recall plan response size", ErrInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan StructuredRecallPlan
	if err := decoder.Decode(&plan); err != nil {
		return StructuredRecallPlan{}, fmt.Errorf("%w: decode recall plan: %v", ErrInvalidInput, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return StructuredRecallPlan{}, fmt.Errorf("%w: recall plan must contain one JSON object", ErrInvalidInput)
	}
	if err := plan.Normalize(minConfidence); err != nil {
		return StructuredRecallPlan{}, err
	}
	return plan, nil
}

func (p *StructuredRecallPlan) Normalize(minConfidence float64) error {
	if p == nil || p.Version != StructuredRecallPlanVersion || minConfidence < 0 || minConfidence > 1 || p.Confidence < 0 || p.Confidence > 1 {
		return fmt.Errorf("%w: recall plan version or confidence", ErrInvalidInput)
	}
	if len(p.Layers) > MaxRecallFilterValues || len(p.Kinds) > MaxRecallFilterValues || len(p.Selectors) > MaxRecallSelectors {
		return fmt.Errorf("%w: recall plan collection limit", ErrInvalidInput)
	}
	var err error
	p.Layers, err = normalizeLayers(p.Layers)
	if err != nil {
		return err
	}
	p.Kinds, err = normalizeKinds(p.Kinds)
	if err != nil {
		return err
	}
	entityKeys := map[string]struct{}{}
	stable := 0
	for i := range p.Selectors {
		if err := p.Selectors[i].normalize(); err != nil {
			return fmt.Errorf("%w: selector %d: %v", ErrInvalidInput, i, err)
		}
		stable++
		if p.Selectors[i].Type == SelectorEntity {
			entityKeys[p.Selectors[i].Entity.Type+"\x00"+p.Selectors[i].Entity.ID] = struct{}{}
		}
	}
	if p.Clarification != nil {
		p.Clarification.Reason = strings.TrimSpace(p.Clarification.Reason)
		p.Clarification.Question = strings.TrimSpace(p.Clarification.Question)
		if len(p.Clarification.Reason) > MaxTaxonomyValueLength || len(p.Clarification.Question) > MaxClarificationLength {
			return fmt.Errorf("%w: clarification too large", ErrInvalidInput)
		}
	}
	switch {
	case p.Confidence < minConfidence:
		p.requireClarification("low_confidence")
	case len(entityKeys) > 1:
		p.requireClarification("ambiguous_entities")
	case stable == 0:
		p.requireClarification("no_stable_selector")
	}
	return nil
}

func (p *StructuredRecallPlan) requireClarification(reason string) {
	if p.Clarification == nil {
		p.Clarification = &RecallClarification{}
	}
	p.Clarification.Needed = true
	if p.Clarification.Reason == "" {
		p.Clarification.Reason = reason
	}
}

func (p StructuredRecallPlan) Executable() bool {
	return len(p.Selectors) > 0 && (p.Clarification == nil || !p.Clarification.Needed)
}

func (s *RecallSelector) normalize() error {
	if s == nil {
		return ErrInvalidInput
	}
	scope, err := NormalizeScope(s.Scope)
	if err != nil {
		return err
	}
	s.Scope = scope
	s.Namespace, err = NormalizeNamespace(s.Namespace)
	if err != nil && s.Namespace != "" {
		return err
	}
	s.SlotKey, err = NormalizeSlotKey(s.SlotKey)
	if err != nil && s.SlotKey != "" {
		return err
	}
	if !s.Entity.Empty() {
		s.Entity, err = NormalizeEntityRef(s.Entity)
		if err != nil {
			return err
		}
	}
	s.ContentHash = strings.ToLower(strings.TrimSpace(s.ContentHash))
	switch s.Type {
	case SelectorEntity:
		if s.Entity.Empty() || s.Namespace != "" || s.SlotKey != "" || s.ContentHash != "" {
			return fmt.Errorf("entity selector has incompatible fields")
		}
	case SelectorSlot:
		if s.Namespace == "" || s.SlotKey == "" || !s.Entity.Empty() || s.ContentHash != "" {
			return fmt.Errorf("slot selector requires namespace and slot_key")
		}
	case SelectorContentHash:
		if !validSHA256(s.ContentHash) || s.Namespace != "" || s.SlotKey != "" || !s.Entity.Empty() {
			return fmt.Errorf("content_hash selector requires one SHA-256 hash")
		}
	case SelectorLocalScope:
		if s.Scope.Type == ScopeUser || s.Namespace != "" || s.SlotKey != "" || !s.Entity.Empty() || s.ContentHash != "" {
			return fmt.Errorf("local_scope selector requires session or workflow scope only")
		}
	default:
		return fmt.Errorf("unknown selector type %q", s.Type)
	}
	return nil
}

func NormalizeNamespace(value string) (string, error) {
	return normalizeTaxonomy(value, map[string]string{
		"user_profile": "profile", "profiles": "profile",
		"preference": "preferences", "user_preferences": "preferences",
		"goal": "goals", "task": "tasks", "reminder": "reminders",
	})
}

func NormalizeSlotKey(value string) (string, error) {
	return normalizeTaxonomy(value, nil)
}

func normalizeTaxonomy(value string, aliases map[string]string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	if alias, ok := aliases[value]; ok {
		value = alias
	}
	if value == "" || len(value) > MaxTaxonomyValueLength || !taxonomyValuePattern.MatchString(value) {
		return "", fmt.Errorf("%w: invalid taxonomy value", ErrInvalidInput)
	}
	return value, nil
}

func NormalizeEntityRef(ref EntityRef) (EntityRef, error) {
	typeName, err := normalizeTaxonomy(ref.Type, map[string]string{"todo": "task", "to_do": "task", "alarm": "reminder"})
	if err != nil {
		return EntityRef{}, err
	}
	id := strings.TrimSpace(ref.ID)
	if id == "" || len(id) > MaxScopeIDLength || strings.ContainsAny(id, "\r\n\t") {
		return EntityRef{}, fmt.Errorf("%w: invalid entity id", ErrInvalidInput)
	}
	return EntityRef{Type: typeName, ID: id}, nil
}

func NormalizeScope(scope Scope) (Scope, error) {
	scope.ID = strings.TrimSpace(scope.ID)
	if len(scope.ID) > MaxScopeIDLength || strings.ContainsAny(scope.ID, "\r\n\t") {
		return Scope{}, fmt.Errorf("%w: invalid scope id", ErrInvalidInput)
	}
	switch scope.Type {
	case ScopeUser:
		if scope.ID != "" {
			return Scope{}, fmt.Errorf("%w: user scope cannot have id", ErrInvalidInput)
		}
	case ScopeSession, ScopeWorkflow:
		if scope.ID == "" {
			return Scope{}, fmt.Errorf("%w: local scope requires id", ErrInvalidInput)
		}
	default:
		return Scope{}, fmt.Errorf("%w: unknown scope", ErrInvalidInput)
	}
	return scope, nil
}

func NormalizeLayer(value Layer) (Layer, error) {
	switch strings.ToLower(strings.TrimSpace(string(value))) {
	case "session":
		return LayerSession, nil
	case "long_term", "long-term", "longterm":
		return LayerLongTerm, nil
	default:
		return "", fmt.Errorf("%w: invalid persistent layer", ErrInvalidInput)
	}
}

func NormalizeKind(value Kind) (Kind, error) {
	normalized := Kind(strings.ToLower(strings.TrimSpace(string(value))))
	switch normalized {
	case KindPreference, KindFact, KindGoal, KindConstraint, KindSummary, KindOutcome:
		return normalized, nil
	default:
		return "", fmt.Errorf("%w: invalid kind", ErrInvalidInput)
	}
}

func normalizeLayers(values []Layer) ([]Layer, error) {
	seen := map[Layer]struct{}{}
	result := make([]Layer, 0, len(values))
	for _, value := range values {
		normalized, err := NormalizeLayer(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; !ok {
			seen[normalized] = struct{}{}
			result = append(result, normalized)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func normalizeKinds(values []Kind) ([]Kind, error) {
	seen := map[Kind]struct{}{}
	result := make([]Kind, 0, len(values))
	for _, value := range values {
		normalized, err := NormalizeKind(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; !ok {
			seen[normalized] = struct{}{}
			result = append(result, normalized)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result, nil
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

type MemoryDraft struct {
	Layer           Layer           `json:"layer"`
	Kind            Kind            `json:"kind"`
	Scope           Scope           `json:"scope"`
	Namespace       string          `json:"namespace"`
	SlotKey         string          `json:"slot_key,omitempty"`
	Entity          EntityRef       `json:"entity,omitempty"`
	CanonicalText   string          `json:"canonical_text"`
	StructuredValue StructuredValue `json:"structured_value"`
	ContentHash     string          `json:"content_hash"`
	Authority       Authority       `json:"authority"`
	Confidence      float64         `json:"confidence"`
	Salience        float64         `json:"salience"`
	Source          SourceRef       `json:"source"`
	ExpiresAt       *time.Time      `json:"expires_at,omitempty"`
}

func (d *MemoryDraft) Normalize() error {
	if d == nil {
		return ErrInvalidInput
	}
	var err error
	d.Layer, err = NormalizeLayer(d.Layer)
	if err != nil {
		return err
	}
	d.Kind, err = NormalizeKind(d.Kind)
	if err != nil {
		return err
	}
	d.Scope, err = NormalizeScope(d.Scope)
	if err != nil {
		return err
	}
	d.Namespace, err = NormalizeNamespace(d.Namespace)
	if err != nil {
		return err
	}
	if strings.TrimSpace(d.SlotKey) != "" {
		d.SlotKey, err = NormalizeSlotKey(d.SlotKey)
		if err != nil {
			return err
		}
	}
	if !d.Entity.Empty() {
		d.Entity, err = NormalizeEntityRef(d.Entity)
		if err != nil {
			return err
		}
	}
	if d.Layer == LayerLongTerm && d.Scope.Type != ScopeUser {
		return fmt.Errorf("%w: long-term draft requires user scope", ErrInvalidInput)
	}
	if d.Layer == LayerSession && d.Scope.Type == ScopeUser {
		return fmt.Errorf("%w: session draft requires local scope", ErrInvalidInput)
	}
	text, value, hash, err := NormalizeContent(d.CanonicalText, d.StructuredValue)
	if err != nil {
		return err
	}
	d.CanonicalText, d.StructuredValue, d.ContentHash = text, value, hash
	if d.Authority.Rank() == 0 || d.Confidence < 0 || d.Confidence > 1 || d.Salience < 0 || d.Salience > 1 {
		return fmt.Errorf("%w: invalid draft authority or scores", ErrInvalidInput)
	}
	return ValidateContent(d.CanonicalText, d.StructuredValue, d.Source)
}
