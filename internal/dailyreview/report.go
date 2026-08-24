package dailyreview

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

const ReportSchemaV1 = "daily_review_report_v1"

var ErrInvalidReport = errors.New("invalid daily review report")

type EvidenceRef struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Version uint64 `json:"version"`
	Hash    string `json:"hash"`
}
type Fact struct {
	Text     string        `json:"text"`
	Evidence []EvidenceRef `json:"evidence"`
}
type Advisory struct {
	Text string `json:"text"`
	Kind string `json:"kind"`
}
type DailyReviewReportV1 struct {
	Version             string            `json:"version"`
	Window              Window            `json:"window"`
	Highlights          []Fact            `json:"highlights"`
	Completed           []Fact            `json:"completed"`
	Unfinished          []Fact            `json:"unfinished"`
	GoalProgress        []Fact            `json:"goal_progress"`
	ReflectionQuestions []Advisory        `json:"reflection_questions"`
	Suggestions         []Advisory        `json:"suggestions"`
	CoverageWarnings    []CoverageWarning `json:"coverage_warnings"`
	EvidenceRefs        []EvidenceRef     `json:"evidence_refs"`
}

type ReportCodec struct{}

func (ReportCodec) Validate(raw json.RawMessage) error {
	_, err := DecodeReportV1(raw)
	return err
}

