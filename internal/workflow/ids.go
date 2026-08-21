package workflow

import "strings"

type WorkflowID string
type WorkflowRunID string
type NodeID string
type WaitID string
type DefinitionVersion string

type SourceRef struct {
	Type string `json:"type,omitempty"`
	ID   string `json:"id,omitempty"`
}

func (s SourceRef) Validate() error {
	hasType := strings.TrimSpace(s.Type) != ""
	hasID := strings.TrimSpace(s.ID) != ""
	if hasType != hasID {
		return contractError("source reference requires both type and id", nil)
	}
	return nil
}

func validIdentifier(value string) bool { return strings.TrimSpace(value) != "" }
