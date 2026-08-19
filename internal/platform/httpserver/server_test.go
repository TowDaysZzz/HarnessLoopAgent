package httpserver

import (
	"encoding/json"
	"testing"

	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

func TestHealthAndReadiness(t *testing.T) {
	server := New("127.0.0.1:0", func() bool { return true })
	tests := []struct {
		path   string
		status string
	}{
		{path: "/healthz", status: "ok"},
		{path: "/readyz", status: "ready"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			resp := ut.PerformRequest(server.Hertz().Engine, consts.MethodGet, tt.path, nil).Result()
			if resp.StatusCode() != consts.StatusOK {
				t.Fatalf("GET %s status = %d", tt.path, resp.StatusCode())
			}
			assertJSONStatus(t, resp.Body(), tt.status)
			if got := string(resp.Header.ContentType()); got != "application/json; charset=utf-8" {
				t.Fatalf("GET %s content-type = %q", tt.path, got)
			}
		})
	}
}

func TestReadinessUnavailable(t *testing.T) {
	server := New("127.0.0.1:0", func() bool { return false })
	resp := ut.PerformRequest(server.Hertz().Engine, consts.MethodGet, "/readyz", nil).Result()
	if resp.StatusCode() != consts.StatusServiceUnavailable {
		t.Fatalf("GET /readyz status = %d", resp.StatusCode())
	}
	assertJSONStatus(t, resp.Body(), "not_ready")
}

func TestHealthRejectsPost(t *testing.T) {
	server := New("127.0.0.1:0", func() bool { return true })
	resp := ut.PerformRequest(server.Hertz().Engine, consts.MethodPost, "/healthz", nil).Result()
	if resp.StatusCode() != consts.StatusMethodNotAllowed {
		t.Fatalf("POST /healthz status = %d, want %d", resp.StatusCode(), consts.StatusMethodNotAllowed)
	}
}

func assertJSONStatus(t *testing.T, body []byte, want string) {
	t.Helper()
	var payload struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode response %q: %v", body, err)
	}
	if payload.Status != want {
		t.Fatalf("status = %q, want %q", payload.Status, want)
	}
}
