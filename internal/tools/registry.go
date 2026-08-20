package tools

import (
	"context"
	"errors"
	"sort"
)

type Definition struct {
	Name        string
	Description string
	Roles       []string
	ReadOnly    bool
	Handler     func(context.Context, []byte) ([]byte, error)
}

type Registry struct{ tools map[string]Definition }

func NewRegistry() *Registry { return &Registry{tools: make(map[string]Definition)} }

func (r *Registry) Register(def Definition) error {
	if def.Name == "" || def.Handler == nil {
		return errors.New("tool name and handler are required")
	}
	if _, exists := r.tools[def.Name]; exists {
		return errors.New("tool already registered")
	}
	r.tools[def.Name] = def
	return nil
}

func (r *Registry) Allowed(name, role string) bool {
	def, ok := r.tools[name]
	if !ok {
		return false
	}
	for _, allowed := range def.Roles {
		if allowed == role || allowed == "*" {
			return true
		}
	}
	return false
}

func (r *Registry) Invoke(ctx context.Context, name, role string, input []byte) ([]byte, error) {
	def, ok := r.tools[name]
	if !ok {
		return nil, errors.New("tool not found")
	}
	if !r.Allowed(name, role) {
		return nil, errors.New("tool permission denied")
	}
	return def.Handler(ctx, input)
}

func (r *Registry) Names() []string {
	result := make([]string, 0, len(r.tools))
	for name := range r.tools {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
