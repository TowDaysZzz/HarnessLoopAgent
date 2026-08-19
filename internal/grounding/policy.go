package grounding

import (
	"strings"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type Policy struct {
	RequireEvidenceGate     bool
	RequireCitationCheck    bool
	MinResults              int
	MinTopScore             float64
	MinItemScore            float64
	RequireCompleteCitation bool
	MaxContextChars         int
	RejectPromptInjection   bool
}

type Observation struct {
	Usable             bool                     `json:"usable"`
	Reason             string                   `json:"reason,omitempty"`
	RequestID          string                   `json:"request_id,omitempty"`
	Items              []ragclient.RetrieveItem `json:"items"`
	EvidenceGateResult string                   `json:"evidence_gate_result,omitempty"`
	CitationCheck      *ragclient.CitationCheck `json:"citation_check,omitempty"`
	Refusal            *ragclient.Refusal       `json:"refusal,omitempty"`
	DroppedItems       int                      `json:"dropped_items,omitempty"`
}

func (p Policy) Evaluate(response *ragclient.RetrieveResponse) Observation {
	p = p.withDefaults()
	observation := Observation{Items: []ragclient.RetrieveItem{}}
	if response == nil {
		observation.Reason = "missing_response"
		return observation
	}
	observation.RequestID = strings.TrimSpace(response.RequestID)
	observation.EvidenceGateResult = strings.TrimSpace(response.EvidenceGateResult)
	observation.CitationCheck = response.CitationCheck
	observation.Refusal = response.Refusal
	if observation.RequestID == "" {
		observation.Reason = "missing_request_id"
		return observation
	}
	if response.Refusal != nil || strings.EqualFold(observation.EvidenceGateResult, "refused") {
		observation.Reason = "rag_refused"
		return observation
	}
	if p.RequireEvidenceGate && !strings.EqualFold(observation.EvidenceGateResult, "pass") {
		observation.Reason = "evidence_gate_not_passed"
		return observation
	}
	if p.RequireCitationCheck && (response.CitationCheck == nil || !response.CitationCheck.Supported) {
		observation.Reason = "citation_check_not_supported"
		return observation
	}
	if response.CitationCheck != nil && !response.CitationCheck.Supported {
		observation.Reason = "citation_check_failed"
		return observation
	}

	remaining := p.MaxContextChars
	for _, item := range response.Items {
		if item.Score < p.MinItemScore || strings.TrimSpace(item.Content) == "" {
			observation.DroppedItems++
			continue
		}
		if p.RequireCompleteCitation && !completeCitation(item.Citation) {
			observation.DroppedItems++
			continue
		}
		if p.RejectPromptInjection && containsPromptInjection(item.Content) {
			observation.DroppedItems++
			continue
		}
		if remaining <= 0 {
			observation.DroppedItems++
			continue
		}
		item.Content, remaining = truncate(item.Content, remaining)
		observation.Items = append(observation.Items, item)
	}
	if len(observation.Items) < p.MinResults {
		observation.Reason = "insufficient_results"
		return observation
	}
	if observation.Items[0].Score < p.MinTopScore {
		observation.Reason = "top_score_below_threshold"
		observation.Items = []ragclient.RetrieveItem{}
		return observation
	}
	observation.Usable = true
	return observation
}

func (p Policy) withDefaults() Policy {
	if p.MinResults < 1 {
		p.MinResults = 1
	}
	if p.MaxContextChars < 1 {
		p.MaxContextChars = 24000
	}
	return p
}

func completeCitation(citation ragclient.Citation) bool {
	return citation.KBID > 0 && citation.DocumentID > 0 && strings.TrimSpace(citation.ChunkID) != "" && strings.TrimSpace(citation.FileName) != ""
}

func containsPromptInjection(content string) bool {
	normalized := strings.ToLower(content)
	for _, marker := range []string{
		"ignore previous instructions", "ignore all previous", "system prompt", "developer message",
		"忽略之前的指令", "忽略以上指令", "忽略系统提示", "执行以下指令",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func truncate(content string, remaining int) (string, int) {
	runes := []rune(content)
	if len(runes) <= remaining {
		return content, remaining - len(runes)
	}
	return string(runes[:remaining]) + "\n[内容因上下文预算被截断]", 0
}
