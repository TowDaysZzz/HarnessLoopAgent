package reminderworkflow

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/workflow"
)

func TestCommandCodecBoundsAndCredentialRejection(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	valid := CommandData{Owner: reminder.Owner{TenantID: 1, UserID: 2}, Query: "提醒我明天九点提交周报", ReceivedAt: now}
	codec := CommandCodec{MaxBytes: 8192}
	raw, err := codec.Encode(valid)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := codec.Decode(raw)
	if err != nil || decoded.Query != valid.Query {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}

	credential := valid
	credential.Query = "Authorization: Bearer secret-token-value"
	if _, err := codec.Encode(credential); !errors.Is(err, ErrInvalidCommandData) {
		t.Fatalf("credential err=%v", err)
	}
	history := valid
	history.Query = strings.Repeat("完整聊天历史", 1000)
	if _, err := codec.Encode(history); !errors.Is(err, ErrInvalidCommandData) {
		t.Fatalf("history err=%v", err)
	}
	oversized := CommandCodec{MaxBytes: 32}
	if _, err := oversized.Encode(valid); !errors.Is(err, ErrInvalidCommandData) {
		t.Fatalf("oversized err=%v", err)
	}
	unknown := append(raw[:len(raw)-1], []byte(`,"unknown":"value"}`)...)
	if _, err := codec.Decode(unknown); err == nil {
		t.Fatal("unknown checkpoint field accepted")
	}
}

func TestReminderWorkflowNodeOrder(t *testing.T) {
	evaluator := &Evaluator{}
	nodes := NewNodes(evaluator, ReviewNode{TTL: time.Hour}, CommitNode{})
	if err := ValidateNodeOrder(nodes); err != nil {
		t.Fatal(err)
	}
	nodes[0], nodes[1] = nodes[1], nodes[0]
	if err := ValidateNodeOrder(nodes); !errors.Is(err, ErrInvalidCommandData) {
		t.Fatalf("order err=%v", err)
	}
	if _, err := workflow.NewRunner(NewNodes(evaluator, ReviewNode{TTL: time.Hour}, CommitNode{}), workflow.RunnerOptions{}); err != nil {
		t.Fatal(err)
	}
}
