package reminder

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

const (
	CommandPlanVersion    = "v1"
	MaxCommandPlanBytes   = 16 * 1024
	MaxMemorySelectors    = 8
	MaxClarificationBytes = 256
)

var promptInjectionText = regexp.MustCompile(`(?i)(ignore\s+(all\s+)?previous|system\s+prompt|developer\s+message|call\s+tool|忽略(之前|以上|系统)|系统提示词|调用工具|绕过权限)`)

type Action string

const (
	ActionCreate Action = "create"
	ActionQuery  Action = "query"
	ActionUpdate Action = "update"
	ActionCancel Action = "cancel"
)

type Trigger struct {
	Type     string `json:"type"`
	At       string `json:"at"`
	Timezone string `json:"timezone"`
}

type TargetSelector struct {
	Label    string   `json:"label,omitempty"`
	From     string   `json:"from,omitempty"`
	Until    string   `json:"until,omitempty"`
	Statuses []Status `json:"statuses,omitempty"`
}

type MemorySelector struct {
	Type        memory.SelectorType `json:"type"`
	Namespace   string              `json:"namespace,omitempty"`
	SlotKey     string              `json:"slot_key,omitempty"`
	Entity      memory.EntityRef    `json:"entity,omitempty"`
	ContentHash string              `json:"content_hash,omitempty"`
}

type Clarification struct {
	Needed   bool   `json:"needed"`
	Reason   string `json:"reason,omitempty"`
	Question string `json:"question,omitempty"`
}

type CommandPlan struct {
	Version         string           `json:"version"`
	Action          Action           `json:"action"`
	Content         string           `json:"content,omitempty"`
	Trigger         *Trigger         `json:"trigger,omitempty"`
	Target          *TargetSelector  `json:"target_selector,omitempty"`
	MemorySelectors []MemorySelector `json:"memory_selectors,omitempty"`
	Confidence      float64          `json:"confidence"`
	Clarification   *Clarification   `json:"clarification,omitempty"`
}

