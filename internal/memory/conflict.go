package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type SemanticCandidate struct {
	MemoryID string
	Score    float64
}

func MergeCandidates(exact []Record, semantic []Record, refs []MemoryRef) []Record {
	byID := make(map[string]Record, len(exact)+len(semantic))
	for _, value := range semantic {
		byID[value.ID] = value
	}
	for _, value := range exact {
		byID[value.ID] = value
	}
	refOrder := map[string]int{}
	for i, ref := range refs {
		refOrder[ref.ID] = i + 1
	}
	out := make([]Record, 0, len(byID))
	for _, value := range byID {
		out = append(out, value)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := refOrder[out[i].ID], refOrder[out[j].ID]
		if ri != rj {
			if ri == 0 {
				return false
			}
			if rj == 0 {
				return true
			}
			return ri < rj
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}

type ProposedRelation string

const (
	ProposalDuplicate      ProposedRelation = "duplicate"
	ProposalRefinement     ProposedRelation = "refinement"
	ProposalCorrection     ProposedRelation = "correction"
	ProposalContradiction  ProposedRelation = "contradiction"
	ProposalTemporalChange ProposedRelation = "temporal_change"
	ProposalIndependent    ProposedRelation = "independent"
)

type RelationProposal struct {
	MemoryID            string           `json:"memory_id"`
	Relation            ProposedRelation `json:"relation"`
	ConflictFields      []string         `json:"conflict_fields,omitempty"`
	Confidence          float64          `json:"confidence"`
	ReasonCode          string           `json:"reason_code"`
	SuggestConfirmation bool             `json:"suggest_confirmation"`
}

func DecodeRelationProposals(raw []byte, allowedIDs map[string]struct{}, max int) ([]RelationProposal, error) {
	if len(raw) == 0 || len(raw) > 32*1024 || max <= 0 {
		return nil, ErrInvalidInput
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var values []RelationProposal
	if err := dec.Decode(&values); err != nil {
		return nil, fmt.Errorf("%w: relation proposal: %v", ErrInvalidInput, err)
	}
	if len(values) > max {
		return nil, fmt.Errorf("%w: too many relation proposals", ErrInvalidInput)
	}
	seen := map[string]struct{}{}
	for i := range values {
		v := &values[i]
		if _, ok := allowedIDs[v.MemoryID]; !ok {
			return nil, fmt.Errorf("%w: unknown candidate", ErrInvalidInput)
		}
		if _, ok := seen[v.MemoryID]; ok {
			return nil, fmt.Errorf("%w: duplicate candidate proposal", ErrInvalidInput)
		}
		seen[v.MemoryID] = struct{}{}
		if !validProposedRelation(v.Relation) || v.Confidence < 0 || v.Confidence > 1 || !validReasonCode(v.ReasonCode) || len(v.ConflictFields) > 16 {
			return nil, fmt.Errorf("%w: invalid relation proposal", ErrInvalidInput)
		}
		for _, field := range v.ConflictFields {
			if len(field) > 64 || strings.TrimSpace(field) == "" {
				return nil, ErrInvalidInput
			}
		}
	}
	return values, nil
}

func validProposedRelation(value ProposedRelation) bool {
	switch value {
	case ProposalDuplicate, ProposalRefinement, ProposalCorrection, ProposalContradiction, ProposalTemporalChange, ProposalIndependent:
		return true
	default:
		return false
	}
}

func validReasonCode(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return !sensitiveKey.MatchString(value)
}

type IntentAuthority string

const (
	IntentModelInference    IntentAuthority = "model_inference"
	IntentTrustedFact       IntentAuthority = "trusted_fact"
	IntentUserStatement     IntentAuthority = "user_statement"
	IntentUserCorrection    IntentAuthority = "user_correction"
	IntentPinnedWorkflowRef IntentAuthority = "pinned_workflow_ref"
)

type PolicyAction string

const (
	ActionAddCandidate PolicyAction = "add_candidate"
	ActionAddActive    PolicyAction = "add_active"
	ActionNoop         PolicyAction = "noop"
	ActionSupersede    PolicyAction = "supersede"
	ActionReview       PolicyAction = "review"
	ActionReject       PolicyAction = "reject"
	ActionIndependent  PolicyAction = "independent"
)

type PolicyInput struct {
	NewMemory Record
	Existing  Record
	Proposal  RelationProposal
	Intent    IntentAuthority
}

type PolicyDecision struct {
	Action         PolicyAction
	TargetMemoryID string
	NeedsReview    bool
	ReasonCode     string
}

func DecidePolicy(input PolicyInput) PolicyDecision {
	if input.NewMemory.ContentHash != "" && input.NewMemory.ContentHash == input.Existing.ContentHash {
		return PolicyDecision{Action: ActionNoop, TargetMemoryID: input.Existing.ID, ReasonCode: "content_duplicate"}
	}
	if !input.NewMemory.Entity.Empty() && !input.Existing.Entity.Empty() && input.NewMemory.Entity != input.Existing.Entity {
		return PolicyDecision{Action: ActionIndependent, ReasonCode: "different_entity"}
	}
	if input.Proposal.Relation == ProposalIndependent {
		return PolicyDecision{Action: ActionIndependent, ReasonCode: boundedReason(input.Proposal.ReasonCode)}
	}
	if input.Intent == IntentUserCorrection && (input.Proposal.Relation == ProposalCorrection || input.Proposal.Relation == ProposalTemporalChange) {
		return PolicyDecision{Action: ActionSupersede, TargetMemoryID: input.Existing.ID, ReasonCode: boundedReason(input.Proposal.ReasonCode)}
	}
	if input.Intent == IntentModelInference && input.Existing.Authority.Rank() >= AuthorityUserStated.Rank() && (input.Proposal.Relation == ProposalContradiction || input.Proposal.Relation == ProposalCorrection) {
		return PolicyDecision{Action: ActionReview, TargetMemoryID: input.Existing.ID, NeedsReview: true, ReasonCode: "low_authority_conflict"}
	}
	if input.Proposal.Confidence < .8 || input.Proposal.SuggestConfirmation {
		return PolicyDecision{Action: ActionReview, TargetMemoryID: input.Existing.ID, NeedsReview: true, ReasonCode: "ambiguous_relation"}
	}
	if input.Proposal.Relation == ProposalDuplicate {
		return PolicyDecision{Action: ActionNoop, TargetMemoryID: input.Existing.ID, ReasonCode: "semantic_duplicate"}
	}
	if input.Proposal.Relation == ProposalRefinement && input.NewMemory.Authority.Rank() >= input.Existing.Authority.Rank() {
		return PolicyDecision{Action: ActionSupersede, TargetMemoryID: input.Existing.ID, ReasonCode: "authoritative_refinement"}
	}
	return PolicyDecision{Action: ActionReview, TargetMemoryID: input.Existing.ID, NeedsReview: true, ReasonCode: "policy_review"}
}
