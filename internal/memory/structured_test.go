package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestDecodeStructuredRecallPlanStrictBoundary(t *testing.T) {
	valid := `{"version":"v1","confidence":0.9,"layers":["long_term"],"kinds":["preference"],"selectors":[{"type":"slot","scope":{"type":"user"},"namespace":"user_profile","slot_key":"Favorite-Drink"}]}`
	plan, err := DecodeStructuredRecallPlan([]byte(valid), .8)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Executable() || plan.Selectors[0].Namespace != "profile" || plan.Selectors[0].SlotKey != "favorite_drink" {
		t.Fatalf("normalized plan = %+v", plan)
	}

	for name, field := range map[string]string{
		"owner":      `,"owner":{"tenant_id":1,"user_id":2}`,
		"sql":        `,"sql":"select * from memories"`,
		"status":     `,"status":"active"`,
		"memory id":  `,"memory_id":"mem-other"`,
		"visibility": `,"visibility":"all"`,
		"unknown":    `,"unexpected":true`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := strings.TrimSuffix(valid, "}") + field + "}"
			if _, err := DecodeStructuredRecallPlan([]byte(raw), .8); err == nil {
				t.Fatal("expected strict decoder rejection")
			}
		})
	}
}

func TestStructuredRecallPlanBoundsAndClarification(t *testing.T) {
	tests := []struct {
		name       string
		plan       StructuredRecallPlan
		executable bool
		reason     string
		wantErr    bool
	}{
		{name: "no selector", plan: StructuredRecallPlan{Version: "v1", Confidence: 1}, reason: "no_stable_selector"},
		{name: "low confidence", plan: StructuredRecallPlan{Version: "v1", Confidence: .2, Selectors: []RecallSelector{{Type: SelectorContentHash, Scope: Scope{Type: ScopeUser}, ContentHash: testHash}}}, reason: "low_confidence"},
		{name: "multiple entities", plan: StructuredRecallPlan{Version: "v1", Confidence: 1, Selectors: []RecallSelector{{Type: SelectorEntity, Scope: Scope{Type: ScopeUser}, Entity: EntityRef{Type: "task", ID: "1"}}, {Type: SelectorEntity, Scope: Scope{Type: ScopeUser}, Entity: EntityRef{Type: "task", ID: "2"}}}}, reason: "ambiguous_entities"},
		{name: "one entity", plan: StructuredRecallPlan{Version: "v1", Confidence: 1, Selectors: []RecallSelector{{Type: SelectorEntity, Scope: Scope{Type: ScopeUser}, Entity: EntityRef{Type: "todo", ID: "1"}}}}, executable: true},
		{name: "unknown selector", plan: StructuredRecallPlan{Version: "v1", Confidence: 1, Selectors: []RecallSelector{{Type: "scan", Scope: Scope{Type: ScopeUser}}}}, wantErr: true},
		{name: "invalid confidence", plan: StructuredRecallPlan{Version: "v1", Confidence: 2}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.plan.Normalize(.8)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.plan.Executable() != tt.executable {
				t.Fatalf("executable=%v plan=%+v", tt.plan.Executable(), tt.plan)
			}
			if tt.reason != "" && (tt.plan.Clarification == nil || tt.plan.Clarification.Reason != tt.reason) {
				t.Fatalf("clarification=%+v", tt.plan.Clarification)
			}
		})
	}

	tooMany := StructuredRecallPlan{Version: "v1", Confidence: 1, Selectors: make([]RecallSelector, MaxRecallSelectors+1)}
	if err := tooMany.Normalize(.8); err == nil {
		t.Fatal("expected selector limit")
	}
	if _, err := DecodeStructuredRecallPlan([]byte(`{"version":"v1","confidence":1} {}`), .8); err == nil {
		t.Fatal("expected trailing JSON rejection")
	}
}

func TestSharedTaxonomyNormalization(t *testing.T) {
	for name, test := range map[string]struct {
		input     string
		want      string
		namespace bool
		wantErr   bool
	}{
		"namespace alias": {input: " User-Profile ", want: "profile", namespace: true},
		"slot normalized": {input: "Favorite Drink", want: "favorite_drink"},
		"empty":           {input: "", wantErr: true},
		"too long":        {input: strings.Repeat("a", MaxTaxonomyValueLength+1), wantErr: true},
		"punctuation":     {input: "bad/value", wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			var got string
			var err error
			if test.namespace {
				got, err = NormalizeNamespace(test.input)
			} else {
				got, err = NormalizeSlotKey(test.input)
			}
			if test.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got=%q err=%v", got, err)
			}
		})
	}

	entity, err := NormalizeEntityRef(EntityRef{Type: "todo", ID: "task-7"})
	if err != nil || entity != (EntityRef{Type: "task", ID: "task-7"}) {
		t.Fatalf("entity=%+v err=%v", entity, err)
	}
	if _, err := NormalizeEntityRef(EntityRef{Type: "task"}); err == nil {
		t.Fatal("expected empty entity id rejection")
	}
	if _, err := NormalizeScope(Scope{Type: ScopeSession}); err == nil {
		t.Fatal("expected session id rejection")
	}
}

func TestMemoryDraftUsesSharedNormalizationAndRecomputesHash(t *testing.T) {
	draft := MemoryDraft{Layer: "long-term", Kind: KindPreference, Scope: Scope{Type: ScopeUser}, Namespace: "user_profile", SlotKey: "Favorite Drink", Entity: EntityRef{Type: "todo", ID: "7"}, CanonicalText: " tea ", StructuredValue: StructuredValue{Schema: "preference", Version: 1, Data: map[string]any{"value": "tea"}}, ContentHash: testHash, Authority: AuthorityUserStated, Confidence: 1, Salience: .8, Source: SourceRef{Type: "workflow", ID: "capture"}}
	if err := draft.Normalize(); err != nil {
		t.Fatal(err)
	}
	if draft.Namespace != "profile" || draft.SlotKey != "favorite_drink" || draft.Entity.Type != "task" || draft.ContentHash == testHash {
		t.Fatalf("draft=%+v", draft)
	}
	raw, _ := json.Marshal(draft.StructuredValue)
	if len(raw) == 0 {
		t.Fatal("expected structured JSON")
	}
}
