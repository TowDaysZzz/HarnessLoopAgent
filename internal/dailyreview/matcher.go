package dailyreview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

var ErrInvalidPlan = errors.New("invalid daily review plan")

type PlanV1 struct {
	Date     string `json:"date"`
	Timezone string `json:"timezone"`
}

type PlanCodec struct{ Timezone string }

func (c PlanCodec) Validate(raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var plan PlanV1
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) == nil {
		return ErrInvalidPlan
	}
	zone := c.Timezone
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	if plan.Timezone != zone {
		return fmt.Errorf("%w: unsupported timezone", ErrInvalidPlan)
	}
	location, err := time.LoadLocation(plan.Timezone)
	if err != nil {
		return fmt.Errorf("%w: timezone", ErrInvalidPlan)
	}
	if _, err := time.ParseInLocation("2006-01-02", plan.Date, location); err != nil {
		return fmt.Errorf("%w: date", ErrInvalidPlan)
	}
	return nil
}

type Matcher struct {
	Timezone        string
	MaxLookbackDays int
}

var (
	isoDate = regexp.MustCompile(`\b(20\d{2}-\d{2}-\d{2})\b`)
	cnDate  = regexp.MustCompile(`(20\d{2})年(\d{1,2})月(\d{1,2})日`)
)

func (m Matcher) Match(_ context.Context, input skill.MatchInput) (skill.Match, bool, error) {
	text := strings.TrimSpace(input.Content)
	if !dailyReviewTrigger(text) {
		return skill.Match{}, false, nil
	}
	zone := m.Timezone
	if zone == "" {
		zone = "Asia/Shanghai"
	}
	location, err := time.LoadLocation(zone)
	if err != nil {
		return skill.Match{}, false, err
	}
	now := time.Unix(input.NowUnix, 0).In(location)
	if input.NowUnix == 0 {
		now = time.Now().In(location)
	}
	confidence, reason := .99, "daily_review"
	if containsAny(text, "忽略系统", "忽略之前", "绕过权限", "伪造身份") {
		confidence, reason = .2, "daily_review_prompt_injection"
	}
	if containsAny(text, "并保存", "保存回顾", "记住这份", "写入记忆", "创建提醒") {
		confidence, reason = .5, "daily_review_write_ambiguous"
	}
	if containsAny(text, "前几天", "最近几天", "这几天", "某一天") {
		confidence, reason = .5, "daily_review_date_ambiguous"
	}
	date := now.Format("2006-01-02")
	switch {
	case strings.Contains(text, "昨天"):
		date = now.AddDate(0, 0, -1).Format("2006-01-02")
	case strings.Contains(text, "前天"):
		date = now.AddDate(0, 0, -2).Format("2006-01-02")
	case isoDate.MatchString(text):
		date = isoDate.FindStringSubmatch(text)[1]
	case cnDate.MatchString(text):
		parts := cnDate.FindStringSubmatch(text)
		var year, month, day int
		fmt.Sscanf(parts[1], "%d", &year)
		fmt.Sscanf(parts[2], "%d", &month)
		fmt.Sscanf(parts[3], "%d", &day)
		date = fmt.Sprintf("%04d-%02d-%02d", year, month, day)
	}
	parsed, parseErr := time.ParseInLocation("2006-01-02", date, location)
	maxDays := m.MaxLookbackDays
	if maxDays < 1 {
		maxDays = 31
	}
	if parseErr != nil || parsed.After(startOfDay(now)) || parsed.Before(startOfDay(now).AddDate(0, 0, -maxDays)) {
		confidence, reason = .4, "daily_review_date_invalid"
		date = now.Format("2006-01-02")
	}
	arguments, err := json.Marshal(PlanV1{Date: date, Timezone: zone})
	if err != nil {
		return skill.Match{}, false, err
	}
	return skill.Match{Arguments: arguments, Confidence: confidence, Reason: reason}, true, nil
}

func dailyReviewTrigger(text string) bool {
	if !containsAny(text, "回顾", "复盘", "总结") {
		return false
	}
	if containsAny(text, "总结刚才", "总结以上", "总结对话", "总结聊天") {
		return false
	}
	return containsAny(text, "每日", "今天", "今日", "昨天", "昨日", "前天", "前几天", "最近几天", "这几天", "某一天") || isoDate.MatchString(text) || cnDate.MatchString(text)
}

func containsAny(text string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func startOfDay(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}
