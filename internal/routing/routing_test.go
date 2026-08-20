package routing

import "testing"

func TestClassifierRoutesExplicitNoteIntents(t *testing.T) {
	c := Classifier{}
	if got := c.Classify("帮我记住：我偏好简洁回答"); got.Intent != IntentNoteCreate || !got.Deterministic {
		t.Fatalf("create route = %#v", got)
	}
	if got := c.Classify("查询我之前的垃圾回收记录"); got.Intent != IntentNoteQuery || !got.NeedsRAG {
		t.Fatalf("query route = %#v", got)
	}
	if got := c.Classify("删除这条笔记"); got.Intent != IntentNoteDelete {
		t.Fatalf("delete route = %#v", got)
	}
}