func DecodeReportV1(raw []byte) (DailyReviewReportV1, error) {
	if len(raw) == 0 || len(raw) > 64*1024 {
		return DailyReviewReportV1{}, ErrInvalidReport
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var report DailyReviewReportV1
	if err := decoder.Decode(&report); err != nil {
		return report, fmt.Errorf("%w: %v", ErrInvalidReport, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return report, fmt.Errorf("%w: multiple objects", ErrInvalidReport)
	}
	if err := report.ValidateShape(); err != nil {
		return report, err
	}
	return report, nil
}
func (r DailyReviewReportV1) ValidateShape() error {
	if r.Version != ReportSchemaV1 || !r.Window.Valid() || len(r.Highlights) > 12 || len(r.Completed) > 20 || len(r.Unfinished) > 20 || len(r.GoalProgress) > 12 || len(r.ReflectionQuestions) > 8 || len(r.Suggestions) > 8 || len(r.CoverageWarnings) > 8 || len(r.EvidenceRefs) > 100 {
		return ErrInvalidReport
	}
	for _, facts := range [][]Fact{r.Highlights, r.Completed, r.Unfinished, r.GoalProgress} {
		for _, fact := range facts {
			if strings.TrimSpace(fact.Text) == "" || len(fact.Text) > 1000 || len(fact.Evidence) == 0 || len(fact.Evidence) > 8 {
				return ErrInvalidReport
			}
			for _, ref := range fact.Evidence {
				if !validEvidenceRef(ref) {
					return ErrInvalidReport
				}
			}
		}
	}
	for _, items := range [][]Advisory{r.ReflectionQuestions, r.Suggestions} {
		for _, item := range items {
			if strings.TrimSpace(item.Text) == "" || len(item.Text) > 1000 || (item.Kind != "suggestion" && item.Kind != "reflection_question") {
				return ErrInvalidReport
			}
		}
	}
	for _, ref := range r.EvidenceRefs {
		if !validEvidenceRef(ref) {
			return ErrInvalidReport
		}
	}
	return nil
}
func validEvidenceRef(ref EvidenceRef) bool {
	decoded, err := hex.DecodeString(ref.Hash)
	return err == nil && len(decoded) == 32 && (ref.Type == "chat" || ref.Type == "note" || ref.Type == "memory") && strings.TrimSpace(ref.ID) != "" && ref.Version > 0
}

func ValidateEvidence(report DailyReviewReportV1, snapshot SourceSnapshot, memories []MemoryRef) (DailyReviewReportV1, error) {
	if err := report.ValidateShape(); err != nil {
		return report, err
	}
	if report.Window.LocalDate != snapshot.Window.LocalDate || report.Window.Timezone != snapshot.Window.Timezone || !report.Window.Start.Equal(snapshot.Window.Start) || !report.Window.End.Equal(snapshot.Window.End) {
		return report, ErrInvalidReport
	}
	allowed := map[string]EvidenceRef{}
	for _, r := range snapshot.Chat {
		allowed["chat:"+r.ID] = EvidenceRef{Type: "chat", ID: r.ID, Version: uint64(r.Sequence), Hash: r.ContentHash}
	}
	for _, r := range snapshot.Notes {
		allowed["note:"+r.ID] = EvidenceRef{Type: "note", ID: r.ID, Version: r.Version, Hash: r.ContentHash}
	}
	for _, r := range memories {
		allowed["memory:"+r.ID] = EvidenceRef{Type: "memory", ID: r.ID, Version: r.LineageVersion, Hash: r.ContentHash}
	}
	validate := func(items []Fact) []Fact {
		out := items[:0]
		for _, fact := range items {
			ok := true
			for _, ref := range fact.Evidence {
				want, exists := allowed[ref.Type+":"+ref.ID]
				if !exists || want.Version != ref.Version || want.Hash != ref.Hash {
					ok = false
					break
				}
			}
			if ok {
				out = append(out, fact)
			}
		}
		return out
	}
	before := len(report.Highlights) + len(report.Completed) + len(report.Unfinished) + len(report.GoalProgress)
	report.Highlights = validate(report.Highlights)
	report.Completed = validate(report.Completed)
	report.Unfinished = validate(report.Unfinished)
	report.GoalProgress = validate(report.GoalProgress)
	after := len(report.Highlights) + len(report.Completed) + len(report.Unfinished) + len(report.GoalProgress)
	if after < before {
		report.CoverageWarnings = append(report.CoverageWarnings, CoverageWarning{Source: "evidence", Code: "invalid_fact_removed", Included: after, Available: before})
	}
	refs := map[string]EvidenceRef{}
	for _, facts := range [][]Fact{report.Highlights, report.Completed, report.Unfinished, report.GoalProgress} {
		for _, fact := range facts {
			for _, ref := range fact.Evidence {
				refs[ref.Type+":"+ref.ID] = ref
			}
		}
	}
	report.EvidenceRefs = report.EvidenceRefs[:0]
	for _, ref := range refs {
		report.EvidenceRefs = append(report.EvidenceRefs, ref)
	}
	sort.Slice(report.EvidenceRefs, func(i, j int) bool {
		if report.EvidenceRefs[i].Type != report.EvidenceRefs[j].Type {
			return report.EvidenceRefs[i].Type < report.EvidenceRefs[j].Type
		}
		return report.EvidenceRefs[i].ID < report.EvidenceRefs[j].ID
	})
	return report, nil
}

func EmptyReport(window Window, warnings []CoverageWarning) DailyReviewReportV1 {
	return DailyReviewReportV1{Version: ReportSchemaV1, Window: window, CoverageWarnings: append([]CoverageWarning(nil), warnings...), ReflectionQuestions: []Advisory{}, Suggestions: []Advisory{}, Highlights: []Fact{}, Completed: []Fact{}, Unfinished: []Fact{}, GoalProgress: []Fact{}, EvidenceRefs: []EvidenceRef{}}
}
func RenderReport(report DailyReviewReportV1) string {
	var b strings.Builder
	b.WriteString("# ")
	b.WriteString(report.Window.LocalDate)
	b.WriteString(" 每日回顾\n")
	writeFacts := func(title string, items []Fact) {
		b.WriteString("\n## ")
		b.WriteString(title)
		b.WriteByte('\n')
		if len(items) == 0 {
			b.WriteString("- 暂无可验证内容\n")
			return
		}
		for _, item := range items {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(item.Text))
			b.WriteByte('\n')
		}
	}
	writeFacts("亮点", report.Highlights)
	writeFacts("已完成", report.Completed)
	writeFacts("待继续", report.Unfinished)
	writeFacts("目标进展", report.GoalProgress)
	if len(report.Suggestions) > 0 {
		b.WriteString("\n## 建议\n")
		for _, item := range report.Suggestions {
			b.WriteString("- 建议：")
			b.WriteString(strings.TrimSpace(item.Text))
			b.WriteByte('\n')
		}
	}
	if len(report.ReflectionQuestions) > 0 {
		b.WriteString("\n## 反思问题\n")
		for _, item := range report.ReflectionQuestions {
			b.WriteString("- ")
			b.WriteString(strings.TrimSpace(item.Text))
			b.WriteByte('\n')
		}
	}
	if len(report.CoverageWarnings) > 0 {
		b.WriteString("\n> 覆盖提示：部分活动因数据上限、变化或证据校验未纳入。\n")
	}
	return b.String()
}
