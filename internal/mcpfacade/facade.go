package mcpfacade

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/tools"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type Response struct {
	JSONRPC string `json:"jsonrpc"`
	ID      any    `json:"id,omitempty"`
	Result  any    `json:"result,omitempty"`
	Error   *Error `json:"error,omitempty"`
}

type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Facade struct{ registry *tools.Registry }

func New(registry *tools.Registry) (*Facade, error) {
	if registry == nil {
		return nil, errors.New("tool registry is required")
	}
	return &Facade{registry: registry}, nil
}

func (f *Facade) Handle(ctx context.Context, role string, body []byte) []byte {
	var request Request
	if err := json.Unmarshal(body, &request); err != nil {
		return marshal(Response{JSONRPC: "2.0", Error: &Error{Code: -32700, Message: "invalid JSON"}})
	}
	response := Response{JSONRPC: "2.0", ID: request.ID}
	switch request.Method {
	case "tools/list":
		response.Result = map[string]any{"tools": f.registry.Names()}
	case "tools/call":
		var params struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || params.Name == "" {
			response.Error = &Error{Code: -32602, Message: "name and arguments are required"}
			break
		}
		result, err := f.registry.Invoke(ctx, params.Name, role, params.Arguments)
		if err != nil {
			response.Error = &Error{Code: -32003, Message: err.Error()}
			break
		}
		response.Result = json.RawMessage(result)
	default:
		response.Error = &Error{Code: -32601, Message: fmt.Sprintf("method %q not found", request.Method)}
	}
	return marshal(response)
}

func marshal(value Response) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
