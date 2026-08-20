package tools

import (
	"context"
	"testing"
)

func TestRegistryEnforcesRolePolicy(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Definition{Name: "notes.delete", Roles: []string{"owner"}, Handler: func(context.Context, []byte) ([]byte, error) { return []byte("ok"), nil }}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Invoke(context.Background(), "notes.delete", "viewer", nil); err == nil {
		t.Fatal("viewer should be denied")
	}
	if result, err := r.Invoke(context.Background(), "notes.delete", "owner", nil); err != nil || string(result) != "ok" {
		t.Fatalf("owner invoke = %q, %v", result, err)
	}
}
