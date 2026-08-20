package mcpfacade

import (
	"context"
	"testing"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/tools"
)

func TestFacadeListsAndCallsAuthorizedTool(t *testing.T) {
	registry := tools.NewRegistry()
	_ = registry.Register(tools.Definition{Name: "notes.search", Roles: []string{"owner"}, Handler: func(context.Context, []byte) ([]byte, error) { return []byte(`{"ok":true}`), nil }})
	facade, err := New(registry)
	if err != nil {
		t.Fatal(err)
	}
	response := string(facade.Handle(context.Background(), "owner", []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"notes.search","arguments":{}}}`)))
	if response == "" || response == "{}" {
		t.Fatalf("response = %s", response)
	}
}