func DecodeCommandPlan(raw []byte, minConfidence float64) (CommandPlan, error) {
	if len(raw) == 0 || len(raw) > MaxCommandPlanBytes {
		return CommandPlan{}, fmt.Errorf("%w: command response size", ErrInvalidInput)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan CommandPlan
	if err := decoder.Decode(&plan); err != nil {
		return CommandPlan{}, fmt.Errorf("%w: decode reminder command: %v", ErrInvalidInput, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return CommandPlan{}, fmt.Errorf("%w: command must contain one JSON object", ErrInvalidInput)
	}
	if err := plan.Normalize(minConfidence); err != nil {
		return CommandPlan{}, err
	}
	return plan, nil
}

func (p *CommandPlan) Normalize(minConfidence float64) error {
	if p == nil || p.Version != CommandPlanVersion || minConfidence < 0 || minConfidence > 1 || p.Confidence < 0 || p.Confidence > 1 {
		return ErrInvalidInput
	}
	switch p.Action {
	case ActionCreate, ActionQuery, ActionUpdate, ActionCancel:
	default:
		return fmt.Errorf("%w: invalid reminder action", ErrInvalidInput)
	}
	if len(p.MemorySelectors) > MaxMemorySelectors {
		return fmt.Errorf("%w: too many memory selectors", ErrInvalidInput)
	}
	if p.Content != "" {
		normalized, err := NormalizeContent(p.Content)
		if err != nil {
			return err
		}
		if promptInjectionText.MatchString(normalized) {
			return fmt.Errorf("%w: prompt injection in content", ErrInvalidInput)
		}
		p.Content = normalized
	}
	for i := range p.MemorySelectors {
		if err := p.MemorySelectors[i].normalize(); err != nil {
			return fmt.Errorf("%w: memory selector %d", err, i)
		}
	}
	if p.Target != nil {
		if err := p.Target.normalize(); err != nil {
			return err
		}
	}
	if p.Clarification != nil {
		p.Clarification.Reason = strings.TrimSpace(p.Clarification.Reason)
		p.Clarification.Question = strings.TrimSpace(p.Clarification.Question)
		if len(p.Clarification.Reason) > MaxLabelBytes || len(p.Clarification.Question) > MaxClarificationBytes {
			return ErrInvalidInput
		}
	}
	switch p.Action {
	case ActionCreate:
		if p.Content == "" || p.Trigger == nil || p.Target != nil {
			return ErrInvalidInput
		}
	case ActionUpdate:
		if p.Content == "" || p.Trigger == nil || p.Target == nil {
			return ErrInvalidInput
		}
	case ActionCancel:
		if p.Content != "" || p.Trigger != nil || p.Target == nil || len(p.MemorySelectors) != 0 {
			return ErrInvalidInput
		}
	case ActionQuery:
		if p.Content != "" || p.Trigger != nil || p.Target == nil || len(p.MemorySelectors) != 0 {
			return ErrInvalidInput
		}
	}
	if p.Trigger != nil && (p.Trigger.Type != "at_time" || strings.TrimSpace(p.Trigger.At) == "" || p.Trigger.Timezone != DefaultTimezone) {
		return fmt.Errorf("%w: invalid trigger", ErrInvalidInput)
	}
	if p.Confidence < minConfidence {
		p.requireClarification("low_confidence", "请确认提醒内容和时间。")
	}
	return nil
}

func (p *CommandPlan) requireClarification(reason, question string) {
	if p.Clarification == nil {
		p.Clarification = &Clarification{}
	}
	p.Clarification.Needed = true
	if p.Clarification.Reason == "" {
		p.Clarification.Reason = reason
	}
	if p.Clarification.Question == "" {
		p.Clarification.Question = question
	}
}
func (p CommandPlan) Executable() bool { return p.Clarification == nil || !p.Clarification.Needed }

func ResolveTrigger(trigger Trigger, anchor time.Time, maxHorizon time.Duration) (time.Time, error) {
	if trigger.Type != "at_time" || trigger.Timezone != DefaultTimezone || anchor.IsZero() {
		return time.Time{}, ErrInvalidInput
	}
	location, err := time.LoadLocation(DefaultTimezone)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339, trigger.At)
	if err != nil {
		local, localErr := time.ParseInLocation("2006-01-02T15:04:05", trigger.At, location)
		if localErr != nil {
			return time.Time{}, fmt.Errorf("%w: invalid trigger time", ErrInvalidInput)
		}
		parsed = local
	}
	if _, offset := parsed.Zone(); offset != 8*60*60 {
		return time.Time{}, fmt.Errorf("%w: trigger offset does not match Asia/Shanghai", ErrInvalidInput)
	}
	if maxHorizon <= 0 {
		maxHorizon = DefaultMaxHorizon
	}
	utc := parsed.UTC()
	if !utc.After(anchor.UTC()) || utc.After(anchor.UTC().Add(maxHorizon)) {
		return time.Time{}, fmt.Errorf("%w: trigger outside allowed horizon", ErrInvalidInput)
	}
	return utc, nil
}

func (s *MemorySelector) normalize() error {
	if s == nil {
		return ErrInvalidInput
	}
	selector := memory.RecallSelector{Type: s.Type, Scope: memory.Scope{Type: memory.ScopeUser}, Namespace: s.Namespace, SlotKey: s.SlotKey, Entity: s.Entity, ContentHash: s.ContentHash}
	plan := memory.StructuredRecallPlan{Version: memory.StructuredRecallPlanVersion, Confidence: 1, Selectors: []memory.RecallSelector{selector}}
	if err := plan.Normalize(0); err != nil || !plan.Executable() {
		return ErrInvalidInput
	}
	value := plan.Selectors[0]
	s.Type, s.Namespace, s.SlotKey, s.Entity, s.ContentHash = value.Type, value.Namespace, value.SlotKey, value.Entity, value.ContentHash
	return nil
}

func (t *TargetSelector) normalize() error {
	if t == nil {
		return ErrInvalidInput
	}
	t.Label = strings.Join(strings.Fields(strings.TrimSpace(t.Label)), " ")
	if len(t.Label) > MaxLabelBytes || promptInjectionText.MatchString(t.Label) || len(t.Statuses) > 5 {
		return ErrInvalidInput
	}
	for _, status := range t.Statuses {
		if !status.Valid() {
			return ErrInvalidInput
		}
	}
	var from, until time.Time
	var err error
	if t.From != "" {
		from, err = time.Parse(time.RFC3339, t.From)
		if err != nil {
			return ErrInvalidInput
		}
	}
	if t.Until != "" {
		until, err = time.Parse(time.RFC3339, t.Until)
		if err != nil {
			return ErrInvalidInput
		}
	}
	if !from.IsZero() && !until.IsZero() && !from.Before(until) {
		return ErrInvalidInput
	}
	return nil
}
